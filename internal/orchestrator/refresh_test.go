package orchestrator

import (
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// newTestStore creates a real AWGTunnelStore in a temp directory and saves a
// tunnel with the given ID so that store.Get(id) succeeds.
func newTestStore(t *testing.T) *storage.AWGTunnelStore {
	t.Helper()
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, dir)
	tunnel := &storage.AWGTunnel{
		ID:       "awg0",
		Backend:  "nativewg",
		NWGIndex: 0,
	}
	if err := store.Create(tunnel); err != nil {
		t.Fatalf("newTestStore: Create failed: %v", err)
	}
	return store
}

// RefreshTunnelState must preserve the runtime-only quiescentUntil window,
// otherwise a settings mutation during boot silently defeats the
// boot-quiescence guard in decideNDMSHook.
func TestRefreshTunnelState_PreservesQuiescentUntil(t *testing.T) {
	store := newTestStore(t)
	o := &Orchestrator{store: store, clock: time.Now}
	o.state = newState()

	q := time.Unix(5000, 0)
	o.state.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "nativewg", Running: true, quiescentUntil: q,
	}

	o.RefreshTunnelState("awg0")

	got := o.state.tunnels["awg0"]
	if got == nil {
		t.Fatal("tunnel missing after refresh")
	}
	if !got.quiescentUntil.Equal(q) {
		t.Fatalf("quiescentUntil not preserved across RefreshTunnelState: want %v, got %v", q, got.quiescentUntil)
	}
}
