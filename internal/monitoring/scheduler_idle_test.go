package monitoring

import (
	"context"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/traffic"
)

// newIdleTestScheduler собирает планировщик с ускоренными периодами: ждать
// минуту и десять в тесте нечем, а проверяется отношение шагов, не их длина.
func newIdleTestScheduler(t *testing.T, bus *events.Bus) (*Scheduler, *fakeProber) {
	t.Helper()
	prober := &fakeProber{ok: true, latency: 10}
	sched := NewScheduler(SchedulerDeps{
		TunnelLister: &fakeLister{tunnels: []traffic.RunningTunnel{{ID: "tn-A", IfaceName: "wg0"}}},
		Prober:       prober,
	}, NewHistory())
	sched.interval = 20 * time.Millisecond
	sched.idleInterval = 10 * time.Second // в окне теста недостижим
	sched.SetEventBus(bus)
	t.Cleanup(sched.Stop)
	return sched, prober
}

// Панель не открыта: после стартового прогона тики пропускаются, пока не
// истечёт idleInterval. Без разрежения за это окно набежал бы десяток зондов,
// каждый — TLS-рукопожатие ради буфера, который никто не прочтёт.
func TestScheduler_IdleThrottles(t *testing.T) {
	bus := events.NewBus()
	// Внутренний подписчик: он есть всегда и разрежение отменять НЕ должен.
	_, _, unsub := bus.Subscribe()
	t.Cleanup(unsub)

	sched, prober := newIdleTestScheduler(t, bus)
	sched.Start(context.Background())
	time.Sleep(250 * time.Millisecond)

	if got := prober.calls.Load(); got != 1 {
		t.Errorf("зондов %d, ожидался 1 (только стартовый прогон)", got)
	}
}

// Открытая панель возвращает обычный шаг: клиентская подписка снимает разрежение.
func TestScheduler_ClientSubscriberRestoresRate(t *testing.T) {
	bus := events.NewBus()
	_, _, unsub := bus.SubscribeClient()
	t.Cleanup(unsub)

	sched, prober := newIdleTestScheduler(t, bus)
	sched.Start(context.Background())
	time.Sleep(250 * time.Millisecond)

	if got := prober.calls.Load(); got < 3 {
		t.Errorf("зондов %d, ожидалось ≥3: с открытой панелью шаг обычный", got)
	}
}
