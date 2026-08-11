package nwg

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/payloads"
	"github.com/hoaxisr/awg-manager/internal/tunnel/netutil"
)

// Endpoint-страж для v6-туннелей на ASC-прошивках. NDMS хранит в своём
// конфиге заглушку 127.0.0.1:1 и ПЕРЕЗАПИСЫВАЕТ ею kernel-endpoint при любом
// переприменении конфига: ребут роутера, up/down интерфейса из веба,
// WAN-failover (на ASC делегирован NDMS), рестарт интерфейса его же
// ping-check'ом. Событийной интеграции, покрывающей все эти случаи, нет —
// страж СХОДИТСЯ к желаемому состоянию опросом: каждые guardInterval
// сверяет `wg show <iface> endpoints` с ожидаемым и возвращает endpoint.
// Для hostname-endpoint'ов (DDNS) ожидаемый адрес перерезолвливается на
// каждом проходе — смена адреса за именем доезжает до ядра за один
// интервал; при недоступном DNS страж работает по последнему известному.
// Анти-флап: адрес меняется, только когда текущий выпал из ПОЛНОГО резолва
// имени — round-robin-ротация записей не перекидывает живую сессию.
//
// Реестр живёт в памяти демона и наполняется в startNative; после рестарта
// awgm его восстанавливает Start (EventReconnect для работающих
// ASC-туннелей, EventBoot для v6-туннелей — orchestrator/decide.go).

// guardIntervalDefault — период сверки. Достаточно короткий, чтобы разрыв
// после NDMS-события был минутного масштаба худшего случая у ping-check
// (сам страж и разрывает его порочный цикл «рестарт → заглушка → пинги
// падают → рестарт»).
var guardInterval = 20 * time.Second

type guardEntry struct {
	iface    string // kernel-имя (nwgN)
	pubkey   string
	endpoint string // последний известный резолв, каноническая форма host:port
	spec     string // endpoint из конфига (hostname:port или литерал) — для перерезолва DDNS
	name     string // NDMS-имя для логов
	// viaNDMS — доводить адрес не в ядро, а в конфиг NDMS. Так для
	// hostname→v4: владелец конфига здесь NDMS, и он переписывает
	// kernel-endpoint своим значением при каждом переприменении —
	// wg set проиграл бы ему гонку.
	viaNDMS bool
	// viaKmod — доводить адрес в слот awg_proxy.ko (proxy-прошивки).
	// Ставится и обрабатывается в Task A2; здесь поле нужно, чтобы
	// kmod-записи не двигали реестр до успешной доводки и не уходили в
	// v6-ветку ядра.
	viaKmod bool
}

// guardModeForEndpoint решает, как страж следит за endpoint'ом:
// v6 — доводкой в ядро (NDMS его не принимает), hostname→v4 — доводкой в
// конфиг NDMS (адрес за именем может смениться, а NDMS хранит литерал),
// v4-литерал — никак, резолвить нечего.
func guardModeForEndpoint(endpoint string, kernelV6 bool) (guard, viaNDMS bool) {
	if kernelV6 {
		return true, false
	}
	host, ok := splitEndpointHost(endpoint)
	if !ok || net.ParseIP(host) != nil {
		return false, false
	}
	return true, true
}

func (o *OperatorNativeWG) guardRegister(id string, e guardEntry) {
	o.guardMu.Lock()
	if o.guard == nil {
		o.guard = make(map[string]guardEntry)
	}
	o.guard[id] = e
	o.guardMu.Unlock()
	o.guardOnce.Do(func() { go o.guardLoop() })
}

func (o *OperatorNativeWG) guardUnregister(id string) {
	o.guardMu.Lock()
	delete(o.guard, id)
	o.guardMu.Unlock()
}

func (o *OperatorNativeWG) guardHas(id string) bool {
	_, ok := o.guardGet(id)
	return ok
}

func (o *OperatorNativeWG) guardGet(id string) (guardEntry, bool) {
	o.guardMu.Lock()
	defer o.guardMu.Unlock()
	e, ok := o.guard[id]
	return e, ok
}

// guardReplaceIfPresent обновляет запись, только если туннель всё ещё в
// реестре: параллельный Stop/Delete (оркестратор — другой лок-домен, чем
// service.Update) мог снять его со стражи, и безусловный register воскресил
// бы запись навсегда. Возвращает, была ли запись заменена.
func (o *OperatorNativeWG) guardReplaceIfPresent(id string, e guardEntry) bool {
	o.guardMu.Lock()
	defer o.guardMu.Unlock()
	if _, ok := o.guard[id]; !ok {
		return false
	}
	o.guard[id] = e
	return true
}

// guardUpdateEndpoint обновляет ожидаемый endpoint записи, только если она
// всё ещё в реестре И несёт тот же spec: иначе гонка со Stop/Delete
// воскресила бы удалённую запись, а гонка с SyncPeer (сменил spec) —
// затёрла бы свежий endpoint резолвом СТАРОГО имени.
func (o *OperatorNativeWG) guardUpdateEndpoint(id, spec, endpoint string) {
	o.guardMu.Lock()
	defer o.guardMu.Unlock()
	if e, ok := o.guard[id]; ok && e.spec == spec {
		e.endpoint = endpoint
		o.guard[id] = e
	}
}

func (o *OperatorNativeWG) guardLoop() {
	ticker := time.NewTicker(guardInterval)
	defer ticker.Stop()
	for range ticker.C {
		o.guardSweep(context.Background())
	}
}

// guardSweep — один проход сверки. Вынесен из цикла ради тестов.
func (o *OperatorNativeWG) guardSweep(ctx context.Context) {
	o.guardMu.Lock()
	entries := make(map[string]guardEntry, len(o.guard))
	for id, e := range o.guard {
		entries[id] = e
	}
	o.guardMu.Unlock()
	if len(entries) == 0 {
		return
	}

	for id, e := range entries {
		// Hostname-spec (DDNS): перерезолвить — адрес мог смениться, а
		// kernel WG сам за чужим DNS не следит. Анти-флап: endpoint
		// меняется, только когда текущий адрес ВЫПАЛ из полного резолва
		// имени — ротация round-robin-записей не дёргает живую сессию.
		// Ошибка резолва не фатальна: работаем по последнему известному.
		expected := e.endpoint
		if host, ok := splitEndpointHost(e.spec); ok && net.ParseIP(host) == nil {
			if ips, rerr := lookupAllWithTimeout(host, resolveAttemptTimeout); rerr == nil && len(ips) > 0 {
				curHost, _, splitErr := net.SplitHostPort(expected)
				if splitErr != nil || !ipInList(curHost, ips) {
					if _, port, perr := net.SplitHostPort(e.spec); perr == nil {
						fresh := net.JoinHostPort(pickEndpointIP(ips), port)
						prev := expected // для лога: «был» обязан печатать старое
						expected = fresh
						if !e.viaNDMS && !e.viaKmod {
							// Только чистый v6-режим: сверка идёт с
							// фактическим wg show, поэтому реестр можно
							// двигать сразу — упавший wg set повторится на
							// следующем проходе. У viaNDMS и viaKmod (Task A2)
							// readback'а нет, они сверяются с самим реестром:
							// преждевременная запись навсегда увела бы их в
							// `continue` после первой же неудачи.
							o.appLog.Info("endpoint-guard", e.name,
								fmt.Sprintf("%s резолвится в новый адрес: %s (был %s)", e.spec, fresh, prev))
							o.guardUpdateEndpoint(id, e.spec, fresh)
						}
					}
				}
			}
		}
		if e.viaNDMS {
			// v4: адрес живёт в конфиге NDMS. Команду шлём только на
			// смену резолва — иначе каждый проход переписывал бы конфиг.
			if expected == e.endpoint {
				continue
			}
			// Перепроверка перед записью — паритет с v6-веткой ниже.
			// Гонка со Stop/Delete/SyncPeer здесь опаснее, чем там:
			// wg set правит эфемерное состояние ядра, а это — конфиг
			// NDMS, который переживёт и рестарт, и ребут.
			if cur, ok := o.guardGet(id); !ok || cur.pubkey != e.pubkey || cur.iface != e.iface || cur.spec != e.spec {
				continue
			}
			if _, err := o.transport.Post(ctx, payloads.CmdWireguardPeerEndpoint(e.name, e.pubkey, expected)); err != nil {
				o.appLog.Warn("endpoint-guard", e.name, "обновление endpoint в NDMS не удалось: "+err.Error())
				continue
			}
			// Реестр двигаем только после успеха: иначе неудачный Post
			// потерял бы смену адреса до перезапуска демона.
			o.guardUpdateEndpoint(id, e.spec, expected)
			o.appLog.Info("endpoint-guard", e.name,
				fmt.Sprintf("%s сменил адрес — в NDMS выставлен %s", e.spec, expected))
			continue
		}
		bin := wgToolLookup()
		if bin == "" {
			continue
		}
		out, err := wgToolOutput(ctx, bin, "show", e.iface, "endpoints")
		if err != nil {
			// Интерфейс может отсутствовать переходно (пересоздание) —
			// не шумим, следующий проход разберётся.
			o.appLog.Full("endpoint-guard", e.name, "wg show: "+err.Error())
			continue
		}
		if wgShowHasEndpoint(out, e.pubkey, expected) {
			continue
		}
		// Перепроверка прямо перед wg set: параллельный Stop/Delete/SyncPeer
		// мог удалить или заменить запись, пока sweep резолвил и читал
		// wg show — wg set по устаревшей записи воскресил бы удалённого
		// пира (wg set создаёт пира, если ключа на интерфейсе нет).
		if cur, ok := o.guardGet(id); !ok || cur.pubkey != e.pubkey || cur.iface != e.iface || cur.spec != e.spec {
			continue
		}
		if err := wgToolRun(ctx, bin, "set", e.iface, "peer", e.pubkey, "endpoint", expected); err != nil {
			o.appLog.Warn("endpoint-guard", e.name, "восстановление endpoint не удалось: "+err.Error())
			continue
		}
		o.appLog.Info("endpoint-guard", e.name,
			fmt.Sprintf("kernel-endpoint слетел (NDMS переприменил конфиг или сменился DDNS-адрес) — выставлен %s на %s", expected, e.iface))
	}
}

// guardLookupIPs — полный резолв имени (все A/AAAA) для анти-флапа.
// Переопределяется в тестах.
var guardLookupIPs = netutil.LookupAllIPs

// lookupAllWithTimeout ограничивает guardLookupIPs таймаутом (у net.LookupIP
// без контекста собственного дедлайна нет).
func lookupAllWithTimeout(host string, timeout time.Duration) ([]string, error) {
	type result struct {
		ips []string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		ips, err := guardLookupIPs(host)
		ch <- result{ips, err}
	}()
	select {
	case r := <-ch:
		return r.ips, r.err
	case <-time.After(timeout):
		return nil, fmt.Errorf("resolve %s: timeout after %s", host, timeout)
	}
}

// ipInList — адрес входит в список (сравнение через ParseIP: разные
// текстовые записи одного адреса равны).
func ipInList(ip string, ips []string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, s := range ips {
		if other := net.ParseIP(s); other != nil && other.Equal(parsed) {
			return true
		}
	}
	return false
}

// pickEndpointIP выбирает адрес из полного резолва с тем же предпочтением
// v4, что и netutil.ResolveHost — семейство endpoint'а не должно зависеть
// от того, каким путём (Start или страж) он получен.
func pickEndpointIP(ips []string) string {
	for _, s := range ips {
		if ip := net.ParseIP(s); ip != nil && ip.To4() != nil {
			return s
		}
	}
	return ips[0]
}

// wgShowHasEndpoint парсит вывод `wg show <iface> endpoints`
// (строки "PUBKEY\tENDPOINT") и сверяет endpoint нужного пира.
func wgShowHasEndpoint(out, pubkey, endpoint string) bool {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == pubkey {
			return fields[1] == endpoint
		}
	}
	return false
}
