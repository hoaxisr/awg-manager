package events

import (
	"sync"
	"sync/atomic"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// RoutingChangedListener is fired after every drain that invalidated at
// least one store. The listener rebuilds the routing snapshot and decides
// (by hash compare) whether to broadcast it — the dispatcher itself stays
// agnostic of routing semantics.
type RoutingChangedListener = func()

// Dispatcher is a push-side cache invalidator. Hook handler Enqueue's
// Events; the worker goroutine drains a pending-set and calls the
// appropriate Store.Invalidate() methods.
//
// The pending-set is idempotent + commutative → bursts of hooks
// collapse to a constant-size invalidation batch, bounded by the number
// of distinct resources in the system. No disk, no ordering, no loss.
type Dispatcher struct {
	queries *query.Queries
	log     Logger

	mu      sync.Mutex
	pending map[invKey]struct{}

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

// invKey is the dedup key in the pending set. "" resourceID means "all".
type invKey struct {
	store      storeType
	resourceID string
}

type storeType int

const (
	storeInterfaces storeType = iota
	storePeers
	storePolicies
	storeHotspot
	storeRoutes
	storeObjectGroups
	storeDNSProxy
	storeRunningConfig
	storeWGServers
)

// NewDispatcher constructs a dispatcher. Call Start() to run the worker.
func NewDispatcher(q *query.Queries, log Logger) *Dispatcher {
	if log == nil {
		log = NopLogger()
	}
	return &Dispatcher{
		queries: q,
		log:     log,
		pending: make(map[invKey]struct{}),
		notify:  make(chan struct{}, 1),
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
}

// SetRoutingChanged registers (or clears with nil) the callback fired after
// every drain. Safe to call at any time; the stored pointer is swapped
// atomically. The callback runs in its own goroutine so slow rebuilds don't
// block the invalidator.
func (d *Dispatcher) SetRoutingChanged(fn RoutingChangedListener) {
	if fn == nil {
		d.onRouting.Store(nil)
		return
	}
	d.onRouting.Store(&fn)
}

// Start launches the worker goroutine. Non-blocking. Safe to call
// multiple times — subsequent calls are no-ops.
func (d *Dispatcher) Start() {
	d.startOnce.Do(func() {
		d.started.Store(true)
		go d.run()
	})
}

// Stop signals the worker to exit and waits for it. Safe to call
// multiple times — subsequent calls are no-ops (they still wait on doneCh
// if Start was called).
func (d *Dispatcher) Stop() {
	d.stopOnce.Do(func() {
		close(d.stopCh)
	})
	if d.started.Load() {
		<-d.doneCh
	}
}

// Enqueue merges an Event into the pending-set and wakes the worker.
// Non-blocking — safe to call from HTTP handler goroutine.
func (d *Dispatcher) Enqueue(e Event) {
	keys := d.eventToKeys(e)
	if len(keys) == 0 {
		return
	}
	d.mu.Lock()
	for _, k := range keys {
		d.pending[k] = struct{}{}
	}
	d.mu.Unlock()
	select {
	case d.notify <- struct{}{}:
	default:
	}
}

// eventToKeys maps a hook event to the set of invalidation keys it implies.
func (d *Dispatcher) eventToKeys(e Event) []invKey {
	switch e.Type {
	case EventIfCreated:
		// A new Wireguard/Proxy/OpkgTun interface may show up in the
		// VPN-server + system-tunnel lists; drop the aggregate cache.
		return []invKey{
			{storeInterfaces, ""},
			{storeWGServers, ""},
		}
	case EventIfDestroyed:
		return []invKey{
			{storeInterfaces, ""},
			{storeInterfaces, e.ID},
			{storePeers, e.ID},
			{storeWGServers, ""},
		}
	case EventIfIPChanged:
		return []invKey{
			{storeInterfaces, ""},
			{storeInterfaces, e.ID},
			{storeRoutes, ""},
		}
	case EventIfLayerChanged:
		keys := []invKey{
			{storeInterfaces, ""},
			{storeInterfaces, e.ID},
			{storePeers, e.ID},
		}
		if e.Layer == "conf" {
			keys = append(keys, invKey{storeRunningConfig, ""})
		}
		if e.Layer == "ipv4" || e.Layer == "ipv6" {
			keys = append(keys, invKey{storeRoutes, ""})
		}
		return keys
	}
	return nil
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
	batch := d.pending
	d.pending = make(map[invKey]struct{})
	d.mu.Unlock()

	if len(batch) == 0 {
		return
	}

	for k := range batch {
		d.invalidate(k)
	}

	if p := d.onRouting.Load(); p != nil {
		go (*p)()
	}
}

func (d *Dispatcher) invalidate(k invKey) {
	switch k.store {
	case storeInterfaces:
		if d.queries.Interfaces == nil {
			d.log.Warnf("invalidate: InterfaceStore not wired")
			return
		}
		if k.resourceID == "" {
			d.queries.Interfaces.InvalidateAll()
		} else {
			d.queries.Interfaces.Invalidate(k.resourceID)
		}
	case storePeers:
		if d.queries.Peers == nil {
			return
		}
		if k.resourceID == "" {
			d.queries.Peers.InvalidateAll()
		} else {
			d.queries.Peers.Invalidate(k.resourceID)
		}
	case storePolicies:
		if d.queries.Policies != nil {
			d.queries.Policies.InvalidateAll()
		}
	case storeHotspot:
		if d.queries.Hotspot != nil {
			d.queries.Hotspot.InvalidateAll()
		}
	case storeRoutes:
		if d.queries.Routes != nil {
			d.queries.Routes.InvalidateAll()
		}
	case storeObjectGroups:
		if d.queries.ObjectGroups != nil {
			d.queries.ObjectGroups.InvalidateAll()
		}
	case storeDNSProxy:
		if d.queries.DNSProxy != nil {
			d.queries.DNSProxy.InvalidateAll()
		}
	case storeRunningConfig:
		if d.queries.RunningConfig != nil {
			d.queries.RunningConfig.InvalidateAll()
		}
	case storeWGServers:
		if d.queries.WGServers != nil {
			d.queries.WGServers.InvalidateAll()
		}
	}
}
