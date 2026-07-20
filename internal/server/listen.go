package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/netif"
)

// listenConfirmWindow — окно confirm-or-revert для живой смены адреса.
// 120с, а не 60: смена интерфейса уводит на другой origin, где cookie сессии
// не действует — пользователю может понадобиться время на вход перед
// подтверждением (сам confirm при этом принимает одноразовый токен и без
// сессии, но токен доезжает только если редирект вообще открылся).
const listenConfirmWindow = 120 * time.Second

// listenHealInterval — период сверки активных листенеров с желаемым spec.
// Ловит дрейф IP интерфейса (DHCP/PPPoE-переподключение) и появление IP у
// интерфейса, который на boot ещё не поднялся.
const listenHealInterval = 60 * time.Second

// ListenSpec — желаемое состояние HTTP-листенеров: порт + kernel-имена
// интерфейсов. Пустой список интерфейсов = слушать всё (0.0.0.0).
type ListenSpec struct {
	Port       int
	Interfaces []string
}

func cloneSpec(sp ListenSpec) ListenSpec {
	out := ListenSpec{Port: sp.Port}
	out.Interfaces = append([]string(nil), sp.Interfaces...)
	return out
}

// listenState — активные листенеры + confirm-окно. Живёт отдельно от Config:
// мутируется после Start (live rebind по API, heal-тикер).
type listenState struct {
	mu        sync.Mutex
	spec      ListenSpec
	listeners map[string]net.Listener // ключ — фактический "ip:port"

	// confirm-or-revert: непустой pendingToken = окно открыто.
	pendingToken    string
	pendingPrevSpec ListenSpec
	pendingDeadline time.Time
	pendingTimer    *time.Timer

	healStop chan struct{}
}

// SetListenSpec задаёт желаемый bind ДО Start (из main.go после выбора порта).
func (s *Server) SetListenSpec(spec ListenSpec) {
	s.listen.mu.Lock()
	defer s.listen.mu.Unlock()
	s.listen.spec = cloneSpec(spec)
}

// resolveListenAddrs разворачивает spec в конкретные "ip:port".
//
// strict=true (API-путь): интерфейс без IPv4 — ошибка, пользователь на связи
// и должен увидеть её сразу. strict=false (boot/heal): такой интерфейс
// пропускается с warning'ом — IP может появиться позже (PPPoE ещё поднимается),
// heal-тикер добиндит.
//
// Loopback 127.0.0.1 добавляется всегда (кроме bind на 0.0.0.0, который его
// уже покрывает): на нём живут реверс-прокси NDMS (KeenDNS «через облако»),
// health-пробы init-скрипта и спасательный люк при ошибочном выборе
// интерфейса.
func (s *Server) resolveListenAddrs(spec ListenSpec, strict bool) ([]string, error) {
	var addrs []string
	seen := make(map[string]struct{})
	add := func(ip string) {
		a := fmt.Sprintf("%s:%d", ip, spec.Port)
		if _, ok := seen[a]; ok {
			return
		}
		seen[a] = struct{}{}
		addrs = append(addrs, a)
	}
	if len(spec.Interfaces) == 0 {
		add("0.0.0.0")
		return addrs, nil
	}
	var missing []string
	for _, iface := range spec.Interfaces {
		ip := netif.FirstIPv4(iface)
		if ip == "" {
			missing = append(missing, iface)
			continue
		}
		add(ip)
	}
	if strict && len(missing) > 0 {
		return nil, fmt.Errorf("интерфейс(ы) без IPv4-адреса: %s", strings.Join(missing, ", "))
	}
	// Ни один интерфейс из списка не отрезолвился (#571: например, мусорное
	// легаси server.interface) — fallback на 0.0.0.0, как до 2.16: демон
	// только на loopback недостижим для пользователя. Heal сузит bind до
	// желаемого spec, когда интерфейс получит IP.
	if len(addrs) == 0 {
		s.appLog.Warn("listen", "", "ни один интерфейс не имеет IPv4 ("+strings.Join(missing, ", ")+") — слушаю 0.0.0.0")
		add("0.0.0.0")
		return addrs, nil
	}
	if len(missing) > 0 {
		s.appLog.Warn("listen", "", "интерфейсы без IPv4 пропущены (heal добиндит при появлении IP): "+strings.Join(missing, ", "))
	}
	add("127.0.0.1")
	return addrs, nil
}

// wildcardConflict — true, если два разных "ip:port" не могут слушать
// одновременно: одинаковый порт и одна из сторон — wildcard 0.0.0.0.
func wildcardConflict(a, b string) bool {
	ha, pa, errA := net.SplitHostPort(a)
	hb, pb, errB := net.SplitHostPort(b)
	if errA != nil || errB != nil {
		return false
	}
	return pa == pb && ha != hb && (ha == "0.0.0.0" || hb == "0.0.0.0")
}

// applyListenLocked приводит активные листенеры к want (make-before-break):
// СНАЧАЛА биндит недостающие адреса, ПОТОМ закрывает лишние. Исключение —
// wildcard/specific-конфликт на одном порту: там точечный break-before-make
// с восстановлением при неудаче (см. closedConflicts ниже). При
// bestEffort=false ошибка бинда любого нового адреса закрывает уже открытые
// новые и оставляет старые нетронутыми — сервер никогда не остаётся без
// листенера. bestEffort=true (boot/heal/revert) — недоступные адреса
// пропускаются с warning'ом. Возвращает число активных листенеров.
// Caller держит s.listen.mu; s.httpServer должен быть построен.
func (s *Server) applyListenLocked(want []string, bestEffort bool) (int, error) {
	if s.listen.listeners == nil {
		s.listen.listeners = make(map[string]net.Listener)
	}
	wantSet := make(map[string]struct{}, len(want))
	for _, a := range want {
		wantSet[a] = struct{}{}
	}

	// Точечный break-before-make: wildcard и конкретный IP на одном порту не
	// сосуществуют в LISTEN (EADDRINUSE), поэтому обречённый (не в want)
	// листенер, конфликтующий с новым bind'ом, закрываем ДО открытия.
	// Переходы «0.0.0.0 → конкретный интерфейс» (сужение fallback'а #571 или
	// живая смена из API) без этого невозможны. Если bind, ради которого
	// закрывали, не удался — закрытый адрес перебиндивается обратно.
	closedConflicts := make(map[string]string) // закрытый адрес → want-адрес, ради которого закрыт
	for addr, ln := range s.listen.listeners {
		if _, keep := wantSet[addr]; keep {
			continue
		}
		for _, w := range want {
			if _, exists := s.listen.listeners[w]; exists || !wildcardConflict(addr, w) {
				continue
			}
			ln.Close()
			delete(s.listen.listeners, addr)
			closedConflicts[addr] = w
			s.appLog.Info("listen", addr, "listener closed (конфликт wildcard/интерфейс с "+w+")")
			break
		}
	}
	// restoreConflicts перебиндивает закрытые конфликтные адреса, чей
	// want-адрес так и не открылся (all=true — все, путь ошибки).
	restoreConflicts := func(all bool, opened map[string]net.Listener) {
		for addr, w := range closedConflicts {
			if !all {
				if _, ok := opened[w]; ok {
					continue
				}
			}
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				s.appLog.Warn("listen", addr, "restore bind failed: "+err.Error())
				continue
			}
			s.listen.listeners[addr] = ln
			go s.serveListener(ln)
			s.appLog.Info("listen", addr, "listener restored")
		}
	}

	opened := make(map[string]net.Listener)
	for _, addr := range want {
		if _, ok := s.listen.listeners[addr]; ok {
			continue
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			if bestEffort {
				s.appLog.Warn("listen", addr, "bind failed: "+err.Error())
				continue
			}
			for _, o := range opened {
				o.Close()
			}
			restoreConflicts(true, nil)
			return len(s.listen.listeners), fmt.Errorf("bind %s: %w", addr, err)
		}
		opened[addr] = ln
	}
	restoreConflicts(false, opened)

	// Zero-listener guard (best-effort пути: revert/heal): если ни один
	// желаемый адрес не выжил (все бинды упали, пересечения с текущими нет —
	// например, старый порт заняли за confirm-окно), НЕ закрываем текущие
	// листенеры — недостижимый демон хуже задержавшегося отката. spec при
	// этом остаётся желаемым: heal-тикер сойдётся к нему, когда адрес
	// освободится, и тогда же закроет лишние.
	survivors := len(opened)
	for addr := range s.listen.listeners {
		if _, ok := wantSet[addr]; ok {
			survivors++
		}
	}
	if survivors == 0 && len(s.listen.listeners) > 0 {
		s.appLog.Warn("listen", "", "ни один желаемый адрес не забиндился — прежние листенеры сохранены до следующей попытки heal")
		return len(s.listen.listeners), nil
	}

	// Новые слушают до закрытия старых — make-before-break.
	for addr, ln := range opened {
		s.listen.listeners[addr] = ln
		go s.serveListener(ln)
		s.appLog.Info("listen", addr, "listener started")
	}
	for addr, ln := range s.listen.listeners {
		if _, ok := wantSet[addr]; ok {
			continue // включает только что открытые: их адреса из want
		}
		ln.Close() // установленные соединения переживают закрытие листенера
		delete(s.listen.listeners, addr)
		s.appLog.Info("listen", addr, "listener closed")
	}
	return len(s.listen.listeners), nil
}

func (s *Server) serveListener(ln net.Listener) {
	err := s.httpServer.Serve(ln)
	if err != nil && err != http.ErrServerClosed && !errors.Is(err, net.ErrClosed) {
		s.appLog.Warn("listen", ln.Addr().String(), "listener stopped: "+err.Error())
	}
}

// startListenHeal запускает минутную сверку «желаемые адреса ↔ активные
// листенеры»: перебиндивает дрейф IP интерфейса (DHCP/PPPoE) и добиндивает
// интерфейсы, получившие IP после boot. Пока открыто confirm-окно — молчит,
// чтобы не спорить с ожидающим откатом.
func (s *Server) startListenHeal() {
	s.listen.healStop = make(chan struct{})
	go func() {
		t := time.NewTicker(listenHealInterval)
		defer t.Stop()
		for {
			select {
			case <-s.listen.healStop:
				return
			case <-t.C:
				s.listen.mu.Lock()
				if s.listen.pendingToken == "" {
					if addrs, err := s.resolveListenAddrs(s.listen.spec, false); err == nil {
						_, _ = s.applyListenLocked(addrs, true)
					}
				}
				s.listen.mu.Unlock()
			}
		}
	}()
}

// BeginListenChange живо применяет новый spec (строгий резолв: все интерфейсы
// обязаны иметь IPv4) и взводит откат к прежнему spec через
// listenConfirmWindow. Настройки НЕ персистятся здесь — только после
// ConfirmListenChange, поэтому креш/рестарт в окне поднимает демона на старом
// адресе. Возвращает одноразовый токен подтверждения: фронт уносит его на
// новый origin в URL-фрагменте (cookie сессии привязана к хосту и смену
// интерфейса не переживает).
func (s *Server) BeginListenChange(port int, interfaces []string) (token string, deadline time.Time, addrs []string, err error) {
	if port < 1 || port > 65535 {
		return "", time.Time{}, nil, fmt.Errorf("порт %d вне диапазона 1–65535", port)
	}
	s.listen.mu.Lock()
	defer s.listen.mu.Unlock()
	if s.httpServer == nil {
		return "", time.Time{}, nil, fmt.Errorf("сервер ещё не запущен")
	}
	if s.listen.pendingToken != "" {
		return "", time.Time{}, nil, fmt.Errorf("предыдущее изменение адреса ещё не подтверждено (откат в %s)", s.listen.pendingDeadline.Format("15:04:05"))
	}

	newSpec := cloneSpec(ListenSpec{Port: port, Interfaces: interfaces})
	want, err := s.resolveListenAddrs(newSpec, true)
	if err != nil {
		return "", time.Time{}, nil, err
	}
	prev := cloneSpec(s.listen.spec)
	if _, err := s.applyListenLocked(want, false); err != nil {
		return "", time.Time{}, nil, err
	}
	s.listen.spec = newSpec

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		// Токен не получился — откатываем bind, состояние прежнее.
		if prevAddrs, rerr := s.resolveListenAddrs(prev, false); rerr == nil {
			_, _ = s.applyListenLocked(prevAddrs, true)
		}
		s.listen.spec = prev
		return "", time.Time{}, nil, fmt.Errorf("generate confirm token: %w", err)
	}
	token = hex.EncodeToString(raw)
	deadline = time.Now().Add(listenConfirmWindow)
	s.listen.pendingToken = token
	s.listen.pendingPrevSpec = prev
	s.listen.pendingDeadline = deadline
	s.listen.pendingTimer = time.AfterFunc(listenConfirmWindow, func() { s.revertListen(token) })
	s.appLog.Warn("listen", "", fmt.Sprintf("адрес изменён (порт %d, интерфейсы: %s) — ожидаю подтверждения до %s, иначе откат",
		port, interfacesLabel(interfaces), deadline.Format("15:04:05")))
	return token, deadline, sortedAddrs(s.listen.listeners), nil
}

// ConfirmListenChange гасит откат по одноразовому токену (constant-time
// сравнение). Возвращает подтверждённые порт/интерфейсы — caller (API-слой)
// персистит их в настройки. Повторный/чужой токен → ok=false. Плоская
// сигнатура (не ListenSpec) — под интерфейс api.ServerListenController без
// импорта server из api.
func (s *Server) ConfirmListenChange(token string) (port int, interfaces []string, ok bool) {
	s.listen.mu.Lock()
	defer s.listen.mu.Unlock()
	if s.listen.pendingToken == "" || token == "" ||
		subtle.ConstantTimeCompare([]byte(s.listen.pendingToken), []byte(token)) != 1 {
		return 0, nil, false
	}
	if s.listen.pendingTimer != nil {
		s.listen.pendingTimer.Stop()
	}
	s.clearPendingLocked()
	s.appLog.Info("listen", "", "смена адреса подтверждена")
	sp := cloneSpec(s.listen.spec)
	return sp.Port, sp.Interfaces, true
}

// revertListen — таймер confirm-окна: подтверждение не пришло, возвращаем
// прежние листенеры и spec. Токен-гард отсекает гонку с успевшим Confirm.
func (s *Server) revertListen(token string) {
	s.listen.mu.Lock()
	defer s.listen.mu.Unlock()
	if s.listen.pendingToken != token {
		return // подтвердили (или это чужое окно) — откат не нужен
	}
	prev := s.listen.pendingPrevSpec
	s.clearPendingLocked()
	if addrs, err := s.resolveListenAddrs(prev, false); err == nil {
		_, _ = s.applyListenLocked(addrs, true)
	}
	s.listen.spec = cloneSpec(prev)
	s.appLog.Warn("listen", "", "смена адреса не подтверждена за отведённое окно — откат к прежнему адресу")
}

func (s *Server) clearPendingLocked() {
	s.listen.pendingToken = ""
	s.listen.pendingPrevSpec = ListenSpec{}
	s.listen.pendingDeadline = time.Time{}
	s.listen.pendingTimer = nil
}

// ListenState отдаёт текущее состояние для GET /api/server/listen.
func (s *Server) ListenState() (port int, interfaces []string, boundAddrs []string, pending bool, deadline time.Time) {
	s.listen.mu.Lock()
	defer s.listen.mu.Unlock()
	sp := cloneSpec(s.listen.spec)
	return sp.Port, sp.Interfaces, sortedAddrs(s.listen.listeners), s.listen.pendingToken != "", s.listen.pendingDeadline
}

func sortedAddrs(listeners map[string]net.Listener) []string {
	out := make([]string, 0, len(listeners))
	for a := range listeners {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

func interfacesLabel(ifaces []string) string {
	if len(ifaces) == 0 {
		return "все (0.0.0.0)"
	}
	return strings.Join(ifaces, ", ")
}
