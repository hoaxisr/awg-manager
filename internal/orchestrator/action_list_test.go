package orchestrator

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// confirmingNWGOp — fakeNWGOp, чьи шаги, как у настоящего оператора,
// подтверждают WireguardN по ctx действия (query.Confirm).
type confirmingNWGOp struct {
	fakeNWGOp
	t        *testing.T
	q        *query.Queries
	confirms atomic.Int64
}

func (c *confirmingNWGOp) confirm(ctx context.Context, stored *storage.AWGTunnel) error {
	c.confirms.Add(1)
	name := fmt.Sprintf("Wireguard%d", stored.NWGIndex)
	if _, _, ok, err := c.q.Interfaces.Confirm(ctx, name); err != nil || !ok {
		c.t.Errorf("Confirm %s: ok=%v err=%v", name, ok, err)
	}
	return nil
}

func (c *confirmingNWGOp) Start(ctx context.Context, st *storage.AWGTunnel) error {
	return c.confirm(ctx, st)
}
func (c *confirmingNWGOp) Stop(ctx context.Context, st *storage.AWGTunnel) error {
	return c.confirm(ctx, st)
}
func (c *confirmingNWGOp) RestoreKmodTunnel(ctx context.Context, st *storage.AWGTunnel) error {
	return c.confirm(ctx, st)
}
func (c *confirmingNWGOp) ConfigurePingCheck(ctx context.Context, st *storage.AWGTunnel, _ ndms.PingCheckConfig) error {
	return c.confirm(ctx, st)
}
func (c *confirmingNWGOp) RemovePingCheck(ctx context.Context, st *storage.AWGTunnel) error {
	return c.confirm(ctx, st)
}

// actionListFixture — оркестратор с n nativewg-туннелями на kmod-прошивке
// (бут = Reconcile → RestoreKmod + ConfigurePingCheck) над оракулом.
func actionListFixture(t *testing.T, n int) (*Orchestrator, *query.FakeNDMS) {
	t.Helper()
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	f := query.NewFakeNDMS()
	q := query.NewQueries(query.Deps{Getter: f, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	op := &confirmingNWGOp{t: t, q: q}
	op.state = tunnel.StateInfo{State: tunnel.StateRunning, HasHandshake: true}
	o := &Orchestrator{state: newState(), store: store, nwgOp: op}
	for i := range n {
		id := fmt.Sprintf("awg%d", i)
		pc := &storage.TunnelPingCheck{Enabled: true}
		if err := store.Create(&storage.AWGTunnel{ID: id, Name: id, Backend: "nativewg", NWGIndex: i, PingCheck: pc}); err != nil {
			t.Fatal(err)
		}
		f.Add(ndms.Interface{ID: fmt.Sprintf("Wireguard%d", i), Type: "Wireguard"})
		o.state.tunnels[id] = &tunnelState{ID: id, Name: id, Backend: "nativewg", Enabled: true,
			Running: true, NWGIndex: i, PingCheck: pc}
	}
	if _, err := q.Interfaces.List(context.Background()); err != nil { // карта тёплая, как в проде
		t.Fatal(err)
	}
	return o, f
}

// F557: бут N туннелей — один полный список на всё событие, а не по списку
// на каждое подтверждение (было 2N: RestoreKmod + ConfigurePingCheck).
func TestHandleEvent_BootOneListForAllTunnels(t *testing.T) {
	const n = 5
	o, f := actionListFixture(t, n)
	lists := f.ListCalls()
	if err := o.HandleEvent(context.Background(), Event{Type: EventBoot, WANUp: true}); err != nil {
		t.Fatal(err)
	}
	if got := o.nwgOp.(*confirmingNWGOp).confirms.Load(); got != 2*n {
		t.Fatalf("подтверждений %d, want %d (RestoreKmod + ConfigurePingCheck на туннель)", got, 2*n)
	}
	if got := f.ListCalls() - lists; got != 1 {
		t.Fatalf("бут %d туннелей: %d списков, want 1", n, got)
	}
	if f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("E=%d phantoms=%d", f.E, f.Phantoms)
	}
}

// Пользовательские Stop и Restart — один список на действие; следующее
// действие читает свой.
func TestHandleEvent_UserActionOneList(t *testing.T) {
	o, f := actionListFixture(t, 1)
	op := o.nwgOp.(*confirmingNWGOp)
	for _, tc := range []struct {
		ev       EventType
		confirms int64 // шагов с подтверждением
	}{
		{EventRestart, 4}, // RemovePingCheck, Stop, Start, ConfigurePingCheck
		{EventStop, 2},    // RemovePingCheck, Stop
	} {
		lists, confirms := f.ListCalls(), op.confirms.Load()
		if err := o.HandleEvent(context.Background(), Event{Type: tc.ev, Tunnel: "awg0"}); err != nil {
			t.Fatal(err)
		}
		if got := op.confirms.Load() - confirms; got != tc.confirms {
			t.Fatalf("%v: подтверждений %d, want %d", tc.ev, got, tc.confirms)
		}
		if got := f.ListCalls() - lists; got != 1 {
			t.Fatalf("%v: %d списков, want 1", tc.ev, got)
		}
	}
}
