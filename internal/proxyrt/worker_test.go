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
