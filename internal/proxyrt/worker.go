package proxyrt

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// queueDepth — глубина очереди будильников. Пачка событий схлопывается в один
// прогон: реконсиляция идемпотентна, и гонять её пять раз подряд на пять
// событий значит пять раз опросить RCI без причины.
const queueDepth = 8

// Worker — единственный обработчик мутаций одного инстанса. Последовательность
// достигается конструкцией, а не набором локов и защёлок.
type Worker struct {
	rec     *Reconciler
	intent  func() Intent
	onState func(Result, Phase)

	ch        chan EventKind
	startOnce sync.Once
	stopOnce  sync.Once
	done      chan struct{}
	closed    chan struct{}
	// started атомарен: Start и Stop зовут из разных горутин — инстансы
	// поднимает одна, гасит другая.
	started atomic.Bool
	// wcancel отменяет собственный контекст воркера, производный от того, что
	// дали в Start. Нужен, чтобы Stop мог прервать идущую реконсиляцию.
	wcancel context.CancelFunc
}

// intent — функция, а не значение: намерение живёт в хранилище конфига и
// меняется параллельно с работой воркера, поэтому читается на каждом прогоне.
//
// Идентификатора инстанса у воркера нет: его знает вызывающий — он держит карту
// «инстанс → воркер» и замыкает идентификатор в onState. Заводить второй
// экземпляр имени незачем.
func NewWorker(rec *Reconciler, intent func() Intent, onState func(Result, Phase)) *Worker {
	return &Worker{
		rec:     rec,
		intent:  intent,
		onState: onState,
		ch:      make(chan EventKind, queueDepth),
		done:    make(chan struct{}),
		closed:  make(chan struct{}),
	}
}

// Start поднимает единственную горутину воркера. Повторный вызов — безвредный
// no-op: инвариант «воркер один на инстанс» держится конструкцией, а не
// договорённостью вызывающего.
func (w *Worker) Start(ctx context.Context) {
	w.startOnce.Do(func() {
		// Приём уже закрыт: Stop мог вернуться раньше, увидев started=false и
		// решив, что ждать некого. Поднять горутину сейчас значит прогнать
		// реконсиляцию с живым контекстом за спиной у остановившего.
		select {
		case <-w.closed:
			return
		default:
		}
		wctx, cancel := context.WithCancel(ctx)
		w.wcancel = cancel
		w.started.Store(true)
		go func() {
			defer close(w.done)
			var recheck <-chan time.Time
			for {
				// Приоритет остановки. В общем select остановка равноправна с
				// событием, и жребий может выпасть на событие — тогда прогон
				// уйдёт уже после возврата Stop. Проверяем отдельно и раньше.
				select {
				case <-wctx.Done():
					return
				case <-w.closed:
					return
				default:
				}
				select {
				case <-wctx.Done():
					return
				case <-w.closed:
					return
				case <-recheck:
					// Таймер идёт тем же путём, что и прочие источники:
					// будильник в очередь, а не вызов мимо неё. Иначе путь
					// пробуждения раздваивается и схлопывание не работает для
					// подстраховочных сверок.
					recheck = nil
					w.Post(EventRecheck)
				case <-w.ch:
					w.coalesce()
					recheck = w.runOnce(wctx)
				}
			}
		}()
	})
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

// runOnce гоняет реконсиляцию и возвращает канал таймера подстраховки, если
// его попросил хоть один ресурс.
func (w *Worker) runOnce(ctx context.Context) <-chan time.Time {
	// Без источника намерения — fail-closed: воркер, которому забыли передать
	// аксессор, не должен применять изменения к живой системе.
	intent := IntentDisabled
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

// Post кладёт будильник в очередь. true означает «событие принято в очередь», а
// не «будет обработано»: при остановке принятое выбрасывается вместе с очередью.
//
// false означает «воркер остановлен» либо «очередь полна» — во втором случае
// прогон и так предстоит.
func (w *Worker) Post(kind EventKind) bool {
	select {
	case <-w.closed:
		return false
	default:
	}
	select {
	case w.ch <- kind:
		return true
	default:
		return false
	}
}

// Stop закрывает приём событий и ждёт завершения. Вызов без предшествующего
// Start возвращает управление сразу: ждать некого.
//
// Остановка НЕ начинает новой работы: недобранные будильники теряются осознанно.
// Реконсиляция идемпотентна и смотрит на факт, поэтому следующий старт наверстает
// всё сам, а Stop не превращается в ещё один RCI-раунд.
//
// Отмена собственного контекста прерывает идущую реконсиляцию: Run вернётся со
// StopCanceled, публикация состояния пропустится. Без этого гашение одного
// инстанса при живом демоне ждало бы конца RCI-раунда. Граница держится на том,
// что ресурсы уважают контекст в Observe и Apply.
func (w *Worker) Stop() {
	w.stopOnce.Do(func() { close(w.closed) })
	if !w.started.Load() {
		return
	}
	if w.wcancel != nil {
		w.wcancel()
	}
	<-w.done
}
