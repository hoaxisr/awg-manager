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
//     and InvalidateAll(). These are PROACTIVE-REFRESH: they
//     immediately re-fetch from NDMS and update the map. Callers use
//     them after a successful write so write→read consistency is
//     preserved without waiting for the eventual hook.
//
// Every hook bumps seq and stamps the id in touched; a list or point
// answer never overwrites an id touched after its request started — the
// hook is newer than the answer.
package query

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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

// unwrapShowInterface strips the {"show":{"interface":{…}}} envelope that
// the JSON-payload form of /show/interface returns. The GET path form
// returned the inner object directly; the POST form (which we use for any
// name that may contain a slash — Vlan, AccessPoint, numbered ports) wraps
// it. Callers receive the inner object so their existing decoders work
// unchanged.
//
// Returns nil for an empty body or an absent "interface" field — both map
// to the same "NDMS-side absence" semantics the previous GET-form
// already encoded with an empty body.
func unwrapShowInterface(raw []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	var w struct {
		Show struct {
			Interface json.RawMessage `json:"interface"`
		} `json:"show"`
	}
	if err := json.Unmarshal(trimmed, &w); err != nil {
		return nil, fmt.Errorf("decode show.interface envelope: %w", err)
	}
	inner := bytes.TrimSpace(w.Show.Interface)
	if len(inner) == 0 {
		return nil, nil
	}
	return inner, nil
}

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
	}
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
	_, err := s.refreshList(ctx)
	return err
}

// refreshList — refreshAll, возвращающий сам ответ NDMS: для подтверждения
// (Confirm) важен он, а не карта — seq-гард держит в pending имя, чей
// хук пришёл, пока список в полёте, хотя в ответе оно есть.
func (s *InterfaceStore) refreshList(ctx context.Context) (map[string]ndms.Interface, error) {
	s.mu.RLock()
	start := s.seq
	s.mu.RUnlock()
	raw, err := s.fetchListMap(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.applyListLocked(raw, start)
	todo := s.unnamedLocked(raw)
	s.mu.Unlock()
	s.booted.Store(true)
	// Единственный вызов резолвера (F570): только id из ответа, который пришёл
	// миллисекунды назад. По требованию имя не спрашивается никогда — к тому
	// моменту запись могли снять, и NDMS пишет E «unable to find».
	s.resolveSystemNames(ctx, todo)
	return raw, nil
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
func (s *InterfaceStore) applyListLocked(raw map[string]ndms.Interface, start uint64) {
	now := time.Now()
	for id, rec := range raw {
		if s.touched[id] > start {
			continue
		}
		cp := rec
		s.byID[id] = &cp
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
	if err := s.ensureBootstrap(ctx); err != nil {
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

// FetchSummary returns InterfaceDetails by issuing a fresh batch-POST
// show.interface query on every call; an interface absent from NDMS is
// answered without the point query (F546). Used by
// state.Manager for kernel-tunnel state determination because NDMS
// `iflayerchanged link=running` hooks are not reliable for OpkgTun:
// the cache that GetDetails consults can stay frozen with Link != "up"
// after `ip link set up`, producing a permanent StateStarting for a
// working tunnel. The direct query sees the layer truth NDMS reports
// right now.
//
// Uptime is consulted from the same daemon-tracked startedAt map as
// GetDetails (cache helper, not authoritative).
func (s *InterfaceStore) FetchSummary(ctx context.Context, name string) (*ndms.InterfaceDetails, error) {
	// Интерфейса нет в кэше — не спрашиваем: на запрос по отсутствующему
	// имени NDMS пишет E «unable to find» в свой журнал (F546). nil — тот же
	// ответ, что давал status-error NDMS.
	p, ok, err := s.Lookup(ctx, name)
	if err != nil || !ok {
		return nil, err
	}
	// Batch POST вместо прямого GET /summary: NDMS обрабатывает GET с
	// фиксированной стоимостью ~115мс независимо от размера ответа, POST
	// ~10x быстрее и коалесцируется батчером (замеры в спеке
	// 2026-06-10-getstate-cache-rci-post-design.md). Свежесть сохранена:
	// это по-прежнему прямой запрос к NDMS на каждый вызов, мимо кеша
	// снапшота.
	inner, err := s.showOne(ctx, p)
	if errors.Is(err, ErrGone) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var resp struct {
		State     string `json:"state"`
		Link      string `json:"link"`
		ConfLayer string `json:"conf-layer"`
		Summary   struct {
			Layer struct {
				Conf string `json:"conf"`
				Link string `json:"link"`
				Ctrl string `json:"ctrl"`
			} `json:"layer"`
		} `json:"summary"`
	}
	if len(inner) > 0 {
		if err := json.Unmarshal(inner, &resp); err != nil {
			return nil, err
		}
	}

	d := &ndms.InterfaceDetails{
		ConfLayer: resp.Summary.Layer.Conf,
		Link:      layerLevelToUpDown(resp.Summary.Layer.Link),
		State:     layerLevelToUpDown(resp.Summary.Layer.Ctrl),
	}
	if resp.Summary.Layer.Conf == "" {
		// Полный объект без summary-подсекции (или status-error на
		// отсутствующий интерфейс): берём верхнеуровневые поля.
		d.ConfLayer = resp.ConfLayer
		d.Link = resp.Link
		d.State = resp.State
	}
	if d.ConfLayer == "" && d.Link == "" && d.State == "" {
		// Ни данных, ни ошибки транспорта — интерфейса нет. nil details
		// = showInterfaceFailed в state-матрице (паритет с прежним 404).
		return nil, nil
	}

	s.mu.RLock()
	if t, ok := s.startedAt[name]; ok && !t.IsZero() {
		d.Uptime = int(time.Since(t).Seconds())
	}
	s.mu.RUnlock()
	return d, nil
}

// GetDetails returns InterfaceDetails synthesised from the cached
// snapshot. Returns (nil, nil) when the interface is absent. Uptime is
// computed live from the daemon-tracked startedAt timestamp — survives
// daemon restarts (bootstrap re-derives startedAt from NDMS Uptime).
func (s *InterfaceStore) GetDetails(ctx context.Context, name string) (*ndms.InterfaceDetails, error) {
	if err := s.ensureBootstrap(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	iface, ok := s.byID[name]
	if !ok {
		return nil, nil
	}
	d := &ndms.InterfaceDetails{
		State:     iface.State,
		Link:      iface.Link,
		Connected: iface.Connected == "yes",
		ConfLayer: iface.ConfLayer,
	}
	if t, ok := s.startedAt[name]; ok && !t.IsZero() {
		d.Uptime = int(time.Since(t).Seconds())
	}
	return d, nil
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
// in the router log. Same gotcha that showOne already solves by using
// POST — see the comment block on that function. The POST form carries
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
	if err := s.ensureBootstrap(ctx); err != nil {
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

// ListFresh — список интерфейсов, прочитанный с роутера сейчас, мимо карты
// событий и без её обновления. Для проверок перед записью (занятые сети #713):
// карта держится хуками NDMS, и пропущенный хук выкинул бы существующий
// интерфейс из проверки. Отказ RCI — ошибка.
func (s *InterfaceStore) ListFresh(ctx context.Context) ([]ndms.Interface, error) {
	raw, err := s.fetchListMap(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ndms.Interface, 0, len(raw))
	for _, iface := range raw {
		out = append(out, iface)
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
	if _, known := s.byID[id]; known {
		return
	}
	s.pending[id] = struct{}{}
}

// Forget — запись снята (ifdestroyed или наш успешный `no interface`).
func (s *InterfaceStore) Forget(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.markTouchedLocked(id)
	delete(s.byID, id)
	delete(s.startedAt, id)
	delete(s.sysNames, id)
	delete(s.pending, id)
}

// OnSystemName — имя ядра из хука NDMS (`system_name` есть в хуках, стенд
// 5.01.C.6: модель WAN строится по нему). Пишется всегда, даже для id, которого
// карта ещё не знает: на создание layer-хуки приходят раньше ifcreated. Снимают
// Forget и список без этого id (applyListLocked).
func (s *InterfaceStore) OnSystemName(id, name string) {
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

// Refresh reads the record as NDMS holds it RIGHT NOW, updates the cache
// from that answer (a known record point-wise, an unknown one through the
// whole fresh list — see below), and returns it. Use this
// instead of Get when the decision must reflect NDMS now rather than the
// last hook-driven snapshot: NDMS hooks (ifcreated/ifdestroyed/…) don't
// fire for an out-of-band edit like `interface OpkgTunN description …`,
// so Get can stay stale indefinitely (F532).
//
// A record the cache knows is read point-wise (`show interface <name>`).
// A record the cache doesn't know is looked up in a fresh full list
// instead: on a point read of an absent name NDMS writes E `unable to find
// "<name>"` into its own log (F546), while the list is silent and just as
// fresh — a record the cache missed (lost hook) is still found, so the
// ownership gate never mistakes a foreign record for an absent one (F517).
//
// Absent record → (nil, nil), and the entry is removed from the cache.
// Transport/parse error → error returned, cache left untouched — same
// contract Invalidate already had.
func (s *InterfaceStore) Refresh(ctx context.Context, name string) (*ndms.Interface, error) {
	if name == "" {
		return nil, nil
	}
	// start — до Lookup: хук, пришедший между проверкой кэша и ответом,
	// ответом не затирается.
	s.mu.RLock()
	start := s.seq
	s.mu.RUnlock()
	p, known, err := s.Lookup(ctx, name)
	if err != nil {
		return nil, err
	}
	if !known {
		// Весь список через applyListLocked: соседей, тронутых хуками, пока
		// шёл запрос, он не затирает.
		if err := s.refreshAll(ctx); err != nil {
			return nil, err
		}
		s.mu.RLock()
		defer s.mu.RUnlock()
		if rec, ok := s.byID[name]; ok {
			cp := *rec
			return &cp, nil
		}
		return nil, nil
	}
	iface, err := s.fetchOne(ctx, p)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.touched[name] > start {
		// Хук по name пришёл, пока шло чтение, — он новее ответа: карту не
		// трогаем. Записи в карте больше нет (ifdestroyed; ifcreated снова —
		// в pending) — прочитанное устарело, отвечаем «нет»: снос прокси и
		// шлюз владения OpkgTun иначе слали бы команды по снятому имени.
		_, inMap := s.byID[name]
		_, pending := s.pending[name]
		if iface == nil || (!inMap && !pending) {
			return nil, nil
		}
		cp := *iface
		return &cp, nil
	}
	if iface == nil {
		// NDMS confirms absent — remove from map.
		delete(s.byID, name)
		delete(s.startedAt, name)
		return nil, nil
	}
	s.byID[name] = iface
	if iface.Uptime > 0 && iface.ConfLayer == "running" {
		if _, exists := s.startedAt[name]; !exists {
			s.startedAt[name] = time.Now().Add(-time.Duration(iface.Uptime) * time.Second)
		}
	}
	cp := *iface
	return &cp, nil
}

// Invalidate is called by command-side code AFTER a successful NDMS
// write to ensure the next read sees the new state without waiting
// for the eventual hook. Thin wrapper over Refresh (5s timeout, own
// background context) that swallows the error into a Warn log — this
// is a fire-and-forget call, callers don't check the outcome.
//
// 404/"unable to find" is not expected here — command callers invoke
// this only after a successful POST, so the interface exists. If it
// does arrive anyway (e.g. a different actor deleted the interface
// concurrently), Refresh already treats it as "absent" and removes the
// entry; any other error is logged and the map is left untouched (next
// bootstrap or hook will reconcile).
func (s *InterfaceStore) Invalidate(name string) {
	if name == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.Refresh(ctx, name); err != nil {
		s.log.Warnf("Invalidate %s: refresh failed: %v", name, err)
	}
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

// Present — доказательство, что запись name была в кэше интерфейсов на момент
// Lookup. Поле неэкспортируемое: вне пакета его не сконструировать, поэтому
// точечное чтение по имени (showOne/showRC) без проверки кэша не собрать (F546).
type Present struct{ name string }

// Name — NDMS-имя записи.
func (p Present) Name() string { return p.name }

// String — имя: %v/%+v печатают запись как `Interface:X`.
func (p Present) String() string { return p.name }

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
	raw, err := s.refreshList(ctx)
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

// ConfirmEach — Confirm для нескольких имён по одному списку. В ответе только
// подтверждённые.
func (s *InterfaceStore) ConfirmEach(ctx context.Context, names []string) (map[string]Confirmed, error) {
	raw, err := s.refreshList(ctx)
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

// ErrGone — записи нет, и по этому имени NDMS не спрашивают. Два источника:
//   - showOne/showRC: NDMS ответил «записи нет» на точечное чтение записи,
//     которую кэш считал существующей (потерян ifdestroyed); запись из кэша
//     уже выселена;
//   - WGServerStore.present: записи нет в кэше — запроса не было вовсе.
//
// Вызывающему оба значат одно: интерфейса нет, читать по имени нельзя.
var ErrGone = errors.New("ndms: interface gone")

// Lookup — есть ли запись в кэше. Ошибка bootstrap — ошибка вызывающему:
// «не знаем» ≠ «спросим NDMS» (F546).
func (s *InterfaceStore) Lookup(ctx context.Context, name string) (Present, bool, error) {
	if name == "" {
		return Present{}, false, nil
	}
	if err := s.ensureBootstrap(ctx); err != nil {
		return Present{}, false, err
	}
	s.mu.RLock()
	_, ok := s.byID[name]
	s.mu.RUnlock()
	if !ok {
		return Present{}, false, nil
	}
	return Present{name: name}, true, nil
}

// showOne — ЕДИНСТВЕННЫЙ POST `show interface <name>` в демоне. Конверт
// 6553619 значит «записи нет»: кэш врал (потерян ifdestroyed) — выселяем и
// возвращаем ErrGone, второго чтения по этому имени уже не будет.
//
// POST, а не GET /show/interface/<name>: NDMS считает слэши в <name>
// разделителями пути — GigabitEthernet0/Vlan2, WifiMaster0/AccessPoint0 и
// нумерованные порты коммутатора получали бы 404. В теле JSON имя
// разбирается верно (internal/ndms/transport/payload.go).
//
// F532: на отсутствующую запись NDMS отвечает HTTP 200 с вложенным
// `{"status":[{"status":"error","code":...}]}`, а не верхнеуровневым
// конвертом, который ловит transport (стенд KN-1810, 5.02.A.11). «Записи нет»
// значит только код 6553619; любой другой — сбой NDMS («не знаем»), и за
// отсутствие его не выдаём: шлюз владения создал бы запись поверх
// существующей, а Refresh выселил бы живую.
//
// Пустой ответ — (nil, nil).
func (s *InterfaceStore) showOne(ctx context.Context, p Present) ([]byte, error) {
	raw, err := s.getter.Post(ctx, transport.ShowInterface(p.name, nil))
	if err != nil {
		return nil, fmt.Errorf("show interface %s: %w", p.name, err)
	}
	inner, err := unwrapShowInterface(raw)
	if err != nil {
		return nil, fmt.Errorf("show interface %s: %w", p.name, err)
	}
	if st := parseNestedStatusError(inner); st != nil {
		if st.Code == ndmsUnableToFindCode {
			s.Forget(p.name)
			return nil, fmt.Errorf("%s: %w", p.name, ErrGone)
		}
		return nil, fmt.Errorf("show interface %s: ndms status error %s: %s", p.name, st.Code, st.Message)
	}
	return inner, nil
}

// ShowRaw — ответ `show interface <name>` без конверта (см. showOne).
func (s *InterfaceStore) ShowRaw(ctx context.Context, p Present) ([]byte, error) {
	return s.showOne(ctx, p)
}

// showRC — ЕДИНСТВЕННЫЙ GET `/show/rc/interface/<name>…`. Путь идёт мимо
// батчера (transport.bypassBatch) и на отсутствующем отвечает 404 + E.
//
// Выселяет (ErrGone) только 404 на голом пути (suffix == ""): он значит «нет
// записи». 404 на поддереве (`/wireguard/asc`) бывает и у живого интерфейса,
// у которого этой секции нет, — это обычная ошибка, кэш не трогаем, иначе
// живая запись пропала бы из кэша до следующего списка.
func (s *InterfaceStore) showRC(ctx context.Context, p Present, suffix string, dst any) error {
	err := s.getter.Get(ctx, "/show/rc/interface/"+p.name+suffix, dst)
	var he *transport.HTTPError
	if suffix == "" && errors.As(err, &he) && he.Status == http.StatusNotFound {
		s.Forget(p.name)
		return fmt.Errorf("%s: %w", p.name, ErrGone)
	}
	return err
}

// === Internal helpers ===

// fetchListMap GETs /show/interface/ and returns the raw map id →
// Interface. Used by bootstrap and InvalidateAll.
func (s *InterfaceStore) fetchListMap(ctx context.Context) (map[string]ndms.Interface, error) {
	var raw map[string]json.RawMessage
	if err := s.getter.Get(ctx, "/show/interface/", &raw); err != nil {
		return nil, fmt.Errorf("fetch interface list: %w", err)
	}
	out := make(map[string]ndms.Interface, len(raw))
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
	}
	return out, nil
}

// fetchOne читает запись p через showOne и разбирает её. Записи нет (пустой
// ответ или ErrGone — showOne уже выселил) — (nil, nil).
func (s *InterfaceStore) fetchOne(ctx context.Context, p Present) (*ndms.Interface, error) {
	inner, err := s.showOne(ctx, p)
	if errors.Is(err, ErrGone) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(inner) == 0 {
		return nil, nil
	}
	var w ifaceWire
	if err := json.Unmarshal(inner, &w); err != nil {
		return nil, fmt.Errorf("parse interface %s: %w", p.name, err)
	}
	if w.ID == "" && w.InterfaceName == "" {
		return nil, nil
	}
	if w.ID == "" {
		w.ID = p.name
	}
	iface := wireToInterface(w)
	return &iface, nil
}

// ndmsUnableToFindCode — код NDMS-конверта "unable to find" (стенд
// KN-1810, 5.02.A.11): единственное значение code, которое означает
// «записи нет», а не «запрос не удался».
const ndmsUnableToFindCode = "6553619"

// ndmsStatusError is one `{"status":"error",...}` element of a nested
// NDMS status array — the shape this POST form wraps into `show.interface`
// on failure, distinct from the top-level status envelope
// transport.ExtractError checks.
type ndmsStatusError struct {
	Code    string
	Message string
}

// parseNestedStatusError reports the first `status: "error"` entry of a
// `{"status":[...]}` array at the top of inner, or nil if inner isn't
// that shape (a normal interface object has no top-level "status" field
// of this form, so this never misfires on a real record).
func parseNestedStatusError(inner []byte) *ndmsStatusError {
	var w struct {
		Status []struct {
			Status  string          `json:"status"`
			Code    json.RawMessage `json:"code"` // строка у стенда; число тоже принимаем
			Message string          `json:"message"`
		} `json:"status"`
	}
	if json.Unmarshal(inner, &w) != nil {
		return nil
	}
	for _, s := range w.Status {
		if s.Status == "error" {
			return &ndmsStatusError{Code: strings.Trim(string(s.Code), `"`), Message: s.Message}
		}
	}
	return nil
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
