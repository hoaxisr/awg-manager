// Package query — InterfaceStore implementation.
//
// Architecture: event-sourced cache. ONE bootstrap HTTP query
// (/show/interface/) populates an in-memory map. Subsequent state
// changes arrive as NDMS hooks (ifcreated / ifdestroyed /
// iflayerchanged / ifipchanged) and patch the map in place — no
// repolling. Read paths (Get / GetDetails / List / ResolveSystemName
// / ListWAN / ListAll) answer purely from the cached snapshot.
//
// Two write APIs feed the map:
//
//   - Hook-side (called from events.Dispatcher): OnCreated /
//     OnDestroyed / OnLayerChanged / OnIPChanged. Pure in-memory
//     mutators (OnCreated does ONE GET for the just-created interface
//     to get its initial snapshot — 404 impossible since the hook
//     fired AFTER NDMS finished creating). No probes for absent names.
//
//   - Command-side (called from internal/ndms/command/* and a few
//     admin handlers after a successful POST to NDMS): Invalidate(name)
//     and InvalidateAll(). These are now PROACTIVE-REFRESH: they
//     immediately re-fetch from NDMS and update the map. Callers use
//     them after a successful write so write→read consistency is
//     preserved without waiting for the eventual hook.
package query

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

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
	// sys is a derived view: NDMSName → kernel-system-name. Built
	// from byID on every mutation. Read paths take this under s.mu
	// (RLock) — no separate lock.
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

	raw, err := s.fetchListMap(ctx)
	if err != nil {
		return fmt.Errorf("interface bootstrap: %w", err)
	}
	now := time.Now()
	s.mu.Lock()
	s.byID = make(map[string]*ndms.Interface, len(raw))
	s.startedAt = make(map[string]time.Time, len(raw))
	for id, iface := range raw {
		cp := iface
		s.byID[id] = &cp
		// Restore startedAt from NDMS Uptime field for already-running
		// interfaces. This survives daemon restart: real connection
		// time is preserved (NDMS knows how long ago the interface
		// came up).
		if cp.Uptime > 0 && cp.ConfLayer == "running" {
			s.startedAt[id] = now.Add(-time.Duration(cp.Uptime) * time.Second)
		}
	}
	s.mu.Unlock()
	s.booted.Store(true)
	return nil
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

// HasIPv6Global reports whether the named interface has a global IPv6
// address. We don't carry the IPv6 addresses array in our cached
// Interface struct, so this still falls through to a single
// /show/interface/<name> probe — but ONLY when the interface exists
// in the map. Absent names short-circuit to false without HTTP, which
// is the entire reason this function exists in the first place (no
// 404 spam in router syslog).
func (s *InterfaceStore) HasIPv6Global(ctx context.Context, name string) bool {
	if err := s.ensureBootstrap(ctx); err != nil {
		return false
	}
	s.mu.RLock()
	_, ok := s.byID[name]
	s.mu.RUnlock()
	if !ok {
		return false
	}
	raw, err := s.getter.GetRaw(ctx, "/show/interface/"+name)
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return false
	}
	var probe struct {
		IPv6 struct {
			Addresses []struct {
				Global bool `json:"global"`
			} `json:"addresses"`
		} `json:"ipv6"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	for _, a := range probe.IPv6.Addresses {
		if a.Global {
			return true
		}
	}
	return false
}

// ResolveSystemName returns the kernel interface name (e.g. "nwg0")
// for an NDMS id (e.g. "Wireguard0"). Reads from the cached snapshot
// when possible — no HTTP on the hot path after first resolution.
//
// NDMS list response (`/show/interface/`) populates the
// `interface-name` field for each entry, but the value is unreliable:
// for Wireguard system tunnels (and likely other types) NDMS echoes
// the NDMS id back instead of the kernel name. Verified against
// production: list-response says `interface-name: "Wireguard0"`,
// per-name detail says the same, but `/show/interface/system-name?
// name=Wireguard0` returns the kernel name `"nwg0"`. The resolver is
// the only authoritative source.
//
// We treat the cached SystemName as garbage when it's empty OR when
// it equals the NDMS id (which signals NDMS handed us its own input
// back). Garbage triggers a one-shot resolver probe, memoised on the
// cached entry. The resolver does not 404 on missing names (returns
// an empty string), so no router-syslog noise is added.
func (s *InterfaceStore) ResolveSystemName(ctx context.Context, ndmsName string) string {
	if ndmsName == "" {
		return ""
	}
	if err := s.ensureBootstrap(ctx); err != nil {
		return ""
	}
	s.mu.RLock()
	var sysName string
	if iface, ok := s.byID[ndmsName]; ok {
		sysName = iface.SystemName
	}
	s.mu.RUnlock()

	// Trustworthy cached value: non-empty AND distinct from the NDMS
	// id we'd otherwise be querying for.
	if sysName != "" && sysName != ndmsName {
		return sysName
	}

	// Fallback: dedicated NDMS resolver endpoint.
	resolved := s.fetchSystemName(ctx, ndmsName)
	if resolved == "" {
		return ""
	}
	s.mu.Lock()
	if iface, ok := s.byID[ndmsName]; ok {
		iface.SystemName = resolved
	}
	s.mu.Unlock()
	return resolved
}

// fetchSystemName queries `/show/interface/system-name?name=X`. NDMS
// returns the kernel name as either a bare JSON string ("nwg0") or an
// object ({"result":"nwg0"}). Returns "" on any error or empty body.
func (s *InterfaceStore) fetchSystemName(ctx context.Context, ndmsName string) string {
	raw, err := s.getter.GetRaw(ctx, "/show/interface/system-name?name="+ndmsName)
	if err != nil {
		return ""
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ""
	}
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
		if json.Unmarshal(trimmed, &resp) == nil {
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

// ListWAN returns public-facing WAN interfaces filtered for ISP use.
// Mirrors the legacy filter logic; reads everything from the cached
// snapshot. Uses ResolveSystemName for kernel-name lookup so the
// fallback resolver kicks in when `interface-name` from the list
// response is unreliable (see ResolveSystemName for details).
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
func (s *InterfaceStore) ListAll(ctx context.Context) ([]ndms.AllInterface, error) {
	all, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ndms.AllInterface, 0, len(all))
	for _, iface := range all {
		kernelName := s.ResolveSystemName(ctx, iface.ID)
		if kernelName == "" {
			kernelName = iface.SystemName
		}
		if kernelName == "" {
			continue
		}
		if isOwnTunnel(kernelName) {
			continue
		}
		out = append(out, ndms.AllInterface{
			Name:  kernelName,
			Label: allInterfaceLabel(iface.Type, kernelName, iface.Description),
			Up:    iface.State == "up" && iface.IPv4 == "running",
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// === Hook-side write API (called from events.Dispatcher) ===

// OnCreated handles ifcreated NDMS events. Issues ONE HTTP
// (/show/interface/<id>) — 404 is impossible because NDMS sends
// ifcreated AFTER the interface exists. On HTTP failure, inserts a
// stub entry so existence checks return non-nil; subsequent
// OnLayerChanged / OnIPChanged events will fill in the rest.
func (s *InterfaceStore) OnCreated(ctx context.Context, id string) {
	if err := s.ensureBootstrap(ctx); err != nil {
		s.log.Warnf("OnCreated %s: bootstrap failed: %v", id, err)
		return
	}
	iface, err := s.fetchOne(ctx, id)
	if err != nil {
		s.log.Warnf("OnCreated %s: fetch failed, inserting stub: %v", id, err)
		s.mu.Lock()
		s.byID[id] = &ndms.Interface{ID: id}
		s.mu.Unlock()
		return
	}
	if iface == nil {
		// NDMS replied empty — race? interface gone before fetch?
		// Insert a stub so layer events have somewhere to land.
		s.mu.Lock()
		s.byID[id] = &ndms.Interface{ID: id}
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	s.byID[id] = iface
	if iface.Uptime > 0 && iface.ConfLayer == "running" {
		s.startedAt[id] = time.Now().Add(-time.Duration(iface.Uptime) * time.Second)
	}
	s.mu.Unlock()
}

// OnDestroyed handles ifdestroyed NDMS events. Pure in-memory delete.
func (s *InterfaceStore) OnDestroyed(id string) {
	s.mu.Lock()
	delete(s.byID, id)
	delete(s.startedAt, id)
	s.mu.Unlock()
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
//
// IPv4 / IPv6 layer events are accepted but currently produce no
// field updates — the existing summary-layer fields aren't part of
// any read path's hot loop yet. If they become hot, mirror the
// running→up mapping.
func (s *InterfaceStore) OnLayerChanged(id, layer, level string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	iface, ok := s.byID[id]
	if !ok {
		// Event for an interface we don't know — typically means we
		// missed an ifcreated. Skip; bootstrap or a future command-
		// side Invalidate will reconcile.
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
// State is owned by the ctrl layer (see OnLayerChanged); Connected is
// also a derived signal we don't trust from this hook payload alone
// because the NDMS event-script forwarder doesn't always populate up/
// connected fields, leading to spurious "down" / "no" overwrites of
// genuinely running interfaces.
func (s *InterfaceStore) OnIPChanged(id, address string, _, _ bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	iface, ok := s.byID[id]
	if !ok {
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

// Invalidate is called by command-side code AFTER a successful NDMS
// write to ensure the next read sees the new state without waiting
// for the eventual hook. Issues ONE HTTP (/show/interface/<name>) and
// patches the map. If the interface no longer exists in NDMS (200 +
// empty body), it is removed from the map.
//
// 404 is not expected here — command callers invoke this only after
// a successful POST, so the interface exists. If a 404 does arrive
// (e.g. a different actor deleted the interface concurrently), the
// HTTPError propagates as a logged warning and the map is left
// untouched (next bootstrap or hook will reconcile).
func (s *InterfaceStore) Invalidate(name string) {
	if name == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.ensureBootstrap(ctx); err != nil {
		s.log.Warnf("Invalidate %s: bootstrap failed: %v", name, err)
		return
	}
	iface, err := s.fetchOne(ctx, name)
	if err != nil {
		s.log.Warnf("Invalidate %s: refresh failed: %v", name, err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if iface == nil {
		// NDMS confirms absent — remove from map.
		delete(s.byID, name)
		delete(s.startedAt, name)
		return
	}
	s.byID[name] = iface
	if iface.Uptime > 0 && iface.ConfLayer == "running" {
		if _, exists := s.startedAt[name]; !exists {
			s.startedAt[name] = time.Now().Add(-time.Duration(iface.Uptime) * time.Second)
		}
	}
}

// InvalidateAll re-fetches the entire interface list from NDMS and
// rebuilds the map. Called by command-side code after operations that
// affect multiple interfaces (e.g. Save, big admin changes).
func (s *InterfaceStore) InvalidateAll() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := s.fetchListMap(ctx)
	if err != nil {
		s.log.Warnf("InvalidateAll: refresh failed: %v", err)
		return
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	// Replace map atomically. Preserve startedAt for interfaces still
	// present and running — uptime clock is daemon-tracked, not NDMS-
	// tracked. Drop startedAt for interfaces gone or stopped.
	nextByID := make(map[string]*ndms.Interface, len(raw))
	nextStartedAt := make(map[string]time.Time, len(raw))
	for id, iface := range raw {
		cp := iface
		nextByID[id] = &cp
		if cp.ConfLayer == "running" {
			if existing, ok := s.startedAt[id]; ok && !existing.IsZero() {
				nextStartedAt[id] = existing
			} else if cp.Uptime > 0 {
				nextStartedAt[id] = now.Add(-time.Duration(cp.Uptime) * time.Second)
			}
		}
	}
	s.byID = nextByID
	s.startedAt = nextStartedAt
	s.booted.Store(true)
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
		out[id] = iface
	}
	return out, nil
}

// fetchOne GETs /show/interface/<name> and parses the response.
// Returns (nil, nil) for empty body (NDMS-side absence). Errors are
// returned to the caller; HTTPError 404 (rare race condition) is
// returned as-is.
func (s *InterfaceStore) fetchOne(ctx context.Context, name string) (*ndms.Interface, error) {
	raw, err := s.getter.GetRaw(ctx, "/show/interface/"+name)
	if err != nil {
		return nil, fmt.Errorf("fetch interface %s: %w", name, err)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	var w ifaceWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("parse interface %s: %w", name, err)
	}
	if w.ID == "" && w.InterfaceName == "" {
		return nil, nil
	}
	if w.ID == "" {
		w.ID = name
	}
	iface := wireToInterface(w)
	return &iface, nil
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
	return ndms.Interface{
		ID:            w.ID,
		SystemName:    w.InterfaceName,
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
//   - proxy: Keenetic proxy interfaces (t2s), depend on underlying WAN
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
		strings.HasPrefix(n, "proxy")
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

