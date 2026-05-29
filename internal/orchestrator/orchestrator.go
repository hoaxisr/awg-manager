package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ops"
	"github.com/hoaxisr/awg-manager/internal/tunnel/state"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// expectedHookTTL bounds how long a self-induced NDMS hook expectation
// stays valid. Past it, the token is pruned so a stale expectation can't
// absorb a later, legitimate external edge.
const expectedHookTTL = 15 * time.Second

// bootQuiescenceWindow is how long after we (re)start a NativeWG tunnel we
// treat an incoming conf=disabled as transient NDMS settling rather than a
// stop command. See decideNDMSHook + updateState.
const bootQuiescenceWindow = 20 * time.Second

// PingCheckExecutor is the interface for monitoring operations.
// Satisfied by *pingcheck.Facade.
type PingCheckExecutor interface {
	StartMonitoring(tunnelID, tunnelName string, skipConfigure ...bool)
	StopMonitoring(tunnelID string)
}

// DNSRouteExecutor is the interface for DNS route operations.
type DNSRouteExecutor interface {
	Reconcile(ctx context.Context) error
	OnTunnelDelete(ctx context.Context, tunnelID string) error
}

// StaticRouteExecutor is the interface for static route operations.
type StaticRouteExecutor interface {
	OnTunnelStart(ctx context.Context, tunnelID, tunnelIface string) error
	OnTunnelStop(ctx context.Context, tunnelID string) error
	OnTunnelDelete(ctx context.Context, tunnelID string) error
	Reconcile(ctx context.Context) error
}

// ClientRouteExecutor is the interface for client route operations.
type ClientRouteExecutor interface {
	OnTunnelStart(ctx context.Context, tunnelID string, kernelIface string) error
	OnTunnelStop(ctx context.Context, tunnelID string) error
	OnTunnelDelete(ctx context.Context, tunnelID string) error
}

// Orchestrator centralizes ALL tunnel lifecycle decisions.
// One brain: receives events, decides actions, executes them.
type Orchestrator struct {
	// Decision state (protected by mu)
	mu    sync.Mutex
	state State

	// Per-tunnel execution locks
	tunnelMu sync.Map

	// Expected NDMS hooks — queue of hooks our own actions will trigger.
	// Consumed in HandleEvent to filter self-triggered iflayerchanged events.
	expectedHooks []expectedHook

	// Executors (no decision logic, only execution)
	store    *storage.AWGTunnelStore
	kernelOp ops.Operator
	nwgOp    *nwg.OperatorNativeWG
	stateMgr state.Manager
	wanModel *wan.Model

	// Downstream executors
	pingCheck   PingCheckExecutor
	dnsRoute    DNSRouteExecutor
	staticRoute StaticRouteExecutor
	clientRoute ClientRouteExecutor

	// Event bus for SSE publishing
	bus *events.Bus

	// Logging
	appLog *logging.ScopedLogger

	// clock returns current time; injectable for tests. nil → time.Now.
	clock func() time.Time
}

// New creates a new Orchestrator.
func New(
	store *storage.AWGTunnelStore,
	kernelOp ops.Operator,
	nwgOp *nwg.OperatorNativeWG,
	stateMgr state.Manager,
	wanModel *wan.Model,
	appLogger logging.AppLogger,
) *Orchestrator {
	return &Orchestrator{
		state:    newState(),
		store:    store,
		kernelOp: kernelOp,
		nwgOp:    nwgOp,
		stateMgr: stateMgr,
		wanModel: wanModel,
		appLog:   logging.NewScopedLogger(appLogger, logging.GroupTunnel, logging.SubOrchestrator),
		clock:    time.Now,
	}
}

// SetPingCheck sets the monitoring executor.
func (o *Orchestrator) SetPingCheck(pc PingCheckExecutor) { o.pingCheck = pc }

// SetDNSRoute sets the DNS route executor.
func (o *Orchestrator) SetDNSRoute(dr DNSRouteExecutor) { o.dnsRoute = dr }

// SetStaticRoute sets the static route executor.
func (o *Orchestrator) SetStaticRoute(sr StaticRouteExecutor) { o.staticRoute = sr }

// SetClientRoute sets the client route executor.
func (o *Orchestrator) SetClientRoute(cr ClientRouteExecutor) { o.clientRoute = cr }

// SetEventBus sets the event bus for SSE publishing.
func (o *Orchestrator) SetEventBus(bus *events.Bus) { o.bus = bus }

// SetSupportsASC sets the ASC support flag.
func (o *Orchestrator) SetSupportsASC(fn func() bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.state.supportsASC = fn()
}

// RefreshTunnelState re-reads a tunnel from storage and updates the
// orchestrator's in-memory cache without emitting any actions.
//
// Settings-only mutations (ping-check toggle, name change, ISP interface
// reassignment, etc.) happen directly against the store in the API
// layer. Without this refresh the decide layer keeps making decisions
// off a stale snapshot — e.g. seeing PingCheck.Enabled=true after the
// user disabled it, which produces spurious ActionRemovePingCheck on
// the next lifecycle event and triggers NDMS "interface has no
// assigned profile" warnings.
//
// Runtime-only fields (Running, Monitoring, quiescentUntil)
// live only in the orchestrator's cache, so they are preserved across
// the refresh — reloading them from storage would clobber the action
// layer's view of the world.
func (o *Orchestrator) RefreshTunnelState(tunnelID string) {
	o.mu.Lock()
	defer o.mu.Unlock()

	stored, err := o.store.Get(tunnelID)
	if err != nil {
		return
	}
	fresh := tunnelStateFromStored(stored)
	if cur, ok := o.state.tunnels[tunnelID]; ok {
		fresh.Running = cur.Running
		fresh.Monitoring = cur.Monitoring
		fresh.quiescentUntil = cur.quiescentUntil
	}
	o.state.tunnels[tunnelID] = fresh
}

// LoadState populates the state cache from storage and live operator state.
// Called once at startup before handling any events.
func (o *Orchestrator) LoadState(ctx context.Context) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.state.loadFromStore(o.store)
	o.state.anyWANUpFn = o.wanModel.AnyUp

	// Detect running state for each tunnel
	for _, t := range o.state.tunnels {
		if t.Backend == "nativewg" && o.nwgOp != nil {
			stored, err := o.store.Get(t.ID)
			if err != nil {
				continue
			}
			info := o.nwgOp.GetState(ctx, stored)
			t.Running = info.State == tunnel.StateRunning || info.State == tunnel.StateStarting
		} else if t.Backend != "nativewg" {
			info := o.stateMgr.GetState(ctx, t.ID)
			t.Running = info.State == tunnel.StateRunning
		}

		if t.Running && t.PingCheck != nil && t.PingCheck.Enabled {
			t.Monitoring = true
		}
	}
}

// expectedHook represents an NDMS hook we expect from our own actions.
type expectedHook struct {
	ndmsName  string
	level     string
	expiresAt time.Time
}

// nowFn returns the current time, honouring an injected clock in tests.
func (o *Orchestrator) nowFn() time.Time {
	if o.clock != nil {
		return o.clock()
	}
	return time.Now()
}

// ExpectHook registers an expected NDMS hook (implements tunnel.HookNotifier).
// Called by operators before InterfaceUp/Down. The expectation expires after
// expectedHookTTL so a stale token cannot absorb an unrelated later edge.
func (o *Orchestrator) ExpectHook(ndmsName, level string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.expectedHooks = append(o.expectedHooks, expectedHook{
		ndmsName:  ndmsName,
		level:     level,
		expiresAt: o.nowFn().Add(expectedHookTTL),
	})
}

// consumeExpectedHook checks if an NDMS hook matches a non-expired expected
// one. It first prunes expired expectations, then removes and returns true on
// the first matching live entry.
func (o *Orchestrator) consumeExpectedHook(ndmsName, level string) bool {
	now := o.nowFn()
	kept := o.expectedHooks[:0]
	for _, h := range o.expectedHooks {
		if !now.Before(h.expiresAt) {
			continue
		}
		kept = append(kept, h)
	}
	o.expectedHooks = kept

	for i, h := range o.expectedHooks {
		if h.ndmsName == ndmsName && h.level == level {
			o.expectedHooks = append(o.expectedHooks[:i], o.expectedHooks[i+1:]...)
			return true
		}
	}
	return false
}

// HandleEvent is the single entry point for ALL events.
// Decides what to do, then executes.
func (o *Orchestrator) HandleEvent(ctx context.Context, event Event) error {
	// Filter self-triggered NDMS hooks before decide.
	// Our operators register expected hooks before InterfaceUp/Down.
	if event.Type == EventNDMSHook {
		o.mu.Lock()
		consumed := o.consumeExpectedHook(event.NDMSName, event.Level)
		o.mu.Unlock()
		if consumed {
			return nil
		}
	}

	if event.Now.IsZero() {
		event.Now = o.nowFn()
	}

	// Decide (under lock)
	o.mu.Lock()
	// Ensure tunnel is in cache (covers tunnels created/imported after startup)
	if event.Tunnel != "" {
		o.state.ensureTunnel(event.Tunnel, o.store)
	}
	actions := decide(event, &o.state)
	o.mu.Unlock()

	if len(actions) == 0 {
		return nil
	}

	// Per-tunnel lock for execution
	tunnelID := event.Tunnel
	if tunnelID == "" {
		// Multi-tunnel events (Boot, Reconnect, WAN): group actions per
		// tunnel and run each group under that tunnel's lock so a concurrent
		// single-tunnel NDMS hook for the same tunnel cannot interleave a
		// Stop into the middle of our Start sequence (the boot kill race).
		return o.executeActionsGrouped(ctx, actions)
	}

	// Single-tunnel event: lock that tunnel
	o.lockTunnel(tunnelID)
	defer o.unlockTunnel(tunnelID)
	return o.executeActions(ctx, actions)
}

// lockTunnel acquires the per-tunnel mutex.
func (o *Orchestrator) lockTunnel(tunnelID string) {
	mu, _ := o.tunnelMu.LoadOrStore(tunnelID, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
}

// unlockTunnel releases the per-tunnel mutex.
func (o *Orchestrator) unlockTunnel(tunnelID string) {
	if mu, ok := o.tunnelMu.Load(tunnelID); ok {
		mu.(*sync.Mutex).Unlock()
	}
}

// LockTunnel acquires the per-tunnel mutex and returns an unlock callback.
// Exposed so the service layer can serialize Update/ReplaceConfig against
// orchestrator-driven Stop/Start/Delete on the same tunnel — otherwise the
// two layers held independent mutexes and could interleave NDMS commands.
func (o *Orchestrator) LockTunnel(tunnelID string) func() {
	o.lockTunnel(tunnelID)
	return func() { o.unlockTunnel(tunnelID) }
}

// UnlockTunnel releases the per-tunnel mutex acquired via LockTunnel.
// Provided for symmetric Lock+defer Unlock at call sites that already use
// that pattern with the service-local lockTunnel/unlockTunnel pair.
func (o *Orchestrator) UnlockTunnel(tunnelID string) {
	o.unlockTunnel(tunnelID)
}

// cleanupTunnelLock removes the lock entry for a deleted tunnel.
func (o *Orchestrator) cleanupTunnelLock(tunnelID string) {
	o.tunnelMu.Delete(tunnelID)
}

// executeActions executes a list of actions sequentially.
// Updates state cache after each successful action.
func (o *Orchestrator) executeActions(ctx context.Context, actions []Action) error {
	var firstErr error
	for _, action := range actions {
		if err := o.executeOne(ctx, action); err != nil {
			o.appLog.Warn("execute-action", action.Tunnel, fmt.Sprintf("action type %d failed: %s", action.Type, err.Error()))
			if firstErr == nil {
				firstErr = err
			}
			// Continue for boot/reconnect (best-effort), stop for user actions
			// TODO: refine error strategy in Phase 2 execute implementation
			continue
		}
		o.updateState(action)
	}
	return firstErr
}

// groupContiguousByTunnel splits a flat action list into contiguous runs
// sharing the same Tunnel value, preserving order. Boot/Reconnect/WANUp emit
// each tunnel's actions contiguously, so those events are fully serialized
// per tunnel. decideWANDown's non-ASC immediate-failover can emit a tunnel's
// Suspend and failover-Start in separate phases (non-contiguous) → that
// tunnel gets two groups and its lock is taken twice with a gap between;
// still deadlock-free and correct in execution order, just not gap-free
// against a concurrent hook. Tightening decideWANDown's ordering is tracked
// separately (out of scope for the boot-race fix).
func groupContiguousByTunnel(actions []Action) [][]Action {
	var groups [][]Action
	i := 0
	for i < len(actions) {
		tid := actions[i].Tunnel
		j := i
		for j < len(actions) && actions[j].Tunnel == tid {
			j++
		}
		groups = append(groups, actions[i:j])
		i = j
	}
	return groups
}

// executeActionsGrouped runs a multi-tunnel action list with per-tunnel
// serialization. Each tunnel's contiguous group runs under that tunnel's
// per-tunnel lock, acquired and released per group — never holding two
// tunnel locks at once, so there is no lock-ordering deadlock against
// concurrent single-tunnel hook events. Tunnel-less groups (Tunnel=="")
// run unlocked.
func (o *Orchestrator) executeActionsGrouped(ctx context.Context, actions []Action) error {
	var firstErr error
	for _, group := range groupContiguousByTunnel(actions) {
		if err := o.executeGroup(ctx, group); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// executeGroup runs one same-tunnel action group. The per-tunnel lock is
// released via defer (iteration-scoped here, matching the single-tunnel
// path in HandleEvent) so a panic in executeActions cannot leak the lock.
func (o *Orchestrator) executeGroup(ctx context.Context, group []Action) error {
	tid := group[0].Tunnel
	if tid == "" {
		return o.executeActions(ctx, group)
	}
	o.lockTunnel(tid)
	defer o.unlockTunnel(tid)
	return o.executeActions(ctx, group)
}

// executeOne is implemented in execute.go.

// updateState updates the internal state cache after a successful action.
func (o *Orchestrator) updateState(action Action) {
	o.mu.Lock()
	defer o.mu.Unlock()

	t := o.state.tunnels[action.Tunnel]
	if t == nil {
		return
	}

	switch action.Type {
	case ActionColdStartKernel, ActionStartNativeWG, ActionReconcileNativeWG, ActionReconcileKernel, ActionResumeKernel:
		t.Running = true
		t.quiescentUntil = o.nowFn().Add(bootQuiescenceWindow)
		// Refresh ActiveWAN from store. Execute layer persists the resolved
		// WAN; we mirror it into the in-memory cache so decideWANDown can
		// match correctly via affectedByWANDown.
		if stored, err := o.store.Get(action.Tunnel); err == nil {
			t.ActiveWAN = stored.ActiveWAN
		}
	case ActionStopKernel, ActionStopNativeWG:
		t.Running = false
		t.Monitoring = false
		t.ActiveWAN = ""
	case ActionSuspendProxy, ActionSuspendKernel:
		// Keep t.Running=true so the next WANUp picks Resume/Reconcile,
		// not a fresh ColdStart. Keep ActiveWAN so a duplicate WANDown
		// for the same iface does not re-trigger failover.
	case ActionStartMonitoring:
		t.Monitoring = true
	case ActionStopMonitoring:
		t.Monitoring = false
	case ActionDeleteKernel, ActionDeleteNativeWG:
		delete(o.state.tunnels, action.Tunnel)
	}

	// Publish SSE event
	if o.bus != nil && t != nil {
		switch action.Type {
		case ActionColdStartKernel, ActionStartNativeWG, ActionReconcileNativeWG, ActionReconcileKernel, ActionResumeKernel:
			// tunnel:state is still consumed internally by
			// connectivity.Monitor (listens for "running" to trigger an
			// immediate check). Keep it until that dependency is
			// migrated. Frontend no longer listens.
			o.bus.Publish("tunnel:state", events.TunnelStateEvent{
				ID: t.ID, Name: t.Name, State: "running", Backend: t.Backend,
			})
			publishInvalidatedBus(o.bus, "tunnels", "state-running")
		case ActionStopKernel, ActionStopNativeWG, ActionSuspendProxy, ActionSuspendKernel:
			o.bus.Publish("tunnel:state", events.TunnelStateEvent{
				ID: t.ID, Name: t.Name, State: "stopped", Backend: t.Backend,
			})
			publishInvalidatedBus(o.bus, "tunnels", "state-stopped")
		case ActionDeleteKernel, ActionDeleteNativeWG:
			// tunnel:deleted remains as a no-op SSE for any legacy
			// subscriber; the frontend handler is removed so nobody
			// reacts. Future cleanup can drop this publish.
			o.bus.Publish("tunnel:deleted", events.TunnelDeletedEvent{ID: action.Tunnel})
			publishInvalidatedBus(o.bus, "tunnels", "deleted")
		}
	}
}

// QuiescentUntil returns the tunnel's current boot-quiescence deadline (the
// time until which a just-(re)started tunnel is considered "coming up"), or
// the zero time if the tunnel is unknown or no bring-up was attempted this
// session. Pure read — the API status layer uses it to display
// "pending/starting" instead of "broken" while a NativeWG tunnel is still
// being brought up. Does not mutate any state.
func (o *Orchestrator) QuiescentUntil(tunnelID string) time.Time {
	o.mu.Lock()
	defer o.mu.Unlock()
	if t, ok := o.state.tunnels[tunnelID]; ok {
		return t.quiescentUntil
	}
	return time.Time{}
}

// publishInvalidatedBus posts a resource:invalidated hint. Duplicated
// here (from internal/api.publishInvalidated) to avoid an import cycle
// between the orchestrator and the api package.
//
// TODO(tech-debt): consolidate publishInvalidatedBus helpers into
// internal/events once the import-cycle with internal/api is resolved.
// Currently duplicated in internal/orchestrator and internal/pingcheck
// because those packages cannot import internal/api.
func publishInvalidatedBus(bus *events.Bus, resource, reason string) {
	if bus == nil {
		return
	}
	bus.Publish("resource:invalidated", events.ResourceInvalidatedEvent{
		Resource: resource,
		Reason:   reason,
	})
}
