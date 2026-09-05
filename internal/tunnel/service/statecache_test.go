package service

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/ndms/cache"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// countingState реализует state.Manager: считает вызовы, отвечает медленно.
type countingState struct {
	calls atomic.Int32
	delay time.Duration
}

func (c *countingState) GetState(ctx context.Context, tunnelID string) tunnel.StateInfo {
	c.calls.Add(1)
	if c.delay > 0 {
		select {
		case <-time.After(c.delay):
		case <-ctx.Done():
			return tunnel.StateInfo{State: tunnel.StateUnknown}
		}
	}
	return tunnel.StateInfo{State: tunnel.StateRunning}
}

func newCacheTestService(t *testing.T, mgr *countingState, ttl time.Duration) *ServiceImpl {
	t.Helper()
	dir := t.TempDir()
	lockDir := filepath.Join(dir, "locks")
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		t.Fatal(err)
	}
	store := storage.NewAWGTunnelStoreWithLockDir(dir, lockDir)
	if err := store.Create(&storage.AWGTunnel{ID: "t1", Name: "t1", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	s := &ServiceImpl{store: store, state: mgr}
	s.stateCache = cache.NewKeyedStore[string, tunnel.StateInfo](ttl, nil, "tunnel state", s.fetchRawStateByID)
	return s
}

func TestFetchRawStateByID_SkipsWdttRawBackend(t *testing.T) {
	mgr := &countingState{}
	s := newCacheTestService(t, mgr, 2*time.Second)
	if err := s.store.Create(&storage.AWGTunnel{
		ID:      "wdttraw-default",
		Name:    "WDTT Raw",
		Backend: "wdtt-raw",
		Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	info, err := s.fetchRawStateByID(context.Background(), "wdttraw-default")
	if err != nil {
		t.Fatal(err)
	}
	if info.State != tunnel.StateRunning {
		t.Fatalf("state = %v, want running", info.State)
	}
	if info.BackendType != "wdtt-raw" {
		t.Fatalf("backendType = %q, want wdtt-raw", info.BackendType)
	}
	if got := mgr.calls.Load(); got != 0 {
		t.Fatalf("manager calls = %d, want 0 (wdtt-raw must not query kernel state)", got)
	}
}

func TestStateCache_SingleFlightCoalesces(t *testing.T) {
	mgr := &countingState{delay: 50 * time.Millisecond}
	s := newCacheTestService(t, mgr, 2*time.Second)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.GetState(context.Background(), "t1")
		}()
	}
	wg.Wait()
	if got := mgr.calls.Load(); got != 1 {
		t.Fatalf("manager calls = %d, want 1 (singleflight)", got)
	}
}

func TestStateCache_TTLExpires(t *testing.T) {
	mgr := &countingState{}
	s := newCacheTestService(t, mgr, 50*time.Millisecond)

	s.GetState(context.Background(), "t1")
	s.GetState(context.Background(), "t1") // hit
	time.Sleep(80 * time.Millisecond)
	s.GetState(context.Background(), "t1") // miss после TTL
	if got := mgr.calls.Load(); got != 2 {
		t.Fatalf("manager calls = %d, want 2", got)
	}
}

func TestStateCache_InvalidateForcesFresh(t *testing.T) {
	mgr := &countingState{}
	s := newCacheTestService(t, mgr, 2*time.Second)

	s.GetState(context.Background(), "t1")
	s.invalidateState("t1")
	s.GetState(context.Background(), "t1")
	if got := mgr.calls.Load(); got != 2 {
		t.Fatalf("manager calls = %d, want 2 после Invalidate", got)
	}
}

func TestStateCache_CancelledCallerDoesNotPoison(t *testing.T) {
	mgr := &countingState{delay: 30 * time.Millisecond}
	s := newCacheTestService(t, mgr, 2*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	info := s.GetState(ctx, "t1")
	if info.State != tunnel.StateRunning {
		t.Fatalf("state = %v, want Running (fetch detached from caller ctx)", info.State)
	}
}

func TestStateCache_BusEventInvalidates(t *testing.T) {
	mgr := &countingState{}
	s := newCacheTestService(t, mgr, 2*time.Second)
	bus := events.NewBus()
	s.SetEventBus(bus)

	s.GetState(context.Background(), "t1")
	bus.Publish("tunnel:state", events.TunnelStateEvent{ID: "t1", State: "stopped"})
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s.GetState(context.Background(), "t1")
		if mgr.calls.Load() >= 2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("manager calls = %d, want >= 2 после tunnel:state", mgr.calls.Load())
}

type fakeNWGState struct{ info tunnel.StateInfo }

func (f fakeNWGState) GetState(context.Context, *storage.AWGTunnel) tunnel.StateInfo { return f.info }

// Состояние nativewg-туннеля читается у nativewg-оператора, а не по
// kernel-матрице чужого имени: иначе карточка врёт. Мутант «ветка снята» был
// зелёным — шва не было.
func TestFetchRawStateByID_NativeWGUsesNativeReader(t *testing.T) {
	mgr := &countingState{}
	s := newCacheTestService(t, mgr, 2*time.Second)
	s.nwgState = fakeNWGState{info: tunnel.StateInfo{State: tunnel.StateStopped, BackendType: "nativewg"}}
	if err := s.store.Create(&storage.AWGTunnel{ID: "awg0", Name: "n", Backend: "nativewg", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	info, err := s.fetchRawStateByID(context.Background(), "awg0")
	if err != nil {
		t.Fatal(err)
	}
	if info.State != tunnel.StateStopped || info.BackendType != "nativewg" {
		t.Fatalf("state=%v backend=%q — ответ не от nativewg-оператора", info.State, info.BackendType)
	}
	if got := mgr.calls.Load(); got != 0 {
		t.Fatalf("kernel-матрица опрошена %d раз для nativewg", got)
	}
}
