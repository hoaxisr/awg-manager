package proxyrt

import (
	"context"
	"sync"
	"time"
)

// queueDepth — глубина очереди будильников. Пачка событий схлопывается в один
// прогон: реконсиляция идемпотентна, и гонять её пять раз подряд на пять
// событий значит пять раз опросить RCI без причины.
const queueDepth = 8

// Worker — единственный обработчик мутаций одного инстанса. Последовательность
// достигается конструкцией, а не набором локов и защёлок.
type Worker struct {
	id      string
	rec     *Reconciler
	intent  func() Intent
	onState func(Result, Phase)

	ch       chan Event
	stopOnce sync.Once
	done     chan struct{}
	closed   chan struct{}
	started  bool
}

// intent — функция, а не значение: намерение живёт в хранилище конфига и
// меняется параллельно с работой воркера, поэтому читается на каждом прогоне.
func NewWorker(id string, rec *Reconciler, intent func() Intent, onState func(Result, Phase)) *Worker {
	return &Worker{
		id:      id,
		rec:     rec,
		intent:  intent,
		onState: onState,
		ch:      make(chan Event, queueDepth),
		done:    make(chan struct{}),
		closed:  make(chan struct{}),
	}
}

func (w *Worker) Start(ctx context.Context) {
	w.started = true
	go func() {
		defer close(w.done)
		var recheck <-chan time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-w.closed:
				w.drainAndRun(ctx)
				return
			case <-recheck:
				recheck = w.runOnce(ctx)
			case <-w.ch:
				w.coalesce()
				recheck = w.runOnce(ctx)
			}
		}
	}()
}

// coalesce забирает из очереди все накопившиеся будильники: один прогон
// покрывает их все.
func (w *Worker) coalesce() {
	for {
		select {
		case <-w.ch:
		default:
			return
		}
	}
}

func (w *Worker) drainAndRun(ctx context.Context) {
	select {
	case <-w.ch:
		w.coalesce()
		w.runOnce(ctx)
	default:
	}
}

// runOnce гоняет реконсиляцию и возвращает канал таймера подстраховки, если
// его попросил хоть один ресурс.
func (w *Worker) runOnce(ctx context.Context) <-chan time.Time {
	intent := IntentEnabled
	if w.intent != nil {
		intent = w.intent()
	}
	res, phase := w.rec.Run(ctx, intent)
	// Отмена контекста — не состояние системы, а наше выключение. Публиковать
	// её значит показать пользователю ложный затык на каждом рестарте демона.
	if res.Stop != StopCanceled && w.onState != nil {
		w.onState(res, phase)
	}
	if res.Recheck > 0 {
		return time.After(res.Recheck)
	}
	return nil
}

// Post кладёт будильник в очередь. false означает «воркер остановлен» либо
// «очередь полна» — во втором случае прогон и так предстоит.
func (w *Worker) Post(e Event) bool {
	select {
	case <-w.closed:
		return false
	default:
	}
	select {
	case w.ch <- e:
		return true
	default:
		return false
	}
}

// Stop закрывает приём событий и ждёт завершения. Вызов без предшествующего
// Start возвращает управление сразу: ждать некого.
func (w *Worker) Stop() {
	w.stopOnce.Do(func() { close(w.closed) })
	if !w.started {
		return
	}
	<-w.done
}
