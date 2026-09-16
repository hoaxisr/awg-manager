package connectivity

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
)

// mockMatrix counts RunOnce calls; safe under goroutines.
type mockMatrix struct {
	calls atomic.Int64
}

func (m *mockMatrix) RunOnce(_ context.Context) {
	m.calls.Add(1)
}

type mockHandshake struct {
	mu    sync.Mutex
	ids   map[string]bool // какие туннели считать рукопожавшимися
	calls atomic.Int64    // сколько раз спросили — по нему видно пакетность
}

func (m *mockHandshake) Handshaked(_ context.Context) map[string]bool {
	m.calls.Add(1)
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]bool, len(m.ids))
	for k, v := range m.ids {
		out[k] = v
	}
	return out
}

func (m *mockHandshake) set(ids ...string) {
	m.mu.Lock()
	if m.ids == nil {
		m.ids = make(map[string]bool, len(ids))
	}
	for _, id := range ids {
		m.ids[id] = true
	}
	m.mu.Unlock()
}

func TestMonitor_TriggersMatrixOnRunningEvent(t *testing.T) {
	bus := events.NewBus()
	matrix := &mockMatrix{}
	hs := &mockHandshake{}
	hs.set("awg0") // рукопожатие уже есть — ждать нечего

	mon := NewMonitor(bus, matrix, hs, nil)
	mon.Start()
	defer mon.Stop()

	// Wait for listener to subscribe before publishing.
	deadline := time.After(time.Second)
	for bus.SubscriberCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for subscriber")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	bus.Publish("tunnel:state", events.TunnelStateEvent{
		ID:    "awg0",
		State: "running",
	})

	deadline = time.After(2 * time.Second)
	for matrix.calls.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("RunOnce was never invoked after running event")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestMonitor_IgnoresNonRunningStateEvents(t *testing.T) {
	bus := events.NewBus()
	matrix := &mockMatrix{}
	mon := NewMonitor(bus, matrix, nil, nil)
	mon.Start()
	defer mon.Stop()

	deadline := time.After(time.Second)
	for bus.SubscriberCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for subscriber")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	bus.Publish("tunnel:state", events.TunnelStateEvent{
		ID:    "awg0",
		State: "stopped",
	})

	time.Sleep(80 * time.Millisecond)
	if matrix.calls.Load() != 0 {
		t.Fatalf("expected no RunOnce for non-running state, got %d", matrix.calls.Load())
	}
}

// Подъём пачки туннелей обязан дать РОВНО ОДИН прогон матрицы. Прежде на каждое
// событие поднималась своя горутина и каждая звала полный прогон по всем
// туннелям: пять штук давали пять проходов, то есть 25 зондов вместо пяти, и
// для метода "http" каждый зонд — TLS-рукопожатие.
func TestMonitor_BatchOfTunnelsRunsMatrixOnce(t *testing.T) {
	bus := events.NewBus()
	matrix := &mockMatrix{}
	hs := &mockHandshake{}

	ids := []string{"awg0", "awg1", "awg2", "awg3", "awg4"}
	hs.set(ids...)

	mon := NewMonitor(bus, matrix, hs, nil)
	mon.Start()
	defer mon.Stop()

	deadline := time.After(time.Second)
	for bus.SubscriberCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("подписчик так и не появился")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	for _, id := range ids {
		bus.Publish("tunnel:state", events.TunnelStateEvent{ID: id, State: "running"})
	}

	deadline = time.After(5 * time.Second)
	for matrix.calls.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("прогон матрицы не случился")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Дать возможность лишним прогонам проявиться, если коалесцирование сломано.
	time.Sleep(500 * time.Millisecond)

	if got := matrix.calls.Load(); got != 1 {
		t.Errorf("прогонов матрицы %d, ожидался 1 на пачку из %d туннелей", got, len(ids))
	}
	// Выборка состояния — одна на шаг опроса, а не на туннель.
	if got := hs.calls.Load(); got > 2 {
		t.Errorf("выборок рукопожатий %d, ожидалось ≤2 (по одной на шаг)", got)
	}
}

// Туннель, рукопожавший позже остальных (типично после ребута — поднимаются не
// синхронно), обязан получить СВОЙ прогон, а не оказаться брошенным. Иначе его
// карточка показывает «нет связи» до планового прогона: минуту при открытой
// панели, до десяти минут при закрытой.
func TestMonitor_LateTunnelStillGetsRun(t *testing.T) {
	bus := events.NewBus()
	matrix := &mockMatrix{}
	hs := &mockHandshake{}
	hs.set("fast") // «slow» рукопожмёт позже

	mon := NewMonitor(bus, matrix, hs, nil)
	mon.Start()
	defer mon.Stop()

	deadline := time.After(time.Second)
	for bus.SubscriberCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("подписчик так и не появился")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	bus.Publish("tunnel:state", events.TunnelStateEvent{ID: "fast", State: "running"})
	bus.Publish("tunnel:state", events.TunnelStateEvent{ID: "slow", State: "running"})

	// Первый прогон — за быстрый.
	deadline = time.After(8 * time.Second)
	for matrix.calls.Load() < 1 {
		select {
		case <-deadline:
			t.Fatal("прогона за быстрый туннель не случилось")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Медленный подъехал — ожидание должно ещё идти.
	hs.set("slow")

	deadline = time.After(8 * time.Second)
	for matrix.calls.Load() < 2 {
		select {
		case <-deadline:
			t.Fatalf("прогонов %d — медленный туннель брошен", matrix.calls.Load())
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// Stop() не должен ждать окончания прогона матрицы: он синхронный и учтён в wg,
// а на роутере init-скрипт не станет ждать полминуты и пришлёт SIGKILL.
func TestMonitor_StopDoesNotBlockOnMatrixRun(t *testing.T) {
	bus := events.NewBus()
	release := make(chan struct{})
	defer close(release)
	matrix := &blockingMatrix{release: release}
	hs := &mockHandshake{}
	hs.set("awg0")

	mon := NewMonitor(bus, matrix, hs, nil)
	mon.Start()

	deadline := time.After(time.Second)
	for bus.SubscriberCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("подписчик так и не появился")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	bus.Publish("tunnel:state", events.TunnelStateEvent{ID: "awg0", State: "running"})

	deadline = time.After(8 * time.Second)
	for matrix.entered.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("прогон матрицы не начался")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	stopped := make(chan struct{})
	go func() { mon.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop() завис на прогоне матрицы")
	}
}

// blockingMatrix держит прогон, пока его контекст не отменят.
type blockingMatrix struct {
	entered atomic.Int64
	release chan struct{}
}

func (b *blockingMatrix) RunOnce(ctx context.Context) {
	b.entered.Add(1)
	select {
	case <-ctx.Done():
	case <-b.release:
	}
}
