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
// store mutates its internal map in place, without HTTP. Ids created
// by a hook and unknown to the map are fetched after the batch with ONE
// full list (ReconcilePending); a created→destroyed pair within one
// batch costs nothing. No point reads by name (F546).
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

	notify    chan struct{} // cap=1, non-blocking wake
	stopCh    chan struct{}
	doneCh    chan struct{}
	stopOnce  sync.Once
	startOnce sync.Once
	started   atomic.Bool

	onRouting atomic.Pointer[RoutingChangedListener]
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

	for _, e := range batch {
		d.apply(e)
	}
	if overflow {
		d.refreshAfterOverflow()
	}
	// Созданные хуком id, которых нет в карте, добираются ОДНИМ списком на
	// пачку: пара created→destroyed одного id к этому моменту уже схлопнулась
	// и не стоит ни одного запроса.
	if d.queries != nil && d.queries.Interfaces != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := d.queries.Interfaces.ReconcilePending(ctx); err != nil {
			d.log.Warnf("reconcile pending interfaces: %v", err)
		}
		cancel()
	}

	if p := d.onRouting.Load(); p != nil {
		go (*p)()
	}
}

// refreshAfterOverflow заменяет отброшенные события: карта интерфейсов
// перечитывается полным списком (Get/List ведутся хуками, метку «грязно» не
// видят), прочие сторы сбрасываются целиком — какой id они касались, неизвестно.
func (d *Dispatcher) refreshAfterOverflow() {
	if d.queries == nil {
		return
	}
	if d.queries.Interfaces != nil {
		d.queries.Interfaces.InvalidateAll()
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
// Interfaces — direct event-sourced patch, no HTTP (unknown created ids
// wait for ReconcilePending in drain).
//
// Other stores — legacy InvalidateAll/Invalidate; their state will
// be re-fetched on the next read. Will be migrated to event-sourcing
// in follow-up PRs.
func (d *Dispatcher) apply(e Event) {
	if d.queries == nil {
		return
	}

	// === Event-sourced InterfaceStore path ===
	if d.queries.Interfaces != nil {
		// Имя ядра приходит в хуке — до разбора типа: ifdestroyed того же
		// id следом его снимет (F570).
		if e.SystemName != "" {
			d.queries.Interfaces.OnSystemName(e.ID, e.SystemName)
		}
		switch e.Type {
		case EventIfCreated:
			d.queries.Interfaces.OnCreated(e.ID)
		case EventIfDestroyed:
			d.queries.Interfaces.OnDestroyed(e.ID)
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
}
