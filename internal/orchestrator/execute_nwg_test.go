package orchestrator

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// fakeNWGOp — счётчик вызовов вместо реального оператора: NativeWGExecutor
// целиком, чтобы фейк годился и соседним execute-действиям.
type fakeNWGOp struct {
	state    tunnel.StateInfo
	starts   int
	restores int

	// Парковка Start: закрыть entered и ждать release. nil — не парковаться.
	entered chan struct{}
	release chan struct{}
}

func (f *fakeNWGOp) Start(context.Context, *storage.AWGTunnel) error {
	f.starts++
	park(f.entered, f.release)
	return nil
}
func (f *fakeNWGOp) Stop(context.Context, *storage.AWGTunnel) error         { return nil }
func (f *fakeNWGOp) Delete(context.Context, *storage.AWGTunnel) error       { return nil }
func (f *fakeNWGOp) SuspendProxy(context.Context, *storage.AWGTunnel) error { return nil }
func (f *fakeNWGOp) RestoreKmodTunnel(context.Context, *storage.AWGTunnel) error {
	f.restores++
	return nil
}
func (f *fakeNWGOp) GetState(context.Context, *storage.AWGTunnel) tunnel.StateInfo {
	return f.state
}
func (f *fakeNWGOp) ResolveActiveWAN(context.Context, *storage.AWGTunnel) string { return "" }
func (f *fakeNWGOp) GetTrackedEndpointIP(string) string                          { return "" }
func (f *fakeNWGOp) ConfigurePingCheck(context.Context, *storage.AWGTunnel, ndms.PingCheckConfig) error {
	return nil
}
func (f *fakeNWGOp) RemovePingCheck(context.Context, *storage.AWGTunnel) error { return nil }

// reconcileFixture — оркестратор с одним nativewg-туннелем в хранилище.
func reconcileFixture(t *testing.T, info tunnel.StateInfo) (*Orchestrator, *fakeNWGOp) {
	t.Helper()
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	if err := store.Create(&storage.AWGTunnel{ID: "awg0", Name: "awg0", Backend: "nativewg"}); err != nil {
		t.Fatalf("save tunnel: %v", err)
	}
	op := &fakeNWGOp{state: info}
	return &Orchestrator{state: newState(), store: store, nwgOp: op}, op
}

// Работающий туннель с хендшейком разрушительный рестарт не получает, но и
// без присмотра не остаётся: слот в ядре надо усыновить, иначе не встанет
// endpoint-страж, а RemoveTunnel на остановке будет no-op (#702).
func TestReconcileNativeWG_RunningWithHandshakeAdoptsSlotWithoutRestart(t *testing.T) {
	o, op := reconcileFixture(t, tunnel.StateInfo{State: tunnel.StateRunning, HasHandshake: true})

	if err := o.executeReconcileNativeWG(context.Background(), Action{Type: ActionReconcileNativeWG, Tunnel: "awg0"}); err != nil {
		t.Fatalf("executeReconcileNativeWG: %v", err)
	}

	if op.starts != 0 {
		t.Errorf("полный старт при живом хендшейке: Start вызван %d раз, ожидалось 0", op.starts)
	}
	if op.restores != 1 {
		t.Errorf("усыновление слота вызвано %d раз, ожидалось ровно 1", op.restores)
	}
}

// Обратная сторона: без хендшейка (случай #183 — NDMS поднял интерфейс мимо
// нашего прокси) идёт полный Start, а не усыновление.
func TestReconcileNativeWG_NoHandshakeRunsFullStart(t *testing.T) {
	o, op := reconcileFixture(t, tunnel.StateInfo{State: tunnel.StateRunning})

	if err := o.executeReconcileNativeWG(context.Background(), Action{Type: ActionReconcileNativeWG, Tunnel: "awg0"}); err != nil {
		t.Fatalf("executeReconcileNativeWG: %v", err)
	}

	if op.starts != 1 {
		t.Errorf("Start вызван %d раз, ожидалось 1", op.starts)
	}
	if op.restores != 0 {
		t.Errorf("усыновление слота на пути полного старта вызвано %d раз, ожидалось 0", op.restores)
	}
}
