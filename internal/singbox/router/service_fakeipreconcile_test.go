package router

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

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
	if !h.log.has("AddRoute:198.18.0.0:255.254.0.0:OpkgTun0") {
		t.Errorf("expected drift-heal to re-add the pool route, got %v", h.log.calls)
	}
	if h.log.has("Create:OpkgTun0:private") || h.log.has("Create:OpkgTun1:private") {
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

	// disableFakeIPTun teardown ran (the reject-route renew is a fakeip teardown call).
	if !h.log.has("AddRejectRoute:198.18.0.0:255.254.0.0:OpkgTun0") {
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
	if !h.log.has("Create:OpkgTun0:private") {
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

	// Provision once.
	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if c := countCalls(h.log, "Create:OpkgTun0:private"); c != 1 {
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
	if c := countCalls(h.log, "Create:OpkgTun0:private"); c != 1 {
		t.Errorf("Create count = %d, want 1 (re-provisioned after iface gone): %v", c, h.log.calls)
	}
}

// ---------------------------------------------------------------------------
// DRIFT-HEAL: provisioned + live + sing-box NOT running → restart attempted,
// routes re-added, DNS re-advertised.
// ---------------------------------------------------------------------------

func TestReconcileFakeIPTun_DriftHealRestartsDeadSingbox(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")

	// Provision with sing-box running so Enable succeeds.
	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{0: true}}
	h.log.calls = nil

	// Now model a DEAD sing-box that comes back up after the restart: IsRunning
	// returns false on the first call (the drift-heal liveness check) then true
	// (waitForSingbox readiness).
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
	// after the heal, routes were re-added.
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
	if !h.log.has("AddRoute:198.18.0.0:255.254.0.0:OpkgTun0") {
		t.Errorf("drift-heal must re-add v4 pool route when absent, got %v", h.log.calls)
	}
	if !h.log.has("AddRoute6:fc00::/18:OpkgTun0") {
		t.Errorf("drift-heal must re-add v6 pool route when v4 absent, got %v", h.log.calls)
	}
	// No re-provision.
	if h.log.has("Create:OpkgTun0:private") || h.log.has("Create:OpkgTun1:private") {
		t.Errorf("drift-heal must not re-provision the iface, got %v", h.log.calls)
	}
}

// ---------------------------------------------------------------------------
// DRIFT-HEAL with a healthy sing-box: NO new index allocated, NO Create.
// ---------------------------------------------------------------------------

func TestReconcileFakeIPTun_NoReprovisionWhenHealthy(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")

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

	if h.log.has("Create:OpkgTun0:private") || h.log.has("Create:OpkgTun1:private") {
		t.Errorf("healthy drift-heal must NOT Create any iface, got %v", h.log.calls)
	}
	// Persist index unchanged.
	if st := h.loadFakeIP(t); st == nil || st.Index != 0 {
		t.Errorf("FakeIP index changed in healthy drift-heal: %+v", st)
	}
	// But it IS a heal: routes re-added idempotently.
	if !h.log.has("AddRoute:198.18.0.0:255.254.0.0:OpkgTun0") {
		t.Errorf("healthy drift-heal still re-adds routes idempotently, got %v", h.log.calls)
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

// ---------------------------------------------------------------------------
// Fix B1/B2: drift DETECTION — steady state makes ZERO NDMS mutations per tick.
// ---------------------------------------------------------------------------

// Provisioned + live + running, route PRESENT (stubbed) → the drift-reconcile
// must make NO AddStaticRoute. This is the core of the fix: zero RCI writes in
// steady state.
func TestReconcileFakeIPTun_NoMutationWhenNoDrift(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")

	// Provision: Enable adds the v4+v6 routes.
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

	// ZERO NDMS mutations: no route add (v4 or v6).
	if h.log.has("AddRoute:198.18.0.0:255.254.0.0:OpkgTun0") {
		t.Errorf("steady-state reconcile must NOT add the v4 route, got %v", h.log.calls)
	}
	if h.log.has("AddRoute6:fc00::/18:OpkgTun0") {
		t.Errorf("steady-state reconcile must NOT add the v6 route, got %v", h.log.calls)
	}
	// Proof: the WHOLE tick produced no recorded NDMS call at all.
	if len(h.log.calls) != 0 {
		t.Errorf("steady-state reconcile must make ZERO NDMS mutations, got %v", h.log.calls)
	}
}

// Route ABSENT (stubbed → false) → the drift-reconcile re-adds it.
func TestReconcileFakeIPTun_ReaddsRouteWhenMissing(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")

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

	if !h.log.has("AddRoute:198.18.0.0:255.254.0.0:OpkgTun0") {
		t.Errorf("absent v4 route must be re-added, got %v", h.log.calls)
	}
	// v6 re-add is gated on the same v4-absence signal.
	if !h.log.has("AddRoute6:fc00::/18:OpkgTun0") {
		t.Errorf("v6 route must be re-added when v4 absent, got %v", h.log.calls)
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

	// Provision so persist is provisioned (index 0).
	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if c := countCalls(h.log, "Create:OpkgTun0:private"); c != 1 {
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

	if h.log.has("Create:OpkgTun0:private") || h.log.has("Create:OpkgTun1:private") {
		t.Errorf("probe error must NOT re-provision, got %v", h.log.calls)
	}
}

// TestReconcileFakeIPTun_RevivalEnablesSlotFakeIPNotSlotRouter asserts that
// when a dead sing-box is restarted by the drift-heal, the reconcile re-enables
// the FAKEIP slot (21-fakeip.json) and NOT the tproxy router slot (20-router.json).
func TestReconcileFakeIPTun_RevivalEnablesSlotFakeIPNotSlotRouter(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")

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
