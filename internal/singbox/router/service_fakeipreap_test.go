package router

import (
	"context"
	"errors"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// recordingOpkgTunProvisioner embeds fakeOpkgTunProvisioner (default no-op
// methods) and overrides DeleteOpkgTun to record the names it was called with
// and optionally return an injected error — the reap test only exercises Delete.
type recordingOpkgTunProvisioner struct {
	fakeOpkgTunProvisioner
	deleted []string
	delErr  error
}

func (r *recordingOpkgTunProvisioner) DeleteOpkgTun(_ context.Context, name string) error {
	r.deleted = append(r.deleted, name)
	return r.delErr
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
		if err := store.SetFakeIPState(&storage.FakeIPState{
			Provisioned: true,
			Index:       index,
			Inet4Range:  "198.18.0.0/15",
			Inet6Range:  "fc00::/18",
		}); err != nil {
			t.Fatalf("SetFakeIPState: %v", err)
		}
	}
	return store
}

func loadFakeIP(t *testing.T, store *storage.SettingsStore) *storage.FakeIPState {
	t.Helper()
	all, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return all.FakeIP
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
