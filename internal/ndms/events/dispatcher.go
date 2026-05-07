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
// store mutates its internal map in place. No probing, no
// invalidate-then-refetch.
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

	mu    sync.Mutex
	queue []Event

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

// Enqueue appends an Event to the FIFO queue and wakes the worker.
// Non-blocking — safe to call from HTTP handler goroutines.
func (d *Dispatcher) Enqueue(e Event) {
	d.mu.Lock()
	d.queue = append(d.queue, e)
	d.mu.Unlock()
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
	batch := d.queue
	d.queue = nil
	d.mu.Unlock()

	if len(batch) == 0 {
		return
	}

	// Time-bound the batch — OnCreated may make ONE HTTP per event.
	// 30s gives plenty of room even for slow NDMS under burst load.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, e := range batch {
		d.apply(ctx, e)
	}

	if p := d.onRouting.Load(); p != nil {
		go (*p)()
	}
}

// apply dispatches a single event to the appropriate store mutator(s).
//
// Interfaces — direct event-sourced patch (no HTTP except OnCreated's
// single targeted fetch).
//
// Other stores — legacy InvalidateAll/Invalidate; their state will
// be re-fetched on the next read. Will be migrated to event-sourcing
// in follow-up PRs.
func (d *Dispatcher) apply(ctx context.Context, e Event) {
	if d.queries == nil {
		return
	}

	// === Event-sourced InterfaceStore path ===
	if d.queries.Interfaces != nil {
		switch e.Type {
		case EventIfCreated:
			d.queries.Interfaces.OnCreated(ctx, e.ID)
		case EventIfDestroyed:
			d.queries.Interfaces.OnDestroyed(e.ID)
		case EventIfLayerChanged:
			d.queries.Interfaces.OnLayerChanged(e.ID, e.Layer, e.Level)
		case EventIfIPChanged:
			d.queries.Interfaces.OnIPChanged(e.ID, e.Address, e.Up, e.Connected)
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
		if e.Layer == "conf" && d.queries.RunningConfig != nil {
			d.queries.RunningConfig.InvalidateAll()
		}
		if (e.Layer == "ipv4" || e.Layer == "ipv6") && d.queries.Routes != nil {
			d.queries.Routes.InvalidateAll()
		}
	}
}
