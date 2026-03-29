package lifecycle

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ops"
	"github.com/hoaxisr/awg-manager/internal/tunnel/state"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// TunnelStore abstracts tunnel storage for testability.
// Implemented by storage.AWGTunnelStore.
type TunnelStore interface {
	Get(id string) (*storage.AWGTunnel, error)
	List() ([]storage.AWGTunnel, error)
	Save(tunnel *storage.AWGTunnel) error
	ClearRuntimeState(id string)
}

// Executor handles complex lifecycle operations that require config file
// generation, WAN resolution, hook notification, and state persistence.
// Implemented by service.ServiceImpl to avoid circular dependencies.
//
// Manager calls operator directly for simple actions (Suspend, Resume,
// Reconfig, Reconnect). Executor is only for multi-step Start/Stop sequences
// that involve 10+ steps with config files, WAN resolution, hooks, etc.
type Executor interface {
	// ColdStartKernel creates a tunnel from scratch — resolves WAN, writes config,
	// calls operator.ColdStart. Used for BootReady, NotCreated, Broken.
	ColdStartKernel(ctx context.Context, tunnelID string) error

	// StartKernel brings up an existing interface — resolves WAN,
	// calls operator.Start (light). Used for Disabled (after Stop), Dead.
	StartKernel(ctx context.Context, tunnelID string) error

	// StopKernel brings down a tunnel — calls operator.Stop + clears runtime state.
	StopKernel(ctx context.Context, tunnelID string) error
}

// Manager is the single point of lifecycle decision-making for kernel tunnels.
//
// All events (boot, WAN, NDMS hooks, PingCheck, API) come here.
// Manager determines current state, consults the decision matrix (Decide),
// and executes the appropriate action via Operator or Executor.
//
// Operator: simple atomic actions (Suspend, Resume, ApplyConfig, RestoreEndpointTracking).
// Executor: complex multi-step sequences (Start, Stop) that need config generation,
// WAN resolution, hook notification, and state persistence.
type Manager struct {
	operator ops.Operator
	store    TunnelStore
	state    state.Manager
	wanModel *wan.Model
	log      *logger.Logger
	appLog   *logging.ScopedLogger

	// executor is set after construction to break circular init with service.
	executor Executor

	// bootInProgress blocks WAN events until HandleBoot completes.
	// 32-bit aligned for atomic ops on MIPS.
	bootInProgress int32

	// Per-tunnel locks — shared with service for CRUD/lifecycle coordination.
	tunnelMu *sync.Map

	// operatingOn: while true for a tunnelID, external hooks are ignored.
	operatingOn   map[string]bool
	operatingOnMu sync.Mutex

	// onTunnelRunning is called when a tunnel enters Running state.
	// Used by PingCheck to start monitoring. Set via SetOnTunnelRunning.
	onTunnelRunning func(tunnelID, tunnelName string)
}

// NewManager creates a new lifecycle Manager.
// Call SetExecutor before use.
func NewManager(
	operator ops.Operator,
	store TunnelStore,
	stateMgr state.Manager,
	wanModel *wan.Model,
	tunnelLocks *sync.Map,
	log *logger.Logger,
	appLogger logging.AppLogger,
) *Manager {
	return &Manager{
		operator:    operator,
		store:       store,
		state:       stateMgr,
		wanModel:    wanModel,
		tunnelMu:    tunnelLocks,
		log:         log,
		appLog:      logging.NewScopedLogger(appLogger, logging.GroupTunnel, logging.SubLifecycle),
		operatingOn: make(map[string]bool),
	}
}

// SetExecutor sets the complex operation executor (service layer).
// Must be called before any Handle methods.
func (m *Manager) SetExecutor(e Executor) {
	m.executor = e
}

// SetOnTunnelRunning sets the callback for when a tunnel enters Running state.
func (m *Manager) SetOnTunnelRunning(fn func(tunnelID, tunnelName string)) {
	m.onTunnelRunning = fn
}

// notifyTunnelRunning fires the OnTunnelRunning callback if set.
func (m *Manager) notifyTunnelRunning(tunnelID string) {
	if m.onTunnelRunning == nil {
		return
	}
	stored, err := m.store.Get(tunnelID)
	if err != nil {
		return
	}
	m.onTunnelRunning(tunnelID, stored.Name)
}

// IsBootInProgress returns true while HandleBoot is running.
func (m *Manager) IsBootInProgress() bool {
	return atomic.LoadInt32(&m.bootInProgress) == 1
}

// ─────────────────────────────────────────────────────────────
// State determination — own matrix, NOT mapping from tunnel.State
// ─────────────────────────────────────────────────────────────

// determineState maps raw system data to lifecycle.TunnelState.
//
// Uses StateInfo raw fields (OpkgTunExists, ProcessRunning, InterfaceUp)
// + stored PingCheck dead flag + sysfs device presence.
// Does NOT use tunnel.State from the old state matrix.
func (m *Manager) determineState(ctx context.Context, tunnelID string, stored *storage.AWGTunnel) TunnelState {
	info := m.state.GetState(ctx, tunnelID)

	// Inconsistent state detection.
	if determineStateBroken(info) {
		return StateBroken
	}

	// No OpkgTun in NDMS → never created.
	if !info.OpkgTunExists {
		return StateNotCreated
	}

	// ProcessRunning for kernel backend = amneziawg interface exists in sysfs
	// (KernelBackend.IsRunning checks type via ip -d link show).
	if info.ProcessRunning && info.InterfaceUp {
		return StateRunning
	}
	if info.ProcessRunning && !info.InterfaceUp {
		// amneziawg exists but link is down.
		// Distinguish Suspended (WAN down, Enabled=true) from Disabled (user Stop, Enabled=false).
		// After our Stop: ip link set down + InterfaceDown → device stays, Enabled=false.
		// After Suspend: ip link set down only → device stays, Enabled=true.
		if !stored.Enabled {
			return StateDisabled
		}
		return StateSuspended
	}

	// OpkgTun exists but no amneziawg process (ProcessRunning=false).
	// Check if a kernel device exists at all (tun type from NDMS boot).
	names := tunnel.NewNames(tunnelID)
	if deviceExists(names.IfaceName) {
		// NDMS recreated a tun-type device after router reboot.
		return StateBootReady
	}

	// OpkgTun in NDMS, no kernel device at all.
	// First start (never launched) or after Delete left OpkgTun.
	return StateDisabled
}

// determineStateBroken checks for inconsistent states not covered above.
// Called when raw StateInfo has unexpected combinations.
func determineStateBroken(info tunnel.StateInfo) bool {
	// Process running but OpkgTun missing from NDMS.
	if info.ProcessRunning && !info.OpkgTunExists {
		return true
	}
	return false
}

// deviceExists checks if a network device exists in sysfs.
func deviceExists(ifaceName string) bool {
	_, err := os.Stat("/sys/class/net/" + ifaceName)
	return err == nil
}

// ─────────────────────────────────────────────────────────────
// Handle methods — single entry point for each event source
// ─────────────────────────────────────────────────────────────

// HandleBoot initializes tunnels at router boot.
// Called once from main.go after WAN model is populated.
func (m *Manager) HandleBoot(ctx context.Context) {
	atomic.StoreInt32(&m.bootInProgress, 1)
	defer atomic.StoreInt32(&m.bootInProgress, 0)

	tunnels, err := m.store.List()
	if err != nil {
		m.logWarn("boot", "list", "Failed to list tunnels: "+err.Error())
		return
	}

	for _, t := range tunnels {
		// Skip NativeWG — separate lifecycle.
		if t.Backend == "nativewg" {
			continue
		}

		st := m.determineState(ctx, t.ID, &t)
		action := Decide(EventBoot, st, EventContext{
			StoredEnabled: t.Enabled,
		})

		m.log.Infof("[boot] %s: state=%s action=%s enabled=%v", t.ID, st, action, t.Enabled)

		if action == ActionNone {
			if st == StateRunning {
				m.appLog.Info("boot", t.ID, "Already running, skipping")
			}
			continue
		}

		// Check for shutdown between tunnels.
		if ctx.Err() != nil {
			m.logWarn("boot", "system", "Boot interrupted by shutdown")
			return
		}

		m.BeginOperation(t.ID)
		m.lockTunnel(t.ID)
		err := m.executeAction(ctx, t.ID, action, "boot")
		m.unlockTunnel(t.ID)
		m.EndOperation(t.ID)

		if err == nil {
			m.notifyTunnelRunning(t.ID)
		}
	}

	m.appLog.Info("boot", "system", "Boot initialization complete")
}

// HandleDaemonRestart handles tunnel reconnection after daemon restart (upgrade).
// Kernel state (interfaces, routes, iptables) survives syscall.Exec.
// Only in-memory operator maps need restoration.
// Calls OnTunnelRunning for each tunnel that ends up running (for PingCheck monitoring).
func (m *Manager) HandleDaemonRestart(ctx context.Context) {
	tunnels, err := m.store.List()
	if err != nil {
		m.logWarn("daemon_restart", "list", "Failed to list tunnels: "+err.Error())
		return
	}

	reconnected := 0

	for _, t := range tunnels {
		if t.Backend == "nativewg" {
			continue
		}

		st := m.determineState(ctx, t.ID, &t)
		action := Decide(EventDaemonRestart, st, EventContext{
			StoredEnabled: t.Enabled,
			HasPeer:       t.Peer.Endpoint != "",
		})

		m.log.Infof("[daemon_restart] %s: state=%s action=%s enabled=%v", t.ID, st, action, t.Enabled)

		if action == ActionNone {
			continue
		}

		if ctx.Err() != nil {
			return
		}

		m.BeginOperation(t.ID)
		m.lockTunnel(t.ID)
		err := m.executeAction(ctx, t.ID, action, "daemon_restart")
		m.unlockTunnel(t.ID)
		m.EndOperation(t.ID)

		if err == nil {
			reconnected++
			m.notifyTunnelRunning(t.ID)
		}
	}

	// Clean up stale ActiveWAN/StartedAt for dead tunnels.
	for _, t := range tunnels {
		if t.Backend == "nativewg" || (t.ActiveWAN == "" && t.StartedAt == "") {
			continue
		}
		info := m.state.GetState(ctx, t.ID)
		if !info.ProcessRunning {
			m.logInfo("daemon_restart", t.ID, "Clearing stale ActiveWAN/StartedAt (process dead)")
			m.store.ClearRuntimeState(t.ID)
		}
	}

	m.appLog.Info("daemon_restart", "system", fmt.Sprintf("Daemon restart: %d kernel tunnels reconnected", reconnected))
}

// HandleWANUp is called when a WAN interface comes up.
func (m *Manager) HandleWANUp(ctx context.Context, iface string) {
	if m.IsBootInProgress() {
		m.logInfo("wan", "event", fmt.Sprintf("WAN up %s deferred — boot in progress", iface))
		return
	}

	m.logInfo("wan", "event", fmt.Sprintf("WAN up: %s", iface))

	tunnels, err := m.store.List()
	if err != nil {
		m.logWarn("wan_up", "list", "Failed to list tunnels: "+err.Error())
		return
	}

	// Classify tunnels into direct and chained.
	// Direct tunnels must start first so chained tunnels can resolve parent's ActiveWAN.
	type tunnelAction struct {
		t      storage.AWGTunnel
		action Action
	}
	var direct, chained []tunnelAction

	for _, t := range tunnels {
		if !t.Enabled || t.Backend == "nativewg" {
			continue
		}

		// ISP match filtering.
		if t.ISPInterface != "" {
			if tunnel.IsTunnelRoute(t.ISPInterface) {
				// Chained tunnel — deferred to phase 2.
				st := m.determineState(ctx, t.ID, &t)
				action := Decide(EventWANUp, st, EventContext{
					StoredEnabled: t.Enabled,
					WANInterface:  iface,
				})
				if action != ActionNone {
					chained = append(chained, tunnelAction{t, action})
				}
				continue
			}
			if t.ISPInterface != iface {
				continue // explicit ISP, wrong WAN
			}
		}

		st := m.determineState(ctx, t.ID, &t)
		action := Decide(EventWANUp, st, EventContext{
			StoredEnabled: t.Enabled,
			WANInterface:  iface,
		})

		if action == ActionNone {
			continue
		}

		direct = append(direct, tunnelAction{t, action})
	}

	// Phase 1: Start direct tunnels in parallel, wait for all to complete.
	// This populates ActiveWAN so chained tunnels can resolve parent.
	var wg sync.WaitGroup
	for _, ta := range direct {
		ta := ta
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.log.Infof("[wan_up] %s: action=%s wan=%s", ta.t.ID, ta.action, iface)

			m.BeginOperation(ta.t.ID)
			defer m.EndOperation(ta.t.ID)
			m.lockTunnel(ta.t.ID)
			defer m.unlockTunnel(ta.t.ID)

			execCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			if err := m.executeAction(execCtx, ta.t.ID, ta.action, "wan_up"); err == nil {
				m.notifyTunnelRunning(ta.t.ID)
			}
		}()
	}
	wg.Wait()

	// Phase 2: Start chained tunnels (parent's ActiveWAN is now populated).
	for _, ta := range chained {
		parentID := tunnel.TunnelRouteID(ta.t.ISPInterface)
		parentStored, err := m.store.Get(parentID)
		if err != nil {
			continue
		}
		// Check if parent's WAN matches this event.
		if parentStored.ActiveWAN != iface {
			continue
		}

		ta := ta
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.log.Infof("[wan_up] %s: chained action=%s wan=%s parent=%s", ta.t.ID, ta.action, iface, parentID)

			m.BeginOperation(ta.t.ID)
			defer m.EndOperation(ta.t.ID)
			m.lockTunnel(ta.t.ID)
			defer m.unlockTunnel(ta.t.ID)

			execCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			if err := m.executeAction(execCtx, ta.t.ID, ta.action, "wan_up"); err == nil {
				m.notifyTunnelRunning(ta.t.ID)
			}
		}()
	}
	wg.Wait()
}

// HandleWANDown is called when a WAN interface goes down.
func (m *Manager) HandleWANDown(ctx context.Context, iface string) {
	if m.IsBootInProgress() {
		m.logInfo("wan", "event", fmt.Sprintf("WAN down %s deferred — boot in progress", iface))
		return
	}

	m.logInfo("wan", "event", fmt.Sprintf("WAN down: %s", iface))

	var wg sync.WaitGroup

	tunnels, err := m.store.List()
	if err != nil {
		m.logWarn("wan_down", "list", "Failed to list tunnels: "+err.Error())
		return
	}

	for _, t := range tunnels {
		if t.Backend == "nativewg" {
			continue
		}

		// Only affect tunnels bound to this WAN.
		if iface != "" && t.ActiveWAN != iface {
			continue
		}
		if iface == "" && t.ActiveWAN == "" {
			continue
		}

		st := m.determineState(ctx, t.ID, &t)

		// Check if another real WAN is available (for auto-mode SwitchRoute).
		// Empty iface = "all WANs down" (boot with no gateway) — never failover.
		hasOtherWAN := false
		if iface != "" && t.ISPInterface == "" {
			if _, ok := m.wanModel.PreferredUp(); ok {
				hasOtherWAN = true
			}
		}

		action := Decide(EventWANDown, st, EventContext{
			StoredEnabled: t.Enabled,
			WANInterface:  iface,
			HasOtherWAN:   hasOtherWAN,
		})

		if action == ActionNone {
			continue
		}

		m.log.Infof("[wan_down] %s: state=%s action=%s wan=%s", t.ID, st, action, iface)

		t := t
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.BeginOperation(t.ID)
			defer m.EndOperation(t.ID)
			m.lockTunnel(t.ID)
			defer m.unlockTunnel(t.ID)

			execCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()

			m.executeAction(execCtx, t.ID, action, "wan_down")
		}()
	}
	wg.Wait()
}

// HandleUserToggle is called by NDMS hook when user toggles interface in router UI.
func (m *Manager) HandleUserToggle(ctx context.Context, tunnelID string, level string) {
	if m.IsOperating(tunnelID) {
		m.logInfo("toggle", tunnelID, fmt.Sprintf("Skipping hook during operation (level=%s)", level))
		return
	}

	stored, err := m.store.Get(tunnelID)
	if err != nil {
		return
	}

	var event Event
	switch level {
	case "running":
		event = EventUserEnable
	case "disabled":
		event = EventUserDisable
	default:
		return
	}

	st := m.determineState(ctx, tunnelID, stored)
	action := Decide(event, st, EventContext{
		StoredEnabled: stored.Enabled,
	})

	if action == ActionNone {
		return
	}

	m.log.Infof("[toggle] %s: level=%s state=%s action=%s", tunnelID, level, st, action)
	m.appLog.Info("toggle", tunnelID, fmt.Sprintf("User toggled %s, action: %s", level, action))

	m.BeginOperation(tunnelID)
	defer m.EndOperation(tunnelID)
	m.lockTunnel(tunnelID)
	defer m.unlockTunnel(tunnelID)

	err = m.executeAction(ctx, tunnelID, action, "toggle")

	// Notify PingCheck if tunnel is now running.
	if err == nil && event == EventUserEnable {
		m.notifyTunnelRunning(tunnelID)
	}

	// Persist enabled state change.
	if fresh, err := m.store.Get(tunnelID); err == nil {
		switch event {
		case EventUserEnable:
			fresh.Enabled = true
		case EventUserDisable:
			fresh.Enabled = false
		}
		_ = m.store.Save(fresh)
	}
}

// HandleAPIStart is called when user presses Start in our UI.
func (m *Manager) HandleAPIStart(ctx context.Context, tunnelID string) error {
	stored, err := m.store.Get(tunnelID)
	if err != nil {
		return tunnel.ErrNotFound
	}

	st := m.determineState(ctx, tunnelID, stored)
	action := Decide(EventAPIStart, st, EventContext{
		StoredEnabled: true, // API start implies intent to enable
	})

	m.log.Infof("[api_start] %s: state=%s action=%s", tunnelID, st, action)

	if action == ActionNone {
		return tunnel.ErrAlreadyRunning
	}

	m.BeginOperation(tunnelID)
	defer m.EndOperation(tunnelID)
	m.lockTunnel(tunnelID)
	defer m.unlockTunnel(tunnelID)

	if err := m.executeAction(ctx, tunnelID, action, "api_start"); err != nil {
		return err
	}

	m.notifyTunnelRunning(tunnelID)

	// Persist Enabled=true (spec: "API Start → Enabled=true").
	if fresh, err := m.store.Get(tunnelID); err == nil && !fresh.Enabled {
		fresh.Enabled = true
		_ = m.store.Save(fresh)
	}
	return nil
}

// HandleAPIStop is called when user presses Stop in our UI.
func (m *Manager) HandleAPIStop(ctx context.Context, tunnelID string) error {
	stored, err := m.store.Get(tunnelID)
	if err != nil {
		return tunnel.ErrNotFound
	}

	st := m.determineState(ctx, tunnelID, stored)
	action := Decide(EventAPIStop, st, EventContext{
		StoredEnabled: stored.Enabled,
	})

	m.log.Infof("[api_stop] %s: state=%s action=%s", tunnelID, st, action)

	if action == ActionNone {
		return tunnel.ErrNotRunning
	}

	m.BeginOperation(tunnelID)
	defer m.EndOperation(tunnelID)
	m.lockTunnel(tunnelID)
	defer m.unlockTunnel(tunnelID)

	if err := m.executeAction(ctx, tunnelID, action, "api_stop"); err != nil {
		return err
	}

	// Persist Enabled=false (spec: "API Stop → Enabled=false").
	if fresh, err := m.store.Get(tunnelID); err == nil && fresh.Enabled {
		fresh.Enabled = false
		_ = m.store.Save(fresh)
	}
	return nil
}

// HandleAPIRestart is called when user presses Restart in our UI.
// Brings tunnel to Running regardless of current state.
func (m *Manager) HandleAPIRestart(ctx context.Context, tunnelID string) error {
	stored, err := m.store.Get(tunnelID)
	if err != nil {
		return tunnel.ErrNotFound
	}

	st := m.determineState(ctx, tunnelID, stored)
	action := Decide(EventAPIRestart, st, EventContext{
		StoredEnabled: true,
	})

	m.log.Infof("[api_restart] %s: state=%s action=%s", tunnelID, st, action)

	m.BeginOperation(tunnelID)
	defer m.EndOperation(tunnelID)
	m.lockTunnel(tunnelID)
	defer m.unlockTunnel(tunnelID)

	if err := m.executeAction(ctx, tunnelID, action, "api_restart"); err != nil {
		return err
	}

	m.notifyTunnelRunning(tunnelID)

	// Persist Enabled=true (restart implies intent to run).
	if fresh, err := m.store.Get(tunnelID); err == nil && !fresh.Enabled {
		fresh.Enabled = true
		_ = m.store.Save(fresh)
	}
	return nil
}


// ─────────────────────────────────────────────────────────────
// Action dispatch
// ─────────────────────────────────────────────────────────────

// executeAction dispatches an Action to the correct operator or executor function.
func (m *Manager) executeAction(ctx context.Context, tunnelID string, action Action, source string) error {
	switch action {
	case ActionNone:
		return nil

	case ActionColdStart:
		// ColdStart: full creation — ip link del + ip link add amneziawg + full config.
		m.appLog.Info(source, tunnelID, "ColdStart: creating amneziawg interface")
		if err := m.executor.ColdStartKernel(ctx, tunnelID); err != nil {
			m.logWarn(source, tunnelID, "ColdStart failed: "+err.Error())
			m.appLog.Warn(source, tunnelID, "ColdStart failed: "+err.Error())
			return err
		}
		m.appLog.Info(source, tunnelID, "Tunnel started (cold)")
		return nil

	case ActionStart:
		// Start: light — bring up existing amneziawg interface.
		m.appLog.Info(source, tunnelID, "Starting tunnel (light)")
		if err := m.executor.StartKernel(ctx, tunnelID); err != nil {
			m.logWarn(source, tunnelID, "Start failed: "+err.Error())
			m.appLog.Warn(source, tunnelID, "Start failed: "+err.Error())
			return err
		}
		m.appLog.Info(source, tunnelID, "Tunnel started")
		return nil

	case ActionStop:
		m.appLog.Info(source, tunnelID, "Stopping tunnel")
		if err := m.executor.StopKernel(ctx, tunnelID); err != nil {
			m.logWarn(source, tunnelID, "Stop failed: "+err.Error())
			m.appLog.Warn(source, tunnelID, "Stop failed: "+err.Error())
			return err
		}
		m.appLog.Info(source, tunnelID, "Tunnel stopped")
		return nil

	case ActionSuspend:
		// Suspend: just ip link set down. NDMS handles failover.
		// ActiveWAN and StartedAt stay — tunnel is paused, not stopped.
		m.appLog.Info(source, tunnelID, "Suspending tunnel (link down)")
		if err := m.operator.Suspend(ctx, tunnelID); err != nil {
			m.logWarn(source, tunnelID, "Suspend failed: "+err.Error())
			return err
		}
		return nil

	case ActionResume:
		// Resume: just ip link set up. NDMS restores routing.
		m.appLog.Info(source, tunnelID, "Resuming tunnel (link up)")
		if err := m.operator.Resume(ctx, tunnelID); err != nil {
			m.logWarn(source, tunnelID, "Resume failed: "+err.Error())
			return err
		}
		return nil

	case ActionReconfig:
		// Reconfig: re-apply WG config without recreating interface.
		names := tunnel.NewNames(tunnelID)
		m.appLog.Info(source, tunnelID, "Reconfiguring WireGuard")
		if err := m.operator.ApplyConfig(ctx, tunnelID, names.ConfPath); err != nil {
			m.logWarn(source, tunnelID, "Reconfig failed: "+err.Error())
			return err
		}
		return nil

	case ActionReconnect:
		// Reconnect: restore in-memory endpoint route tracking after daemon restart.
		stored, err := m.store.Get(tunnelID)
		if err != nil {
			return nil
		}
		m.appLog.Info(source, tunnelID, "Reconnecting (restore tracking)")
		isp := stored.ActiveWAN
		if _, err := m.operator.RestoreEndpointTracking(ctx, tunnelID, stored.Peer.Endpoint, isp); err != nil {
			m.logWarn(source, tunnelID, "Reconnect failed: "+err.Error())
			return err
		}
		return nil

	case ActionSwitchRoute:
		// SwitchRoute: WAN changed, re-route endpoint through new WAN.
		// Lightweight: only endpoint route changes, interface stays up.
		// NOTE: Only used for auto-mode tunnels (ISPInterface="").
		// Chained tunnels (ISPInterface="tunnel:xxx") never get SwitchRoute —
		// HasOtherWAN is only set when ISPInterface is empty in HandleWANDown.
		stored, err := m.store.Get(tunnelID)
		if err != nil {
			return nil
		}
		newWAN, ok := m.wanModel.PreferredUp()
		if !ok {
			m.logWarn(source, tunnelID, "SwitchRoute: no WAN available")
			return nil
		}
		m.appLog.Info(source, tunnelID, fmt.Sprintf("Switching route: %s → %s", stored.ActiveWAN, newWAN))

		// Clean old endpoint route.
		_ = m.operator.CleanupEndpointRoute(ctx, tunnelID)

		// Setup new endpoint route through the new WAN.
		// For non-tunnel routes, kernelDevice = WAN interface name.
		if stored.Peer.Endpoint != "" {
			if _, err := m.operator.SetupEndpointRoute(ctx, tunnelID, stored.Peer.Endpoint, newWAN, newWAN); err != nil {
				m.logWarn(source, tunnelID, "SwitchRoute: setup failed: "+err.Error())
				// Non-fatal: tunnel still works, just routing may be suboptimal.
			}
		}

		// Update stored ActiveWAN.
		stored.ActiveWAN = newWAN
		_ = m.store.Save(stored)

		m.appLog.Info(source, tunnelID, "Route switched to "+newWAN)
		return nil

	case ActionRestart:
		// Restart: Stop if alive, then Start.
		m.appLog.Info(source, tunnelID, "Restarting tunnel")
		// Stop (ignore errors — tunnel may not be running).
		_ = m.executor.StopKernel(ctx, tunnelID)
		// Start.
		if err := m.executor.StartKernel(ctx, tunnelID); err != nil {
			m.logWarn(source, tunnelID, "Restart start failed: "+err.Error())
			m.appLog.Warn(source, tunnelID, "Restart failed: "+err.Error())
			return err
		}
		m.appLog.Info(source, tunnelID, "Tunnel restarted")
		return nil
	}

	m.logWarn(source, tunnelID, fmt.Sprintf("Unknown action: %s", action))
	return nil
}

// ─────────────────────────────────────────────────────────────
// Locking
// ─────────────────────────────────────────────────────────────

func (m *Manager) lockTunnel(tunnelID string) {
	mu, _ := m.tunnelMu.LoadOrStore(tunnelID, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
}

func (m *Manager) unlockTunnel(tunnelID string) {
	if mu, ok := m.tunnelMu.Load(tunnelID); ok {
		mu.(*sync.Mutex).Unlock()
	}
}

// ─────────────────────────────────────────────────────────────
// Operation suppress
// ─────────────────────────────────────────────────────────────

// BeginOperation marks a tunnel as being modified. Exported for service layer.
func (m *Manager) BeginOperation(tunnelID string) {
	m.operatingOnMu.Lock()
	m.operatingOn[tunnelID] = true
	m.operatingOnMu.Unlock()
}

// EndOperation clears the operation flag. Exported for service layer.
func (m *Manager) EndOperation(tunnelID string) {
	m.operatingOnMu.Lock()
	delete(m.operatingOn, tunnelID)
	m.operatingOnMu.Unlock()
}

// IsOperating checks the operation flag. Exported for service layer.
func (m *Manager) IsOperating(tunnelID string) bool {
	m.operatingOnMu.Lock()
	defer m.operatingOnMu.Unlock()
	return m.operatingOn[tunnelID]
}

// ─────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────

// clearActiveWAN clears persisted ActiveWAN/StartedAt after Suspend.
func (m *Manager) clearActiveWAN(tunnelID string) {
	m.store.ClearRuntimeState(tunnelID)
}

func (m *Manager) logInfo(action, target, message string) {
	if m.log != nil {
		m.log.Infof("[%s] %s: %s", action, target, message)
	}
}

func (m *Manager) logWarn(action, target, message string) {
	if m.log != nil {
		m.log.Warnf("[%s] %s: %s", action, target, message)
	}
}
