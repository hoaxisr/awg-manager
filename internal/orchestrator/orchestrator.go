package orchestrator

import (
	"context"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ops"
	"github.com/hoaxisr/awg-manager/internal/tunnel/state"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

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
	store      *storage.AWGTunnelStore
	kernelOp   ops.Operator
	nwgOp      *nwg.OperatorNativeWG
	stateMgr   state.Manager
	wanModel   *wan.Model
	ndmsClient ndms.Client

	// Downstream executors
	pingCheck   PingCheckExecutor
	dnsRoute    DNSRouteExecutor
	staticRoute StaticRouteExecutor
	clientRoute ClientRouteExecutor

	// Event bus for SSE publishing
	bus *events.Bus

	// Logging
	log    *logger.Logger
	appLog *logging.ScopedLogger
}

// New creates a new Orchestrator.
func New(
	store *storage.AWGTunnelStore,
	kernelOp ops.Operator,
	nwgOp *nwg.OperatorNativeWG,
	stateMgr state.Manager,
	wanModel *wan.Model,
	ndmsClient ndms.Client,
	log *logger.Logger,
	appLogger logging.AppLogger,
) *Orchestrator {
	return &Orchestrator{
		state:      newState(),
		store:      store,
		kernelOp:   kernelOp,
		nwgOp:      nwgOp,
		stateMgr:   stateMgr,
		wanModel:   wanModel,
		ndmsClient: ndmsClient,
		log:        log,
		appLog:     logging.NewScopedLogger(appLogger, logging.GroupTunnel, logging.SubLifecycle),
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
	ndmsName string
	level    string
}

// ExpectHook registers an expected NDMS hook (implements tunnel.HookNotifier).
// Called by operators before InterfaceUp/Down.
func (o *Orchestrator) ExpectHook(ndmsName, level string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.expectedHooks = append(o.expectedHooks, expectedHook{ndmsName, level})
}

// consumeExpectedHook checks if an NDMS hook matches an expected one.
// If yes, removes it from the queue and returns true.
func (o *Orchestrator) consumeExpectedHook(ndmsName, level string) bool {
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
		// Multi-tunnel events (Boot, Reconnect, WAN): execute inline
		return o.executeActions(ctx, actions)
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
			o.logWarn(action.Tunnel, "execute %d failed: %s", action.Type, err.Error())
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
	case ActionColdStartKernel, ActionStartNativeWG, ActionReconcileKernel, ActionResumeKernel:
		t.Running = true
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
		case ActionColdStartKernel, ActionStartNativeWG, ActionReconcileKernel, ActionResumeKernel:
			o.bus.Publish("tunnel:state", events.TunnelStateEvent{
				ID: t.ID, Name: t.Name, State: "running", Backend: t.Backend,
			})
		case ActionStopKernel, ActionStopNativeWG, ActionSuspendProxy, ActionSuspendKernel:
			o.bus.Publish("tunnel:state", events.TunnelStateEvent{
				ID: t.ID, Name: t.Name, State: "stopped", Backend: t.Backend,
			})
		case ActionDeleteKernel, ActionDeleteNativeWG:
			o.bus.Publish("tunnel:deleted", events.TunnelDeletedEvent{ID: action.Tunnel})
		}
	}
}

// logWarn logs a warning.
func (o *Orchestrator) logWarn(target, format string, args ...interface{}) {
	if o.log != nil {
		o.log.Warnf("[orchestrator] %s: "+format, append([]interface{}{target}, args...)...)
	}
}

// logInfo logs an info message.
func (o *Orchestrator) logInfo(target, format string, args ...interface{}) {
	if o.log != nil {
		o.log.Infof("[orchestrator] %s: "+format, append([]interface{}{target}, args...)...)
	}
}
