package router

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// ---------------------------------------------------------------------------
// Recording fakes — every method appends an ordered entry to a shared *callLog
// so tests can assert the exact provisioning sequence and the rollback inverse.
// ---------------------------------------------------------------------------

type callLog struct {
	calls []string
}

func (l *callLog) add(s string) { l.calls = append(l.calls, s) }

// idxOf returns the position of the first call equal to want, or -1.
func (l *callLog) idxOf(want string) int {
	for i, c := range l.calls {
		if c == want {
			return i
		}
	}
	return -1
}

func (l *callLog) has(want string) bool { return l.idxOf(want) >= 0 }

// failAt names a single call (by its recorded label) that should return an
// injected error; "" disables injection.
type recOpkgTun struct {
	log    *callLog
	failAt string
}

func (r *recOpkgTun) maybeFail(label string) error {
	if r.failAt == label {
		return errors.New("injected: " + label)
	}
	return nil
}

func (r *recOpkgTun) CreateOpkgTunWithSecurityLevel(_ context.Context, name, _, level string) error {
	r.log.add("Create:" + name + ":" + level)
	return r.maybeFail("Create")
}
func (r *recOpkgTun) SetIPGlobal(_ context.Context, name string) error {
	r.log.add("SetIPGlobal:" + name)
	return r.maybeFail("SetIPGlobal")
}
func (r *recOpkgTun) DeleteOpkgTun(_ context.Context, name string) error {
	r.log.add("Delete:" + name)
	return nil
}
func (r *recOpkgTun) SetAddress(_ context.Context, name, addr, mask string) error {
	r.log.add("SetAddress:" + name + ":" + addr + ":" + mask)
	return r.maybeFail("SetAddress")
}
func (r *recOpkgTun) SetIPv6Address(_ context.Context, name, addr string) error {
	r.log.add("SetIPv6Address:" + name + ":" + addr)
	return r.maybeFail("SetIPv6Address")
}
func (r *recOpkgTun) ClearIPv6Address(_ context.Context, name string) error {
	r.log.add("ClearIPv6Address:" + name)
	return nil
}
func (r *recOpkgTun) SetMTU(_ context.Context, name string, mtu int) error {
	r.log.add("SetMTU:" + name + ":" + strconv.Itoa(mtu))
	return r.maybeFail("SetMTU")
}
func (r *recOpkgTun) InterfaceUp(_ context.Context, name string) error {
	r.log.add("InterfaceUp:" + name)
	return r.maybeFail("InterfaceUp")
}
func (r *recOpkgTun) InterfaceDown(_ context.Context, name string) error {
	r.log.add("InterfaceDown:" + name)
	return nil
}

type recStaticRoutes struct {
	log    *callLog
	failAt string
}

func (r *recStaticRoutes) AddStaticRoute(_ context.Context, route StaticRouteSpec) error {
	if route.V6 {
		r.log.add("AddRoute6:" + route.Network + ":" + route.Interface)
		if r.failAt == "AddRoute6" {
			return errors.New("injected: AddRoute6")
		}
		return nil
	}
	if route.Reject {
		// The reject route is a kill-switch FLAG renewed onto the pool→OpkgTun
		// route — it carries the NDMS Interface (stand-verified), so record it.
		r.log.add("AddRejectRoute:" + route.Network + ":" + route.Mask + ":" + route.Interface)
		if r.failAt == "AddRejectRoute" {
			return errors.New("injected: AddRejectRoute")
		}
		return nil
	}
	r.log.add("AddRoute:" + route.Network + ":" + route.Mask + ":" + route.Interface)
	if r.failAt == "AddRoute" {
		return errors.New("injected: AddRoute")
	}
	return nil
}
func (r *recStaticRoutes) RemoveStaticRoute(_ context.Context, route StaticRouteSpec) error {
	if route.V6 {
		r.log.add("RemoveRoute6:" + route.Network + ":" + route.Interface)
		return nil
	}
	if route.Reject {
		r.log.add("RemoveRejectRoute:" + route.Network + ":" + route.Mask + ":" + route.Interface)
		if r.failAt == "RemoveRejectRoute" {
			return errors.New("injected: RemoveRejectRoute")
		}
		return nil
	}
	r.log.add("RemoveRoute:" + route.Network + ":" + route.Interface)
	if r.failAt == "RemoveRoute" {
		return errors.New("injected: RemoveRoute")
	}
	return nil
}

type recIndices struct {
	live map[int]bool
}

func (r *recIndices) LiveOpkgTunIndices(context.Context) (map[int]bool, error) {
	return r.live, nil
}

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

// fakeIPEnableHarness bundles an orch-backed service wired with recording fakes
// and the shared call log. It seeds RoutingMode=fakeip-tun and a router config
// carrying a proxy outbound + route.final so the egress check passes.
type fakeIPEnableHarness struct {
	svc    *ServiceImpl
	log    *callLog
	opkg   *recOpkgTun
	routes *recStaticRoutes
	store  *storage.SettingsStore
	dir    string
}

func newFakeIPEnableHarness(t *testing.T, failAt string) *fakeIPEnableHarness {
	t.Helper()
	svc, dir := newOrchedTestService(t)

	// RoutingMode=fakeip-tun in settings.
	store := svc.deps.Settings
	all, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	all.SingboxRouter = storage.SingboxRouterSettings{RoutingMode: "fakeip-tun", WANAutoDetect: true}
	if err := store.Save(all); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Seed a router config with a proxy outbound + route.final so loadRouterConfig
	// returns a usable egress. Written to the active slot file (LoadEffective reads it).
	routerCfg := `{"outbounds":[{"tag":"proxy-out","type":"socks","server":"1.2.3.4"},{"tag":"direct","type":"direct"}],"route":{"final":"proxy-out","rules":[]}}`
	if err := os.WriteFile(filepath.Join(dir, "20-router.json"), []byte(routerCfg), 0644); err != nil {
		t.Fatalf("write router cfg: %v", err)
	}

	log := &callLog{}
	opkg := &recOpkgTun{log: log, failAt: failAt}
	routes := &recStaticRoutes{log: log, failAt: failAt}

	singbox := newTestSingbox(t)
	singbox.dir = dir
	singbox.isRunningFn = func() (bool, int) { return true, 1234 }
	svc.deps.Singbox = singbox

	svc.deps.OpkgTun = opkg
	svc.deps.StaticRoutes = routes
	svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{}}
	svc.deps.FakeIPTun = DefaultFakeIPTunParams()
	svc.deps.FakeIPTun.CachePath = filepath.Join(dir, "cache.db")

	// fakeip readiness probes → ready; flush records into the log.
	stubTunReadyProbe(t, func(string) bool { return true })
	stubFakeIPDNSProbe(t, func(context.Context, string, netip.Prefix) bool { return true })
	old := fakeIPAddrFlush
	fakeIPAddrFlush = func(_ context.Context, iface string) error {
		log.add("Flush:" + iface)
		if failAt == "Flush" {
			return errors.New("injected: Flush")
		}
		return nil
	}
	t.Cleanup(func() { fakeIPAddrFlush = old })

	return &fakeIPEnableHarness{
		svc: svc, log: log, opkg: opkg, routes: routes, store: store, dir: dir,
	}
}

func (h *fakeIPEnableHarness) loadFakeIP(t *testing.T) *storage.FakeIPState {
	t.Helper()
	all, err := h.store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return all.FakeIP
}

// ---------------------------------------------------------------------------
// Happy path: dispatch + ordering
// ---------------------------------------------------------------------------

func TestEnable_DispatchesFakeIPTun(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(fakeip-tun): %v", err)
	}

	// Index 0 is the lowest free. NDMS RCI ops take the CamelCase OpkgTun0
	// (stand-verified — NDMS rejects the lowercase kernel name); sing-box / kernel
	// sites (flush) take the lowercase opkgtun0.
	const ndmsName = "OpkgTun0"
	const iface = "opkgtun0"

	// Persist FakeIP state must land BEFORE the iface is created.
	st := h.loadFakeIP(t)
	if st == nil || !st.Provisioned || st.Index != 0 {
		t.Fatalf("FakeIP persist = %+v, want provisioned index 0", st)
	}
	if st.Inet4Range != "198.18.0.0/15" || st.Inet6Range != "fc00::/18" {
		t.Errorf("FakeIP ranges = %q/%q, want pool defaults", st.Inet4Range, st.Inet6Range)
	}

	// Ordered sequence assertions.
	mustOrder := func(a, b string) {
		ia, ib := h.log.idxOf(a), h.log.idxOf(b)
		if ia < 0 {
			t.Fatalf("missing call %q in %v", a, h.log.calls)
		}
		if ib < 0 {
			t.Fatalf("missing call %q in %v", b, h.log.calls)
		}
		if ia >= ib {
			t.Errorf("expected %q (#%d) before %q (#%d): %v", a, ia, b, ib, h.log.calls)
		}
	}

	// Bug 1 guard: the NDMS interface name MUST be CamelCase OpkgTun0, NOT the
	// lowercase kernel name (which NDMS rejects with "unsupported interface type").
	createCall := "Create:" + ndmsName + ":private"
	if !h.log.has(createCall) {
		t.Fatalf("Create with NDMS CamelCase name + private security-level missing: %v", h.log.calls)
	}
	if h.log.has("Create:" + iface + ":private") {
		t.Fatalf("Create used the lowercase kernel name (NDMS would reject it): %v", h.log.calls)
	}
	// sing-box / kernel sites use the lowercase kernel name (flush).
	if !h.log.has("Flush:" + iface) {
		t.Fatalf("Flush must use the lowercase kernel name %q: %v", iface, h.log.calls)
	}
	// The pool route Interface is the NDMS name.
	if !h.log.has("AddRoute:198.18.0.0:255.254.0.0:" + ndmsName) {
		t.Fatalf("pool route Interface must be the NDMS name %q: %v", ndmsName, h.log.calls)
	}
	// SetIPGlobal must NOT be called: steering is via specific pool/CIDR routes,
	// not access-policy exit (policy-exit model abandoned). The tun is private.
	if h.log.has("SetIPGlobal:" + ndmsName) {
		t.Errorf("fakeip must NOT set ip global (no policy-exit), got %v", h.log.calls)
	}
	mustOrder(createCall, "SetAddress:"+ndmsName+":172.18.0.1:255.255.255.252")
	mustOrder("SetAddress:"+ndmsName+":172.18.0.1:255.255.255.252", "SetMTU:"+ndmsName+":1500")
	// v6: SetIPv6Address is driven (defaults carry TunAddr6) and lands after the
	// v4 SetAddress, before SetMTU.
	mustOrder("SetAddress:"+ndmsName+":172.18.0.1:255.255.255.252", "SetIPv6Address:"+ndmsName+":fdfe:dcba:9876::1")
	mustOrder("SetIPv6Address:"+ndmsName+":fdfe:dcba:9876::1", "SetMTU:"+ndmsName+":1500")
	// v6 pool route is added (defaults carry Inet6Range) after the v4 pool route.
	mustOrder("AddRoute:198.18.0.0:255.254.0.0:"+ndmsName, "AddRoute6:fc00::/18:"+ndmsName)
	mustOrder("SetMTU:"+ndmsName+":1500", "InterfaceUp:"+ndmsName)
	// Flush runs PRE-start (right after iface up + config build), clearing stale
	// addrs before sing-box attaches the gvisor tun.
	mustOrder("InterfaceUp:"+ndmsName, "Flush:"+iface)
	// The pool route is installed POST-readiness (after the flush and the stubbed
	// waitForSingbox). No tun default route is installed — pool/CIDR traffic reaches
	// the tun via specific routes; everything else egresses the normal WAN default.
	mustOrder("Flush:"+iface, "AddRoute:198.18.0.0:255.254.0.0:"+ndmsName)
	mustOrder("AddRoute:198.18.0.0:255.254.0.0:"+ndmsName, "AddRoute6:fc00::/18:"+ndmsName)

	// The v6 pool route must be the LAST provisioning call (no DHCP advertise).
	last := h.log.calls[len(h.log.calls)-1]
	if last != "AddRoute6:fc00::/18:"+ndmsName {
		t.Errorf("last call = %q, want AddRoute6 last", last)
	}

	// SingboxRouter.Enabled persisted true.
	all, _ := h.store.Load()
	if !all.SingboxRouter.Enabled {
		t.Error("SingboxRouter.Enabled must be true after Enable")
	}
}

// TestEnableFakeIPTun_SlotFakeIPWritten asserts the new enable contract:
//   - SlotFakeIP is ENABLED and SlotRouter is DISABLED (XOR) after a successful enable.
//   - The persisted active file is 21-fakeip.json, not 20-router.json.
//   - 21-fakeip.json contains the engine-locked overlay bits (tun-in, fakeip DNS
//     server, hijack-dns at route.rules[0]) and the seeded A/AAAA→fakeip DNS rule.
//   - route.final is set (seed provides "direct").
//   - SlotRouter was NOT promoted by enable (20-router.json is unchanged).
func TestEnableFakeIPTun_SlotFakeIPWritten(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(fakeip-tun): %v", err)
	}

	// --- Slot XOR: SlotFakeIP on, SlotRouter off ---
	orch := h.svc.deps.Orch
	snap := orch.Snapshot()
	var fakeIPEnabled, routerEnabled bool
	for _, s := range snap {
		switch s.Slot {
		case "fakeip":
			fakeIPEnabled = s.Enabled
		case "router":
			routerEnabled = s.Enabled
		}
	}
	if !fakeIPEnabled {
		t.Error("SlotFakeIP must be ENABLED after enable")
	}
	if routerEnabled {
		t.Error("SlotRouter must be DISABLED (XOR) after fakeip enable")
	}

	// --- 21-fakeip.json exists and has the locked bits + seed ---
	fakeIPPath := filepath.Join(h.dir, "21-fakeip.json")
	data, err := os.ReadFile(fakeIPPath)
	if err != nil {
		t.Fatalf("21-fakeip.json missing: %v", err)
	}
	var cfg RouterConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal 21-fakeip.json: %v", err)
	}

	// tun-in inbound present (overlay).
	found := false
	for _, in := range cfg.Inbounds {
		if in.Tag == "tun-in" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("21-fakeip.json: tun-in inbound missing: %s", data)
	}

	// fakeip DNS server present (overlay).
	hasFakeipServer := false
	for _, sv := range cfg.DNS.Servers {
		if sv.Type == "fakeip" {
			hasFakeipServer = true
			break
		}
	}
	if !hasFakeipServer {
		t.Errorf("21-fakeip.json: fakeip DNS server missing: %s", data)
	}

	// hijack-dns rule at rules[0] (overlay).
	if len(cfg.Route.Rules) == 0 || cfg.Route.Rules[0].Action != "hijack-dns" {
		t.Errorf("21-fakeip.json: route.rules[0] must be hijack-dns, got %+v", cfg.Route.Rules)
	}

	// route.final set (seed provides "direct" or user-set).
	if cfg.Route.Final == "" {
		t.Errorf("21-fakeip.json: route.final must not be empty after enable")
	}

	// seeded A/AAAA→fakeip DNS rule present.
	hasAAAAARule := false
	for _, r := range cfg.DNS.Rules {
		if r.Action == "route" && r.Server == "fakeip" {
			for _, qt := range r.QueryType {
				if qt == "A" || qt == "AAAA" {
					hasAAAAARule = true
					break
				}
			}
		}
	}
	if !hasAAAAARule {
		t.Errorf("21-fakeip.json: seeded A/AAAA→fakeip DNS rule missing: %s", data)
	}

	// 20-router.json was NOT promoted (still the file we seeded the harness with,
	// not overwritten by fakeip enable).
	routerData, err := os.ReadFile(filepath.Join(h.dir, "20-router.json"))
	if err != nil {
		t.Fatalf("20-router.json missing: %v", err)
	}
	var routerCfg RouterConfig
	if err := json.Unmarshal(routerData, &routerCfg); err != nil {
		t.Fatalf("unmarshal 20-router.json: %v", err)
	}
	// The harness seeds the router config with a proxy outbound and no fakeip overlay.
	// After enable, 20-router.json must NOT contain tun-in (fakeip enable must not touch it).
	for _, in := range routerCfg.Inbounds {
		if in.Tag == "tun-in" {
			t.Errorf("20-router.json must not be modified by fakeip enable (found tun-in): %s", routerData)
		}
	}
}

// TestEnableFakeIPTun_UsesPersistedEngineSettings asserts the user-editable
// fakeip engine settings (stack/pool4/pool6/MTU) override the wired defaults and
// flow into the NDMS calls + the persisted sing-box config.
func TestEnableFakeIPTun_UsesPersistedEngineSettings(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")

	// Persist custom fakeip engine settings (normalized through the real pipeline).
	store := h.svc.deps.Settings
	all, _ := store.Load()
	all.SingboxRouter = storage.SingboxRouterSettings{
		RoutingMode:   "fakeip-tun",
		WANAutoDetect: true,
		FakeIPStack:   "system",
		FakeIPPool4:   "10.64.0.0/12",
		FakeIPPool6:   "fc00::/7",
		FakeIPMTU:     1280,
	}
	normalized, err := NormalizeSingboxRouterSettings(all.SingboxRouter)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	all.SingboxRouter = normalized
	if err := store.Save(all); err != nil {
		t.Fatalf("save: %v", err)
	}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}

	const ndmsName = "OpkgTun0"
	// MTU override flows into the NDMS SetMTU call.
	if !h.log.has("SetMTU:" + ndmsName + ":1280") {
		t.Errorf("SetMTU did not use overridden MTU 1280: %v", h.log.calls)
	}
	// The v4 pool override flows into the pool auto-route (10.64.0.0/12 → mask /12).
	if !h.log.has("AddRoute:10.64.0.0:255.240.0.0:" + ndmsName) {
		t.Errorf("pool route did not use overridden pool4: %v", h.log.calls)
	}
	// The v6 pool override flows into the v6 pool route.
	if !h.log.has("AddRoute6:fc00::/7:" + ndmsName) {
		t.Errorf("v6 pool route did not use overridden pool6: %v", h.log.calls)
	}
	// Persisted FakeIP state records the overridden ranges.
	st := h.loadFakeIP(t)
	if st == nil || st.Inet4Range != "10.64.0.0/12" || st.Inet6Range != "fc00::/7" {
		t.Errorf("FakeIP state ranges = %+v, want overridden", st)
	}

	// The persisted fakeip sing-box config (21-fakeip.json) reflects
	// stack=system + gso:false + the overridden pools.
	data, err := os.ReadFile(filepath.Join(h.dir, "21-fakeip.json"))
	if err != nil {
		t.Fatalf("read persisted fakeip config: %v", err)
	}
	var cfg RouterConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal 21-fakeip.json: %v", err)
	}
	if len(cfg.Inbounds) == 0 {
		t.Fatalf("no inbounds in persisted fakeip config: %s", data)
	}
	in := cfg.Inbounds[0]
	if in.Stack != "system" {
		t.Errorf("persisted Stack = %q, want system", in.Stack)
	}
	if in.GSO == nil || *in.GSO != false {
		t.Errorf("persisted GSO = %v, want false for system stack", in.GSO)
	}
	if in.MTU != 1280 {
		t.Errorf("persisted MTU = %d, want 1280", in.MTU)
	}
	if !strings.Contains(string(data), `"gso": false`) {
		t.Errorf("persisted fakeip config must carry \"gso\": false: %s", data)
	}
	// Find the fakeip DNS server (by type) for pool range assertions.
	var fakeipSrv *DNSServer
	for i := range cfg.DNS.Servers {
		if cfg.DNS.Servers[i].Type == "fakeip" {
			fakeipSrv = &cfg.DNS.Servers[i]
			break
		}
	}
	if fakeipSrv == nil {
		t.Fatalf("fakeip DNS server missing in 21-fakeip.json: %s", data)
	}
	if fakeipSrv.Inet4Range != "10.64.0.0/12" || fakeipSrv.Inet6Range != "fc00::/7" {
		t.Errorf("fakeip DNS server ranges = %q/%q, want overridden", fakeipSrv.Inet4Range, fakeipSrv.Inet6Range)
	}
}

// ---------------------------------------------------------------------------
// Rollback: failure injected at each post-persist step
// ---------------------------------------------------------------------------

func TestEnableFakeIPTun_RollbackOnFailure(t *testing.T) {
	// Execution order: provision → Flush (pre-start) → [waitForSingbox] →
	// AddRoute → AddRoute6.
	steps := []string{"Create", "SetAddress", "SetIPv6Address", "SetMTU", "InterfaceUp", "Flush", "AddRoute", "AddRoute6"}
	for _, step := range steps {
		t.Run(step, func(t *testing.T) {
			h := newFakeIPEnableHarness(t, step)

			err := h.svc.Enable(context.Background())
			if err == nil {
				t.Fatalf("expected error when %s fails", step)
			}

			// No orphan persist: FakeIP must be cleared by rollback.
			if st := h.loadFakeIP(t); st != nil {
				t.Errorf("FakeIP persist = %+v, want nil after rollback", st)
			}
			// SingboxRouter.Enabled must NOT be set.
			all, _ := h.store.Load()
			if all.SingboxRouter.Enabled {
				t.Error("SingboxRouter.Enabled must stay false after rollback")
			}

			const ndmsName = "OpkgTun0" // NDMS iface ops + route Interface
			// If Create SUCCEEDED (failure injected at a later step), rollback must
			// tear the iface down (InterfaceDown + Delete) via the NDMS name. When
			// Create itself fails, its undo is never pushed (nothing was created), so
			// no teardown is due.
			if step != "Create" {
				if !h.log.has("InterfaceDown:" + ndmsName) {
					t.Errorf("%s: rollback missing InterfaceDown: %v", step, h.log.calls)
				}
				if !h.log.has("Delete:" + ndmsName) {
					t.Errorf("%s: rollback missing Delete: %v", step, h.log.calls)
				}
			} else {
				// Create failed → nothing created → no teardown.
				if h.log.has("Delete:" + ndmsName) {
					t.Errorf("Create-fail must not run iface teardown: %v", h.log.calls)
				}
			}
			// Order: Flush (pre-start) → [waitForSingbox] → AddRoute → AddRoute6.
			// A failure rolls back exactly what landed before it (LIFO).
			switch step {
			case "Create", "SetAddress", "SetIPv6Address", "SetMTU", "InterfaceUp", "Flush":
				// Failure at/before the v4 pool route: no pool route added.
				if h.log.has("AddRoute:198.18.0.0:255.254.0.0:" + ndmsName) {
					t.Errorf("%s: pool route should not have been added", step)
				}
			case "AddRoute":
				// Pool-route v4 add failed → nothing to remove for it.
				if h.log.has("RemoveRoute:198.18.0.0:" + ndmsName) {
					t.Errorf("AddRoute: nothing landed → no RemoveRoute expected: %v", h.log.calls)
				}
			case "AddRoute6":
				// v6-route-add failure: v4 pool route landed and must be removed; the
				// v6 route never landed.
				if !h.log.has("RemoveRoute:198.18.0.0:" + ndmsName) {
					t.Errorf("AddRoute6: rollback missing RemoveRoute (v4): %v", h.log.calls)
				}
			}
		})
	}
}

// slotEnabled reports whether the orchestrator currently has the slot enabled.
func slotEnabled(t *testing.T, svc *ServiceImpl, slot orchestrator.Slot) bool {
	t.Helper()
	for _, st := range svc.deps.Orch.Snapshot() {
		if st.Slot == slot {
			return st.Enabled
		}
	}
	return false
}

// Rollback must restore SlotRouter to its PRIOR state, not a hardcoded true.
// When fakeip is enabled from a state where SlotRouter was already OFF (boot
// into fakeip / first enable), a post-flip failure must leave SlotRouter OFF —
// re-enabling tproxy would be wrong (and break XOR intent).
func TestEnableFakeIPTun_RollbackRestoresPriorRouterSlotState(t *testing.T) {
	h := newFakeIPEnableHarness(t, "AddRoute") // post-flip failure
	// Force the prior state: SlotRouter DISABLED (the harness writes 20-router.json
	// to active, which counts as enabled — flip it off to model boot-into-fakeip).
	if err := h.svc.deps.Orch.SetEnabled(orchestrator.SlotRouter, false); err != nil {
		t.Fatalf("pre-disable SlotRouter: %v", err)
	}

	err := h.svc.Enable(context.Background())
	if err == nil {
		t.Fatal("expected error when AddRoute fails")
	}

	if slotEnabled(t, h.svc, orchestrator.SlotRouter) {
		t.Error("rollback wrongly re-enabled SlotRouter: prior state was disabled")
	}
	if slotEnabled(t, h.svc, orchestrator.SlotFakeIP) {
		t.Error("rollback must leave SlotFakeIP disabled")
	}
}

// waitForSingbox failure (readiness times out) must roll back everything created so far.
func TestEnableFakeIPTun_RollbackOnReadinessTimeout(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	// Force the tun carrier to never come up → waitForSingbox never becomes ready
	// (carrier is now the sole readiness signal; the DNS probe was demoted out of
	// the gate, so stubbing it false would no longer cause a timeout).
	stubTunReadyProbe(t, func(string) bool { return false })

	// bootWait is clamped to a 60s floor, so bound the wait via a short ctx;
	// waitForSingbox returns ctx.Err() on cancellation, which Enable propagates.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	err := h.svc.Enable(ctx)
	if err == nil {
		t.Fatal("expected error when readiness never becomes ready")
	}
	if st := h.loadFakeIP(t); st != nil {
		t.Errorf("FakeIP persist = %+v, want nil after readiness-timeout rollback", st)
	}
	const ndmsName = "OpkgTun0"
	if !h.log.has("InterfaceDown:"+ndmsName) || !h.log.has("Delete:"+ndmsName) {
		t.Errorf("readiness-timeout rollback must tear down iface: %v", h.log.calls)
	}
	if h.log.has("AddRoute:198.18.0.0:255.254.0.0:" + ndmsName) {
		t.Errorf("routes must not be added when readiness fails: %v", h.log.calls)
	}
}

// ---------------------------------------------------------------------------
// No usable egress
// ---------------------------------------------------------------------------

func TestEnableFakeIPTun_RefusesWithoutEgress(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	// Seed the fakeip config (21-fakeip.json) with a route.final that references a
	// non-existent outbound. The egress is now sourced from SlotFakeIP, not SlotRouter.
	// A pre-seeded bad egress prevents the "empty config" seed from applying "direct"
	// (which would pass). We include at least one DNS rule so fakeIPConfigEmpty returns
	// false and the seed is skipped.
	bad := `{"dns":{"rules":[{"action":"route","server":"fakeip","query_type":["A","AAAA"]}]},"route":{"final":"missing-tag","rules":[]}}`
	if err := os.WriteFile(filepath.Join(h.dir, "21-fakeip.json"), []byte(bad), 0644); err != nil {
		t.Fatalf("write 21-fakeip.json: %v", err)
	}

	err := h.svc.Enable(context.Background())
	if err == nil {
		t.Fatal("expected error when fakeip route.final is not a known outbound")
	}
	// Nothing provisioned, nothing persisted.
	if st := h.loadFakeIP(t); st != nil {
		t.Errorf("FakeIP persist = %+v, want nil (refused before any work)", st)
	}
	if len(h.log.calls) != 0 {
		t.Errorf("no provisioning calls expected, got %v", h.log.calls)
	}
}

// ---------------------------------------------------------------------------
// Best-effort post-readiness DNS confirm (NOT a gate)
//
// After waitForSingbox (now gated on process+carrier) succeeds AND the pool
// routes are added, enableFakeIPTun runs the live .2→fakeip DNS probe ONCE as a
// best-effort, logged confirmation. A false result must WARN but NOT fail Enable
// — sing-box is up by carrier; DNS delivery may just be briefly degraded.
// ---------------------------------------------------------------------------

func TestEnableFakeIPTun_DNSConfirmFalse_StillSucceeds(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")

	// Carrier is up (harness stubs tunReadyProbe→true) so readiness passes; the
	// best-effort DNS confirm returns false (the round-trip didn't answer in time).
	var dnsCalls int
	stubFakeIPDNSProbe(t, func(context.Context, string, netip.Prefix) bool {
		dnsCalls++
		return false
	})

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable must succeed despite an unconfirmed DNS round-trip: %v", err)
	}

	// Provisioning completed: persist written (the last step runs).
	st := h.loadFakeIP(t)
	if st == nil || !st.Provisioned || st.Index != 0 {
		t.Fatalf("FakeIP persist = %+v, want provisioned index 0", st)
	}
	all, _ := h.store.Load()
	if !all.SingboxRouter.Enabled {
		t.Error("SingboxRouter.Enabled must be true (Enable not failed by best-effort confirm)")
	}
	// The confirm runs exactly once (post-readiness, NOT in the poll loop).
	if dnsCalls != 1 {
		t.Errorf("DNS confirm call count = %d, want exactly 1 (once, post-readiness)", dnsCalls)
	}
}

func TestEnableFakeIPTun_DNSConfirmTrue_Succeeds(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")

	var dnsCalls int
	stubFakeIPDNSProbe(t, func(_ context.Context, dnsAddr string, n netip.Prefix) bool {
		dnsCalls++
		if dnsAddr != "172.18.0.2" {
			t.Errorf("DNS confirm addr = %q, want 172.18.0.2", dnsAddr)
		}
		return n.Contains(netip.MustParseAddr("10.128.0.5")) // in-pool ⇒ confirmed
	})

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	st := h.loadFakeIP(t)
	if st == nil || !st.Provisioned {
		t.Fatalf("FakeIP persist = %+v, want provisioned", st)
	}
	if dnsCalls != 1 {
		t.Errorf("DNS confirm call count = %d, want exactly 1", dnsCalls)
	}
}

// TestEnable_TproxyUnchanged verifies the dispatch only branches for
// fakeip-tun: a tproxy-mode Enable must run the tproxy path and never touch the
// fakeip provisioner deps, even when they are wired.
func TestEnable_TproxyUnchanged(t *testing.T) {
	settingsStore := newTestSettingsStore(t, storage.SingboxRouterSettings{
		RoutingMode:   "tproxy",
		DeviceMode:    "all",
		WANAutoDetect: true,
	})
	singbox := newTestSingbox(t)
	singbox.isRunningFn = func() (bool, int) { return true, 1234 }
	stubListeningProbe(t, func() bool { return true })

	log := &callLog{}
	svc := newTestService(t, Deps{
		Settings:           settingsStore,
		Policies:           &fakeAccessPolicyProvider{},
		IPTables:           newStubIPTables(func(context.Context, string) error { return nil }),
		Singbox:            singbox,
		WANIPCollector:     &fakeWANIPCollector{},
		NetfilterPreflight: func(context.Context) error { return nil },
		// Fakeip deps wired but must NEVER be exercised in tproxy mode.
		OpkgTun:        &recOpkgTun{log: log},
		StaticRoutes:   &recStaticRoutes{log: log},
		OpkgTunIndices: &recIndices{live: map[int]bool{}},
		FakeIPTun:      DefaultFakeIPTunParams(),
	})

	if err := svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable (tproxy): %v", err)
	}
	if len(log.calls) != 0 {
		t.Errorf("tproxy Enable must not call any fakeip provisioner, got %v", log.calls)
	}
	all, _ := settingsStore.Load()
	if !all.SingboxRouter.Enabled {
		t.Error("tproxy Enable must persist Enabled=true")
	}
	if all.FakeIP != nil {
		t.Errorf("tproxy Enable must not write FakeIP persist, got %+v", all.FakeIP)
	}
}

// ---------------------------------------------------------------------------
// Explicit Enable clears the sticky master-Stop intent (both modes)
// ---------------------------------------------------------------------------

// enableTproxyClearStopSvc builds an orch-backed tproxy-mode service so the
// real SetEnabled path runs and we can assert ClearManualStop fired before it.
func enableTproxyClearStopSvc(t *testing.T) (*ServiceImpl, *fakeSingbox, *storage.SettingsStore) {
	t.Helper()
	settingsStore := newTestSettingsStore(t, storage.SingboxRouterSettings{
		RoutingMode:   "tproxy",
		DeviceMode:    "all",
		WANAutoDetect: true,
	})
	singbox := newTestSingbox(t)
	singbox.isRunningFn = func() (bool, int) { return true, 1234 }
	stubListeningProbe(t, func() bool { return true })
	svc := newTestService(t, Deps{
		Settings:           settingsStore,
		Policies:           &fakeAccessPolicyProvider{},
		IPTables:           newStubIPTables(func(context.Context, string) error { return nil }),
		Singbox:            singbox,
		WANIPCollector:     &fakeWANIPCollector{},
		NetfilterPreflight: func(context.Context) error { return nil },
	})
	return svc, singbox, settingsStore
}

func TestEnable_Tproxy_ClearsManualStop(t *testing.T) {
	svc, singbox, settingsStore := enableTproxyClearStopSvc(t)

	if err := svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable (tproxy): %v", err)
	}
	if singbox.clearManualStopCalls != 1 {
		t.Errorf("ClearManualStop calls = %d, want 1", singbox.clearManualStopCalls)
	}
	all, _ := settingsStore.Load()
	if !all.SingboxRouter.Enabled {
		t.Error("Enable must still persist Enabled=true")
	}
}

func TestEnable_Tproxy_ClearManualStopError_FailsFast(t *testing.T) {
	svc, singbox, settingsStore := enableTproxyClearStopSvc(t)
	singbox.clearManualStopErr = errors.New("disk full")

	err := svc.Enable(context.Background())
	if err == nil {
		t.Fatalf("Enable: want error when ClearManualStop fails, got nil")
	}
	if !strings.Contains(err.Error(), "clear manual-stop intent") {
		t.Errorf("error = %q, want wrapped 'clear manual-stop intent'", err)
	}
	if singbox.clearManualStopCalls != 1 {
		t.Errorf("ClearManualStop calls = %d, want 1", singbox.clearManualStopCalls)
	}
	// Fast-fail: no enable persisted (SetEnabled/Start never reached).
	all, _ := settingsStore.Load()
	if all.SingboxRouter.Enabled {
		t.Error("Enable must NOT persist Enabled=true when ClearManualStop fails")
	}
}

func TestEnable_FakeIP_ClearsManualStopBeforeProvisioning(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable (fakeip-tun): %v", err)
	}
	fs := h.svc.deps.Singbox.(*fakeSingbox)
	if fs.clearManualStopCalls != 1 {
		t.Errorf("ClearManualStop calls = %d, want 1", fs.clearManualStopCalls)
	}
	// Provisioning ran (proof the clear didn't abort the path).
	if len(h.log.calls) == 0 {
		t.Error("expected provisioning calls after a successful clear")
	}
}

func TestEnable_FakeIP_ClearManualStopError_NoProvisioning(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	fs := h.svc.deps.Singbox.(*fakeSingbox)
	fs.clearManualStopErr = errors.New("disk full")

	err := h.svc.Enable(context.Background())
	if err == nil {
		t.Fatalf("Enable: want error when ClearManualStop fails, got nil")
	}
	if !strings.Contains(err.Error(), "clear manual-stop intent") {
		t.Errorf("error = %q, want wrapped 'clear manual-stop intent'", err)
	}
	// Fail before any fakeip provisioning op and before persisting state.
	if len(h.log.calls) != 0 {
		t.Errorf("no provisioning must happen when ClearManualStop fails, got %v", h.log.calls)
	}
	if st := h.loadFakeIP(t); st != nil {
		t.Errorf("FakeIP persist = %+v, want nil", st)
	}
}

// ---------------------------------------------------------------------------
// Index allocation skips an occupied opkgtun
// ---------------------------------------------------------------------------

func TestEnableFakeIPTun_AllocatesLowestFreeIndex(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{0: true, 1: true}}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	st := h.loadFakeIP(t)
	if st == nil || st.Index != 2 {
		t.Fatalf("FakeIP index = %v, want 2 (0,1 occupied)", st)
	}
	// NDMS Create uses the CamelCase name for the allocated index.
	if !h.log.has("Create:OpkgTun2:private") {
		t.Errorf("expected Create OpkgTun2, got %v", h.log.calls)
	}
}

// ---------------------------------------------------------------------------
// Idempotency guard: a second Enable while already provisioned + iface LIVE
// must be a no-op (CRITICAL: Reconcile routes here every 30s tick because
// fakeip-tun installs no iptables → installed-check always false).
// ---------------------------------------------------------------------------

func TestEnableFakeIPTun_IdempotentWhenProvisioned(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")

	// First Enable provisions opkgtun0.
	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("first Enable: %v", err)
	}
	st1 := h.loadFakeIP(t)
	if st1 == nil || !st1.Provisioned || st1.Index != 0 {
		t.Fatalf("after first Enable FakeIP = %+v, want provisioned index 0", st1)
	}
	createCount1 := countCalls(h.log, "Create:OpkgTun0:private")
	if createCount1 != 1 {
		t.Fatalf("first Enable Create count = %d, want 1", createCount1)
	}

	// Make the allocator report the provisioned index (0) as LIVE so the
	// idempotency guard sees a live iface, and arrange that a (bogus) re-provision
	// would pick a DIFFERENT index (1) — proving the guard, not a coincidence.
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{0: true}}

	// Second Enable with the same settings → no-op.
	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("second Enable (idempotent): %v", err)
	}

	// No second Create at all (neither opkgtun0 nor any other index).
	if c := countCalls(h.log, "Create:OpkgTun0:private"); c != 1 {
		t.Errorf("Create:opkgtun0 count = %d after second Enable, want 1 (no re-provision)", c)
	}
	if h.log.has("Create:OpkgTun1:private") {
		t.Errorf("second Enable allocated a NEW index: %v", h.log.calls)
	}

	// Persist index unchanged.
	st2 := h.loadFakeIP(t)
	if st2 == nil || st2.Index != st1.Index {
		t.Errorf("FakeIP index changed: %+v → %+v (guard must not re-allocate)", st1, st2)
	}
}

// Fall-through: provisioned in persist but the iface is NOT live (crash / manual
// removal) → the guard must NOT short-circuit; Enable re-provisions (Create
// runs). allocateFakeIPIndex reuses the now-free index, so no leak.
func TestEnableFakeIPTun_ReprovisionsWhenIfaceGone(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("first Enable: %v", err)
	}
	if c := countCalls(h.log, "Create:OpkgTun0:private"); c != 1 {
		t.Fatalf("first Enable Create count = %d, want 1", c)
	}

	// Persist says provisioned (index 0) but the allocator reports NOTHING live —
	// the iface vanished. Guard must fall through and re-provision into index 0.
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{}}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("second Enable (reprovision): %v", err)
	}
	if c := countCalls(h.log, "Create:OpkgTun0:private"); c != 2 {
		t.Errorf("Create:opkgtun0 count = %d, want 2 (re-provisioned after iface gone): %v", c, h.log.calls)
	}
}

// ---------------------------------------------------------------------------
// Disable(fakeip-tun): safe-ordering teardown + fail-closed drain (Task 1D.3a)
// ---------------------------------------------------------------------------

// captureDrain overrides the fakeIPScheduleDrain seam so the removeReject
// closure is captured (not run via a real sleep) and can be invoked
// synchronously by the test. Returns a getter for the captured closure (nil
// until scheduled). Restores the seam via t.Cleanup.
func captureDrain(t *testing.T) func() func() {
	t.Helper()
	old := fakeIPScheduleDrain
	var captured func()
	fakeIPScheduleDrain = func(removeReject func()) { captured = removeReject }
	t.Cleanup(func() { fakeIPScheduleDrain = old })
	return func() func() { return captured }
}

// provisionForDisable runs Enable so the service is fully provisioned, then
// clears the call log so subsequent Disable assertions see only teardown calls.
func provisionForDisable(t *testing.T, h *fakeIPEnableHarness) {
	t.Helper()
	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable (provision for disable): %v", err)
	}
	// Allocator reflects the now-live iface so a stray Reconcile would no-op.
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{0: true}}
	h.log.calls = nil
}

// stubOrphanNetdev overrides the orphan-netdev seams (PE-E). present controls
// whether the kernel netdev is reported as lingering after DeleteOpkgTun; the
// returned getter reports how many times fakeIPLinkDelete was called.
func stubOrphanNetdev(t *testing.T, present bool) func() int {
	t.Helper()
	oldPresent := fakeIPLinkPresent
	oldDelete := fakeIPLinkDelete
	deletes := 0
	fakeIPLinkPresent = func(context.Context, string) bool { return present }
	fakeIPLinkDelete = func(_ context.Context, _ string) error { deletes++; return nil }
	t.Cleanup(func() { fakeIPLinkPresent = oldPresent; fakeIPLinkDelete = oldDelete })
	return func() int { return deletes }
}

// TestDisableFakeIPTun_OrphanNetdevDeleted asserts that when a DOWN orphan netdev
// lingers after DeleteOpkgTun, the teardown reaps it via `ip link delete`.
func TestDisableFakeIPTun_OrphanNetdevDeleted(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	captureDrain(t)
	provisionForDisable(t, h)
	deletes := stubOrphanNetdev(t, true) // orphan present

	if err := h.svc.Disable(context.Background()); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if got := deletes(); got != 1 {
		t.Errorf("orphan netdev present → fakeIPLinkDelete calls = %d, want 1", got)
	}
}

// TestDisableFakeIPTun_NoOrphanNoDelete asserts that when the kernel netdev is
// already gone (NDMS cleaned it up), no `ip link delete` is attempted.
func TestDisableFakeIPTun_NoOrphanNoDelete(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	captureDrain(t)
	provisionForDisable(t, h)
	deletes := stubOrphanNetdev(t, false) // netdev absent

	if err := h.svc.Disable(context.Background()); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if got := deletes(); got != 0 {
		t.Errorf("no orphan → fakeIPLinkDelete calls = %d, want 0", got)
	}
}

func TestDisableFakeIPTun_Ordering(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	getDrain := captureDrain(t)
	provisionForDisable(t, h)

	// NDMS RCI ops + the route Interface take the CamelCase OpkgTun0; the kernel
	// name is unused on the teardown path (no flush on disable).
	const ndmsName = "OpkgTun0"
	if err := h.svc.Disable(context.Background()); err != nil {
		t.Fatalf("Disable(fakeip-tun): %v", err)
	}

	mustOrder := func(a, b string) {
		ia, ib := h.log.idxOf(a), h.log.idxOf(b)
		if ia < 0 {
			t.Fatalf("missing call %q in %v", a, h.log.calls)
		}
		if ib < 0 {
			t.Fatalf("missing call %q in %v", b, h.log.calls)
		}
		if ia >= ib {
			t.Errorf("expected %q (#%d) before %q (#%d): %v", a, ia, b, ib, h.log.calls)
		}
	}

	// Bug 2: the reject route is the pool→OpkgTun route RENEWED with reject:true ON
	// the OpkgTun interface (kill-switch flag), NOT an interface-less blackhole.
	renewReject := "AddRejectRoute:198.18.0.0:255.254.0.0:" + ndmsName
	rmAuto6 := "RemoveRoute6:fc00::/18:" + ndmsName

	// The pool route is renewed to a reject kill-switch (interface-bound) FIRST —
	// awg-manager no longer touches client DNS, so teardown opens with the route.
	if first := h.log.calls[0]; first != renewReject {
		t.Errorf("first call = %q, want %q first", first, renewReject)
	}
	// Bug 2: there must be NO separate auto-route removal before the iface delete —
	// the single pool route is the kill-switch and is only removed by the async
	// drain LAST.
	if h.log.has("RemoveRoute:198.18.0.0:" + ndmsName) {
		t.Errorf("v4 auto-route removed inline; the kill-switch route must survive until the async drain: %v", h.log.calls)
	}
	// v6 pool route removed (no v6 reject equivalent — fail-open, see disable doc).
	if !h.log.has(rmAuto6) {
		t.Errorf("v6 auto-route not removed: %v", h.log.calls)
	}
	// reject-renew before iface torn down, then iface down→delete (NDMS name).
	mustOrder(renewReject, "InterfaceDown:"+ndmsName)
	mustOrder("InterfaceDown:"+ndmsName, "Delete:"+ndmsName)

	// persist: FakeIP cleared, Enabled=false.
	if st := h.loadFakeIP(t); st != nil {
		t.Errorf("FakeIP persist = %+v, want nil after Disable", st)
	}
	all, _ := h.store.Load()
	if all.SingboxRouter.Enabled {
		t.Error("SingboxRouter.Enabled must be false after Disable")
	}

	// The kill-switch route must NOT yet be removed (drain scheduled, not run
	// inline). The stand-verified REMOVE form is {network,mask,interface,no:true}
	// WITHOUT a reject flag (the fake records that as RemoveRoute on the iface).
	rmKillSwitch := "RemoveRoute:198.18.0.0:" + ndmsName
	if h.log.has(rmKillSwitch) {
		t.Errorf("kill-switch route removed before drain window: %v", h.log.calls)
	}

	// Invoke the captured drain closure → kill-switch route removed LAST, on the NDMS iface.
	drain := getDrain()
	if drain == nil {
		t.Fatal("drain closure was not scheduled")
	}
	drain()
	if !h.log.has(rmKillSwitch) {
		t.Fatalf("kill-switch route not removed after drain: %v", h.log.calls)
	}
	if last := h.log.calls[len(h.log.calls)-1]; last != rmKillSwitch {
		t.Errorf("last call = %q, want kill-switch removal LAST", last)
	}
}

// The reject removal must be scheduled via the seam, not run inline: before the
// captured closure is invoked, the reject route is still "present".
func TestDisableFakeIPTun_DrainOffLock(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	getDrain := captureDrain(t)
	provisionForDisable(t, h)

	if err := h.svc.Disable(context.Background()); err != nil {
		t.Fatalf("Disable: %v", err)
	}

	// Reject renew happened (on the OpkgTun0 iface); kill-switch removal has NOT
	// (still scheduled off-lock via the seam). The remove form is a plain
	// RemoveRoute on the iface (stand-verified {…,no:true}, no reject flag).
	if !h.log.has("AddRejectRoute:198.18.0.0:255.254.0.0:OpkgTun0") {
		t.Fatalf("reject route was not renewed: %v", h.log.calls)
	}
	if h.log.has("RemoveRoute:198.18.0.0:OpkgTun0") {
		t.Errorf("kill-switch removal ran inline (must be off-lock via seam): %v", h.log.calls)
	}
	if getDrain() == nil {
		t.Error("drain closure was not scheduled via the seam")
	}
}

// Best-effort push-through: a mid-step failure (reject-renew, or a v6 route op)
// must NOT abort teardown — persist clear + Enabled=false + drain schedule still
// run (asymmetric vs Enable's rollback-on-first-error).
//
// Bug 2 model: there is no longer a separate v4 auto-route REMOVAL during disable
// (the single pool route is renewed in place to a reject kill-switch and only the
// async drain removes it). So the meaningful injectable failure is AddRejectRoute
// (the reject-renew) — when it fails, no drain is scheduled (no kill-switch route
// was established to remove). RemoveRejectRoute exercises a drain-time failure but
// teardown is already done, so it is covered via the drain closure elsewhere.
func TestDisableFakeIPTun_BestEffort(t *testing.T) {
	for _, failAt := range []string{"AddRejectRoute"} {
		t.Run(failAt, func(t *testing.T) {
			h := newFakeIPEnableHarness(t, "")
			getDrain := captureDrain(t)
			provisionForDisable(t, h)
			// Inject the failure into the routes fake for the Disable phase.
			h.routes.failAt = failAt

			if err := h.svc.Disable(context.Background()); err != nil {
				t.Fatalf("Disable must push through best-effort errors, got: %v", err)
			}

			// Mandatory steps still ran.
			if st := h.loadFakeIP(t); st != nil {
				t.Errorf("FakeIP persist = %+v, want nil (push-through must clear)", st)
			}
			all, _ := h.store.Load()
			if all.SingboxRouter.Enabled {
				t.Error("Enabled must be false (push-through must persist disabled)")
			}
			// An AddRejectRoute failure leaves no kill-switch route to remove, so no
			// drain must be scheduled (the plain pool route is kept; the startup sweep
			// is the safety net).
			if failAt == "AddRejectRoute" && getDrain() != nil {
				t.Error("no drain must be scheduled when reject-renew failed (no kill-switch route to remove)")
			}
		})
	}
}

// Fix 2: a failed reject-add must NOT open a WAN-leak window. The v4 auto-route
// removal is GATED on reject-add success — when the reject add fails, the v4 pool
// auto-route is KEPT (traffic dead-ends at the about-to-be-deleted tun = dropped,
// not leaked). The rest of teardown still pushes through (persist clear + Enabled
// false), and no drain is scheduled (no reject route to remove).
func TestDisableFakeIPTun_RejectAddFailKeepsAutoRoute(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	getDrain := captureDrain(t)
	provisionForDisable(t, h)
	h.routes.failAt = "AddRejectRoute"

	const ndmsName = "OpkgTun0"
	if err := h.svc.Disable(context.Background()); err != nil {
		t.Fatalf("Disable must push through, got: %v", err)
	}

	// Reject renew was attempted but failed (on the OpkgTun0 iface).
	if !h.log.has("AddRejectRoute:198.18.0.0:255.254.0.0:" + ndmsName) {
		t.Fatalf("reject renew was not attempted: %v", h.log.calls)
	}
	// The plain v4 pool route must NOT have been removed (Bug 2 model: disable never
	// removes the pool route inline; a failed reject-renew leaves it plain + present,
	// so there is no WAN-leak window — traffic dead-ends at the deleted tun).
	if h.log.has("RemoveRoute:198.18.0.0:" + ndmsName) {
		t.Errorf("v4 pool route removed despite failed reject-renew (WAN-leak window): %v", h.log.calls)
	}
	// Teardown still reached the mandatory persist steps.
	if st := h.loadFakeIP(t); st != nil {
		t.Errorf("FakeIP persist = %+v, want nil (teardown must reach SetFakeIPState(nil))", st)
	}
	all, _ := h.store.Load()
	if all.SingboxRouter.Enabled {
		t.Error("Enabled must be false after teardown")
	}
	// No drain scheduled: there is no reject route to remove later.
	if getDrain() != nil {
		t.Error("no drain must be scheduled when the reject route was never added")
	}
}

// FakeIP nil (not provisioned) → idempotent: persist Enabled=false, no NDMS
// teardown, no drain, no panic.
func TestDisableFakeIPTun_NotProvisioned(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	getDrain := captureDrain(t)
	// Do NOT provision: FakeIP persist is nil. Seed Enabled=true to prove Disable
	// flips it.
	all, _ := h.store.Load()
	all.SingboxRouter.Enabled = true
	if err := h.store.Save(all); err != nil {
		t.Fatalf("Save: %v", err)
	}
	h.log.calls = nil

	if err := h.svc.Disable(context.Background()); err != nil {
		t.Fatalf("Disable (not provisioned): %v", err)
	}

	if len(h.log.calls) != 0 {
		t.Errorf("no NDMS teardown expected when not provisioned, got %v", h.log.calls)
	}
	if getDrain() != nil {
		t.Error("no drain should be scheduled when not provisioned")
	}
	after, _ := h.store.Load()
	if after.SingboxRouter.Enabled {
		t.Error("Enabled must be false after Disable")
	}
	if after.FakeIP != nil {
		t.Errorf("FakeIP must stay nil, got %+v", after.FakeIP)
	}
}

// TestDisableFakeIPTun_DisablesSlotFakeIPNotSlotRouter asserts that disabling a
// provisioned fakeip-tun disables the FAKEIP slot (21-fakeip.json) and does NOT
// touch the tproxy router slot (20-router.json). The XOR contract: after disable,
// SlotFakeIP is DISABLED and SlotRouter is unchanged (it was already disabled by
// the prior Enable's XOR flip).
func TestDisableFakeIPTun_DisablesSlotFakeIPNotSlotRouter(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	captureDrain(t)
	provisionForDisable(t, h) // provisions fakeip: SlotFakeIP ON, SlotRouter OFF

	// Sanity pre-condition: Enable flipped the slots correctly.
	if !slotEnabled(t, h.svc, orchestrator.SlotFakeIP) {
		t.Fatal("precondition: SlotFakeIP must be enabled after Enable")
	}
	if slotEnabled(t, h.svc, orchestrator.SlotRouter) {
		t.Fatal("precondition: SlotRouter must be disabled after Enable (XOR)")
	}

	if err := h.svc.Disable(context.Background()); err != nil {
		t.Fatalf("Disable: %v", err)
	}

	// SlotFakeIP must now be DISABLED (the fakeip lifecycle slot was toggled).
	if slotEnabled(t, h.svc, orchestrator.SlotFakeIP) {
		t.Error("SlotFakeIP must be DISABLED after fakeip Disable")
	}
	// SlotRouter must remain DISABLED (untouched — disabling fakeip must not
	// re-enable the tproxy slot, which would be wrong and violate XOR).
	if slotEnabled(t, h.svc, orchestrator.SlotRouter) {
		t.Error("SlotRouter must remain DISABLED (untouched) after fakeip Disable — must NOT toggle the tproxy slot")
	}
}

// Fix 5: Disable dispatches on the RAW persisted RoutingMode, not the normalized
// value. A settings blob that fails Normalize (corrupt DeviceMode) but carries
// RoutingMode=="fakeip-tun" must still route to the fakeip teardown — not fall
// through to the tproxy body (which would orphan the opkgtun/routes/DHCP).
func TestDisableFakeIPTun_DispatchOnRawModeDespiteNormalizeError(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	captureDrain(t)
	provisionForDisable(t, h)

	// Corrupt DeviceMode so NormalizeSingboxRouterSettings returns an error, while
	// keeping RoutingMode=="fakeip-tun" raw.
	all, _ := h.store.Load()
	all.SingboxRouter.DeviceMode = "bogus-mode"
	if _, err := NormalizeSingboxRouterSettings(all.SingboxRouter); err == nil {
		t.Fatal("test precondition: DeviceMode bogus must make Normalize error")
	}
	if err := h.store.Save(all); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := h.svc.Disable(context.Background()); err != nil {
		t.Fatalf("Disable: %v", err)
	}

	// Fakeip teardown ran (the reject-route renew is a fakeip-only call).
	if !h.log.has("AddRejectRoute:198.18.0.0:255.254.0.0:OpkgTun0") {
		t.Errorf("raw-mode dispatch failed: fakeip teardown not run, got %v", h.log.calls)
	}
	if st := h.loadFakeIP(t); st != nil {
		t.Errorf("FakeIP persist = %+v, want nil (fakeip teardown must clear it)", st)
	}
}

// TestDisable_TproxyUnchanged: tproxy-mode Disable must run the tproxy teardown
// path and never touch the fakeip teardown (no opkgtun/route calls).
func TestDisable_TproxyUnchanged(t *testing.T) {
	settingsStore := newTestSettingsStore(t, storage.SingboxRouterSettings{
		RoutingMode:   "tproxy",
		DeviceMode:    "all",
		WANAutoDetect: true,
		Enabled:       true,
	})
	singbox := newTestSingbox(t)
	singbox.isRunningFn = func() (bool, int) { return true, 1234 }

	log := &callLog{}
	uninstalled := false
	svc := newTestService(t, Deps{
		Settings:       settingsStore,
		Policies:       &fakeAccessPolicyProvider{},
		IPTables:       newStubIPTables(func(context.Context, string) error { return nil }),
		Singbox:        singbox,
		WANIPCollector: &fakeWANIPCollector{},
		// Fakeip deps wired but must NEVER be exercised in tproxy mode.
		OpkgTun:        &recOpkgTun{log: log},
		StaticRoutes:   &recStaticRoutes{log: log},
		OpkgTunIndices: &recIndices{live: map[int]bool{}},
		FakeIPTun:      DefaultFakeIPTunParams(),
	})
	_ = uninstalled

	if err := svc.Disable(context.Background()); err != nil {
		t.Fatalf("Disable (tproxy): %v", err)
	}
	if len(log.calls) != 0 {
		t.Errorf("tproxy Disable must not call any fakeip teardown, got %v", log.calls)
	}
	all, _ := settingsStore.Load()
	if all.SingboxRouter.Enabled {
		t.Error("tproxy Disable must persist Enabled=false")
	}
}

// countCalls returns how many times the exact label appears in the log.
func countCalls(l *callLog, want string) int {
	n := 0
	for _, c := range l.calls {
		if c == want {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// nil-guard: a mis-wired build (a required fakeip dep nil) must fail loudly,
// not nil-panic mid-provision.
// ---------------------------------------------------------------------------

func TestEnableFakeIPTun_NilDepsFailFast(t *testing.T) {
	cases := []struct {
		name string
		nilf func(*ServiceImpl)
	}{
		{"OpkgTun", func(s *ServiceImpl) { s.deps.OpkgTun = nil }},
		{"StaticRoutes", func(s *ServiceImpl) { s.deps.StaticRoutes = nil }},
		{"OpkgTunIndices", func(s *ServiceImpl) { s.deps.OpkgTunIndices = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newFakeIPEnableHarness(t, "")
			tc.nilf(h.svc)
			err := h.svc.Enable(context.Background())
			if err == nil {
				t.Fatalf("expected error when %s is nil", tc.name)
			}
			// Nothing provisioned, nothing persisted.
			if st := h.loadFakeIP(t); st != nil {
				t.Errorf("FakeIP persist = %+v, want nil (refused before any work)", st)
			}
			if len(h.log.calls) != 0 {
				t.Errorf("no provisioning calls expected, got %v", h.log.calls)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Reconcile regression: the real-world trigger. fakeip-tun installs no iptables,
// so Reconcile's installed-check is always false and it routes to Enable on
// EVERY tick. A second Reconcile while enabled+provisioned must NOT re-provision.
// ---------------------------------------------------------------------------

func TestReconcileFakeIPTun_NoReprovision(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	// Wire an IPTables whose probes always error → IsInstalled/HasAnyInstalled
	// both false, exactly like the real fakeip-tun path (no chains installed).
	h.svc.deps.IPTables = &IPTables{
		runIPTables:    func(context.Context, ...string) error { return errors.New("no chain") },
		runIPTablesOut: func(context.Context, ...string) (string, error) { return "", errors.New("no chain") },
	}

	// First Reconcile: Enabled=false initially → nothing. We must first Enable so
	// settings.Enabled flips true and the index is provisioned.
	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	createCount1 := countCalls(h.log, "Create:OpkgTun0:private")
	if createCount1 != 1 {
		t.Fatalf("after Enable Create count = %d, want 1", createCount1)
	}

	// The scheduler's allocator reflects the live iface (index 0 occupied).
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{0: true}}

	// Reconcile sees Enabled=true && !installedComplete → routes to Enable → must
	// hit the idempotency guard and no-op.
	if err := h.svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if c := countCalls(h.log, "Create:OpkgTun0:private"); c != 1 {
		t.Errorf("Create count = %d after Reconcile, want 1 (no re-provision): %v", c, h.log.calls)
	}
	if h.log.has("Create:OpkgTun1:private") {
		t.Errorf("Reconcile leaked a new index: %v", h.log.calls)
	}
}
