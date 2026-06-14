package router

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	r.log.add("SetMTU:" + name)
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
	r.log.add("AddRoute:" + route.Network + ":" + route.Mask + ":" + route.Interface)
	if r.failAt == "AddRoute" {
		return errors.New("injected: AddRoute")
	}
	return nil
}
func (r *recStaticRoutes) RemoveStaticRoute(_ context.Context, route StaticRouteSpec) error {
	r.log.add("RemoveRoute:" + route.Network + ":" + route.Interface)
	return nil
}
func (r *recStaticRoutes) AddStaticRoute6(_ context.Context, network, iface string) error {
	r.log.add("AddRoute6:" + network + ":" + iface)
	if r.failAt == "AddRoute6" {
		return errors.New("injected: AddRoute6")
	}
	return nil
}
func (r *recStaticRoutes) RemoveStaticRoute6(_ context.Context, network, iface string) error {
	r.log.add("RemoveRoute6:" + network + ":" + iface)
	return nil
}

type recDHCP struct {
	log    *callLog
	failAt string
}

func (r *recDHCP) SetPoolDNS(_ context.Context, pool string, servers []string) error {
	r.log.add("SetPoolDNS:" + pool + ":" + servers[0])
	if r.failAt == "SetPoolDNS" {
		return errors.New("injected: SetPoolDNS")
	}
	return nil
}
func (r *recDHCP) ClearPoolDNS(_ context.Context, pool string) error {
	r.log.add("ClearPoolDNS:" + pool)
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
	dhcp   *recDHCP
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
	dhcp := &recDHCP{log: log, failAt: failAt}

	singbox := newTestSingbox(t)
	singbox.dir = dir
	singbox.isRunningFn = func() (bool, int) { return true, 1234 }
	svc.deps.Singbox = singbox

	svc.deps.OpkgTun = opkg
	svc.deps.StaticRoutes = routes
	svc.deps.DHCP = dhcp
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
		svc: svc, log: log, opkg: opkg, routes: routes, dhcp: dhcp, store: store, dir: dir,
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

	// Index 0 is the lowest free → opkgtun0.
	const iface = "opkgtun0"

	// Persist FakeIP state must land BEFORE the iface is created.
	st := h.loadFakeIP(t)
	if st == nil || !st.Provisioned || st.Index != 0 {
		t.Fatalf("FakeIP persist = %+v, want provisioned index 0", st)
	}
	if st.Inet4Range != "10.128.0.0/10" || st.Inet6Range != "3f80::/10" {
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

	createCall := "Create:" + iface + ":private"
	if !h.log.has(createCall) {
		t.Fatalf("Create with private security-level missing: %v", h.log.calls)
	}
	// SetIPGlobal must NOT be called (no such recorded label could exist; assert
	// no global-ish call leaked — Create is the only creation op).
	mustOrder(createCall, "SetAddress:"+iface+":172.18.0.1:255.255.255.252")
	mustOrder("SetAddress:"+iface+":172.18.0.1:255.255.255.252", "SetMTU:"+iface)
	// v6: SetIPv6Address is driven (defaults carry TunAddr6) and lands after the
	// v4 SetAddress, before SetMTU.
	mustOrder("SetAddress:"+iface+":172.18.0.1:255.255.255.252", "SetIPv6Address:"+iface+":fdfe:dcba:9876::1")
	mustOrder("SetIPv6Address:"+iface+":fdfe:dcba:9876::1", "SetMTU:"+iface)
	// v6 pool route is added (defaults carry Inet6Range) after the v4 pool route.
	mustOrder("AddRoute:10.128.0.0:255.192.0.0:"+iface, "AddRoute6:3f80::/10:"+iface)
	mustOrder("SetMTU:"+iface, "InterfaceUp:"+iface)
	mustOrder("InterfaceUp:"+iface, "Flush:"+iface)
	// Flush precedes the pool route (waitForSingbox sits between, no recorded call).
	mustOrder("Flush:"+iface, "AddRoute:10.128.0.0:255.192.0.0:"+iface)
	mustOrder("AddRoute:10.128.0.0:255.192.0.0:"+iface, "SetPoolDNS:_WEBADMIN:172.18.0.2")

	// DHCP SetPoolDNS must be the LAST provisioning call.
	last := h.log.calls[len(h.log.calls)-1]
	if last != "SetPoolDNS:_WEBADMIN:172.18.0.2" {
		t.Errorf("last call = %q, want SetPoolDNS last", last)
	}

	// SingboxRouter.Enabled persisted true.
	all, _ := h.store.Load()
	if !all.SingboxRouter.Enabled {
		t.Error("SingboxRouter.Enabled must be true after Enable")
	}
}

// ---------------------------------------------------------------------------
// Rollback: failure injected at each post-persist step
// ---------------------------------------------------------------------------

func TestEnableFakeIPTun_RollbackOnFailure(t *testing.T) {
	steps := []string{"Create", "SetAddress", "SetIPv6Address", "SetMTU", "InterfaceUp", "Flush", "AddRoute", "AddRoute6", "SetPoolDNS"}
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

			const iface = "opkgtun0"
			// If Create SUCCEEDED (failure injected at a later step), rollback must
			// tear the iface down (InterfaceDown + Delete). When Create itself fails,
			// its undo is never pushed (nothing was created), so no teardown is due.
			if step != "Create" {
				if !h.log.has("InterfaceDown:" + iface) {
					t.Errorf("%s: rollback missing InterfaceDown: %v", step, h.log.calls)
				}
				if !h.log.has("Delete:" + iface) {
					t.Errorf("%s: rollback missing Delete: %v", step, h.log.calls)
				}
			} else {
				// Create failed → nothing created → no teardown.
				if h.log.has("Delete:" + iface) {
					t.Errorf("Create-fail must not run iface teardown: %v", h.log.calls)
				}
			}
			// A failure before the route step must not leave a route applied.
			switch step {
			case "Create", "SetAddress", "SetIPv6Address", "SetMTU", "InterfaceUp", "Flush":
				if h.log.has("AddRoute:10.128.0.0:255.192.0.0:" + iface) {
					t.Errorf("%s: route should not have been added", step)
				}
				if h.log.has("SetPoolDNS:_WEBADMIN:172.18.0.2") {
					t.Errorf("%s: DHCP DNS should not have been set", step)
				}
			case "AddRoute":
				// DHCP must not be set; route add failed so nothing to remove.
				if h.log.has("SetPoolDNS:_WEBADMIN:172.18.0.2") {
					t.Errorf("AddRoute: DHCP DNS should not have been set")
				}
			case "AddRoute6":
				// v6-route-add failure: the v4 route was already added and must be
				// removed in rollback; the v6 route itself never landed (so nothing
				// to remove for it). DHCP must not be set. (iface teardown + persist
				// clear are asserted by the common checks above.)
				if !h.log.has("RemoveRoute:10.128.0.0:" + iface) {
					t.Errorf("AddRoute6: rollback missing RemoveRoute (v4): %v", h.log.calls)
				}
				if h.log.has("SetPoolDNS:_WEBADMIN:172.18.0.2") {
					t.Errorf("AddRoute6: DHCP DNS should not have been set")
				}
			case "SetPoolDNS":
				// Routes were added then must be removed in rollback (v4 + v6).
				if !h.log.has("RemoveRoute:10.128.0.0:" + iface) {
					t.Errorf("SetPoolDNS: rollback missing RemoveRoute: %v", h.log.calls)
				}
				if !h.log.has("RemoveRoute6:3f80::/10:" + iface) {
					t.Errorf("SetPoolDNS: rollback missing RemoveRoute6: %v", h.log.calls)
				}
			}
		})
	}
}

// waitForSingbox failure (readiness times out) must roll back everything created so far.
func TestEnableFakeIPTun_RollbackOnReadinessTimeout(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	// Force DNS probe to never answer → waitForSingbox never becomes ready.
	stubFakeIPDNSProbe(t, func(context.Context, string, netip.Prefix) bool { return false })

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
	const iface = "opkgtun0"
	if !h.log.has("InterfaceDown:"+iface) || !h.log.has("Delete:"+iface) {
		t.Errorf("readiness-timeout rollback must tear down iface: %v", h.log.calls)
	}
	if h.log.has("AddRoute:10.128.0.0:255.192.0.0:" + iface) {
		t.Errorf("routes must not be added when readiness fails: %v", h.log.calls)
	}
}

// ---------------------------------------------------------------------------
// No usable egress
// ---------------------------------------------------------------------------

func TestEnableFakeIPTun_RefusesWithoutEgress(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	// Overwrite the router config so route.final references no outbound.
	bad := `{"outbounds":[{"tag":"direct","type":"direct"}],"route":{"final":"missing-tag","rules":[]}}`
	if err := os.WriteFile(filepath.Join(h.dir, "20-router.json"), []byte(bad), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	err := h.svc.Enable(context.Background())
	if err == nil {
		t.Fatal("expected error when route.final is not a configured outbound")
	}
	// Nothing provisioned, nothing persisted.
	if st := h.loadFakeIP(t); st != nil {
		t.Errorf("FakeIP persist = %+v, want nil (refused before any work)", st)
	}
	if len(h.log.calls) != 0 {
		t.Errorf("no provisioning calls expected, got %v", h.log.calls)
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
		DHCP:           &recDHCP{log: log},
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
// advertiseDNSIfHealthy
// ---------------------------------------------------------------------------

func TestAdvertiseDNS_HealthGated(t *testing.T) {
	cfgProxy := &RouterConfig{
		Outbounds: []Outbound{{Tag: "proxy-out", Type: "socks", Server: "1.2.3.4"}},
		Route:     Route{Final: "proxy-out"},
	}

	t.Run("running+egress-up sets DNS", func(t *testing.T) {
		log := &callLog{}
		singbox := newTestSingbox(t)
		singbox.isRunningFn = func() (bool, int) { return true, 1 }
		svc := newTestService(t, Deps{Singbox: singbox, DHCP: &recDHCP{log: log}})

		if err := svc.advertiseDNSIfHealthy(context.Background(), "_WEBADMIN", "172.18.0.2", "opkgtun0", cfgProxy); err != nil {
			t.Fatalf("advertiseDNSIfHealthy: %v", err)
		}
		if !log.has("SetPoolDNS:_WEBADMIN:172.18.0.2") {
			t.Errorf("want SetPoolDNS, got %v", log.calls)
		}
	})

	t.Run("not running clears DNS", func(t *testing.T) {
		log := &callLog{}
		singbox := newTestSingbox(t)
		singbox.isRunningFn = func() (bool, int) { return false, 0 }
		svc := newTestService(t, Deps{Singbox: singbox, DHCP: &recDHCP{log: log}})

		if err := svc.advertiseDNSIfHealthy(context.Background(), "_WEBADMIN", "172.18.0.2", "opkgtun0", cfgProxy); err != nil {
			t.Fatalf("advertiseDNSIfHealthy: %v", err)
		}
		if !log.has("ClearPoolDNS:_WEBADMIN") {
			t.Errorf("want ClearPoolDNS when not running, got %v", log.calls)
		}
	})

	t.Run("egress-down (bind iface no carrier) clears DNS", func(t *testing.T) {
		log := &callLog{}
		singbox := newTestSingbox(t)
		singbox.isRunningFn = func() (bool, int) { return true, 1 }
		svc := newTestService(t, Deps{Singbox: singbox, DHCP: &recDHCP{log: log}})

		cfgBound := &RouterConfig{
			Outbounds: []Outbound{{Tag: "direct-bound", Type: "direct", BindInterface: "nwg2"}},
			Route:     Route{Final: "direct-bound"},
		}
		stubTunReadyProbe(t, func(string) bool { return false }) // carrier down
		if err := svc.advertiseDNSIfHealthy(context.Background(), "_WEBADMIN", "172.18.0.2", "opkgtun0", cfgBound); err != nil {
			t.Fatalf("advertiseDNSIfHealthy: %v", err)
		}
		if !log.has("ClearPoolDNS:_WEBADMIN") {
			t.Errorf("want ClearPoolDNS when bind-iface carrier down, got %v", log.calls)
		}
	})
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
	if !h.log.has("Create:opkgtun2:private") {
		t.Errorf("expected Create opkgtun2, got %v", h.log.calls)
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
	createCount1 := countCalls(h.log, "Create:opkgtun0:private")
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
	if c := countCalls(h.log, "Create:opkgtun0:private"); c != 1 {
		t.Errorf("Create:opkgtun0 count = %d after second Enable, want 1 (no re-provision)", c)
	}
	if h.log.has("Create:opkgtun1:private") {
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
	if c := countCalls(h.log, "Create:opkgtun0:private"); c != 1 {
		t.Fatalf("first Enable Create count = %d, want 1", c)
	}

	// Persist says provisioned (index 0) but the allocator reports NOTHING live —
	// the iface vanished. Guard must fall through and re-provision into index 0.
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{}}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("second Enable (reprovision): %v", err)
	}
	if c := countCalls(h.log, "Create:opkgtun0:private"); c != 2 {
		t.Errorf("Create:opkgtun0 count = %d, want 2 (re-provisioned after iface gone): %v", c, h.log.calls)
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
		{"DHCP", func(s *ServiceImpl) { s.deps.DHCP = nil }},
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
	createCount1 := countCalls(h.log, "Create:opkgtun0:private")
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
	if c := countCalls(h.log, "Create:opkgtun0:private"); c != 1 {
		t.Errorf("Create count = %d after Reconcile, want 1 (no re-provision): %v", c, h.log.calls)
	}
	if h.log.has("Create:opkgtun1:private") {
		t.Errorf("Reconcile leaked a new index: %v", h.log.calls)
	}
}
