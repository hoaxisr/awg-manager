// Package query — InterfaceStore implementation.
//
// Architecture: event-sourced cache. ONE bootstrap HTTP query
// (/show/interface/) populates an in-memory map. Subsequent state
// changes arrive as NDMS hooks (ifcreated / ifdestroyed /
// iflayerchanged / ifipchanged) and patch the map in place — no
// repolling. Read paths (Get / GetDetails / List / ResolveSystemName
// / ListWAN / ListAll) answer purely from the cached snapshot.
//
// Имя ядра (ResolveSystemName / SystemNames / ListWAN / ListAll) — тоже
// память, RCI по требованию нет (F570). Источники: таблица ndms.KernelName
// (Wireguard, OpkgTun, Proxy, PPPoE, Bridge), `system_name` из хуков
// (OnSystemName) и ОДИН пакетный резолвер `show interface system-name`
// сразу после каждого свежего списка — только для id из этого ответа, чьё
// имя ещё не известно.
//
// Two write APIs feed the map:
//
//   - Hook-side (called from events.Dispatcher): OnCreated /
//     OnDestroyed / OnLayerChanged / OnIPChanged. Pure in-memory
//     mutators, no HTTP. OnCreated, OnLayerChanged and OnIPChanged only
//     mark an unknown id as pending (on creation NDMS sends
//     iflayerchanged ctrl ×2 before ifcreated, stand 5.01.C.6);
//     after each hook batch the dispatcher calls ReconcilePending, which
//     reads ONE full list if anything is pending. No point reads by name
//     from hooks: by the time the hook is applied the name may already be
//     gone, and NDMS logs E `unable to find` for it (F546).
//
//   - Command-side (called from internal/ndms/command/* and a few
//     admin handlers after a successful POST to NDMS): Invalidate(name)
//     ставит метку «грязно» без RCI — следующий Snapshot читает ОДИН
//     свежий список (F546); InvalidateAll() читает список сразу.
//
// Snapshot — записи и сырой JSON записей полного списка с возрастом:
// читатели состояния и счётчиков берут его вместо чтения по имени (F546).
//
// Every hook bumps seq and stamps the id in touched; a list answer never
// overwrites an id touched after its request started — the hook is newer
// than the answer. Чтений по имени (`show interface name=X`,
// `/show/rc/interface/X`) в пакете нет (F546, TestByNameReads_Absent).
package query

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// looksLikeKernelIfname reports whether s is a syntactically valid Linux
// network interface name. Linux kernel names use a constrained set
// (lowercase a-z, digits, ".", "_", "-") and fit in IFNAMSIZ-1 = 15 bytes.
// NDMS-style identifiers ("Wireguard0", "GigabitEthernet1", "ISP", "PPPoE0")
// contain upper-case letters and are rejected — they are not kernel device
// names and using them for SO_BINDTODEVICE / curl --interface fails with
// ENODEV. Used as the first-line filter in wireToInterface and as part of
// the trust checks in systemNameLocked / trustedSystemName.
func looksLikeKernelIfname(s string) bool {
	if s == "" || len(s) > 15 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			// OK
		case r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// kernelIfaceExists reports whether a network interface with the given
// name is present in the running kernel. Defends against firmware quirks
// where NDMS list response populates `interface-name` with a logical NDMS
// label (e.g. "ISP" for a physical port) that may pass the syntactic
// filter but isn't a real kernel device. Overridable for tests via the
// package-level variable.
var kernelIfaceExists = func(name string) bool {
	if name == "" {
		return false
	}
	_, err := os.Stat("/sys/class/net/" + name)
	return err == nil
}

// InterfaceStore is the event-sourced cache of NDMS interfaces.
type InterfaceStore struct {
	getter Getter
	log    Logger

	// bootMu serialises the bootstrap *operation* so concurrent boots
	// coalesce to ONE HTTP. booted is atomic because InvalidateAll
	// also writes it (without bootMu) — atomicity keeps the
	// data-race detector happy while the per-write lock keeps the
	// fetch logic single-flight.
	bootMu sync.Mutex
	booted atomic.Bool

	mu        sync.RWMutex
	byID      map[string]*ndms.Interface
	startedAt map[string]time.Time
	// sysNames — имена ядра из хуков (OnSystemName) и от резолвера вслед за
	// списком (refreshList), по NDMS-id. Отдельно от
	// byID, потому что InvalidateAll и OnCreated строят записи заново из
	// ответа RCI, а `interface-name` там не имя ядра (5.02.A.11: NDMS-id
	// или подпись, `Bridge0` → `Home`). Жило бы в записи — терялось бы при
	// каждом сбросе, и следующий список снова спрашивал бы все ~20
	// интерфейсов (F473). Снимается на ifdestroyed и списком без этого id.
	sysNames map[string]string

	// seq растёт на каждом хуке; touched — seq последнего хука по id.
	// Ответ списка или точечного чтения не затирает id, тронутый хуком после
	// начала запроса: хук новее ответа. Всё читается и пишется под mu.
	seq     uint64
	touched map[string]uint64
	pending map[string]struct{}

	// raw — запись полного списка как есть (пиры, ключи, счётчики), по id;
	// правило то же, что у byID: хук новее ответа — ответ её не затирает.
	raw map[string]json.RawMessage
	// listedAt — когда применён последний успешный список.
	listedAt time.Time
	// dirtyAt — seq метки Invalidate; 0 — не грязно. Снимает её только
	// список, начатый не раньше метки.
	dirtyAt uint64
	// flight — список в полёте (последний начатый), к нему присоединяется
	// Snapshot с maxAge > 0.
	flight *listFlight
	// flights — счётчик начатых списков; appliedNo — номер последнего
	// применённого: ответ, начатый раньше применённого, карту не трогает.
	flights, appliedNo uint64

	// awaiting — ConfirmCreated в ожидании записи по имени (F584); будят
	// применённый список, начатый после вызова и содержащий имя, и снос имени
	// (Forget/ifdestroyed); хуки создания оставляют след (traced).
	awaiting map[string]*createdWait
	// createdBackoff — паузы между списками ConfirmCreated.
	createdBackoff []time.Duration
}

// createdWait — ожидание одной созданной записи. Поля — под mu.
type createdWait struct {
	after uint64        // полёты с номером больше начаты после вызова
	ch    chan struct{} // закрывает fire: список с записью или снос имени
	fired bool
	// listed/rec — запись из списка, начатого после вызова.
	listed bool
	rec    ndms.Interface
	// traced — NDMS знает запись: по имени пришёл хук создания или слоя
	// (или имя уже ждало в pending). Без следа сносить нельзя — `no interface`
	// по отсутствующему пишет E в журнал ndm (стенд 5.01.C.6).
	traced bool
	// removed — имя снято (ifdestroyed или Forget) во время ожидания.
	removed bool
}

func (w *createdWait) fire() {
	if !w.fired {
		w.fired = true
		close(w.ch)
	}
}

// confirmCreatedBackoff — паузы между списками ConfirmCreated: не больше
// 4 полных списков (~15 тиков ndm каждый) и ~2,5 с на всё. Под нагрузкой
// NDMS кладёт созданную запись в список до ~1 с после ответа на создание
// (ifcreated, стенд 5.01.C.6, F584); обычно её раньше приносит список
// ReconcilePending по хукам — тогда своих списков после первого нет.
var confirmCreatedBackoff = []time.Duration{300 * time.Millisecond, 700 * time.Millisecond, 1500 * time.Millisecond}

// listFlight — один запрос полного списка. err и готовность читаются после done.
type listFlight struct {
	no    uint64 // порядковый номер начала: применяется только не старше применённого
	start uint64
	done  chan struct{}
	err   error
}

// NewInterfaceStore constructs a new InterfaceStore. Bootstrap is
// lazy — fires on the first read call.
func NewInterfaceStore(g Getter, log Logger) *InterfaceStore {
	if log == nil {
		log = NopLogger()
	}
	return &InterfaceStore{
		getter:    g,
		log:       log,
		byID:      make(map[string]*ndms.Interface),
		startedAt: make(map[string]time.Time),
		sysNames:  make(map[string]string),
		touched:   make(map[string]uint64),
		pending:   make(map[string]struct{}),
		raw:       make(map[string]json.RawMessage),
		awaiting:  make(map[string]*createdWait),

		createdBackoff: confirmCreatedBackoff,
	}
}

// SetCreatedBackoff — паузы ConfirmCreated (для тестов других пакетов).
func (s *InterfaceStore) SetCreatedBackoff(d ...time.Duration) {
	s.mu.Lock()
	s.createdBackoff = d
	s.mu.Unlock()
}

// NewInterfaceStoreWithTTL exists for backwards-compatible test wiring;
// the TTL parameters are ignored — the new store has no TTL (hooks +
// proactive refresh are the freshness mechanism). Tests that previously
// used short TTLs to force re-fetch should drive Invalidate explicitly.
func NewInterfaceStoreWithTTL(g Getter, log Logger, _ time.Duration, _ time.Duration) *InterfaceStore {
	return NewInterfaceStore(g, log)
}

// === Bootstrap ===

// ensureBootstrap fetches the full interface list from NDMS exactly
// once. Subsequent calls are no-ops on the fast path.
func (s *InterfaceStore) ensureBootstrap(ctx context.Context) error {
	if s.booted.Load() {
		return nil
	}
	s.bootMu.Lock()
	defer s.bootMu.Unlock()
	// Double-check inside the lock — another goroutine may have
	// completed bootstrap (or InvalidateAll, which also flips the
	// flag) between the load above and this critical section.
	if s.booted.Load() {
		return nil
	}

	if err := s.refreshAll(ctx); err != nil {
		return fmt.Errorf("interface bootstrap: %w", err)
	}
	return nil
}

// refreshAll читает полный список и кладёт его поверх карты через
// applyListLocked. start снимается ДО запроса: хуки, пришедшие, пока
// список в полёте, получают seq > start и ответом не затираются.
func (s *InterfaceStore) refreshAll(ctx context.Context) error {
	_, err := s.refreshList(ctx, nil)
	return err
}

// refreshList — refreshAll, возвращающий сам ответ NDMS: для подтверждения
// (Confirm) важен он, а не карта — seq-гард держит в pending имя, чей
// хук пришёл, пока список в полёте, хотя в ответе оно есть.
//
// fl == nil — свой запрос без присоединения (семантика Confirm: «список начат
// после моего вызова»); fl — полёт, уже зарегистрированный Snapshot. Любой
// запрос регистрируется как полёт, чтобы Snapshot мог к нему присоединиться.
// Кладёт список в карту, снимает метку «грязно», если список начат не раньше
// неё, и завершает полёт — после резолвера имён, чтобы присоединившиеся
// видели то же, что и начавший.
func (s *InterfaceStore) refreshList(ctx context.Context, fl *listFlight) (map[string]ndms.Interface, error) {
	if fl == nil {
		s.mu.Lock()
		fl = s.beginFlightLocked()
		s.mu.Unlock()
	}
	recs, raw, err := s.fetchListMap(ctx)
	var todo []string
	if err == nil {
		s.mu.Lock()
		// Ответы приходят не по порядку: начатый раньше уже применённого старее
		// его — карту, метку и возраст не трогает (ответ вызывающему — свой).
		if fl.no >= s.appliedNo {
			s.appliedNo = fl.no
			s.applyListLocked(recs, raw, fl.start)
			if s.dirtyAt <= fl.start {
				s.dirtyAt = 0
			}
			s.listedAt = time.Now()
			todo = s.unnamedLocked(recs)
			s.wakeCreatedLocked(recs, fl.no)
		}
		s.mu.Unlock()
		s.booted.Store(true)
		// Единственный вызов резолвера (F570): только id из ответа, который пришёл
		// миллисекунды назад. По требованию имя не спрашивается никогда — к тому
		// моменту запись могли снять, и NDMS пишет E «unable to find».
		s.resolveSystemNames(ctx, todo)
	}
	s.mu.Lock()
	fl.err = err
	if s.flight == fl {
		s.flight = nil
	}
	s.mu.Unlock()
	close(fl.done)
	if err != nil {
		return nil, err
	}
	return recs, nil
}

// wakeCreatedLocked будит ConfirmCreated, чья запись есть в применённом
// ответе полёта no, начатого после вызова.
func (s *InterfaceStore) wakeCreatedLocked(recs map[string]ndms.Interface, no uint64) {
	for name, w := range s.awaiting {
		rec, ok := recs[name]
		if !ok || no <= w.after || w.listed {
			continue
		}
		w.listed, w.rec = true, rec
		w.fire()
	}
}

// beginFlightLocked регистрирует новый полёт; start — seq ДО запроса: хуки,
// пришедшие, пока список в полёте, получают seq > start и ответом не затираются.
func (s *InterfaceStore) beginFlightLocked() *listFlight {
	s.flights++
	fl := &listFlight{no: s.flights, start: s.seq, done: make(chan struct{})}
	s.flight = fl
	return fl
}

// unnamedLocked — id свежего ответа, имени ядра которых не знает никто:
// класс не из таблицы ndms.KernelName, ни хук, ни прежний резолвер его не
// назвали, `interface-name` списка не годится. Порты коммутатора не
// спрашиваются: у них нет своего устройства ядра (см. ListAll). Снятые хуком,
// пока список был в полёте, — тоже: спросить их значит получить E.
func (s *InterfaceStore) unnamedLocked(raw map[string]ndms.Interface) []string {
	var todo []string
	for id, rec := range raw {
		if rec.Type == "Port" {
			continue
		}
		if _, ok := ndms.KernelName(id); ok {
			continue
		}
		if _, ok := s.sysNames[id]; ok {
			continue
		}
		iface, ok := s.byID[id]
		if !ok || trustedSystemName(id, iface.SystemName) {
			continue
		}
		todo = append(todo, id)
	}
	sort.Strings(todo)
	return todo
}

// applyListLocked кладёт свежий список поверх карты, не затирая id, тронутые
// хуками после начала запроса (start): хук новее списка. Отсутствующие в
// списке и не тронутые — удаляются. pending чистится по тому же правилу.
func (s *InterfaceStore) applyListLocked(raw map[string]ndms.Interface, wire map[string]json.RawMessage, start uint64) {
	now := time.Now()
	for id, rec := range raw {
		if s.touched[id] > start {
			// Хук новее списка — но только в своих полях. Прочие (описание,
			// маска, MTU, security-level…) хуки не несут никогда: берём из
			// списка, иначе переименование, совпавшее с хуком слоя, оставило
			// бы в карте старое описание (гейт владения OpkgTun, F517).
			if cur, ok := s.byID[id]; ok {
				*cur = mergeHookOwned(rec, cur)
			}
			continue
		}
		cp := rec
		s.byID[id] = &cp
		s.raw[id] = wire[id]
		// Часы аптайма ведёт демон; для уже поднятого интерфейса без них
		// восстанавливаем старт из Uptime NDMS — переживает рестарт демона.
		if cp.ConfLayer == "running" {
			if t, ok := s.startedAt[id]; !ok || t.IsZero() {
				if cp.Uptime > 0 {
					s.startedAt[id] = now.Add(-time.Duration(cp.Uptime) * time.Second)
				}
			}
		} else {
			delete(s.startedAt, id)
		}
	}
	for id := range s.byID {
		if _, ok := raw[id]; ok || s.touched[id] > start {
			continue
		}
		delete(s.byID, id)
		delete(s.startedAt, id)
		delete(s.raw, id)
	}
	// Имена — и тех id, что в карту так и не попали (имя пришло хуком).
	for id := range s.sysNames {
		if _, ok := raw[id]; !ok && s.touched[id] <= start {
			delete(s.sysNames, id)
		}
	}
	for id := range s.pending {
		if s.touched[id] <= start {
			delete(s.pending, id) // в списке — уже положен выше; нет — хук был ложным/поздним, не ждём
		}
	}
}

// mergeHookOwned — запись списка list с полями, которыми владеют хуки, из cur:
// OnLayerChanged (ConfLayer, Link, State, IPv4) и OnIPChanged (Address).
func mergeHookOwned(list ndms.Interface, cur *ndms.Interface) ndms.Interface {
	list.ConfLayer = cur.ConfLayer
	list.Link = cur.Link
	list.State = cur.State
	list.IPv4 = cur.IPv4
	list.Address = cur.Address
	return list
}

// markTouchedLocked отмечает id как тронутый хуком сейчас.
func (s *InterfaceStore) markTouchedLocked(id string) {
	s.seq++
	s.touched[id] = s.seq
}

// === Read paths ===

// Get returns a copy of the cached interface, or (nil, nil) if absent.
// Never issues HTTP for absent names — the map is the authoritative
// source of "what exists". Bootstrap (one HTTP) runs on first call.
func (s *InterfaceStore) Get(ctx context.Context, name string) (*ndms.Interface, error) {
	if err := s.freshenIfDirty(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	iface, ok := s.byID[name]
	if !ok {
		return nil, nil
	}
	cp := *iface
	return &cp, nil
}

// GetProxy is the Proxy-typed view of Get. Always returns a non-nil
// ProxyInfo (with Exists=false for absent interfaces) — matches the
// existing singbox.ProxyManager contract.
func (s *InterfaceStore) GetProxy(ctx context.Context, name string) (*ndms.ProxyInfo, error) {
	iface, err := s.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	if iface == nil {
		return &ndms.ProxyInfo{Name: name, Exists: false}, nil
	}
	return &ndms.ProxyInfo{
		Name:        iface.ID,
		Type:        iface.Type,
		Description: iface.Description,
		State:       iface.State,
		Link:        iface.Link,
		Up:          iface.State == "up",
		Exists:      true,
	}, nil
}

// GetDetails returns InterfaceDetails synthesised from the cached
// snapshot. Returns (nil, nil) when the interface is absent. Uptime is
// computed live from the daemon-tracked startedAt timestamp — survives
// daemon restarts (bootstrap re-derives startedAt from NDMS Uptime).
func (s *InterfaceStore) GetDetails(ctx context.Context, name string) (*ndms.InterfaceDetails, error) {
	if err := s.freshenIfDirty(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	iface, ok := s.byID[name]
	if !ok {
		return nil, nil
	}
	return detailsOf(iface, s.startedAt[name]), nil
}

// detailsOf — InterfaceDetails записи; Uptime — от часов демона started.
func detailsOf(iface *ndms.Interface, started time.Time) *ndms.InterfaceDetails {
	d := &ndms.InterfaceDetails{
		State:     iface.State,
		Link:      iface.Link,
		Connected: iface.Connected == "yes",
		ConfLayer: iface.ConfLayer,
	}
	if !started.IsZero() {
		d.Uptime = int(time.Since(started).Seconds())
	}
	return d
}

// Snapshot — записи и сырой JSON записей из последнего полного списка плюс
// правки хуков поверх записей. Неизменяем после выдачи.
type Snapshot struct {
	recs     map[string]ndms.Interface  // копии из byID (хуки применены)
	raw      map[string]json.RawMessage // запись списка как есть (пиры, ключи, счётчики)
	started  map[string]time.Time       // часы аптайма демона на момент снимка
	ListedAt time.Time                  // когда пришёл список
}

// Record — копия записи name; false — записи в снимке нет.
func (s *Snapshot) Record(name string) (ndms.Interface, bool) {
	rec, ok := s.recs[name]
	return rec, ok
}

// Records — копии всех записей снимка; порядок не определён.
func (s *Snapshot) Records() []ndms.Interface {
	out := make([]ndms.Interface, 0, len(s.recs))
	for _, rec := range s.recs {
		out = append(out, rec)
	}
	return out
}

// Raw — запись name из ответа списка как есть; false — её нет.
func (s *Snapshot) Raw(name string) (json.RawMessage, bool) {
	raw, ok := s.raw[name]
	return raw, ok && len(raw) > 0
}

// Details — то же, что GetDetails, по записи снимка; nil — записи нет.
func (s *Snapshot) Details(name string) *ndms.InterfaceDetails {
	rec, ok := s.recs[name]
	if !ok {
		return nil
	}
	return detailsOf(&rec, s.started[name])
}

// SnapshotRecent — возраст, до которого читатели «для показа и состояния»
// довольствуются прочитанным: равен stateCacheTTL сервиса туннелей (2 с).
// SnapshotLive — только что прочитанный список (пробы оркестратора).
const (
	SnapshotRecent = 2 * time.Second
	SnapshotLive   = 0
)

// SnapshotBackground — допуск возраста для фоновых поллеров в простое (R36):
// шаг простоя поллеров 60 с (+ дрожание тикера), и два поллера со сдвигом
// делят один список в минуту вместо своего на тик.
const SnapshotBackground = 65 * time.Second

type snapshotBackgroundKey struct{}

// WithSnapshotBackground — ctx фонового тика, когда панель не открыта ни у
// кого (решение владельца R36): чтения «не старше SnapshotRecent» под этим
// ctx принимают память до SnapshotBackground. Метка «грязно» (наша запись)
// по-прежнему даёт свежий список; SnapshotLive/Confirm допуском не
// расширяются — решения о мутациях по нему не принимаются.
func WithSnapshotBackground(ctx context.Context) context.Context {
	return context.WithValue(ctx, snapshotBackgroundKey{}, true)
}

// Snapshot — снимок не старше maxAge. Правила:
//  1. bootstrap, если карты ещё нет;
//  2. не грязно и список моложе maxAge → память, 0 RCI;
//  3. иначе — список; при maxAge > 0 вызывающий присоединяется к списку
//     в полёте, если тот начат НЕ РАНЬШЕ последней метки Invalidate
//     (ответ, начатый до нашей записи, не доказывает её результат);
//     при maxAge == 0 — всегда свой список (как Confirm).
//
// Список не прочитан — ошибка; по имени не спрашиваем (решение 4, F546).
// Снимок — не доказательство для мутаций: валюта мутаций — Confirmed.
func (s *InterfaceStore) Snapshot(ctx context.Context, maxAge time.Duration) (*Snapshot, error) {
	if err := s.freshen(ctx, maxAge); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked(), nil
}

// freshen — правила Snapshot без копии карты: после успешного возврата
// память не старше maxAge и не грязна (с точностью до записей после вызова).
//
// Присоединившийся к чужому полёту не наследует отмену чужого ctx: ведущий —
// HTTP-запрос панели, вкладку закрыли, его ctx отменён; у присоединившегося
// ctx жив — он читает свой список, а не отвечает «не прочитано».
func (s *InterfaceStore) freshen(ctx context.Context, maxAge time.Duration) error {
	if err := s.ensureBootstrap(ctx); err != nil {
		return err
	}
	if maxAge > 0 && maxAge < SnapshotBackground && ctx.Value(snapshotBackgroundKey{}) != nil {
		maxAge = SnapshotBackground
	}
	s.mu.Lock()
	for maxAge > 0 {
		if s.dirtyAt == 0 && time.Since(s.listedAt) < maxAge {
			s.mu.Unlock()
			return nil
		}
		fl := s.flight
		if fl == nil || fl.start < s.dirtyAt {
			break
		}
		s.mu.Unlock()
		s.log.Debugf("interface snapshot: joined list in flight")
		select {
		case <-fl.done:
		case <-ctx.Done():
			return ctx.Err()
		}
		if fl.err == nil {
			return nil
		}
		if !isCtxErr(fl.err) || ctx.Err() != nil {
			return fl.err
		}
		// Ведущего отменили: заново под mu — свежий снимок или полёт, начатый
		// другим присоединившимся; иначе полёт начинает этот (один на всех).
		s.mu.Lock()
	}
	fl := s.beginFlightLocked()
	s.mu.Unlock()
	_, err := s.refreshList(ctx, fl)
	return err
}

// isCtxErr — ошибка вызвана отменой или дедлайном ctx.
func isCtxErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// freshenIfDirty — перед чтением памяти: после нашей записи (метка
// Invalidate) — один свежий список (или присоединение к полёту), иначе поля,
// которых хуки не несут (Description, Mask, MTU, SecurityLevel), и link
// OpkgTun (#328) оставались бы прежними. Сбой списка — Warn и прежняя память,
// как у прежнего Invalidate с точечным чтением.
func (s *InterfaceStore) freshenIfDirty(ctx context.Context) error {
	if err := s.ensureBootstrap(ctx); err != nil {
		return err
	}
	s.mu.RLock()
	dirty := s.dirtyAt != 0
	s.mu.RUnlock()
	if dirty {
		if err := s.freshen(ctx, SnapshotRecent); err != nil {
			s.log.Warnf("interface list after write: %v", err)
		}
	}
	return nil
}

// snapshotLocked копирует карту в неизменяемый снимок (под mu, чтение).
func (s *InterfaceStore) snapshotLocked() *Snapshot {
	snap := &Snapshot{
		recs:     make(map[string]ndms.Interface, len(s.byID)),
		raw:      make(map[string]json.RawMessage, len(s.raw)),
		started:  make(map[string]time.Time, len(s.startedAt)),
		ListedAt: s.listedAt,
	}
	for id, rec := range s.byID {
		snap.recs[id] = *rec
	}
	for id, raw := range s.raw {
		snap.raw[id] = raw
	}
	for id, t := range s.startedAt {
		snap.started[id] = t
	}
	return snap
}

// DetailsRecent — Details(name) по памяти не старше SnapshotRecent;
// (nil, nil) — записи нет (запроса по имени нет вовсе).
func (s *InterfaceStore) DetailsRecent(ctx context.Context, name string) (*ndms.InterfaceDetails, error) {
	return s.detailsAged(ctx, name, SnapshotRecent)
}

// DetailsLive — Details(name) из только что прочитанного списка.
func (s *InterfaceStore) DetailsLive(ctx context.Context, name string) (*ndms.InterfaceDetails, error) {
	return s.detailsAged(ctx, name, SnapshotLive)
}

// detailsAged — одна запись под RLock после freshen, без копии всей карты.
func (s *InterfaceStore) detailsAged(ctx context.Context, name string, maxAge time.Duration) (*ndms.InterfaceDetails, error) {
	if err := s.freshen(ctx, maxAge); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	iface, ok := s.byID[name]
	if !ok {
		return nil, nil
	}
	return detailsOf(iface, s.startedAt[name]), nil
}

// ResolveSystemName returns the kernel interface name (e.g. "nwg0")
// for an NDMS id (e.g. "Wireguard0"). Чтение памяти, RCI — никогда (F570):
// запись могли снять после последнего списка, ifdestroyed доезжает секундами
// позже (F571), и вопрос по имени дал бы E «unable to find» в журнале ndm.
//
// NDMS list response (`/show/interface/`) populates the `interface-name`
// field for each entry, but the value is unreliable: for Wireguard system
// tunnels NDMS echoes the NDMS id back instead of the kernel name, on
// 5.02.A.11 it can be a label (`Bridge0` → `Home`, F473). Поэтому источники
// идут по порядку (systemNameLocked):
//   - ndms.KernelName — класс, у которого имя ядра задано номером записи
//     (Wireguard, OpkgTun, Proxy, PPPoE, Bridge); только для записи из кэша;
//   - sysNames — имя из хука NDMS (`system_name`, OnSystemName) или от
//     резолвера, который спрашивается один раз вслед за свежим списком
//     (refreshList → resolveSystemNames);
//   - `interface-name` списка, если ему можно верить (trustedSystemName).
//
// Пусто — записи нет в кэше или её имени не знает никто.
func (s *InterfaceStore) ResolveSystemName(ctx context.Context, ndmsName string) string {
	if ndmsName == "" || s.ensureBootstrap(ctx) != nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.systemNameLocked(ndmsName)
}

// systemNameLocked — имя ядра из памяти, см. ResolveSystemName.
//
// Для класса из таблицы живость устройства (/sys/class/net) не проверяется:
// прежний резолвер отвечал именем записи и при опущенном устройстве (ppp0
// лежащего PPPoE), и вызывающие (правило MASQUERADE на WAN, привязка
// sing-box) на этом стоят. Имя хука/резолвера — тоже без проверки: оно
// получено от NDMS для этого id.
func (s *InterfaceStore) systemNameLocked(id string) string {
	iface, known := s.byID[id]
	if name, ok := ndms.KernelName(id); ok {
		if !known {
			return ""
		}
		return name
	}
	if name := s.sysNames[id]; name != id && looksLikeKernelIfname(name) {
		return name
	}
	if known && trustedSystemName(id, iface.SystemName) {
		return iface.SystemName
	}
	return ""
}

// rememberSystemName запоминает ответ резолвера только для интерфейса, который
// есть в сторе: ifdestroyed между запросом и ответом резолвера иначе оставил
// бы имя удалённого интерфейса. Пустой ответ тоже запоминается — следующий
// список этот id не переспрашивает.
func (s *InterfaceStore) rememberSystemName(ndmsName, resolved string) {
	s.mu.Lock()
	if iface, ok := s.byID[ndmsName]; ok {
		s.sysNames[ndmsName] = resolved
		if resolved != "" {
			iface.SystemName = resolved
		}
	}
	s.mu.Unlock()
}

// trustedSystemName: non-empty, distinct from NDMS id, looks like a kernel
// name, AND exists in the running kernel. The last check defends against
// firmware quirks where the parser filter has already nominally accepted a
// value but the device is missing (hotplug races, label-typed values that
// happen to be lowercase).
func trustedSystemName(ndmsName, sysName string) bool {
	return sysName != "" && sysName != ndmsName &&
		looksLikeKernelIfname(sysName) &&
		kernelIfaceExists(sysName)
}

// resolveSystemNames спрашивает имена ядра для todo ОДНИМ пакетным POST (по
// одному — стенд: 22 запроса, ~0.6 с); один id — одиночной формой. Зовётся
// только из refreshList (TestResolver_OnlyAfterList). Сбой транспорта —
// ничего не запомнено, спросит следующий список.
func (s *InterfaceStore) resolveSystemNames(ctx context.Context, todo []string) {
	switch len(todo) {
	case 0:
		return
	case 1:
		if name, err := s.fetchSystemName(ctx, todo[0]); err == nil {
			s.rememberSystemName(todo[0], name)
		}
		return
	}
	batch := make([]any, len(todo))
	for i, id := range todo {
		batch[i] = transport.ShowQuery([]string{"interface", "system-name"}, map[string]any{"name": id})
	}
	raw, err := s.getter.Post(ctx, batch)
	if err != nil {
		return
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil || len(items) != len(todo) {
		return
	}
	for i, item := range items {
		s.rememberSystemName(todo[i], parseSystemName(item))
	}
}

// SystemNames — имена ядра для ids (id → имя; неизвестных в карте нет). То же,
// что ResolveSystemName по каждому id, под одним замком; RCI — никогда.
func (s *InterfaceStore) SystemNames(ctx context.Context, ids []string) map[string]string {
	out := make(map[string]string, len(ids))
	if len(ids) == 0 || s.ensureBootstrap(ctx) != nil {
		return out
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, id := range ids {
		if name := s.systemNameLocked(id); name != "" {
			out[id] = name
		}
	}
	return out
}

// fetchSystemName resolves an NDMS interface id to its kernel name via
// {"show":{"interface":{"system-name":{"name":X}}}} POST payload.
//
// Earlier this used GET /show/interface/system-name?name=X. NDMS treats
// slashes inside <X> as URL path separators, so names like
// "WifiMaster0/WifiStation0" or "GigabitEthernet0/Vlan2" came back with
// 'Core::Configurator: not found: "show/interface/system-name?name=..."'
// in the router log. The POST form carries
// the name inside the JSON body where the RCI parser handles it
// regardless of contained slashes.
//
// NDMS response shape (verified curl'd on 5.00.C.11):
//
//	{"show":{"interface":{"system-name":"apcli0"}}}
//
// Older firmware also produced bare "nwg0" or {"result":"nwg0"} for the
// GET form — kept as fallbacks for safety.
func (s *InterfaceStore) fetchSystemName(ctx context.Context, ndmsName string) (string, error) {
	payload := transport.ShowQuery(
		[]string{"interface", "system-name"},
		map[string]any{"name": ndmsName},
	)
	raw, err := s.getter.Post(ctx, payload)
	if err != nil {
		return "", err
	}
	return parseSystemName(raw), nil
}

// parseSystemName разбирает ответ резолвера (одиночный или элемент пакета).
func parseSystemName(raw []byte) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ""
	}

	// POST-form: walk into .show.interface."system-name"; value is the
	// bare kernel-name string.
	var wrap struct {
		Show struct {
			Interface struct {
				SystemName json.RawMessage `json:"system-name"`
			} `json:"interface"`
		} `json:"show"`
	}
	if err := json.Unmarshal(trimmed, &wrap); err == nil && len(wrap.Show.Interface.SystemName) > 0 {
		inner := bytes.TrimSpace(wrap.Show.Interface.SystemName)
		if len(inner) > 0 {
			if inner[0] == '"' {
				var str string
				if json.Unmarshal(inner, &str) == nil {
					return str
				}
			}
			if inner[0] == '{' {
				var resp struct {
					Result string `json:"result"`
				}
				if json.Unmarshal(inner, &resp) == nil {
					return resp.Result
				}
			}
		}
	}

	// Legacy GET-form fallbacks: bare string или {"result": "..."}.
	if trimmed[0] == '"' {
		var str string
		if json.Unmarshal(trimmed, &str) == nil {
			return str
		}
	}
	if trimmed[0] == '{' {
		var resp struct {
			Result string `json:"result"`
		}
		if json.Unmarshal(trimmed, &resp) == nil && resp.Result != "" {
			return resp.Result
		}
	}
	return ""
}

// List returns a snapshot of all interfaces. Returned slice is freshly
// allocated; callers may mutate it freely. Order is unstable (map
// iteration order); callers that need ordering must sort.
func (s *InterfaceStore) List(ctx context.Context) ([]ndms.Interface, error) {
	if err := s.freshenIfDirty(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ndms.Interface, 0, len(s.byID))
	for _, iface := range s.byID {
		out = append(out, *iface)
	}
	return out, nil
}

// LANBridge — LAN-сегмент (бридж) с подсетью, для выбора в LAN-forward.
// Description — человекочитаемое имя сегмента (NDMS description, напр. "LAN").
type LANBridge struct{ Name, Description, Address, Mask string }

// ListLANBridges возвращает LAN-бриджи (type=Bridge) с адресом/маской.
func (s *InterfaceStore) ListLANBridges(ctx context.Context) ([]LANBridge, error) {
	ifaces, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	out := []LANBridge{}
	for _, i := range ifaces {
		if !strings.EqualFold(i.Type, "Bridge") || i.Address == "" {
			continue
		}
		out = append(out, LANBridge{Name: i.ID, Description: i.Description, Address: i.Address, Mask: i.Mask})
	}
	return out, nil
}

// ListWAN returns public-facing WAN interfaces filtered for ISP use.
// Mirrors the legacy filter logic; reads everything from the cached
// snapshot. Kernel names come from ResolveSystemName — memory only, the
// resolver ran right after the list (see ResolveSystemName for details).
func (s *InterfaceStore) ListWAN(ctx context.Context) ([]wan.Interface, error) {
	all, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]wan.Interface, 0, len(all))
	for _, iface := range all {
		if iface.SecurityLevel != "public" {
			continue
		}
		kernelName := s.ResolveSystemName(ctx, iface.ID)
		if kernelName == "" {
			kernelName = iface.SystemName
		}
		if IsNonISPInterface(kernelName) {
			continue
		}
		out = append(out, wan.Interface{
			Name:     kernelName,
			ID:       iface.ID,
			Label:    wanInterfaceLabel(iface.Type, kernelName, iface.Description),
			Up:       iface.State == "up" && iface.IPv4 == "running",
			Priority: iface.Priority,
		})
	}
	return out, nil
}

// ListAll returns ALL router interfaces (no security-level filter),
// dropping awg-manager's own kernel interfaces (opkgtun*, awgm*).
// Sorted by Name for deterministic UI rendering. Uses
// ResolveSystemName for kernel-name lookup (see notes on ListWAN).
//
// Порты коммутатора (type Port) пропускаются: это не отдельное устройство
// ядра — NDMS резолвит их в имя родителя (`GigabitEthernet1/0` → eth3, как
// сам `GigabitEthernet1`), security-level у них нет.
//
// Deduplicates by kernel Name: if multiple NDMS entries resolve to the
// same kernel ifname (e.g. a stale stub from a failed bootstrap fetch
// coexists with the real entry, or WifiMaster0 and its AccessPoint0 both
// map to ra0), the winner is chosen by preferCandidate — the same one on
// every call. До F475 ничья решалась порядком обхода map, и у `eth3`
// security-level прыгал между public WAN и пустым портом — WAN случайно
// пропадал из списков привязки sing-box (они берут только public).
func (s *InterfaceStore) ListAll(ctx context.Context) ([]ndms.AllInterface, error) {
	listed, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	all := listed[:0]
	for _, iface := range listed {
		if iface.Type != "Port" {
			all = append(all, iface)
		}
	}
	seen := make(map[string]ndms.AllInterface, len(all))
	winnerID := make(map[string]string, len(all))
	for _, iface := range all {
		kernelName := s.ResolveSystemName(ctx, iface.ID)
		// Запасной путь оставлен намеренно: эхо метки (`Home`,
		// `GigabitEthernet0`) wireToInterface уже вычистил, сюда доходит
		// имя ядра отсутствующего сейчас устройства (выдернутый usb0).
		if kernelName == "" {
			kernelName = iface.SystemName
		}
		if kernelName == "" {
			continue
		}
		if isOwnTunnel(kernelName) {
			continue
		}
		candidate := ndms.AllInterface{
			Name:          kernelName,
			Label:         allInterfaceLabel(iface.Type, kernelName, iface.Description),
			Up:            iface.State == "up" && iface.IPv4 == "running",
			Type:          iface.Type,
			SecurityLevel: iface.SecurityLevel,
		}
		existing, dup := seen[kernelName]
		if !dup {
			seen[kernelName] = candidate
			winnerID[kernelName] = iface.ID
			continue
		}
		prevWinner := winnerID[kernelName]
		kept, dropped := prevWinner, iface.ID
		if preferCandidate(candidate, iface.ID, existing, prevWinner) {
			seen[kernelName] = candidate
			winnerID[kernelName] = iface.ID
			kept, dropped = iface.ID, prevWinner
		}
		s.log.Debugf("ListAll: duplicate kernel name %q from NDMS IDs %q and %q; kept %q", kernelName, kept, dropped, kept)
	}
	out := make([]ndms.AllInterface, 0, len(seen))
	for _, v := range seen {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// preferCandidate — побеждает ли кандидат (id) текущего победителя (winID)
// за одно имя ядра: поднятый, затем с security-level, затем меньший id.
func preferCandidate(c ndms.AllInterface, id string, win ndms.AllInterface, winID string) bool {
	if c.Up != win.Up {
		return c.Up
	}
	if (c.SecurityLevel != "") != (win.SecurityLevel != "") {
		return c.SecurityLevel != ""
	}
	return id < winID
}

// === Hook-side write API (called from events.Dispatcher) ===

// OnCreated — хук ifcreated. Никаких чтений: известный id — уже в карте;
// неизвестный ждёт ReconcilePending, который после пачки хуков читает ОДИН
// полный список. Точечное чтение здесь давало E «unable to find» на паре
// created→destroyed одного id (F546) и заглушки без Type на любой сбой.
func (s *InterfaceStore) OnCreated(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.markTouchedLocked(id)
	s.traceCreatedLocked(id)
	if _, known := s.byID[id]; known {
		return
	}
	s.pending[id] = struct{}{}
}

// traceCreatedLocked — след для ConfirmCreated: хук по имени пришёл, NDMS
// запись знает (F584).
func (s *InterfaceStore) traceCreatedLocked(id string) {
	if w, ok := s.awaiting[id]; ok {
		w.traced = true
	}
}

// Forget — запись снята (ifdestroyed или наш успешный `no interface`).
func (s *InterfaceStore) Forget(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.markTouchedLocked(id)
	if w, ok := s.awaiting[id]; ok {
		w.removed = true
		w.fire()
	}
	delete(s.byID, id)
	delete(s.startedAt, id)
	delete(s.sysNames, id)
	delete(s.pending, id)
	delete(s.raw, id)
}

// OnSystemName — имя ядра из хука NDMS (`system_name` есть в хуках, стенд
// 5.01.C.6: модель WAN строится по нему). Пишется всегда, даже для id, которого
// карта ещё не знает: на создание layer-хуки приходят раньше ifcreated. Снимают
// Forget и список без этого id (applyListLocked). Эхо id или подпись не
// кладутся: запись в sysNames снимает id с резолвера вслед за списком
// (unnamedLocked), и мусор закрыл бы ему имя навсегда.
func (s *InterfaceStore) OnSystemName(id, name string) {
	if name == id || !looksLikeKernelIfname(name) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sysNames[id] = name
}

// OnDestroyed — хук ifdestroyed; то же, что Forget.
func (s *InterfaceStore) OnDestroyed(id string) { s.Forget(id) }

// HasPending — есть id из хуков, которых ещё нет в карте.
func (s *InterfaceStore) HasPending() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.pending) > 0
}

// ReconcilePending — один полный список на пачку хуков, только если есть
// неизвестные id из хуков.
func (s *InterfaceStore) ReconcilePending(ctx context.Context) error {
	if !s.HasPending() {
		return nil
	}
	return s.refreshAll(ctx)
}

// OnLayerChanged handles iflayerchanged NDMS events. Patches the
// layer-specific field on the cached interface, mapping NDMS layer-
// state values (running/pending/disabled) to the field semantics each
// caller expects.
//
// Naming systems do NOT line up across layers:
//   - ConfLayer field uses NDMS layer-state words directly
//     ("running" / "disabled" / "pending") — the JSON shape and the
//     hook payload agree. Pass level through.
//   - Link field uses kernel link-status words ("up" / "down"). The
//     JSON `link` field is already mapped on the NDMS side; the hook
//     payload speaks layer-state, so we map ourselves: running=up,
//     anything else=down.
//   - State field is the overall interface-up flag and tracks the
//     ctrl layer the same way: running=up, anything else=down. ctrl
//     also gates startedAt (the uptime clock).
//   - IPv4 layer events store the level as-is into the IPv4 field (it
//     is layer-state, not up/down). IPv6 events produce no updates.
func (s *InterfaceStore) OnLayerChanged(id, layer, level string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Даже для незнакомого id: список в полёте не должен положить запись
	// старее хука — применится со следующим списком.
	s.markTouchedLocked(id)
	s.traceCreatedLocked(id)
	iface, ok := s.byID[id]
	if !ok {
		// Незнакомый id — запись есть, карта её не знает: на создание NDMS
		// шлёт iflayerchanged ctrl ×2 раньше ifcreated (~1 с, стенд
		// 5.01.C.6). В pending, как в OnCreated: ReconcilePending положит её
		// одним списком, Confirm подтвердит по ответу. Без этого хук,
		// пришедший, пока список Confirm в полёте, прятал запись: список её
		// не кладёт (хук новее), pending пуст — «записи нет», интерфейс
		// осиротел.
		s.pending[id] = struct{}{}
		return
	}
	switch layer {
	case "conf":
		iface.ConfLayer = level
	case "link":
		iface.Link = layerLevelToUpDown(level)
	case "ctrl":
		iface.State = layerLevelToUpDown(level)
		switch level {
		case "running":
			s.startedAt[id] = time.Now()
		case "disabled":
			delete(s.startedAt, id)
		}
	case "ipv4":
		// summary.layer.ipv4 maps the same way (running / pending /
		// disabled). Stored as-is — IPv4 string field semantically
		// IS layer-state, not up/down.
		iface.IPv4 = level
	}
}

// OnIPChanged handles ifipchanged NDMS events. Patches address only.
//
// Состояние линка сюда не приходит СОЗНАТЕЛЬНО: оно принадлежит ctrl-слою
// (OnLayerChanged), а поля up/connected из этого хука недостоверны —
// форвардер событий NDMS заполняет их не всегда, и доверие к ним затирало
// живые интерфейсы ложными "down"/"no". Раньше они принимались параметрами и
// выбрасывались внутри; параметр, который никто не читает, приглашает начать
// его читать, поэтому их здесь нет вовсе.
func (s *InterfaceStore) OnIPChanged(id, address string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.markTouchedLocked(id)
	s.traceCreatedLocked(id)
	iface, ok := s.byID[id]
	if !ok {
		s.pending[id] = struct{}{} // как в OnLayerChanged
		return
	}
	if address != "" {
		iface.Address = address
	}
}

// layerLevelToUpDown maps NDMS layer-state words to kernel up/down.
// "running" → "up"; everything else (pending, disabled, error, "") →
// "down".
func layerLevelToUpDown(level string) string {
	if level == "running" {
		return "up"
	}
	return "down"
}

// === Command-side write API (proactive refresh after a successful POST) ===

// Invalidate зовётся командами ПОСЛЕ успешной записи в NDMS: ставит метку
// «грязно», RCI в момент вызова нет. Следующий Snapshot читает ОДИН свежий
// список, начатый после метки, — так «следующее чтение видит результат моей
// записи» без чтения по имени (F546). Get/List и прочие чтения памяти метку не
// учитывают: они ведутся хуками. Имя — только для журнала.
func (s *InterfaceStore) Invalidate(name string) {
	if name == "" {
		return
	}
	s.mu.Lock()
	s.seq++
	s.dirtyAt = s.seq
	s.mu.Unlock()
	s.log.Debugf("Invalidate %s: next snapshot reads the list", name)
}

// InvalidateAll re-fetches the entire interface list from NDMS and
// applies it over the map (see applyListLocked). Called by command-side
// code after operations that affect multiple interfaces (e.g. Save, big
// admin changes).
func (s *InterfaceStore) InvalidateAll() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.refreshAll(ctx); err != nil {
		s.log.Warnf("InvalidateAll: refresh failed: %v", err)
	}
}

// Refresh — InvalidateAll с ответом: ошибка, если список не прочитан
// (диспетчер повторяет обновление после переполнения очереди, F572).
func (s *InterfaceStore) Refresh(ctx context.Context) error {
	return s.refreshAll(ctx)
}

// Confirmed — «запись name была в свежем полном списке NDMS». Единственная
// валюта мутаций по существующему интерфейсу: команда `interface X …` по
// отсутствующему X СОЗДАЁТ X (стенд 5.01/5.02), а ссылка на него из другого
// раздела пишет E в журнал ndm. Получить можно только здесь; в поля структур
// не класть — доказательство действительно для одного потока действий.
type Confirmed struct{ name string }

// Name — NDMS-имя записи.
func (c Confirmed) Name() string { return c.name }

// String — имя: %v/%+v печатают запись как `Interface:X`.
func (c Confirmed) String() string { return c.name }

// Confirm читает ОДИН полный список (кладёт его в карту) и подтверждает name
// по нему. Список не прочитан — ошибка: присутствие из кэша подтверждением
// не считается (F546). Запись — копия.
func (s *InterfaceStore) Confirm(ctx context.Context, name string) (Confirmed, *ndms.Interface, bool, error) {
	if name == "" {
		return Confirmed{}, nil, false, errors.New("confirm: пустое имя интерфейса")
	}
	raw, err := s.refreshList(ctx, nil)
	if err != nil {
		return Confirmed{}, nil, false, fmt.Errorf("confirm %s: %w", name, err)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.confirmedLocked(raw, name)
	if !ok {
		return Confirmed{}, nil, false, nil
	}
	return Confirmed{name: name}, rec, true, nil
}

// ErrNotListed — NDMS принял создание, по имени пришёл хук (запись NDMS
// знает), а в свежих списках её нет за всё ожидание ConfirmCreated (F584).
// Только в этом случае вызывающий вправе снести имя (command.ConfirmCreated).
var ErrNotListed = errors.New("NDMS принял создание, но записи нет в списке")

// ErrNotSeen — записи нет ни в списках, ни в хуках: знает ли её NDMS,
// неизвестно — сносить нельзя (снос отсутствующего — E в журнале ndm).
var ErrNotSeen = errors.New("NDMS принял создание, но записи нет ни в списке, ни в хуках")

// ErrCreatedThenRemoved — имя снято (ifdestroyed) во время ожидания: сносить
// нечего.
var ErrCreatedThenRemoved = errors.New("созданная запись снята до подтверждения")

// ConfirmCreated — Confirm только что созданной записи name с ограниченным
// ожиданием (F584): под нагрузкой NDMS отвечает на создание раньше, чем
// кладёт запись в список. Первый список — сразу; нет записи — ждёт, пока её
// принесёт список, начатый после вызова (ReconcilePending по хукам
// iflayerchanged/ifcreated), или свою паузу из createdBackoff и читает список
// сам. Доказательство то же, что у Confirm: запись в свежем полном списке.
// Список не прочитан — ошибка сразу (решение 4). Имя снято во время ожидания —
// ErrCreatedThenRemoved сразу. За все попытки записи нет — ErrNotListed, если
// по имени был хук (след), иначе ErrNotSeen.
func (s *InterfaceStore) ConfirmCreated(ctx context.Context, name string) (Confirmed, error) {
	if name == "" {
		return Confirmed{}, errors.New("confirm: пустое имя интерфейса")
	}
	s.mu.Lock()
	w := &createdWait{after: s.flights, ch: make(chan struct{})}
	// Хук мог прийти, пока шёл POST создания: имя ждёт в pending (снос его
	// оттуда убирает) — это тоже след.
	_, w.traced = s.pending[name]
	s.awaiting[name] = w
	backoff := s.createdBackoff
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.awaiting[name] == w {
			delete(s.awaiting, name)
		}
		s.mu.Unlock()
	}()
	// state — снимок полей ожидания под mu.
	state := func() (removed, traced bool) {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return w.removed, w.traced
	}
	wake := w.ch
	for i := 0; ; i++ {
		c, _, ok, err := s.Confirm(ctx, name)
		if err != nil {
			return Confirmed{}, err
		}
		if ok {
			return c, nil
		}
		removed, traced := state()
		switch {
		case removed:
			return Confirmed{}, fmt.Errorf("%w: %s", ErrCreatedThenRemoved, name)
		case i < len(backoff):
		case traced:
			return Confirmed{}, fmt.Errorf("%w: %s (%d списков)", ErrNotListed, name, i+1)
		default:
			return Confirmed{}, fmt.Errorf("%w: %s (%d списков)", ErrNotSeen, name, i+1)
		}
		t := time.NewTimer(backoff[i])
		select {
		case <-wake:
			t.Stop()
			wake = nil // будит один раз; дальше — паузы
			s.mu.RLock()
			removed, listed := w.removed, w.listed
			_, ok := s.confirmedLocked(map[string]ndms.Interface{name: w.rec}, name)
			s.mu.RUnlock()
			if removed {
				return Confirmed{}, fmt.Errorf("%w: %s", ErrCreatedThenRemoved, name)
			}
			if listed && ok {
				return Confirmed{name: name}, nil
			}
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return Confirmed{}, fmt.Errorf("confirm %s: %w", name, ctx.Err())
		}
	}
}

// ConfirmEach — Confirm для нескольких имён по одному списку. В ответе только
// подтверждённые.
func (s *InterfaceStore) ConfirmEach(ctx context.Context, names []string) (map[string]Confirmed, error) {
	raw, err := s.refreshList(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("confirm: %w", err)
	}
	out := make(map[string]Confirmed, len(names))
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, n := range names {
		if _, ok := s.confirmedLocked(raw, n); ok {
			out[n] = Confirmed{name: n}
		}
	}
	return out, nil
}

// ConfirmAll — Confirm для каждой записи одного свежего списка: кандидаты и
// доказательство из одного чтения (проверка занятости сетей по всем серверам).
func (s *InterfaceStore) ConfirmAll(ctx context.Context) (map[string]Confirmed, error) {
	raw, err := s.refreshList(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("confirm: %w", err)
	}
	out := make(map[string]Confirmed, len(raw))
	s.mu.RLock()
	defer s.mu.RUnlock()
	for n := range raw {
		if _, ok := s.confirmedLocked(raw, n); ok {
			out[n] = Confirmed{name: n}
		}
	}
	return out, nil
}

// confirmedLocked — копия записи, если name есть в свежем ответе raw и не
// снята хуком после него. В карте запись новее ответа (хуки её правят); нет в
// карте, но в pending — хук по ней (ifcreated, layer, ip) пришёл, пока список
// в полёте: карта её ещё не взяла, берём из ответа. Нет ни там, ни там —
// ifdestroyed новее ответа.
// Имени нет в ответе — не подтверждено, даже если оно в карте или в pending:
// доказательство — только свежий список.
func (s *InterfaceStore) confirmedLocked(raw map[string]ndms.Interface, name string) (*ndms.Interface, bool) {
	fresh, inList := raw[name]
	if !inList {
		return nil, false
	}
	if rec, ok := s.byID[name]; ok {
		cp := *rec
		return &cp, true
	}
	if _, ok := s.pending[name]; ok {
		return &fresh, true
	}
	return nil, false
}

// ErrGone — записи нет, и по этому имени NDMS не спрашивают (F546): её нет в
// снимке полного списка (WGServerStore.fetchItem/fetchConfig) или в дереве rc
// (PeersRC/PeersRCEach, ASC). Вызывающему — интерфейса нет.
var ErrGone = errors.New("ndms: interface gone")

// === Internal helpers ===

// fetchListMap GETs /show/interface/ and returns id → Interface plus
// id → запись ответа как есть. Used by bootstrap and InvalidateAll.
func (s *InterfaceStore) fetchListMap(ctx context.Context) (map[string]ndms.Interface, map[string]json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := s.getter.Get(ctx, "/show/interface/", &raw); err != nil {
		return nil, nil, fmt.Errorf("fetch interface list: %w", err)
	}
	out := make(map[string]ndms.Interface, len(raw))
	wire := make(map[string]json.RawMessage, len(raw))
	for id, data := range raw {
		iface, err := parseInterface(id, data)
		if err != nil {
			s.log.Warnf("parse interface %s: %v", id, err)
			continue
		}
		// Ключ — id записи, а не ключ ответа: порты коммутатора NDMS
		// отдаёт под ключами "0".."4" с id `GigabitEthernet0/0`… (стенд
		// 5.02.A.11). По ключу ответа их не находили ни Get, ни запоминание
		// имени резолвера — порты спрашивались на каждом ListAll (F473).
		out[iface.ID] = iface
		wire[iface.ID] = data
	}
	return out, wire, nil
}

// === Wire format ===

// ifaceWire is the shape /show/interface/ returns per entry.
type ifaceWire struct {
	ID            string `json:"id"`
	InterfaceName string `json:"interface-name"`
	Type          string `json:"type"`
	Description   string `json:"description"`
	State         string `json:"state"`
	Link          string `json:"link"`
	Connected     string `json:"connected"`
	SecurityLevel string `json:"security-level"`
	Address       string `json:"address"`
	Mask          string `json:"mask"`
	MTU           int    `json:"mtu"`
	Uptime        int64  `json:"uptime"`
	ConfLayer     string `json:"conf-layer"`
	Priority      int    `json:"priority"`
	Summary       struct {
		Layer struct {
			IPv4 string `json:"ipv4"`
			Conf string `json:"conf"`
		} `json:"layer"`
	} `json:"summary"`
}

func parseInterface(id string, data json.RawMessage) (ndms.Interface, error) {
	var w ifaceWire
	if err := json.Unmarshal(data, &w); err != nil {
		return ndms.Interface{}, err
	}
	if w.ID == "" {
		w.ID = id
	}
	return wireToInterface(w), nil
}

func wireToInterface(w ifaceWire) ndms.Interface {
	confLayer := w.ConfLayer
	if confLayer == "" {
		confLayer = w.Summary.Layer.Conf
	}
	// Drop interface-name values that are not syntactically kernel names.
	// On some Keenetic firmwares the list response sets `interface-name`
	// to a logical NDMS label (e.g. "ISP" for a physical port) rather than
	// the actual kernel device — that value would otherwise poison the
	// cache and slip past the ResolveSystemName echo-check. Leaving it
	// empty here forces ResolveSystemName to fall back to the dedicated
	// /show/interface/system-name resolver.
	sysName := w.InterfaceName
	if !looksLikeKernelIfname(sysName) {
		sysName = ""
	}
	return ndms.Interface{
		ID:            w.ID,
		SystemName:    sysName,
		Type:          w.Type,
		Description:   w.Description,
		State:         w.State,
		Link:          w.Link,
		Connected:     w.Connected,
		SecurityLevel: w.SecurityLevel,
		IPv4:          w.Summary.Layer.IPv4,
		Address:       w.Address,
		Mask:          w.Mask,
		MTU:           w.MTU,
		Uptime:        w.Uptime,
		ConfLayer:     confLayer,
		Priority:      w.Priority,
	}
}

// === Cached helpers (unchanged from previous implementation) ===

// IsNonISPInterface returns true for VPN/tunnel interface kernel names.
// These should not be treated as WAN regardless of security-level.
// Only excludes protocols that are NEVER used by ISPs:
//   - opkgtun/awg: our own managed tunnels
//   - wireguard/nwg/wg: WireGuard (Keenetic native or third-party)
//   - ipsec/sstp/openvpn: pure VPN protocols
//   - proxy/t2s: Keenetic sing-box proxy interfaces, depend on underlying WAN.
//     NDMS id is ProxyN but the hook's system_name carries the kernel name t2sN.
//
// NOT excluded (ISPs do use these): PPTP, L2TP, GRE, IPIP, EoIP, PPPoE, IPoE.
func IsNonISPInterface(name string) bool {
	n := strings.ToLower(name)
	return strings.HasPrefix(n, "opkgtun") ||
		strings.HasPrefix(n, "awg") ||
		strings.HasPrefix(n, "nwg") ||
		strings.HasPrefix(n, "wg") ||
		strings.HasPrefix(n, "wireguard") ||
		strings.HasPrefix(n, "ipsec") ||
		strings.HasPrefix(n, "sstp") ||
		strings.HasPrefix(n, "openvpn") ||
		strings.HasPrefix(n, "proxy") ||
		strings.HasPrefix(n, "t2s")
}

// isOwnTunnel returns true for interfaces owned by awg-manager itself
// (kernel names: opkgtun*, awgm*). Only excludes our tunnels, not other
// VPNs (user might want to route through them).
func isOwnTunnel(name string) bool {
	n := strings.ToLower(name)
	return strings.HasPrefix(n, "opkgtun") || strings.HasPrefix(n, "awgm")
}

// wanInterfaceLabel builds a human-readable label for the WAN interface list.
// If NDMS has a user-set description, it's used as the label.
// Otherwise, a label is generated from the interface type.
func wanInterfaceLabel(ifaceType, kernelName, description string) string {
	if description != "" && description != kernelName {
		return description
	}
	switch ifaceType {
	case "WifiStation":
		if strings.HasPrefix(kernelName, "WifiMaster1") {
			return "Wi-Fi клиент 5 ГГц"
		}
		return "Wi-Fi клиент 2.4 ГГц"
	case "GigabitEthernet":
		return "Ethernet"
	case "FastEthernet":
		return "Ethernet"
	case "PPPoE":
		return "PPPoE"
	case "PPTP":
		return "PPTP"
	case "L2TP":
		return "L2TP"
	case "IPoE":
		return "IPoE"
	case "UsbModem", "CdcEthernet", "UsbLte", "UsbQmi":
		return "USB-модем"
	case "Vlan":
		return "VLAN"
	}
	return kernelName
}

// allInterfaceLabel generates a label for any router interface.
func allInterfaceLabel(ifaceType, kernelName, description string) string {
	if description != "" && description != kernelName {
		return description
	}
	switch ifaceType {
	case "Bridge":
		return "Bridge"
	case "Loopback":
		return "Loopback"
	case "GigabitEthernet", "FastEthernet":
		return "Ethernet"
	case "WifiStation":
		if strings.HasPrefix(kernelName, "WifiMaster1") {
			return "Wi-Fi клиент 5 ГГц"
		}
		return "Wi-Fi клиент 2.4 ГГц"
	case "WifiMaster":
		return "Wi-Fi"
	case "PPPoE":
		return "PPPoE"
	case "PPTP":
		return "PPTP"
	case "L2TP":
		return "L2TP"
	case "IPoE":
		return "IPoE"
	case "UsbModem", "CdcEthernet", "UsbLte", "UsbQmi":
		return "USB-модем"
	case "Vlan":
		return "VLAN"
	}
	return kernelName
}
