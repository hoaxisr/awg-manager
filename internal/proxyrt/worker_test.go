package proxyrt

import (
	"context"
	"sync"
	"testing"
	"time"
)

// probeResource считает наблюдения и ловит параллельность.
type probeResource struct {
	mu       sync.Mutex
	id       ResourceID
	inFlight int
	maxPar   int
	runs     int
	recheck  time.Duration
}

func (p *probeResource) ID() ResourceID { return p.id }

func (p *probeResource) Observe(context.Context) (Observation, error) {
	p.mu.Lock()
	p.runs++
	p.inFlight++
	if p.inFlight > p.maxPar {
		p.maxPar = p.inFlight
	}
	p.mu.Unlock()
	time.Sleep(2 * time.Millisecond)
	p.mu.Lock()
	p.inFlight--
	p.mu.Unlock()
	return Observation{Known: true}, nil
}

func (p *probeResource) Plan(Observation) []Step { return nil }

func (p *probeResource) Apply(context.Context, Step) error { return nil }

func (p *probeResource) RecheckAfter() time.Duration { return p.recheck }

func TestWorkerSerializesEvents(t *testing.T) {
	r := &probeResource{id: "a"}
	rec := NewReconciler(staticRole{res: []Resource{r}}, nil, ReconcileOpts{})

	var mu sync.Mutex
	var phases []Phase
	w := NewWorker("inst1", rec, func() Intent { return IntentEnabled }, func(_ Result, p Phase) {
		mu.Lock()
		phases = append(phases, p)
		mu.Unlock()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	for i := 0; i < 5; i++ {
		w.Post(Event{Kind: EventBoot, Instance: "inst1"})
	}
	// Дождаться первого прогона: Stop новой работы не начинает, поэтому
	// утверждать что-либо о прогонах можно лишь после того, как воркер
	// действительно проснулся.
	deadline := time.After(2 * time.Second)
	for {
		r.mu.Lock()
		started := r.runs > 0
		r.mu.Unlock()
		if started {
			break
		}
		select {
		case <-deadline:
			t.Fatal("воркер не проснулся за 2 секунды")
		case <-time.After(time.Millisecond):
		}
	}
	w.Stop()

	r.mu.Lock()
	maxPar, runs := r.maxPar, r.runs
	r.mu.Unlock()
	if maxPar != 1 {
		t.Fatalf("максимум параллельных наблюдений %d, ожидали 1", maxPar)
	}
	if runs == 0 {
		t.Fatal("реконсиляция не запускалась ни разу")
	}
	// Схлопывание: пять событий подряд не обязаны дать пять полных прогонов.
	if runs > 5 {
		t.Fatalf("прогонов %d на пять событий — схлопывание не работает", runs)
	}

	mu.Lock()
	got := len(phases)
	mu.Unlock()
	if got == 0 {
		t.Fatal("обратный вызов состояния не сработал ни разу")
	}
}

func TestWorkerPostAfterStopReturnsFalse(t *testing.T) {
	rec := NewReconciler(staticRole{res: nil}, nil, ReconcileOpts{})
	w := NewWorker("inst1", rec, func() Intent { return IntentEnabled }, func(Result, Phase) {})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	w.Stop()

	if w.Post(Event{Kind: EventBoot, Instance: "inst1"}) {
		t.Fatal("после остановки события приниматься не должны")
	}
}

func TestWorkerSkipsPublishOnCanceledContext(t *testing.T) {
	// Выключение демона не должно публиковать ложное состояние.
	rec := NewReconciler(staticRole{res: []Resource{&probeResource{id: "a"}}}, nil, ReconcileOpts{})
	var calls int
	var mu sync.Mutex
	w := NewWorker("inst1", rec, func() Intent { return IntentEnabled }, func(Result, Phase) {
		mu.Lock()
		calls++
		mu.Unlock()
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.Start(ctx)
	w.Post(Event{Kind: EventBoot, Instance: "inst1"})
	w.Stop()

	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Fatalf("публикаций %d, ожидали 0 при отменённом контексте", calls)
	}
}

// blockingResource виснет в наблюдении, пока его не отпустят либо не отменят
// контекст. Уважение контекста в Observe и делает границу Stop настоящей.
type blockingResource struct {
	id      ResourceID
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (b *blockingResource) ID() ResourceID { return b.id }

func (b *blockingResource) Observe(ctx context.Context) (Observation, error) {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
		return Observation{Known: true}, nil
	case <-ctx.Done():
		return Observation{}, ctx.Err()
	}
}

func (b *blockingResource) Plan(Observation) []Step { return nil }

func (b *blockingResource) Apply(context.Context, Step) error { return nil }

func (b *blockingResource) RecheckAfter() time.Duration { return 0 }

func TestWorkerStopInterruptsInFlightReconcile(t *testing.T) {
	// Гашение одного инстанса не должно ждать конца длинного RCI-раунда.
	slow := &blockingResource{id: "a", entered: make(chan struct{}), release: make(chan struct{})}
	rec := NewReconciler(staticRole{res: []Resource{slow}}, nil, ReconcileOpts{})
	w := NewWorker("inst1", rec, func() Intent { return IntentEnabled }, func(Result, Phase) {})

	w.Start(context.Background())
	w.Post(Event{Kind: EventBoot, Instance: "inst1"})
	<-slow.entered // реконсиляция началась и висит в наблюдении

	done := make(chan struct{})
	go func() { w.Stop(); close(done) }()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop не вернулся за 2 секунды — идущая реконсиляция не прерывается")
	}
}

func TestWorkerArmsRecheckTimer(t *testing.T) {
	// Ресурс просит подстраховочную сверку — воркер обязан сам себя разбудить.
	r := &probeResource{id: "a", recheck: 20 * time.Millisecond}
	rec := NewReconciler(staticRole{res: []Resource{r}}, nil, ReconcileOpts{})
	w := NewWorker("inst1", rec, func() Intent { return IntentEnabled }, func(Result, Phase) {})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	w.Post(Event{Kind: EventBoot, Instance: "inst1"})

	deadline := time.After(2 * time.Second)
	for {
		r.mu.Lock()
		runs := r.runs
		r.mu.Unlock()
		if runs >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("прогонов %d за 2 секунды — таймер подстраховки не заводится", runs)
		case <-time.After(5 * time.Millisecond):
		}
	}
	w.Stop()
}
