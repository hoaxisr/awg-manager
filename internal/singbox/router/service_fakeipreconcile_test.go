package router

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// stubSourcePreservedProbe overrides the source-preservation seam for the test
// duration and restores it via t.Cleanup.
func stubSourcePreservedProbe(t *testing.T, fn func(iface, tunOwnAddr string) (bool, bool)) {
	t.Helper()
	old := sourcePreservedProbe
	sourcePreservedProbe = fn
	t.Cleanup(func() { sourcePreservedProbe = old })
}

// neutralSourceProbe is a seam stub that reports "unknown" so drift-heal tests
// not concerned with source-preservation don't store a verdict or warn.
func neutralSourceProbe(t *testing.T) {
	stubSourcePreservedProbe(t, func(string, string) (bool, bool) { return false, false })
}

// errProbeIPTables returns an IPTables whose probes always error — GetStatus
// calls Probe() and the orched harness leaves IPTables nil, which would panic.
func errProbeIPTables() *IPTables {
	return &IPTables{
		runIPTables:    func(context.Context, ...string) error { return errors.New("no chain") },
		runIPTablesOut: func(context.Context, ...string) (string, error) { return "", errors.New("no chain") },
	}
}

// ---------------------------------------------------------------------------
// Dispatch: fakeip-tun mode routes Reconcile to reconcileFakeIPTun; tproxy mode
// still uses the installed-check switch.
// ---------------------------------------------------------------------------

func TestReconcile_DispatchesFakeIPTun(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	neutralSourceProbe(t)
	// IPTables that errors on every probe — exactly the fakeip-tun reality.
	h.svc.deps.IPTables = &IPTables{
		runIPTables:    func(context.Context, ...string) error { return errors.New("no chain") },
		runIPTablesOut: func(context.Context, ...string) (string, error) { return "", errors.New("no chain") },
	}

	// Provision first so Enabled=true + provisioned + live, then a Reconcile must
	// take the drift-heal arm (NOT the tproxy switch, NOT Enable re-provision).
	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{0: true}}
	h.log.calls = nil

	if err := h.svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// Drift-heal re-adds the pool route idempotently — a fakeip-only call that the
	// tproxy switch would never make. No new Create (no re-provision).
	if !h.log.has("AddRoute:10.128.0.0:255.192.0.0:opkgtun0") {
		t.Errorf("expected drift-heal to re-add the pool route, got %v", h.log.calls)
	}
	if h.log.has("Create:opkgtun0:private") || h.log.has("Create:opkgtun1:private") {
		t.Errorf("drift-heal must not re-provision, got %v", h.log.calls)
	}
}

// tproxy Reconcile must still flow through the installed-check switch and never
// touch the fakeip deps.
func TestReconcile_TproxyStillUsesSwitch(t *testing.T) {
	settingsStore := newTestSettingsStore(t, storage.SingboxRouterSettings{
		RoutingMode:   "tproxy",
		DeviceMode:    "all",
		WANAutoDetect: true,
		Enabled:       false, // disabled + nothing installed → switch returns nil (no-op)
	})
	singbox := newTestSingbox(t)
	log := &callLog{}
	svc := newTestService(t, Deps{
		Settings:       settingsStore,
		Policies:       &fakeAccessPolicyProvider{},
		IPTables:       newStubIPTables(func(context.Context, string) error { return nil }),
		Singbox:        singbox,
		WANIPCollector: &fakeWANIPCollector{},
		OpkgTun:        &recOpkgTun{log: log},
		StaticRoutes:   &recStaticRoutes{log: log},
		DHCP:           &recDHCP{log: log},
		OpkgTunIndices: &recIndices{live: map[int]bool{}},
		FakeIPTun:      DefaultFakeIPTunParams(),
	})

	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile (tproxy): %v", err)
	}
	if len(log.calls) != 0 {
		t.Errorf("tproxy Reconcile must not call any fakeip dep, got %v", log.calls)
	}
}

// ---------------------------------------------------------------------------
// !Enabled → Disable (teardown).
// ---------------------------------------------------------------------------

func TestReconcileFakeIPTun_DisabledDisables(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	captureDrain(t)
	provisionForDisable(t, h) // provisions + clears log + live index 0

	// Flip persisted Enabled=false so reconcile takes the Disable arm.
	all, _ := h.store.Load()
	all.SingboxRouter.Enabled = false
	if err := h.store.Save(all); err != nil {
		t.Fatalf("Save: %v", err)
	}

	sr, _ := NormalizeSingboxRouterSettings(all.SingboxRouter)
	if err := h.svc.reconcileFakeIPTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcileFakeIPTun: %v", err)
	}

	// disableFakeIPTun teardown ran (ClearPoolDNS is a fakeip teardown call).
	if !h.log.has("ClearPoolDNS:_WEBADMIN") {
		t.Errorf("disabled reconcile must run teardown, got %v", h.log.calls)
	}
	if st := h.loadFakeIP(t); st != nil {
		t.Errorf("FakeIP persist = %+v, want nil after teardown", st)
	}
}

// ---------------------------------------------------------------------------
// Enabled + not-provisioned / iface-gone → Enable (re-provision).
// ---------------------------------------------------------------------------

func TestReconcileFakeIPTun_ReprovisionsWhenGone(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	neutralSourceProbe(t)

	// Provision once.
	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if c := countCalls(h.log, "Create:opkgtun0:private"); c != 1 {
		t.Fatalf("after Enable Create count = %d, want 1", c)
	}
	h.log.calls = nil

	// Persist still says provisioned (index 0) but NOTHING is live — the iface
	// vanished. reconcile must fall to Enable, which re-provisions into index 0.
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{}}

	all, _ := h.store.Load()
	sr, _ := NormalizeSingboxRouterSettings(all.SingboxRouter)
	if err := h.svc.reconcileFakeIPTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcileFakeIPTun: %v", err)
	}
	if c := countCalls(h.log, "Create:opkgtun0:private"); c != 1 {
		t.Errorf("Create count = %d, want 1 (re-provisioned after iface gone): %v", c, h.log.calls)
	}
}

// ---------------------------------------------------------------------------
// DRIFT-HEAL: provisioned + live + sing-box NOT running → restart attempted,
// routes re-added, DNS re-advertised.
// ---------------------------------------------------------------------------

func TestReconcileFakeIPTun_DriftHealRestartsDeadSingbox(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	neutralSourceProbe(t)

	// Provision with sing-box running so Enable succeeds.
	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{0: true}}
	h.log.calls = nil

	// Now model a DEAD sing-box that comes back up after the restart: IsRunning
	// returns false on the first call (the drift-heal liveness check) then true
	// (waitForSingbox readiness + advertiseDNSIfHealthy).
	sb := h.svc.deps.Singbox.(*fakeSingbox)
	calls := 0
	sb.isRunningFn = func() (bool, int) {
		calls++
		if calls == 1 {
			return false, 0
		}
		return true, 1234
	}

	// Track the orchestrator restart: SetEnabled(SlotRouter,true) is the restart.
	// The real orch records it via the slot's enabled file; assert via behaviour —
	// after the heal, routes were re-added and DNS re-advertised.
	all, _ := h.store.Load()
	sr, _ := NormalizeSingboxRouterSettings(all.SingboxRouter)
	if err := h.svc.reconcileFakeIPTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcileFakeIPTun: %v", err)
	}

	if calls < 1 {
		t.Fatalf("IsRunning was never probed (restart path not taken)")
	}
	// Routes re-added because the live route probe is unstubbed here → reads
	// /proc/net/route → opkgtun0 route absent → drift detected → re-add fires.
	if !h.log.has("AddRoute:10.128.0.0:255.192.0.0:opkgtun0") {
		t.Errorf("drift-heal must re-add v4 pool route when absent, got %v", h.log.calls)
	}
	if !h.log.has("AddRoute6:3f80::/10:opkgtun0") {
		t.Errorf("drift-heal must re-add v6 pool route when v4 absent, got %v", h.log.calls)
	}
	// DNS is NOT rewritten: Enable already advertised (cache=true) and sing-box
	// recovers, so the DESIRED state stays "advertise" → change-detection skips the
	// DHCP write. The restart itself still happened (IsRunning-gated). This is the
	// B2 fix — an egress/process flap that returns to the same desired state makes
	// no DHCP write.
	if h.log.has("SetPoolDNS:_WEBADMIN:172.18.0.2") {
		t.Errorf("drift-heal must NOT rewrite DNS when desired state unchanged, got %v", h.log.calls)
	}
	if h.log.has("ClearPoolDNS:_WEBADMIN") {
		t.Errorf("drift-heal must NOT clear DNS when desired state unchanged, got %v", h.log.calls)
	}
	// No re-provision.
	if h.log.has("Create:opkgtun0:private") || h.log.has("Create:opkgtun1:private") {
		t.Errorf("drift-heal must not re-provision the iface, got %v", h.log.calls)
	}
}

// ---------------------------------------------------------------------------
// DRIFT-HEAL with a healthy sing-box: NO new index allocated, NO Create.
// ---------------------------------------------------------------------------

func TestReconcileFakeIPTun_NoReprovisionWhenHealthy(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	neutralSourceProbe(t)

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	// Index 0 live + a (bogus) re-provision would pick index 1 → proves no realloc.
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{0: true}}
	h.log.calls = nil

	all, _ := h.store.Load()
	sr, _ := NormalizeSingboxRouterSettings(all.SingboxRouter)
	if err := h.svc.reconcileFakeIPTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcileFakeIPTun: %v", err)
	}

	if h.log.has("Create:opkgtun0:private") || h.log.has("Create:opkgtun1:private") {
		t.Errorf("healthy drift-heal must NOT Create any iface, got %v", h.log.calls)
	}
	// Persist index unchanged.
	if st := h.loadFakeIP(t); st == nil || st.Index != 0 {
		t.Errorf("FakeIP index changed in healthy drift-heal: %+v", st)
	}
	// But it IS a heal: routes re-added idempotently.
	if !h.log.has("AddRoute:10.128.0.0:255.192.0.0:opkgtun0") {
		t.Errorf("healthy drift-heal still re-adds routes idempotently, got %v", h.log.calls)
	}
}

// ---------------------------------------------------------------------------
// source-preservation assert: seam → Status wiring.
// ---------------------------------------------------------------------------

func TestAssertSourcePreserved_SNATDetected(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	stubSourcePreservedProbe(t, func(string, string) (bool, bool) { return false, true })

	h.svc.assertSourcePreserved("opkgtun0", "172.18.0.1")

	v := h.svc.loadSourcePreserved()
	if v == nil || *v != false {
		t.Fatalf("stored verdict = %v, want false (SNAT detected)", v)
	}

	// Provision so GetStatus reads RoutingMode=fakeip-tun.
	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	h.svc.deps.IPTables = errProbeIPTables()
	// Re-store false (Enable does not touch the verdict, but be explicit).
	f := false
	h.svc.storeSourcePreserved(&f)

	st, err := h.svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.SourcePreserved == nil || *st.SourcePreserved != false {
		t.Errorf("Status.SourcePreserved = %v, want false", st.SourcePreserved)
	}
	found := false
	for _, is := range st.Issues {
		if is.Kind == "source-preservation" {
			found = true
			if is.Severity != "warning" {
				t.Errorf("source-preservation issue severity = %q, want warning", is.Severity)
			}
		}
	}
	if !found {
		t.Errorf("expected a source-preservation Issue, got %+v", st.Issues)
	}
}

func TestAssertSourcePreserved_Preserved(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	stubSourcePreservedProbe(t, func(string, string) (bool, bool) { return true, true })

	h.svc.assertSourcePreserved("opkgtun0", "172.18.0.1")

	v := h.svc.loadSourcePreserved()
	if v == nil || *v != true {
		t.Fatalf("stored verdict = %v, want true", v)
	}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	h.svc.deps.IPTables = errProbeIPTables()
	tr := true
	h.svc.storeSourcePreserved(&tr)

	st, err := h.svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.SourcePreserved == nil || *st.SourcePreserved != true {
		t.Errorf("Status.SourcePreserved = %v, want true", st.SourcePreserved)
	}
	for _, is := range st.Issues {
		if is.Kind == "source-preservation" {
			t.Errorf("preserved verdict must raise NO source-preservation issue, got %+v", st.Issues)
		}
	}
}

// ---------------------------------------------------------------------------
// Fix B1/B2: drift DETECTION — steady state makes ZERO NDMS mutations per tick.
// ---------------------------------------------------------------------------

// Provisioned + live + running, route PRESENT (stubbed), DNS already advertised
// (Enable set the cache) → the drift-reconcile must make NO AddStaticRoute and NO
// SetPoolDNS/ClearPoolDNS. This is the core of the fix: zero RCI writes in steady
// state.
func TestReconcileFakeIPTun_NoMutationWhenNoDrift(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	neutralSourceProbe(t)

	// Provision: Enable adds the v4+v6 routes and advertises DNS (cache → true).
	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{0: true}}

	// Steady state: the pool route is PRESENT. Stub the seam → true so the
	// drift-reconcile sees no route drift.
	stubFakeIPPoolRoutePresent(t, func(string, netip.Prefix) bool { return true })

	h.log.calls = nil // observe only the reconcile tick from here.

	all, _ := h.store.Load()
	sr, _ := NormalizeSingboxRouterSettings(all.SingboxRouter)
	if err := h.svc.reconcileFakeIPTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcileFakeIPTun: %v", err)
	}

	// ZERO NDMS mutations: no route add (v4 or v6), no DHCP write.
	if h.log.has("AddRoute:10.128.0.0:255.192.0.0:opkgtun0") {
		t.Errorf("steady-state reconcile must NOT add the v4 route, got %v", h.log.calls)
	}
	if h.log.has("AddRoute6:3f80::/10:opkgtun0") {
		t.Errorf("steady-state reconcile must NOT add the v6 route, got %v", h.log.calls)
	}
	if h.log.has("SetPoolDNS:_WEBADMIN:172.18.0.2") {
		t.Errorf("steady-state reconcile must NOT set pool DNS, got %v", h.log.calls)
	}
	if h.log.has("ClearPoolDNS:_WEBADMIN") {
		t.Errorf("steady-state reconcile must NOT clear pool DNS, got %v", h.log.calls)
	}
	// Proof: the WHOLE tick produced no recorded NDMS call at all.
	if len(h.log.calls) != 0 {
		t.Errorf("steady-state reconcile must make ZERO NDMS mutations, got %v", h.log.calls)
	}
}

// Route ABSENT (stubbed → false) → the drift-reconcile re-adds it.
func TestReconcileFakeIPTun_ReaddsRouteWhenMissing(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	neutralSourceProbe(t)

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{0: true}}

	// Route drifted away.
	stubFakeIPPoolRoutePresent(t, func(string, netip.Prefix) bool { return false })

	h.log.calls = nil

	all, _ := h.store.Load()
	sr, _ := NormalizeSingboxRouterSettings(all.SingboxRouter)
	if err := h.svc.reconcileFakeIPTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcileFakeIPTun: %v", err)
	}

	if !h.log.has("AddRoute:10.128.0.0:255.192.0.0:opkgtun0") {
		t.Errorf("absent v4 route must be re-added, got %v", h.log.calls)
	}
	// v6 re-add is gated on the same v4-absence signal.
	if !h.log.has("AddRoute6:3f80::/10:opkgtun0") {
		t.Errorf("v6 route must be re-added when v4 absent, got %v", h.log.calls)
	}
	// DNS unchanged (Enable already advertised, sing-box still up) → no DHCP write.
	if h.log.has("SetPoolDNS:_WEBADMIN:172.18.0.2") || h.log.has("ClearPoolDNS:_WEBADMIN") {
		t.Errorf("DNS desired state unchanged → no DHCP write, got %v", h.log.calls)
	}
}

// ---------------------------------------------------------------------------
// Fix B4: a transient LiveOpkgTunIndices error must NOT trigger re-provision.
// ---------------------------------------------------------------------------

// errIndices reports a probe error from LiveOpkgTunIndices.
type errIndices struct{}

func (errIndices) LiveOpkgTunIndices(context.Context) (map[int]bool, error) {
	return nil, errors.New("transient NDMS probe glitch")
}

func TestReconcileFakeIPTun_ProbeErrorNoReprovision(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	neutralSourceProbe(t)

	// Provision so persist is provisioned (index 0).
	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if c := countCalls(h.log, "Create:opkgtun0:private"); c != 1 {
		t.Fatalf("after Enable Create count = %d, want 1", c)
	}
	h.log.calls = nil

	// The liveness probe now ERRORS. A transient glitch must NOT be read as
	// "iface gone" → no Enable re-provision (no new Create / no new index).
	h.svc.deps.OpkgTunIndices = errIndices{}

	all, _ := h.store.Load()
	sr, _ := NormalizeSingboxRouterSettings(all.SingboxRouter)
	if err := h.svc.reconcileFakeIPTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcileFakeIPTun: %v", err)
	}

	if h.log.has("Create:opkgtun0:private") || h.log.has("Create:opkgtun1:private") {
		t.Errorf("probe error must NOT re-provision, got %v", h.log.calls)
	}
}

// ---------------------------------------------------------------------------
// Fix B3: source-preservation surface must not survive Disable / a disabled
// fakeip-mode router.
// ---------------------------------------------------------------------------

// Verdict stored false, RoutingMode=fakeip-tun but Enabled=false → GetStatus
// surfaces neither Status.SourcePreserved nor a source-preservation Issue.
func TestGetStatus_SourcePreservation_HiddenWhenDisabled(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	h.svc.deps.IPTables = errProbeIPTables()

	// Persist fakeip-tun mode but DISABLED.
	all, _ := h.store.Load()
	all.SingboxRouter = storage.SingboxRouterSettings{RoutingMode: "fakeip-tun", Enabled: false, WANAutoDetect: true}
	if err := h.store.Save(all); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// A stale verdict lingers in memory.
	f := false
	h.svc.storeSourcePreserved(&f)

	st, err := h.svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.SourcePreserved != nil {
		t.Errorf("Status.SourcePreserved = %v, want nil when disabled", st.SourcePreserved)
	}
	for _, is := range st.Issues {
		if is.Kind == "source-preservation" {
			t.Errorf("disabled fakeip-mode router must raise NO source-preservation issue, got %+v", st.Issues)
		}
	}
}

func TestAssertSourcePreserved_UnknownNoWarn(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	// Seed a prior verdict to prove "unknown" overwrites it to nil.
	f := false
	h.svc.storeSourcePreserved(&f)
	stubSourcePreservedProbe(t, func(string, string) (bool, bool) { return false, false })

	h.svc.assertSourcePreserved("opkgtun0", "172.18.0.1")

	if v := h.svc.loadSourcePreserved(); v != nil {
		t.Fatalf("stored verdict = %v, want nil (unknown)", v)
	}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	h.svc.deps.IPTables = errProbeIPTables()
	st, err := h.svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.SourcePreserved != nil {
		t.Errorf("Status.SourcePreserved = %v, want nil (unknown)", st.SourcePreserved)
	}
	for _, is := range st.Issues {
		if is.Kind == "source-preservation" {
			t.Errorf("unknown verdict must raise NO issue (no false alarm), got %+v", st.Issues)
		}
	}
}
