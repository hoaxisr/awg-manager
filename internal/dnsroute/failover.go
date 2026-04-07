package dnsroute

import (
	"sync"

	"github.com/hoaxisr/awg-manager/internal/events"
)

// Logger is the minimal logger interface used by FailoverManager.
// (Will be filled in by Task 2 — for now just a type stub.)
type Logger interface {
	Warnf(format string, args ...interface{})
}

// AffectedList describes a DNS route list affected by a tunnel state change.
type AffectedList struct {
	ListID     string
	ListName   string
	FromTunnel string // tunnel interface name — currently active for the list
	ToTunnel   string // tunnel interface name that will become active after failover
}

// AffectedListsLookup returns the DNS route lists that reference a given tunnelID.
// For "switched" action, FromTunnel is the failed tunnel and ToTunnel is the next active.
// For "restored" action, the order is reversed.
type AffectedListsLookup func(tunnelID string, action string) []AffectedList

// FailoverManager tracks which tunnels have failed and need DNS route failover.
// In-memory only — state resets on restart (reconcile rebuilds from primary targets).
type FailoverManager struct {
	mu             sync.RWMutex
	failedSet      map[string]struct{}
	reconcileFn    func() error // returns error so we can rollback / retry
	// dirty is a global "best-effort retry" flag, not a per-tunnel retry queue.
	// Set when any reconcile fails; cleared on the next successful reconcile.
	// While dirty, the next event force-retries even on duplicate (alreadyFailed)
	// tunnels. A success for tunnel B clears the flag and won't retry tunnel A —
	// but A's next pingcheck event will see alreadyFailed=false (after rollback)
	// and proceed normally, so self-healing works via the periodic event stream.
	dirty bool
	stopCh         chan struct{}
	stopped        chan struct{}
	bus            *events.Bus
	log            Logger
	lookupAffected AffectedListsLookup
}

// NewFailoverManager creates a new failover manager.
// reconcileFn is called whenever the failed set changes; returns error to enable rollback.
// Pass nil if no reconcile callback is needed (for testing).
func NewFailoverManager(reconcileFn func() error) *FailoverManager {
	return &FailoverManager{
		failedSet:   make(map[string]struct{}),
		reconcileFn: reconcileFn,
	}
}

// SetEventBus sets the event bus for publishing failover notifications.
func (fm *FailoverManager) SetEventBus(bus *events.Bus) {
	fm.bus = bus
}

// SetLogger sets the logger for failover diagnostics.
func (fm *FailoverManager) SetLogger(log Logger) {
	fm.log = log
}

// SetAffectedListsLookup sets the callback that resolves which DNS lists are
// affected by a tunnel state change. Used to enrich SSE failover events.
func (fm *FailoverManager) SetAffectedListsLookup(lookup AffectedListsLookup) {
	fm.lookupAffected = lookup
}

// publishFailoverEvents publishes one SSE event per affected DNS list.
// If reconcileErr is non-nil, publishes "error" action with the error message.
// If lookupAffected is nil or returns no lists, no events are published.
func (fm *FailoverManager) publishFailoverEvents(tunnelID, baseAction string, reconcileErr error) {
	if fm.bus == nil || fm.lookupAffected == nil {
		return
	}

	affected := fm.lookupAffected(tunnelID, baseAction)
	if len(affected) == 0 {
		return
	}

	action := baseAction
	errMsg := ""
	if reconcileErr != nil {
		action = "error"
		errMsg = reconcileErr.Error()
	}

	for _, a := range affected {
		fm.bus.Publish("dnsroute:failover", events.DNSRouteFailoverEvent{
			ListID:     a.ListID,
			ListName:   a.ListName,
			TunnelID:   tunnelID,
			FromTunnel: a.FromTunnel,
			ToTunnel:   a.ToTunnel,
			Action:     action,
			Error:      errMsg,
		})
	}
}

// MarkFailed adds a tunnel to the failed set and triggers reconcile.
// If reconcile fails, the change is rolled back and the dirty flag is set
// so the next event will force-retry even if it's a duplicate.
// Returns nil on success, or the reconcile error.
func (fm *FailoverManager) MarkFailed(tunnelID string) error {
	fm.mu.Lock()
	wasDirty := fm.dirty
	_, alreadyFailed := fm.failedSet[tunnelID]

	// Skip no-op only if state is clean (no pending retry needed)
	if alreadyFailed && !wasDirty {
		fm.mu.Unlock()
		return nil
	}

	if !alreadyFailed {
		fm.failedSet[tunnelID] = struct{}{}
	}
	fm.mu.Unlock()

	if fm.reconcileFn == nil {
		return nil
	}

	err := fm.reconcileFn()

	fm.mu.Lock()
	if err != nil {
		// Rollback only if WE added it (not if it was already there before this call)
		if !alreadyFailed {
			delete(fm.failedSet, tunnelID)
		}
		fm.dirty = true
	} else {
		fm.dirty = false
	}
	fm.mu.Unlock()

	fm.publishFailoverEvents(tunnelID, "switched", err)
	return err
}

// MarkRecovered removes a tunnel from the failed set and triggers reconcile.
// If reconcile fails, the change is rolled back and the dirty flag is set.
// Returns nil on success, or the reconcile error.
func (fm *FailoverManager) MarkRecovered(tunnelID string) error {
	fm.mu.Lock()
	wasDirty := fm.dirty
	_, wasFailed := fm.failedSet[tunnelID]

	// Skip no-op only if state is clean
	if !wasFailed && !wasDirty {
		fm.mu.Unlock()
		return nil
	}

	if wasFailed {
		delete(fm.failedSet, tunnelID)
	}
	fm.mu.Unlock()

	if fm.reconcileFn == nil {
		return nil
	}

	err := fm.reconcileFn()

	fm.mu.Lock()
	if err != nil {
		if wasFailed {
			fm.failedSet[tunnelID] = struct{}{}
		}
		fm.dirty = true
	} else {
		fm.dirty = false
	}
	fm.mu.Unlock()

	fm.publishFailoverEvents(tunnelID, "restored", err)
	return err
}

// IsFailed checks if a tunnel is in the failed set.
func (fm *FailoverManager) IsFailed(tunnelID string) bool {
	fm.mu.RLock()
	defer fm.mu.RUnlock()
	_, exists := fm.failedSet[tunnelID]
	return exists
}

// FailedTunnels returns a copy of all currently failed tunnel IDs.
func (fm *FailoverManager) FailedTunnels() []string {
	fm.mu.RLock()
	defer fm.mu.RUnlock()
	result := make([]string, 0, len(fm.failedSet))
	for id := range fm.failedSet {
		result = append(result, id)
	}
	return result
}

// StartListener subscribes to the event bus and listens for pingcheck:state events.
// Runs a goroutine that processes events until StopListener is called.
func (fm *FailoverManager) StartListener(bus *events.Bus) {
	fm.stopCh = make(chan struct{})
	fm.stopped = make(chan struct{})

	_, ch, unsub := bus.Subscribe()

	go func() {
		defer close(fm.stopped)
		defer unsub()

		for {
			select {
			case ev, ok := <-ch:
				if !ok {
					return
				}
				if ev.Type != "pingcheck:state" {
					continue
				}
				fm.handlePingCheckEvent(ev.Data)
			case <-fm.stopCh:
				return
			}
		}
	}()
}

// StopListener stops the event listener goroutine.
func (fm *FailoverManager) StopListener() {
	if fm.stopCh != nil {
		close(fm.stopCh)
		<-fm.stopped
	}
}

func (fm *FailoverManager) handlePingCheckEvent(data any) {
	ev, ok := data.(events.PingCheckStateEvent)
	if !ok {
		if fm.log != nil {
			fm.log.Warnf("dnsroute failover: unexpected pingcheck event type: %T", data)
		}
		return
	}

	switch ev.Status {
	case "fail":
		_ = fm.MarkFailed(ev.TunnelID)
	case "pass":
		_ = fm.MarkRecovered(ev.TunnelID)
	}
}
