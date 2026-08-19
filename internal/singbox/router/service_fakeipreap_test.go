package router

import (
	"context"
	"errors"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// recordingOpkgTunProvisioner embeds fakeOpkgTunProvisioner (default no-op
// methods) and overrides DeleteOpkgTun/ClearAddress to record the names they
// were called with; Delete optionally returns an injected error.
type recordingOpkgTunProvisioner struct {
	fakeOpkgTunProvisioner
	deleted []string
	cleared []string
	delErr  error
}

func (r *recordingOpkgTunProvisioner) DeleteOpkgTun(_ context.Context, name string) error {
	r.deleted = append(r.deleted, name)
	return r.delErr
}

func (r *recordingOpkgTunProvisioner) ClearAddress(_ context.Context, name string) error {
	r.cleared = append(r.cleared, name)
	return nil
}

// scanReturning builds an OpkgTunScan hook returning a fixed set of NDMS
// OpkgTun IDs (or an error) for the description-scan fallback tests. The ids
// answer the FAKEIP description only — the reap scans policy-tun with its own
// description too, and in production the two sets are disjoint (one iface
// carries one description).
func scanReturning(ids []string, err error) func(context.Context, string) ([]string, error) {
	return func(_ context.Context, desc string) ([]string, error) {
		if err != nil {
			return nil, err
		}
		if desc != fakeIPTunDescription {
			return nil, nil
		}
		return ids, nil
	}
}

// newReapSettingsStore seeds a store with the given RoutingMode and, when
// provisioned, a FakeIPState at index — the crash-recovery input for the reap.
func newReapSettingsStore(t *testing.T, mode string, index int, provisioned bool) *storage.SettingsStore {
	t.Helper()
	store := newTestSettingsStore(t, storage.SingboxRouterSettings{
		RoutingMode:   mode,
		WANAutoDetect: true,
	})
	if provisioned {
		if err := store.SetOpkgTunState(&storage.OpkgTunState{Mode: storage.OpkgTunModeFakeIP, Provisioned: true, Index: index, FakeIP: &storage.OpkgTunFakeIPData{Inet4Range: "198.18.0.0/15", Inet6Range: "fc00::/18"}}); err != nil {
			t.Fatalf("SetOpkgTunState: %v", err)
		}
	}
	return store
}

func loadFakeIP(t *testing.T, store *storage.SettingsStore) *storage.OpkgTunState {
	t.Helper()
	all, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return all.OpkgTun
}

func TestReapOrphaned_RemovesAndClears(t *testing.T) {
	store := newReapSettingsStore(t, "tproxy", 3, true)
	opkg := &recordingOpkgTunProvisioner{}
	svc := newTestService(t, Deps{Settings: store, OpkgTun: opkg})

	if err := svc.ReapOrphanedFakeIPTun(context.Background()); err != nil {
		t.Fatalf("ReapOrphanedFakeIPTun: %v", err)
	}
	// Bug 1: DeleteOpkgTun takes the CamelCase NDMS name (NDMS rejects lowercase).
	if len(opkg.deleted) != 1 || opkg.deleted[0] != "OpkgTun3" {
		t.Errorf("DeleteOpkgTun calls = %v, want [OpkgTun3]", opkg.deleted)
	}
	if got := loadFakeIP(t, store); got != nil {
		t.Errorf("FakeIP persist = %+v, want nil after reap", got)
	}
}

func TestReapOrphaned_NoopInFakeIPMode(t *testing.T) {
	store := newReapSettingsStore(t, "fakeip-tun", 2, true)
	opkg := &recordingOpkgTunProvisioner{}
	svc := newTestService(t, Deps{Settings: store, OpkgTun: opkg})

	if err := svc.ReapOrphanedFakeIPTun(context.Background()); err != nil {
		t.Fatalf("ReapOrphanedFakeIPTun: %v", err)
	}
	if len(opkg.deleted) != 0 {
		t.Errorf("DeleteOpkgTun must not be called in fakeip-tun mode, got %v", opkg.deleted)
	}
	if got := loadFakeIP(t, store); got == nil || !got.Provisioned || got.Index != 2 {
		t.Errorf("persist must be unchanged in fakeip-tun mode, got %+v", got)
	}
}

func TestReapOrphaned_NoopWhenNotProvisioned(t *testing.T) {
	store := newReapSettingsStore(t, "tproxy", 0, false)
	opkg := &recordingOpkgTunProvisioner{}
	svc := newTestService(t, Deps{Settings: store, OpkgTun: opkg})

	if err := svc.ReapOrphanedFakeIPTun(context.Background()); err != nil {
		t.Fatalf("ReapOrphanedFakeIPTun: %v", err)
	}
	if len(opkg.deleted) != 0 {
		t.Errorf("DeleteOpkgTun must not be called when nothing is provisioned, got %v", opkg.deleted)
	}
	if got := loadFakeIP(t, store); got != nil {
		t.Errorf("FakeIP persist = %+v, want nil (was never set)", got)
	}
}

func TestReapOrphaned_Idempotent(t *testing.T) {
	store := newReapSettingsStore(t, "tproxy", 1, true)
	opkg := &recordingOpkgTunProvisioner{}
	svc := newTestService(t, Deps{Settings: store, OpkgTun: opkg})

	if err := svc.ReapOrphanedFakeIPTun(context.Background()); err != nil {
		t.Fatalf("first reap: %v", err)
	}
	// Second call: persist is already cleared, so it must be a pure no-op.
	if err := svc.ReapOrphanedFakeIPTun(context.Background()); err != nil {
		t.Fatalf("second reap: %v", err)
	}
	if len(opkg.deleted) != 1 {
		t.Errorf("DeleteOpkgTun called %d times, want exactly 1 (second call no-op)", len(opkg.deleted))
	}
}

func TestReapOrphaned_DeleteFailureKeepsPersist(t *testing.T) {
	store := newReapSettingsStore(t, "tproxy", 4, true)
	opkg := &recordingOpkgTunProvisioner{delErr: errors.New("ndms down")}
	svc := newTestService(t, Deps{Settings: store, OpkgTun: opkg})

	err := svc.ReapOrphanedFakeIPTun(context.Background())
	if err == nil {
		t.Fatal("expected error when DeleteOpkgTun fails")
	}
	// Persist must survive so the next boot retries the reap.
	if got := loadFakeIP(t, store); got == nil || got.Index != 4 {
		t.Errorf("persist must be kept on delete failure for retry, got %+v", got)
	}
}

// Fix 1: in NON-fakeip mode the reap also best-effort sweeps a stale v4 drain
// reject route for the CONFIGURED pool (startup safety net for a drain
// interrupted by restart / an async-remove that didn't match).
func TestReapOrphaned_SweepsStaleRejectRoute(t *testing.T) {
	store := newReapSettingsStore(t, "tproxy", 3, true)
	opkg := &recordingOpkgTunProvisioner{}
	log := &callLog{}
	routes := &recStaticRoutes{log: log}
	svc := newTestService(t, Deps{
		Settings:     store,
		OpkgTun:      opkg,
		StaticRoutes: routes,
		FakeIPTun:    DefaultFakeIPTunParams(), // Inet4Range default 198.18.0.0/15
	})

	if err := svc.ReapOrphanedFakeIPTun(context.Background()); err != nil {
		t.Fatalf("ReapOrphanedFakeIPTun: %v", err)
	}
	// Bug 2 model: the kill-switch reject route is interface-bound, so the sweep
	// targets it by the persisted index's NDMS name (OpkgTun3) via the stand-
	// verified remove form ({…,no:true}, no reject flag → fake records RemoveRoute).
	if !log.has("RemoveRoute:198.18.0.0:OpkgTun3") {
		t.Errorf("stale kill-switch route sweep missing, got %v", log.calls)
	}
}

// Fix 1: in fakeip-tun mode the reap early-returns BEFORE the sweep — the active
// owner manages its own drain; the startup sweep must NOT touch it.
func TestReapOrphaned_NoSweepInFakeIPMode(t *testing.T) {
	store := newReapSettingsStore(t, "fakeip-tun", 2, true)
	opkg := &recordingOpkgTunProvisioner{}
	log := &callLog{}
	routes := &recStaticRoutes{log: log}
	svc := newTestService(t, Deps{
		Settings:     store,
		OpkgTun:      opkg,
		StaticRoutes: routes,
		FakeIPTun:    DefaultFakeIPTunParams(),
	})

	if err := svc.ReapOrphanedFakeIPTun(context.Background()); err != nil {
		t.Fatalf("ReapOrphanedFakeIPTun: %v", err)
	}
	if log.has("RemoveRoute:198.18.0.0:OpkgTun2") {
		t.Errorf("fakeip-tun mode must NOT sweep the reject route (early return), got %v", log.calls)
	}
}

func TestReapOrphaned_NilOpkgKeepsPersist(t *testing.T) {
	// Degraded/test path: no provisioner to reap with. We KEEP the persist —
	// clearing it would convert a tracked orphan into an un-reapable persist-less
	// one. The index isn't leaked (the allocator is live-sourced). A future boot
	// with a real provisioner reaps it.
	store := newReapSettingsStore(t, "tproxy", 5, true)
	svc := newTestService(t, Deps{Settings: store, OpkgTun: nil})

	if err := svc.ReapOrphanedFakeIPTun(context.Background()); err != nil {
		t.Fatalf("ReapOrphanedFakeIPTun (nil OpkgTun): %v", err)
	}
	if got := loadFakeIP(t, store); got == nil {
		t.Error("persist must be retained with nil OpkgTun, got nil")
	}
}

// === Description-scan fallback (persist-less orphans) ===

// A persist-less orphan (crash mid-Enable, failed disable delete after the
// mandatory persist clear) is found by description and removed. On a
// successful delete NO address clears are issued (they exist only for the
// delete-failure path).
func TestReapOrphaned_ScanRemovesPersistlessOrphan(t *testing.T) {
	store := newReapSettingsStore(t, "tproxy", 0, false)
	opkg := &recordingOpkgTunProvisioner{}
	svc := newTestService(t, Deps{Settings: store, OpkgTun: opkg, OpkgTunScan: scanReturning([]string{"OpkgTun1"}, nil)})

	if err := svc.ReapOrphanedFakeIPTun(context.Background()); err != nil {
		t.Fatalf("ReapOrphanedFakeIPTun: %v", err)
	}
	if len(opkg.deleted) != 1 || opkg.deleted[0] != "OpkgTun1" {
		t.Errorf("deleted = %v, want [OpkgTun1]", opkg.deleted)
	}
	if len(opkg.cleared) != 0 {
		t.Errorf("cleared = %v, want none on successful delete", opkg.cleared)
	}
}

// The scan removes the orphan's interface-bound pool route BEFORE deleting the
// iface: the route survives the iface deletion (stand-verified) and would
// otherwise stay reject-routed with no owner able to address it.
func TestReapOrphaned_ScanRemovesPoolRouteBeforeDelete(t *testing.T) {
	store := newReapSettingsStore(t, "tproxy", 0, false)
	opkg := &recordingOpkgTunProvisioner{}
	log := &callLog{}
	svc := newTestService(t, Deps{
		Settings:     store,
		OpkgTun:      opkg,
		OpkgTunScan:  scanReturning([]string{"OpkgTun1"}, nil),
		StaticRoutes: &recStaticRoutes{log: log},
		FakeIPTun:    DefaultFakeIPTunParams(),
	})

	if err := svc.ReapOrphanedFakeIPTun(context.Background()); err != nil {
		t.Fatalf("ReapOrphanedFakeIPTun: %v", err)
	}
	if !log.has("RemoveRoute:198.18.0.0:OpkgTun1") {
		t.Errorf("orphan pool route not removed, got %v", log.calls)
	}
	if len(opkg.deleted) != 1 || opkg.deleted[0] != "OpkgTun1" {
		t.Errorf("deleted = %v, want [OpkgTun1]", opkg.deleted)
	}
}

// The currently-persisted iface is excluded from the scan: in fakeip-tun mode
// the active Enable/Reconcile own it, foreign orphans still go.
func TestReapOrphaned_ScanSkipsOwnedIface(t *testing.T) {
	store := newReapSettingsStore(t, "fakeip-tun", 2, true)
	opkg := &recordingOpkgTunProvisioner{}
	svc := newTestService(t, Deps{Settings: store, OpkgTun: opkg, OpkgTunScan: scanReturning([]string{"OpkgTun2", "OpkgTun0"}, nil)})

	if err := svc.ReapOrphanedFakeIPTun(context.Background()); err != nil {
		t.Fatalf("ReapOrphanedFakeIPTun: %v", err)
	}
	if len(opkg.deleted) != 1 || opkg.deleted[0] != "OpkgTun0" {
		t.Errorf("deleted = %v, want [OpkgTun0] (owned OpkgTun2 must be skipped)", opkg.deleted)
	}
	if got := loadFakeIP(t, store); got == nil || got.Index != 2 {
		t.Errorf("persist must be unchanged, got %+v", got)
	}
}

// A failed scan delete clears the addresses (the loop-defusing part) and must
// not fail the reap: the scan is best-effort, the next tick/boot retries.
func TestReapOrphaned_ScanDeleteFailureStillClearsAddress(t *testing.T) {
	store := newReapSettingsStore(t, "tproxy", 0, false)
	opkg := &recordingOpkgTunProvisioner{delErr: errors.New("ndms down")}
	svc := newTestService(t, Deps{Settings: store, OpkgTun: opkg, OpkgTunScan: scanReturning([]string{"OpkgTun1"}, nil)})

	if err := svc.ReapOrphanedFakeIPTun(context.Background()); err != nil {
		t.Fatalf("scan delete failure must not fail the reap: %v", err)
	}
	if len(opkg.cleared) != 1 || opkg.cleared[0] != "OpkgTun1" {
		t.Errorf("cleared = %v, want [OpkgTun1]", opkg.cleared)
	}
}

// A scanner error is logged and skipped — the persist-based reap still runs.
func TestReapOrphaned_ScanErrorFallsBackToPersistReap(t *testing.T) {
	store := newReapSettingsStore(t, "tproxy", 3, true)
	opkg := &recordingOpkgTunProvisioner{}
	svc := newTestService(t, Deps{Settings: store, OpkgTun: opkg, OpkgTunScan: scanReturning(nil, errors.New("rci down"))})

	if err := svc.ReapOrphanedFakeIPTun(context.Background()); err != nil {
		t.Fatalf("ReapOrphanedFakeIPTun: %v", err)
	}
	if len(opkg.deleted) != 1 || opkg.deleted[0] != "OpkgTun3" {
		t.Errorf("deleted = %v, want [OpkgTun3] via persist-based reap", opkg.deleted)
	}
}

// Reconcile reaps persist-less orphans on every tick — a runtime orphan (e.g.
// failed disable delete) heals without waiting for a reboot; the active
// (persisted) iface is untouched.
func TestReconcile_ReapsForeignOrphanEachTick(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	_ = captureDrain(t)
	provisionForDisable(t, h) // fakeip-tun включён, OpkgTun0 — owned, лог очищен
	h.svc.deps.OpkgTunScan = scanReturning([]string{"OpkgTun0", "OpkgTun7"}, nil)

	if err := h.svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !h.log.has("Delete:OpkgTun7") {
		t.Errorf("foreign orphan not reaped on tick: %v", h.log.calls)
	}
	if h.log.has("Delete:OpkgTun0") {
		t.Errorf("owned iface must not be touched: %v", h.log.calls)
	}
}

// Тик scheduler'а реапает сирот и при ВЫКЛЮЧЕННОМ движке — runtime-сирота
// (провал delete при disable) возникает именно в выключенном состоянии и не
// должна ждать перезагрузки (стенд-регрессия 2026-07-16).
func TestSchedulerTick_ReapsOrphanWhenEngineDisabled(t *testing.T) {
	store := newReapSettingsStore(t, "tproxy", 0, false) // Enabled=false по умолчанию
	opkg := &recordingOpkgTunProvisioner{}
	svc := newTestService(t, Deps{Settings: store, OpkgTun: opkg, OpkgTunScan: scanReturning([]string{"OpkgTun5"}, nil)})
	sched := NewScheduler(svc, store)

	sched.tickPolicySync(context.Background())

	if len(opkg.deleted) != 1 || opkg.deleted[0] != "OpkgTun5" {
		t.Errorf("deleted = %v, want [OpkgTun5] (reap must run with engine disabled)", opkg.deleted)
	}
}
