package router

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
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
	if !h.log.has("AddRoute:10.128.0.0:255.192.0.0:OpkgTun0") {
		t.Errorf("expected drift-heal to re-add the pool route, got %v", h.log.calls)
	}
	if h.log.has("Create:OpkgTun0:public") || h.log.has("Create:OpkgTun1:public") {
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
// Drift-heal must NOT clear the sticky master-Stop intent. The reprovision
// branch dispatches through enableLocked(ctx, false): a periodic reconcile (or
// the first post-reboot reconcile) that re-provisions a vanished iface must
// honour a user's prior master-Stop, never silently wipe it. Regression for
// the adversarial finding on the unconditional Enable→ClearManualStop.
// ---------------------------------------------------------------------------

func TestReconcileFakeIPTun_Reprovision_DoesNotClearManualStop(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	neutralSourceProbe(t)

	// Provision once via the USER path (this one is allowed to clear).
	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	sb := h.svc.deps.Singbox.(*fakeSingbox)
	sb.clearManualStopCalls = 0 // reset: count only what the drift-heal does.

	// Iface vanished → reconcile takes the reprovision (Enable) arm.
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{}}
	all, _ := h.store.Load()
	sr, _ := NormalizeSingboxRouterSettings(all.SingboxRouter)
	if err := h.svc.reconcileFakeIPTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcileFakeIPTun: %v", err)
	}

	if sb.clearManualStopCalls != 0 {
		t.Errorf("drift-heal reprovision must NOT clear master-Stop intent, ClearManualStop calls = %d", sb.clearManualStopCalls)
	}
	// Sanity: the reprovision actually happened (proves the arm was taken).
	if !h.log.has("Create:OpkgTun0:public") {
		t.Errorf("expected re-provision Create, got %v", h.log.calls)
	}
}

// enableLocked(ctx, false) is the drift-heal entry; it must skip the clear in
// tproxy mode too. Direct unit assertion on the seam, independent of the
// Reconcile dispatch wiring.
func TestEnableLocked_DriftHeal_TproxySkipsClearManualStop(t *testing.T) {
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

	if err := svc.enableLocked(context.Background(), false); err != nil {
		t.Fatalf("enableLocked(false): %v", err)
	}
	if singbox.clearManualStopCalls != 0 {
		t.Errorf("drift-heal enableLocked(false) must NOT clear, calls = %d", singbox.clearManualStopCalls)
	}
	// Public Enable (user path) DOES clear — guards the gate both ways.
	if err := svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable (user): %v", err)
	}
	if singbox.clearManualStopCalls != 1 {
		t.Errorf("user Enable must clear exactly once, calls = %d", singbox.clearManualStopCalls)
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
	if c := countCalls(h.log, "Create:OpkgTun0:public"); c != 1 {
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
	if c := countCalls(h.log, "Create:OpkgTun0:public"); c != 1 {
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

	// Track the orchestrator restart: SetEnabled(SlotFakeIP,true) is the restart.
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
	// The drift-heal MUST directly (re)spawn the dead process: SetEnabled is a
	// no-op for an already-enabled slot (Orch != nil here), so only an explicit
	// Singbox.Start() actually revives it. Assert the spawn happened exactly once.
	if sb.startCalls != 1 {
		t.Errorf("Singbox.Start calls = %d, want 1 (drift-heal must respawn the dead process)", sb.startCalls)
	}
	// Routes re-added because the live route probe is unstubbed here → reads
	// /proc/net/route → opkgtun0 route absent → drift detected → re-add fires.
	if !h.log.has("AddRoute:10.128.0.0:255.192.0.0:OpkgTun0") {
		t.Errorf("drift-heal must re-add v4 pool route when absent, got %v", h.log.calls)
	}
	if !h.log.has("AddRoute6:3f80::/10:OpkgTun0") {
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
	if h.log.has("Create:OpkgTun0:public") || h.log.has("Create:OpkgTun1:public") {
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

	if h.log.has("Create:OpkgTun0:public") || h.log.has("Create:OpkgTun1:public") {
		t.Errorf("healthy drift-heal must NOT Create any iface, got %v", h.log.calls)
	}
	// Persist index unchanged.
	if st := h.loadFakeIP(t); st == nil || st.Index != 0 {
		t.Errorf("FakeIP index changed in healthy drift-heal: %+v", st)
	}
	// But it IS a heal: routes re-added idempotently.
	if !h.log.has("AddRoute:10.128.0.0:255.192.0.0:OpkgTun0") {
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

// TestGetStatus_FakeIPIface asserts the active fakeip iface name is surfaced in
// Status once provisioned in fakeip-tun mode, and is empty when not provisioned.
func TestGetStatus_FakeIPIface(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	h.svc.deps.IPTables = errProbeIPTables()

	// Before provisioning: no fakeip iface in status.
	st0, err := h.svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st0.FakeIPIface != "" {
		t.Errorf("FakeIPIface = %q, want empty before provisioning", st0.FakeIPIface)
	}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	h.svc.deps.IPTables = errProbeIPTables()

	st, err := h.svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.FakeIPIface != "opkgtun0" {
		t.Errorf("FakeIPIface = %q, want opkgtun0", st.FakeIPIface)
	}
}

// TestGetStatus_FakeIPEgressUp asserts the global egress-health signal (Task 25)
// is nil before provisioning and populated (true, for the harness's bindless
// proxy final) once provisioned in fakeip-tun mode.
func TestGetStatus_FakeIPEgressUp(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	h.svc.deps.IPTables = errProbeIPTables()

	// Before provisioning: no egress-health signal (nil → omitted from JSON).
	st0, err := h.svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st0.FakeIPEgressUp != nil {
		t.Errorf("FakeIPEgressUp = %v, want nil before provisioning", *st0.FakeIPEgressUp)
	}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	h.svc.deps.IPTables = errProbeIPTables()

	st, err := h.svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	// The harness route.final="proxy-out" is a bindless proxy outbound, so
	// fakeIPEgressUp returns true (no carrier signal to gate on).
	if st.FakeIPEgressUp == nil || *st.FakeIPEgressUp != true {
		t.Errorf("FakeIPEgressUp = %v, want true (bindless proxy egress)", st.FakeIPEgressUp)
	}
}

// TestGetStatus_PolicyExitFields asserts the PE-E Status fields are GATED on the
// engine being active: nil while inactive (even in fakeip-tun mode), and populated
// (sourcePreserve default-true mirror + policyExitReady true) only once the engine
// is active. Nil for a tproxy router is covered separately.
func TestGetStatus_PolicyExitFields(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	h.svc.deps.IPTables = errProbeIPTables()

	// Before provisioning (fakeip-tun mode but no iface → not active): both fields
	// stay nil (the active gate, not just mode).
	st0, err := h.svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st0.FakeIPSourcePreserve != nil {
		t.Errorf("FakeIPSourcePreserve = %v, want nil while inactive", *st0.FakeIPSourcePreserve)
	}
	if st0.FakeIPPolicyExitReady != nil {
		t.Errorf("FakeIPPolicyExitReady = %v, want nil while inactive", *st0.FakeIPPolicyExitReady)
	}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	h.svc.deps.IPTables = errProbeIPTables()

	// Engine active requires process up (isRunningFn=true in harness) + tun carrier
	// (tunReadyProbe=true in harness) + pool route present — stub the last true.
	stubFakeIPPoolRoutePresent(t, func(string, netip.Prefix) bool { return true })

	st, err := h.svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if !st.Active {
		t.Fatalf("precondition: engine must be active for the PE-E fields to populate")
	}
	if st.FakeIPSourcePreserve == nil || *st.FakeIPSourcePreserve != true {
		t.Errorf("FakeIPSourcePreserve = %v, want true once active", st.FakeIPSourcePreserve)
	}
	if st.FakeIPPolicyExitReady == nil || *st.FakeIPPolicyExitReady != true {
		t.Errorf("FakeIPPolicyExitReady = %v, want true once active in fakeip-tun mode", st.FakeIPPolicyExitReady)
	}

	// Engine goes inactive (e.g. after `POST /mode off` the process is down but
	// RoutingMode stays "fakeip-tun") → both fields revert to nil.
	h.svc.deps.Singbox.(*fakeSingbox).isRunningFn = func() (bool, int) { return false, 0 }
	stInactive, err := h.svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus (inactive): %v", err)
	}
	if stInactive.Active {
		t.Fatalf("precondition: engine must be inactive after process down")
	}
	if stInactive.FakeIPSourcePreserve != nil {
		t.Errorf("FakeIPSourcePreserve = %v, want nil when inactive (mode still fakeip-tun)", *stInactive.FakeIPSourcePreserve)
	}
	if stInactive.FakeIPPolicyExitReady != nil {
		t.Errorf("FakeIPPolicyExitReady = %v, want nil when inactive (mode still fakeip-tun)", *stInactive.FakeIPPolicyExitReady)
	}
}

// TestGetStatus_PolicyExitFields_NilForTproxy asserts the PE-E fields stay nil
// (omitted from JSON) for a tproxy router.
func TestGetStatus_PolicyExitFields_NilForTproxy(t *testing.T) {
	settingsStore := newTestSettingsStore(t, storage.SingboxRouterSettings{
		RoutingMode:   "tproxy",
		DeviceMode:    "all",
		WANAutoDetect: true,
	})
	svc := newTestService(t, Deps{
		Settings: settingsStore,
		Policies: &fakeAccessPolicyProvider{},
		IPTables: errProbeIPTables(),
		Singbox:  newTestSingbox(t),
	})

	st, err := svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.FakeIPSourcePreserve != nil {
		t.Errorf("FakeIPSourcePreserve = %v, want nil for tproxy", *st.FakeIPSourcePreserve)
	}
	if st.FakeIPPolicyExitReady != nil {
		t.Errorf("FakeIPPolicyExitReady = %v, want nil for tproxy", *st.FakeIPPolicyExitReady)
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

	// Steady state: the pool route AND the tun default route are PRESENT. Stub both
	// seams → true so the drift-reconcile sees no route drift. (StaticNAT reader is
	// unwired here → static-NAT drift is skipped.)
	stubFakeIPPoolRoutePresent(t, func(string, netip.Prefix) bool { return true })
	stubFakeIPDefaultRoutePresent(t, func(string) bool { return true })

	h.log.calls = nil // observe only the reconcile tick from here.

	all, _ := h.store.Load()
	sr, _ := NormalizeSingboxRouterSettings(all.SingboxRouter)
	if err := h.svc.reconcileFakeIPTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcileFakeIPTun: %v", err)
	}

	// ZERO NDMS mutations: no route add (v4 or v6), no DHCP write.
	if h.log.has("AddRoute:10.128.0.0:255.192.0.0:OpkgTun0") {
		t.Errorf("steady-state reconcile must NOT add the v4 route, got %v", h.log.calls)
	}
	if h.log.has("AddRoute6:3f80::/10:OpkgTun0") {
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

	if !h.log.has("AddRoute:10.128.0.0:255.192.0.0:OpkgTun0") {
		t.Errorf("absent v4 route must be re-added, got %v", h.log.calls)
	}
	// v6 re-add is gated on the same v4-absence signal.
	if !h.log.has("AddRoute6:3f80::/10:OpkgTun0") {
		t.Errorf("v6 route must be re-added when v4 absent, got %v", h.log.calls)
	}
	// DNS unchanged (Enable already advertised, sing-box still up) → no DHCP write.
	if h.log.has("SetPoolDNS:_WEBADMIN:172.18.0.2") || h.log.has("ClearPoolDNS:_WEBADMIN") {
		t.Errorf("DNS desired state unchanged → no DHCP write, got %v", h.log.calls)
	}
}

// ---------------------------------------------------------------------------
// PE-E: default-route + static-NAT drift re-apply.
// ---------------------------------------------------------------------------

// stubFakeIPDefaultRoutePresent overrides the default-route presence seam.
func stubFakeIPDefaultRoutePresent(t *testing.T, fn func(iface string) bool) {
	t.Helper()
	old := fakeIPDefaultRoutePresent
	fakeIPDefaultRoutePresent = fn
	t.Cleanup(func() { fakeIPDefaultRoutePresent = old })
}

// stubStaticNATReader reports a fixed static-NAT presence for the segment.
type stubStaticNATReader struct {
	present bool
	wan     string
	err     error
}

func (s stubStaticNATReader) ForInterface(context.Context, string) (bool, string, error) {
	return s.present, s.wan, s.err
}

// TestReconcileFakeIPTun_ReappliesDefaultRouteAndStaticNATWhenMissing asserts the
// drift-heal re-installs the tun default route(s) when absent and re-applies
// static-NAT when the delivery segment has reverted to dynamic NAT.
func TestReconcileFakeIPTun_ReappliesDefaultRouteAndStaticNATWhenMissing(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	neutralSourceProbe(t)

	// Wire the source-preserve deps so Enable applies static-NAT and the drift
	// probe has something to read.
	segNAT := &recordingSegmentNAT{}
	h.svc.deps.SegmentNAT = segNAT
	h.svc.deps.DHCPPoolSegments = fakePoolSegments{seg: "Home"}
	h.svc.deps.DefaultGateway = fakeDefaultGateway{id: "PPPoE0"}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{0: true}}

	// Pool route present (don't conflate with default-route drift), default route
	// ABSENT, static-NAT ABSENT (segment drifted back to dynamic masquerade).
	stubFakeIPPoolRoutePresent(t, func(string, netip.Prefix) bool { return true })
	stubFakeIPDefaultRoutePresent(t, func(string) bool { return false })
	h.svc.deps.StaticNAT = stubStaticNATReader{present: false}

	h.log.calls = nil
	segNAT.calls = nil

	all, _ := h.store.Load()
	sr, _ := NormalizeSingboxRouterSettings(all.SingboxRouter)
	if err := h.svc.reconcileFakeIPTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcileFakeIPTun: %v", err)
	}

	// Default route v4 + v6 re-installed (defaults carry a v6 tun addr).
	if !h.log.has("SetDefaultRoute:OpkgTun0") {
		t.Errorf("absent v4 default route must be re-added, got %v", h.log.calls)
	}
	if !h.log.has("SetIPv6DefaultRoute:OpkgTun0") {
		t.Errorf("absent v6 default route must be re-added, got %v", h.log.calls)
	}
	// static-NAT re-applied: RemoveSegmentNAT then SetStaticNAT for the segment.
	wantNAT := []string{"RemoveSegmentNAT Home", "SetStaticNAT Home PPPoE0"}
	if len(segNAT.calls) != len(wantNAT) {
		t.Fatalf("segment NAT calls = %v, want %v", segNAT.calls, wantNAT)
	}
	for i := range wantNAT {
		if segNAT.calls[i] != wantNAT[i] {
			t.Errorf("NAT call[%d] = %q, want %q", i, segNAT.calls[i], wantNAT[i])
		}
	}
}

// TestReconcileFakeIPTun_NoDefaultRouteOrNATReapplyWhenPresent asserts steady
// state makes ZERO default-route / static-NAT mutations.
func TestReconcileFakeIPTun_NoDefaultRouteOrNATReapplyWhenPresent(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	neutralSourceProbe(t)

	segNAT := &recordingSegmentNAT{}
	h.svc.deps.SegmentNAT = segNAT
	h.svc.deps.DHCPPoolSegments = fakePoolSegments{seg: "Home"}
	h.svc.deps.DefaultGateway = fakeDefaultGateway{id: "PPPoE0"}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{0: true}}

	// Everything present: no drift.
	stubFakeIPPoolRoutePresent(t, func(string, netip.Prefix) bool { return true })
	stubFakeIPDefaultRoutePresent(t, func(string) bool { return true })
	h.svc.deps.StaticNAT = stubStaticNATReader{present: true, wan: "PPPoE0"}

	h.log.calls = nil
	segNAT.calls = nil

	all, _ := h.store.Load()
	sr, _ := NormalizeSingboxRouterSettings(all.SingboxRouter)
	if err := h.svc.reconcileFakeIPTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcileFakeIPTun: %v", err)
	}

	if h.log.has("SetDefaultRoute:OpkgTun0") || h.log.has("SetIPv6DefaultRoute:OpkgTun0") {
		t.Errorf("present default route must NOT be re-added, got %v", h.log.calls)
	}
	if len(segNAT.calls) != 0 {
		t.Errorf("present static-NAT must make ZERO NAT calls, got %v", segNAT.calls)
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
	if c := countCalls(h.log, "Create:OpkgTun0:public"); c != 1 {
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

	if h.log.has("Create:OpkgTun0:public") || h.log.has("Create:OpkgTun1:public") {
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

// ---------------------------------------------------------------------------
// Bug #3 live-flip: reconcile tears down static-NAT when preserve flipped off.
// ---------------------------------------------------------------------------

// TestReconcileFakeIPTun_TearsDownStaticNATOnPreserveFlipOff asserts that if
// FakeIPSourcePreserve is turned off while fakeip-tun is RUNNING (persisted
// StaticNATSeg/WAN fact is set), a reconcile tick calls teardownStaticNAT and
// clears the fact — restoring dynamic masquerade without a full disable cycle.
func TestReconcileFakeIPTun_TearsDownStaticNATOnPreserveFlipOff(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	neutralSourceProbe(t)

	// Seed a running state: provisioned + static-NAT fact applied.
	if err := h.store.SetFakeIPState(&storage.FakeIPState{
		Provisioned:  true,
		Index:        0,
		StaticNATSeg: "Home",
		StaticNATWAN: "PPPoE0",
	}); err != nil {
		t.Fatalf("SetFakeIPState: %v", err)
	}

	// Live setting: fakeip-tun ENABLED + preserve FLIPPED OFF.
	all, err := h.store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	f := false
	all.SingboxRouter.Enabled = true
	all.SingboxRouter.FakeIPSourcePreserve = &f
	if err := h.store.Save(all); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Wire SegmentNAT so teardown calls are recorded.
	segNAT := &recordingSegmentNAT{}
	h.svc.deps.SegmentNAT = segNAT
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{0: true}}

	// Pool route + default route present so those drift blocks are no-ops.
	stubFakeIPPoolRoutePresent(t, func(string, netip.Prefix) bool { return true })
	stubFakeIPDefaultRoutePresent(t, func(string) bool { return true })

	all, _ = h.store.Load()
	sr, _ := NormalizeSingboxRouterSettings(all.SingboxRouter)
	if err := h.svc.reconcileFakeIPTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcileFakeIPTun: %v", err)
	}

	// Teardown must have run: RemoveStaticNAT then SetSegmentNAT (dynamic masquerade).
	wantNAT := []string{"RemoveStaticNAT Home PPPoE0", "SetSegmentNAT Home"}
	if len(segNAT.calls) != len(wantNAT) {
		t.Fatalf("segment NAT calls = %v, want %v", segNAT.calls, wantNAT)
	}
	for i := range wantNAT {
		if segNAT.calls[i] != wantNAT[i] {
			t.Errorf("NAT call[%d] = %q, want %q", i, segNAT.calls[i], wantNAT[i])
		}
	}

	// Persisted fact must be cleared.
	st := h.loadFakeIP(t)
	if st == nil {
		t.Fatal("FakeIPState was nil after flip-off teardown (want non-nil with cleared NAT fields)")
	}
	if st.StaticNATSeg != "" || st.StaticNATWAN != "" {
		t.Errorf("static-NAT fact not cleared: StaticNATSeg=%q StaticNATWAN=%q, want both empty", st.StaticNATSeg, st.StaticNATWAN)
	}
}

// TestReconcileFakeIPTun_RevivalEnablesSlotFakeIPNotSlotRouter asserts that
// when a dead sing-box is restarted by the drift-heal, the reconcile re-enables
// the FAKEIP slot (21-fakeip.json) and NOT the tproxy router slot (20-router.json).
func TestReconcileFakeIPTun_RevivalEnablesSlotFakeIPNotSlotRouter(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	neutralSourceProbe(t)

	// Provision with sing-box running.
	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{0: true}}
	h.log.calls = nil

	// Manually flip SlotFakeIP OFF to simulate it having been disabled (e.g.
	// after a prior disable or crash) — the reconcile must re-enable it.
	if err := h.svc.deps.Orch.SetEnabled(orchestrator.SlotFakeIP, false); err != nil {
		t.Fatalf("pre-flip SlotFakeIP off: %v", err)
	}
	// SlotRouter stays OFF (XOR invariant set by Enable).
	if slotEnabled(t, h.svc, orchestrator.SlotRouter) {
		t.Fatal("precondition: SlotRouter must be off (XOR)")
	}

	// Model a dead sing-box: IsRunning returns false on the first probe (the
	// drift-heal liveness check), then true (waitForSingbox + DNS).
	sb := h.svc.deps.Singbox.(*fakeSingbox)
	calls := 0
	sb.isRunningFn = func() (bool, int) {
		calls++
		if calls == 1 {
			return false, 0
		}
		return true, 1234
	}

	all, _ := h.store.Load()
	sr, _ := NormalizeSingboxRouterSettings(all.SingboxRouter)
	if err := h.svc.reconcileFakeIPTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcileFakeIPTun: %v", err)
	}

	// After revival, SlotFakeIP must be ENABLED (reconcile re-enabled the fakeip slot).
	if !slotEnabled(t, h.svc, orchestrator.SlotFakeIP) {
		t.Error("SlotFakeIP must be ENABLED after fakeip-reconcile revival")
	}
	// SlotRouter must remain DISABLED — revival must NOT toggle the tproxy slot.
	if slotEnabled(t, h.svc, orchestrator.SlotRouter) {
		t.Error("SlotRouter must remain DISABLED after fakeip-reconcile revival — tproxy slot must not be touched")
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
