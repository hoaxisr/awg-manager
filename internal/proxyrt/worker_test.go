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
	w := NewWorker(rec, func() Intent { return IntentEnabled }, func(_ Result, p Phase) {
		mu.Lock()
		phases = append(phases, p)
		mu.Unlock()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	for i := 0; i < 5; i++ {
		w.Post(EventBoot)
	}
	// Дождаться первого ОПУБЛИКОВАННОГО состояния. Stop новой работы не
	// начинает и прерывает идущую, а прерванный прогон ничего не публикует —
	// значит утверждать что-либо можно лишь после полностью завершённого.
	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		published := len(phases) > 0
		mu.Unlock()
		if published {
			break
		}
		select {
		case <-deadline:
			t.Fatal("воркер не опубликовал состояние за 2 секунды")
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

// publishedState гоняет воркер до первого опубликованного состояния и отдаёт
// то, что легло в хранилище. Ровно этот путь пройдёт план 5: прогон → onState →
// StateStore → шина.
func publishedState(t *testing.T, intent func() Intent) InstanceState {
	t.Helper()
	rec := NewReconciler(staticRole{res: []Resource{&probeResource{id: "a"}}}, nil, ReconcileOpts{})
	store := NewStateStore(&fakePublisher{}, fixedNow)
	w := NewWorker(rec, intent, func(res Result, ph Phase) {
		store.Update("inst1", res, ph)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	w.Post(EventBoot)
	defer w.Stop()

	deadline := time.After(2 * time.Second)
	for {
		if st, ok := store.Get("inst1"); ok {
			return st
		}
		select {
		case <-deadline:
			t.Fatal("воркер не опубликовал состояние за 2 секунды")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestWorkerPublishesIntentPairFromRun(t *testing.T) {
	// Наружу обязана уехать та же пара «намерение + фаза», по которой цикл
	// считал. Иначе вызывающему пришлось бы перечитать намерение после прогона, и
	// на шину попала бы пара, которой не было: фронт красит инстанс по намерению,
	// а объясняет по фазе.
	st := publishedState(t, func() Intent { return IntentEnabled })

	if st.Intent != IntentEnabled || st.Phase != PhaseSettled {
		t.Fatalf("наружу уехала пара {%q, %q}, ожидали {enabled, settled}", st.Intent, st.Phase)
	}
}

func TestWorkerWithoutIntentAccessorStaysDisabled(t *testing.T) {
	// Воркеру забыли передать источник намерения. Fail-closed: живую систему без
	// намерения не трогаем, а наружу едет честное «выключено», а не «достигнуто».
	st := publishedState(t, nil)

	if st.Intent != IntentDisabled || st.Phase != PhaseDisabled {
		t.Fatalf("наружу уехала пара {%q, %q}, ожидали {disabled, disabled}", st.Intent, st.Phase)
	}
}

func TestWorkerPostAfterStopReturnsFalse(t *testing.T) {
	rec := NewReconciler(staticRole{res: nil}, nil, ReconcileOpts{})
	w := NewWorker(rec, func() Intent { return IntentEnabled }, func(Result, Phase) {})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	w.Stop()

	if w.Post(EventBoot) {
		t.Fatal("после остановки события приниматься не должны")
	}
}

func TestWorkerSkipsPublishOnCanceledContext(t *testing.T) {
	// Выключение демона не должно публиковать ложное состояние.
	rec := NewReconciler(staticRole{res: []Resource{&probeResource{id: "a"}}}, nil, ReconcileOpts{})
	var calls int
	var mu sync.Mutex
	w := NewWorker(rec, func() Intent { return IntentEnabled }, func(Result, Phase) {
		mu.Lock()
		calls++
		mu.Unlock()
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.Start(ctx)
	w.Post(EventBoot)
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
	mu   sync.Mutex
	runs int
	id   ResourceID
	// skip — сколько первых наблюдений пропустить свободно. Нужен, чтобы
	// повесить ресурс не на первом проходе, а на втором.
	skip    int
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (b *blockingResource) ID() ResourceID { return b.id }

func (b *blockingResource) Observe(ctx context.Context) (Observation, error) {
	b.mu.Lock()
	b.runs++
	n := b.runs
	b.mu.Unlock()
	if n <= b.skip {
		return Observation{Known: true}, nil
	}
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
	w := NewWorker(rec, func() Intent { return IntentEnabled }, func(Result, Phase) {})

	w.Start(context.Background())
	w.Post(EventBoot)
	<-slow.entered // реконсиляция началась и висит в наблюдении

	done := make(chan struct{})
	go func() { w.Stop(); close(done) }()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop не вернулся за 2 секунды — идущая реконсиляция не прерывается")
	}
}

func TestWorkerDoesNotPublishWhenCanceledDuringObserve(t *testing.T) {
	slow := &blockingResource{id: "a", entered: make(chan struct{}), release: make(chan struct{})}
	rec := NewReconciler(staticRole{res: []Resource{slow}}, nil, ReconcileOpts{})

	var mu sync.Mutex
	var phases []Phase
	w := NewWorker(rec, func() Intent { return IntentEnabled }, func(_ Result, p Phase) {
		mu.Lock()
		phases = append(phases, p)
		mu.Unlock()
	})

	w.Start(context.Background())
	w.Post(EventBoot)
	<-slow.entered
	w.Stop()

	mu.Lock()
	defer mu.Unlock()
	if len(phases) != 0 {
		t.Fatalf("опубликованы фазы %v — выключение не должно оставлять следа", phases)
	}
}

func TestWorkerDoesNotPublishWhenCanceledOnSecondPass(t *testing.T) {
	// Проход 1 применяет шаг async-ресурса. На проходе 2 отмена ловится в
	// наблюдении второго ресурса, план повторяется, свежих шагов нет — выход
	// StopAwaiting. Публиковать при выключении нельзя и на этом пути.
	async := &asyncResource{id: "async"}
	slow := &blockingResource{id: "slow", skip: 1, entered: make(chan struct{}), release: make(chan struct{})}
	rec := NewReconciler(staticRole{res: []Resource{async, slow}}, nil, ReconcileOpts{})

	var mu sync.Mutex
	var phases []Phase
	w := NewWorker(rec, func() Intent { return IntentEnabled }, func(_ Result, p Phase) {
		mu.Lock()
		phases = append(phases, p)
		mu.Unlock()
	})

	w.Start(context.Background())
	w.Post(EventBoot)
	<-slow.entered
	w.Stop()

	mu.Lock()
	defer mu.Unlock()
	if len(phases) != 0 {
		t.Fatalf("опубликованы фазы %v — выключение не должно оставлять следа ни на одном пути выхода", phases)
	}
}

func TestWorkerCoalescesQueuedEvents(t *testing.T) {
	// Схлопывание закрепляем защёлкой: пока первое наблюдение висит, копим
	// будильники, и все они обязаны слиться в ОДИН прогон. Порога runs <= 5 для
	// этого мало — пять событий без схлопывания дают ровно пять прогонов.
	r := &blockingResource{id: "a", entered: make(chan struct{}), release: make(chan struct{})}
	rec := NewReconciler(staticRole{res: []Resource{r}}, nil, ReconcileOpts{})
	w := NewWorker(rec, func() Intent { return IntentEnabled }, func(Result, Phase) {})

	w.Start(context.Background())
	w.Post(EventBoot)
	<-r.entered // первый прогон висит в наблюдении

	for i := 0; i < 4; i++ {
		w.Post(EventBoot)
	}
	close(r.release)

	// Дождаться прогона, который разгребает очередь.
	deadline := time.After(2 * time.Second)
	for {
		r.mu.Lock()
		runs := r.runs
		r.mu.Unlock()
		if runs >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("прогонов %d — воркер не разгрёб очередь", runs)
		case <-time.After(time.Millisecond):
		}
	}
	// Окно на лишние прогоны: без схлопывания четыре события дали бы ещё
	// четыре, и после снятой защёлки все они укладываются в микросекунды.
	time.Sleep(50 * time.Millisecond)
	w.Stop()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runs > 2 {
		t.Fatalf("прогонов %d, ожидали не больше двух — схлопывание не работает", r.runs)
	}
}

func TestWorkerStopBeforeStartDoesNotRun(t *testing.T) {
	// Stop успел раньше Start: он уже вернулся, решив, что ждать некого, и
	// контекст не отменял. Поднимать горутину после этого нельзя — она прогонит
	// реконсиляцию с живым контекстом за спиной у остановившего.
	r := &probeResource{id: "a"}
	rec := NewReconciler(staticRole{res: []Resource{r}}, nil, ReconcileOpts{})
	w := NewWorker(rec, func() Intent { return IntentEnabled }, func(Result, Phase) {})

	w.Post(EventBoot)
	w.Stop()
	w.Start(context.Background())

	time.Sleep(50 * time.Millisecond)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runs != 0 {
		t.Fatalf("прогонов %d после остановки, ожидали 0", r.runs)
	}
}

func TestWorkerArmsRecheckTimer(t *testing.T) {
	// Ресурс просит подстраховочную сверку — воркер обязан сам себя разбудить.
	r := &probeResource{id: "a", recheck: 20 * time.Millisecond}
	rec := NewReconciler(staticRole{res: []Resource{r}}, nil, ReconcileOpts{})
	w := NewWorker(rec, func() Intent { return IntentEnabled }, func(Result, Phase) {})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	w.Post(EventBoot)

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

func TestWorkerRecheckWakesThroughQueue(t *testing.T) {
	// Таймер подстраховки будит воркер тем же путём, что и все прочие источники:
	// кладёт будильник в очередь, а не зовёт прогон мимо неё. Иначе путь
	// пробуждения раздваивается, схлопывание для подстраховочных сверок не
	// работает, а EventRecheck становится мёртвой константой.
	//
	// Проба забирает будильник из очереди сама: пока она ждёт на приёме, отправка
	// из ветки таймера отдаёт значение прямо ей — воркер в этот момент из своего
	// select уже вышел. Мимо очереди в неё ничего не попадёт вовсе.
	r := &probeResource{id: "a", recheck: 20 * time.Millisecond}
	rec := NewReconciler(staticRole{res: []Resource{r}}, nil, ReconcileOpts{})
	w := NewWorker(rec, func() Intent { return IntentEnabled }, func(Result, Phase) {})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	w.Post(EventBoot)

	// Дождаться конца первого прогона: иначе проба перехватит собственный boot.
	deadline := time.After(2 * time.Second)
	for {
		r.mu.Lock()
		runs := r.runs
		r.mu.Unlock()
		if runs >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("первый прогон не состоялся за 2 секунды")
		case <-time.After(time.Millisecond):
		}
	}

	for {
		select {
		case kind := <-w.ch:
			if kind == EventRecheck {
				w.Stop()
				return
			}
			// Свой же boot, если воркер не успел его забрать: ждём дальше.
		case <-time.After(3 * time.Second):
			w.Stop()
			t.Fatal("будильник подстраховки в очередь не попал — таймер зовёт прогон мимо неё")
		}
	}
}
