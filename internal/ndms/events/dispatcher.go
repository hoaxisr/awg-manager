package events

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// RoutingChangedListener is fired after every drain that processed at
// least one event. The listener rebuilds the routing snapshot and
// decides (by hash compare) whether to broadcast it — the dispatcher
// itself stays agnostic of routing semantics.
type RoutingChangedListener = func()

// Dispatcher is the bridge from NDMS hook scripts to in-process state.
//
// For InterfaceStore — event-sourced: each event is applied directly
// (OnCreated / OnDestroyed / OnLayerChanged / OnIPChanged) and the
// store mutates its internal map in place, without HTTP. A hook of
// existence that disagrees with the map marks it dirty. A batch with
// hooks of existence (ifcreated/ifdestroyed) ends with ONE full list
// (ReconcileDirty) in the dispatcher's single list goroutine — the next
// batch is not held by it; batches arriving during a list coalesce into
// ONE more list after it — and only after a list the existence listener is called
// (SetExistenceListed): the UI is told to refetch tunnels/servers when
// the map already reflects the batch. No point reads by name (F546).
//
// For all other stores (Peers, Routes, RunningConfig, WGServers, ...)
// the legacy invalidate-on-event pattern is preserved — those stores
// will be migrated to event-sourcing in follow-up PRs.
//
// Enqueue is non-blocking. The worker goroutine drains a FIFO queue
// of events in arrival order so semantically-ordered hook bursts (e.g.
// ifcreated → conf=running → link=running) apply correctly.
type Dispatcher struct {
	queries *query.Queries
	log     Logger

	mu       sync.Mutex
	queue    []Event
	overflow bool // с прошлого прохода отброшены события (F572)

	// Одна горутина списка на диспетчер (listing); пачки существования,
	// пришедшие за время её списка, склеиваются в ОДИН следующий (again,
	// againPublish — OR их publish).
	listMu       sync.Mutex
	listing      bool
	again        bool
	againPublish bool

	notify    chan struct{} // cap=1, non-blocking wake
	stopCh    chan struct{}
	doneCh    chan struct{}
	stopOnce  sync.Once
	startOnce sync.Once
	started   atomic.Bool

	onRouting  atomic.Pointer[RoutingChangedListener]
	onExisting atomic.Pointer[func(publish bool)]
}

// Logger is the minimal logging surface Dispatcher uses.
type Logger interface {
	Warnf(format string, args ...any)
}

type nopLogger struct{}

func (nopLogger) Warnf(string, ...any) {}

// NopLogger returns a logger that drops everything. Use in tests.
func NopLogger() Logger { return nopLogger{} }

// NewDispatcher constructs a dispatcher. Call Start() to run the worker.
func NewDispatcher(q *query.Queries, log Logger) *Dispatcher {
	if log == nil {
		log = NopLogger()
	}
	return &Dispatcher{
		queries: q,
		log:     log,
		notify:  make(chan struct{}, 1),
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
}

// SetRoutingChanged registers (or clears with nil) the callback fired
// after every non-empty drain. Stored atomically; safe at any time.
// The callback runs in its own goroutine so slow rebuilds don't block
// the dispatch loop.
func (d *Dispatcher) SetRoutingChanged(fn RoutingChangedListener) {
	if fn == nil {
		d.onRouting.Store(nil)
		return
	}
	d.onRouting.Store(&fn)
}

// SetExistenceListed registers (or clears with nil) the callback fired after
// the list of EVERY batch that carried ifcreated/ifdestroyed — even when the
// list failed (Warn; the map stays dirty for the next reader). publish — в
// пачке был хук существования чужого имени: ifcreated не из owned,
// ifdestroyed не из removed (InterfaceStore, П14/П20). Свои создание и
// снятие хуком не публикуются (решение владельца В3): до записи туннеля в
// стор новый интерфейс показался бы в «системных» призраком. nil — nothing
// is published.
func (d *Dispatcher) SetExistenceListed(fn func(publish bool)) {
	if fn == nil {
		d.onExisting.Store(nil)
		return
	}
	d.onExisting.Store(&fn)
}

// Start launches the worker goroutine. Non-blocking. Idempotent.
func (d *Dispatcher) Start() {
	d.startOnce.Do(func() {
		d.started.Store(true)
		go d.run()
	})
}

// Stop signals the worker to exit and waits for it. Idempotent.
func (d *Dispatcher) Stop() {
	d.stopOnce.Do(func() {
		close(d.stopCh)
	})
	if d.started.Load() {
		<-d.doneCh
	}
}

// maxQueuedEvents — предел очереди: spool пишет хуки с загрузки, а воркер
// стартует лишь после готовности NDMS (F572).
const maxQueuedEvents = 1024

// Enqueue appends an Event to the FIFO queue and wakes the worker.
// Non-blocking — safe to call from HTTP handler goroutines.
//
// Очередь полна — отбрасывается самое старое событие (одно предупреждение
// на проход), а проход вместо потерянного перечитывает всё (см. drain).
func (d *Dispatcher) Enqueue(e Event) {
	d.mu.Lock()
	first := false
	if len(d.queue) >= maxQueuedEvents {
		d.queue = d.queue[1:]
		first = !d.overflow
		d.overflow = true
	}
	d.queue = append(d.queue, e)
	d.mu.Unlock()
	if first {
		d.log.Warnf("очередь хуков NDMS переполнена (%d): старые события отброшены, следующий проход перечитает список", maxQueuedEvents)
	}
	select {
	case d.notify <- struct{}{}:
	default:
	}
}

func (d *Dispatcher) run() {
	defer close(d.doneCh)
	for {
		select {
		case <-d.stopCh:
			return
		case <-d.notify:
			d.drain()
		}
	}
}

func (d *Dispatcher) drain() {
	d.mu.Lock()
	batch, overflow := d.queue, d.overflow
	d.queue, d.overflow = nil, false
	d.mu.Unlock()

	if len(batch) == 0 {
		return
	}

	existence, publish := false, false
	for _, e := range batch {
		own := d.apply(e)
		switch e.Type {
		case EventIfCreated, EventIfDestroyed:
			existence = true
			publish = publish || !own
		}
	}
	if overflow {
		d.refreshAfterOverflow()
		// Среди отброшенных могли быть создание/снятие: UI узнаёт о них только
		// публикацией. Список не удваивается — после Refresh ReconcileDirty пуст.
		existence, publish = true, true
	}
	if existence {
		d.scheduleList(publish)
	}

	if p := d.onRouting.Load(); p != nil {
		go (*p)()
	}
}

// scheduleList запускает горутину списка или, если она уже работает,
// заказывает ей ровно один следующий список: всплеск хуков стоит ≤2 списков,
// а не по списку на пачку параллельно.
func (d *Dispatcher) scheduleList(publish bool) {
	d.listMu.Lock()
	defer d.listMu.Unlock()
	if d.listing {
		d.again = true
		d.againPublish = d.againPublish || publish
		return
	}
	d.listing = true
	go d.listExistence(publish)
}

// listExistence — список пачки хуков существования, затем слушатель; пока
// за время списка пришли новые пачки — ещё круг. Хуки, разошедшиеся с картой,
// сверяются ОДНИМ списком (join с читателями карты); совпавшие с картой не
// стоят ни одного запроса. Слушатель — строго после списка: публикация до него
// отдала бы UI карту без этой пачки.
func (d *Dispatcher) listExistence(publish bool) {
	for {
		if d.queries != nil && d.queries.Interfaces != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := d.queries.Interfaces.ReconcileDirty(ctx); err != nil {
				d.log.Warnf("reconcile dirty interfaces: %v", err)
			}
			cancel()
		}
		if p := d.onExisting.Load(); p != nil {
			(*p)(publish)
		}
		d.listMu.Lock()
		if !d.again {
			d.listing = false
			d.listMu.Unlock()
			return
		}
		publish = d.againPublish
		d.again, d.againPublish = false, false
		d.listMu.Unlock()
	}
}

// refreshAfterOverflow заменяет отброшенные события: карта интерфейсов
// перечитывается полным списком (Get/List ведутся хуками, метку «грязно» не
// видят), прочие сторы сбрасываются целиком — какой id они касались, неизвестно.
// Список не прочитан — метка переполнения возвращается: следующий проход
// повторит обновление.
func (d *Dispatcher) refreshAfterOverflow() {
	if d.queries == nil {
		return
	}
	if d.queries.Interfaces != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := d.queries.Interfaces.Refresh(ctx)
		cancel()
		if err != nil {
			d.log.Warnf("после переполнения очереди список не прочитан, повтор на следующем проходе: %v", err)
			d.mu.Lock()
			d.overflow = true
			d.mu.Unlock()
		}
	}
	if d.queries.Peers != nil {
		d.queries.Peers.InvalidateAll()
	}
	if d.queries.WGServers != nil {
		d.queries.WGServers.InvalidateAll()
	}
	if d.queries.Routes != nil {
		d.queries.Routes.InvalidateAll()
	}
	if d.queries.RunningConfig != nil {
		d.queries.RunningConfig.InvalidateAll()
	}
}

// apply dispatches a single event to the appropriate store mutator(s).
//
// Interfaces — direct event-sourced patch, no HTTP (a dirty map waits
// for ReconcileDirty in listExistence).
//
// Other stores — legacy InvalidateAll/Invalidate; their state will
// be re-fetched on the next read. Will be migrated to event-sourcing
// in follow-up PRs.
//
// own — хук существования своего имени (OnCreated/OnDestroyed): ifcreated
// созданного нами, ifdestroyed снятого нами. Без карты — чужое.
func (d *Dispatcher) apply(e Event) (own bool) {
	if d.queries == nil {
		return false
	}

	// === Event-sourced InterfaceStore path ===
	if d.queries.Interfaces != nil {
		// Имя ядра приходит в хуке — до разбора типа; снимает его список без
		// этого id (F570).
		if e.SystemName != "" {
			d.queries.Interfaces.OnSystemName(e.ID, e.SystemName)
		}
		switch e.Type {
		case EventIfCreated:
			own = d.queries.Interfaces.OnCreated(e.ID)
		case EventIfDestroyed:
			own = d.queries.Interfaces.OnDestroyed(e.ID)
		case EventIfLayerChanged:
			d.queries.Interfaces.OnLayerChanged(e.ID, e.Layer, e.Level)
		case EventIfIPChanged:
			d.queries.Interfaces.OnIPChanged(e.ID, e.Address)
		}
	}

	// === Legacy invalidate-on-event for non-Interface stores ===
	switch e.Type {
	case EventIfCreated:
		// New interface may show up in the WG-server list.
		if d.queries.WGServers != nil {
			d.queries.WGServers.InvalidateAll()
		}
	case EventIfDestroyed:
		if d.queries.Peers != nil {
			d.queries.Peers.Invalidate(e.ID)
		}
		if d.queries.WGServers != nil {
			d.queries.WGServers.InvalidateAll()
		}
	case EventIfIPChanged:
		if d.queries.Routes != nil {
			d.queries.Routes.InvalidateAll()
		}
	case EventIfLayerChanged:
		if d.queries.Peers != nil {
			d.queries.Peers.Invalidate(e.ID)
		}
		// Смена уровня меняет Status интерфейса, а по нему отбирается состав
		// для поллера метрик (`Status == "up"`). Без сброса список системных
		// туннелей жил бы до TTL, и поллер минутами не видел бы поднявшийся
		// или упавший туннель (F364). Дерево rc — нет: слой конфигурацию не
		// меняет, а дерево стоит ~90 тиков ndm на каждый хук (F546).
		if d.queries.WGServers != nil {
			d.queries.WGServers.InvalidateRuntime()
		}
		if e.Layer == "conf" && d.queries.RunningConfig != nil {
			d.queries.RunningConfig.InvalidateAll()
		}
		if (e.Layer == "ipv4" || e.Layer == "ipv6") && d.queries.Routes != nil {
			d.queries.Routes.InvalidateAll()
		}
	}
	return own
}
