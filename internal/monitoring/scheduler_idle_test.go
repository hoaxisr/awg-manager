package monitoring

import (
	"context"
	"sync"
	"sync/atomic"
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

// Параллельный вход в RunOnce пропускается: второй проход зондировал бы то же
// самое второй раз, а workerLimit держит каждый проход по отдельности и от
// удвоения не спасает. Триггерный прогон (подъём туннеля) приходит со стороны
// и раньше свободно накладывался на периодический тик.
func TestScheduler_RunOnce_SkipsWhenAlreadyRunning(t *testing.T) {
	release := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseOnce)
	prober := &blockingProber{release: release}
	sched := NewScheduler(SchedulerDeps{
		TunnelLister: &fakeLister{tunnels: []traffic.RunningTunnel{{ID: "tn-A", IfaceName: "wg0"}}},
		Prober:       prober,
	}, NewHistory())

	done := make(chan struct{})
	go func() {
		sched.RunOnce(context.Background())
		close(done)
	}()

	// Дождаться, пока первый проход займёт стража и встанет на зонде.
	deadline := time.After(2 * time.Second)
	for prober.started.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("первый проход не начался")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	// Второй вызов — в горутине с таймаутом: если страж снят, он встанет на
	// том же зонде и тест повиснет, а зависший сторож бесполезен.
	second := make(chan struct{})
	go func() {
		sched.RunOnce(context.Background())
		close(second)
	}()

	select {
	case <-second: // вернулся сразу — страж сработал
	case <-time.After(500 * time.Millisecond):
		releaseOnce()
		<-done
		t.Fatal("второй RunOnce не пропустился — вошёл параллельно с первым")
	}

	releaseOnce()
	<-done

	if got := prober.started.Load(); got != 1 {
		t.Errorf("зондов начато %d, ожидался 1: второй проход должен был пропуститься", got)
	}
}

// blockingProber держит зонд, пока тест не отпустит, — так первый проход
// гарантированно остаётся в полёте на момент второго вызова.
type blockingProber struct {
	started atomic.Int64
	release chan struct{}
}

func (p *blockingProber) Probe(_ context.Context, _, _ string, _ time.Duration) (int, bool) {
	p.started.Add(1)
	<-p.release
	return 5, true
}

// Форсированный прогон зовётся после смены настроек — ровно тогда, когда
// свежие данные и нужны. Пропустить его молча нельзя: пользователь применил
// настройку, а показ остался бы прежним. Плюс прогон, идущий в полёте, прочитал
// данные Clash ДО сброса их кэша, то есть его результат уже устарел.
func TestScheduler_RunOnceForced_WaitsForInFlight(t *testing.T) {
	release := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseOnce)
	prober := &blockingProber{release: release}
	sched := NewScheduler(SchedulerDeps{
		TunnelLister: &fakeLister{tunnels: []traffic.RunningTunnel{{ID: "tn-A", IfaceName: "wg0"}}},
		Prober:       prober,
	}, NewHistory())

	done := make(chan struct{})
	go func() { sched.RunOnce(context.Background()); close(done) }()

	deadline := time.After(2 * time.Second)
	for prober.started.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("первый проход не начался")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	forced := make(chan struct{})
	go func() { sched.RunOnceForced(context.Background()); close(forced) }()

	// Пока первый в полёте, форсированный обязан ЖДАТЬ, а не вернуться.
	select {
	case <-forced:
		t.Fatal("форсированный прогон пропустился вместо ожидания")
	case <-time.After(300 * time.Millisecond):
	}

	releaseOnce()
	<-done

	select {
	case <-forced:
	case <-time.After(3 * time.Second):
		t.Fatal("форсированный прогон не состоялся после освобождения стража")
	}
	if got := prober.started.Load(); got != 2 {
		t.Errorf("зондов начато %d, ожидалось 2 (обычный + форсированный)", got)
	}
}

// Форсированный прогон приходит с бюджетом 10 с и может прождать стража почти
// весь. Зайти с остатком и прозондировать всё на мёртвом контексте нельзя:
// runProbeCell отдаст ok=false по каждой ячейке, и это запишется КАК ПРАВДА —
// «probe unreachable» в журнал, провалы в историю, красная матрица в панель.
// Пользователь применил настройку — и связь «пропала».
func TestScheduler_ExpiredContext_DoesNotRecordFalseFailures(t *testing.T) {
	prober := &fakeProber{ok: true, latency: 12}
	hist := NewHistory()
	sched := NewScheduler(SchedulerDeps{
		TunnelLister: &fakeLister{tunnels: []traffic.RunningTunnel{{ID: "tn-A", IfaceName: "wg0"}}},
		Prober:       prober,
	}, hist)

	// Сначала честный прогон — чтобы снимку было что терять.
	sched.RunOnce(context.Background())
	before := sched.LatestSnapshot()
	if len(before.Cells) == 0 {
		t.Fatal("первый прогон не дал ячеек — тест ничего не проверяет")
	}
	probesBefore := prober.calls.Load()

	// Теперь форсированный на уже истёкшем контексте.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sched.RunOnceForced(ctx)

	if got := prober.calls.Load(); got != probesBefore {
		t.Errorf("зондов %d против %d — прогон пошёл на мёртвом контексте", got, probesBefore)
	}
	after := sched.LatestSnapshot()
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Error("снимок заменён результатом оборванного прогона")
	}
	for _, c := range after.Cells {
		if !c.OK {
			t.Errorf("ячейка %s/%s помечена недоступной оборванным прогоном", c.TargetID, c.TunnelID)
		}
	}
}
