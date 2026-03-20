package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
	"github.com/hoaxisr/awg-manager/internal/tunnel/netutil"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
	"github.com/hoaxisr/awg-manager/internal/tunnel/sysinfo"
)

// === Lifecycle Operations ===

// Start starts a tunnel.
// Safe to call on boot — operator only applies NDMS config when OpkgTun
// was just created (import flow), not on every start.
func (s *ServiceImpl) Start(ctx context.Context, tunnelID string) error {
	s.clearReconcileLoop(tunnelID) // reset loop detection on manual start
	s.suppressReconcile(tunnelID)
	s.setLifecycleOp(tunnelID, tunnel.StateStarting)
	defer s.clearLifecycleOp(tunnelID)

	s.lockTunnel(tunnelID)
	defer s.unlockTunnel(tunnelID)

	return s.startInternal(ctx, tunnelID)
}

// startInternal starts a tunnel (assumes lock is held).
func (s *ServiceImpl) startInternal(ctx context.Context, tunnelID string) error {
	// Get stored tunnel
	stored, err := s.store.Get(tunnelID)
	if err != nil {
		return tunnel.ErrNotFound
	}

	// NativeWG dispatch — operator handles everything internally
	if s.isNativeWG(stored) {
		return s.startNativeWG(ctx, stored)
	}

	// === Kernel path ===

	// Check current state
	stateInfo := s.state.GetState(ctx, tunnelID)

	switch stateInfo.State {
	case tunnel.StateRunning:
		// Clear dead flag — user's manual Start intent overrides monitoring state
		s.clearDeadFlag(tunnelID)
		s.appLog.Debug("start", tunnelID, "Already running, skipping")
		return tunnel.ErrAlreadyRunning

	case tunnel.StateBroken:
		// Recover first
		s.logInfo("start", tunnelID, "Recovering from broken state before start")
		if err := s.legacyOperator.Recover(ctx, tunnelID, stateInfo); err != nil {
			return fmt.Errorf("recover before start: %w", err)
		}
		// Continue with start

	case tunnel.StateStarting:
		// Stale starting state — interface exists but link is down.
		// Happens after kernel KillLink (ip link set down preserves sysfs entry).
		// Under per-tunnel lock, a genuine Start can't be running simultaneously.
		s.logInfo("start", tunnelID, "Recovering from stale starting state")
		if err := s.legacyOperator.Recover(ctx, tunnelID, stateInfo); err != nil {
			return fmt.Errorf("recover before start: %w", err)
		}
		// Continue with start

	case tunnel.StateStopping:
		return tunnel.ErrTransitioning
	}

	// Resolve WAN interface first (needed for IPv6 detection)
	resolvedWAN, err := s.resolveWAN(ctx, stored.ISPInterface)
	if err != nil {
		return fmt.Errorf("resolve WAN: %w", err)
	}
	// For explicit WAN selection, verify the interface is actually up.
	// Auto mode (empty) already guarantees connectivity: PreferredUp returns
	// only UP interfaces, GetDefaultGatewayInterface proves a route exists.
	// Tunnel chaining checks parent state inside resolveWAN.
	// Only check IsUp for interfaces known to the WAN model — non-WAN interfaces
	// (bridge mode, etc.) are not tracked by the model and should not be blocked.
	if stored.ISPInterface != "" && !tunnel.IsTunnelRoute(stored.ISPInterface) && s.wan.Known(resolvedWAN) && !s.wan.IsUp(resolvedWAN) {
		return fmt.Errorf("WAN %s is down", resolvedWAN)
	}

	// Check if ISP provides IPv6: default route exists AND WAN has IPv6 layer running.
	// HasWANIPv6 uses NDMS RCI which needs NDMS ID, not kernel name.
	hasIPv6 := sysinfo.HasDefaultIPv6Route() && s.legacyOperator.HasWANIPv6(ctx, s.wan.IDFor(resolvedWAN))
	if !hasIPv6 {
		s.logInfo("start", tunnelID, fmt.Sprintf("No IPv6 on WAN %s, skipping ::/0 and IPv6 routes", resolvedWAN))
	}

	// Write config file (filters ::/0 from AllowedIPs when no ISP IPv6)
	if err := s.writeConfigFileForStart(stored, hasIPv6); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	// Build config
	cfg := s.storedToConfig(stored)
	cfg.ISPInterface = resolvedWAN
	cfg.KernelDevice = s.resolveKernelDevice(resolvedWAN)

	// Skip IPv6 setup if ISP has no IPv6
	if !hasIPv6 {
		cfg.AddressIPv6 = ""
	}

	cfg.DefaultRoute = stored.DefaultRoute
	cfg.Endpoint = stored.Peer.Endpoint
	if ip, err := netutil.ResolveEndpointIP(stored.Peer.Endpoint); err == nil {
		cfg.EndpointIP = ip
	}

	// Check address availability in the system.
	// Exclude ALL managed tunnel interfaces — addresses may linger after
	// incomplete cleanup (KillLink, NDMS delay) and cause false positives.
	managedIfaces := s.collectManagedIfaceNames()
	if err := checkSystemAddressConflict(cfg.Address, cfg.AddressIPv6, managedIfaces); err != nil {
		return fmt.Errorf("start %s: %w", tunnelID, err)
	}

	// Start tunnel
	if err := s.legacyOperator.Start(ctx, cfg); err != nil {
		s.appLog.Warn("start", tunnelID, "Failed to start: "+err.Error())
		return err
	}

	// Notify hook services about tunnel start
	names := tunnel.NewNames(tunnelID)
	s.fireStartHooks(ctx, tunnelID, names.IfaceName)

	// Persist state after successful start
	stored.ActiveWAN = resolvedWAN
	stored.StartedAt = time.Now().UTC().Format(time.RFC3339)
	if ip := s.legacyOperator.GetTrackedEndpointIP(tunnelID); ip != "" {
		stored.ResolvedEndpointIP = ip
	}
	if stored.PingCheck != nil && stored.PingCheck.IsDeadByMonitoring {
		stored.PingCheck.IsDeadByMonitoring = false
		stored.PingCheck.DeadSince = nil
	}
	if err := s.store.Save(stored); err != nil {
		s.logWarn("save", stored.ID, "Failed to persist state: "+err.Error())
	}

	s.logInfo("start", tunnelID, "Tunnel started")
	s.appLog.Info("start", tunnelID, "Tunnel started")

	if s.reconcileHooks != nil {
		s.reconcileHooks.OnReconcileStart(tunnelID, stored.Name)
	}

	return nil
}

// Reconcile re-applies system configuration around an already-running process.
func (s *ServiceImpl) Reconcile(ctx context.Context, tunnelID string) error {
	if s.isNativeWGByID(tunnelID) {
		return nil // NativeWG: NDMS manages tunnel persistence
	}

	s.suppressReconcile(tunnelID)
	s.lockTunnel(tunnelID)
	defer s.unlockTunnel(tunnelID)

	stateInfo := s.state.GetState(ctx, tunnelID)

	if stateInfo.State == tunnel.StateRunning {
		return tunnel.ErrAlreadyRunning
	}

	if !stateInfo.ProcessRunning {
		return fmt.Errorf("process not running, use Start instead")
	}

	return s.reconcileInternal(ctx, tunnelID)
}

// reconcileInternal re-applies firewall, routes, NDMS config around an
// already-running process (assumes per-tunnel lock is held).
func (s *ServiceImpl) reconcileInternal(ctx context.Context, tunnelID string) error {
	stored, err := s.store.Get(tunnelID)
	if err != nil {
		return tunnel.ErrNotFound
	}

	// Resolve WAN interface first (needed for IPv6 detection)
	resolvedWAN, err := s.resolveWAN(ctx, stored.ISPInterface)
	if err != nil {
		return fmt.Errorf("resolve WAN: %w", err)
	}

	// Check if ISP provides IPv6: default route exists AND WAN has IPv6 layer running.
	// HasWANIPv6 uses NDMS RCI which needs NDMS ID, not kernel name.
	hasIPv6 := sysinfo.HasDefaultIPv6Route() && s.legacyOperator.HasWANIPv6(ctx, s.wan.IDFor(resolvedWAN))
	if !hasIPv6 {
		s.logInfo("reconcile", tunnelID, fmt.Sprintf("No IPv6 on WAN %s, skipping IPv6 routes", resolvedWAN))
	}

	// Write config file
	if err := s.writeConfigFileForStart(stored, hasIPv6); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	// Build config
	cfg := s.storedToConfig(stored)
	cfg.ISPInterface = resolvedWAN
	cfg.KernelDevice = s.resolveKernelDevice(resolvedWAN)
	if !hasIPv6 {
		cfg.AddressIPv6 = ""
	}

	cfg.DefaultRoute = stored.DefaultRoute
	cfg.Endpoint = stored.Peer.Endpoint
	if ip, err := netutil.ResolveEndpointIP(stored.Peer.Endpoint); err == nil {
		cfg.EndpointIP = ip
	}

	if err := s.legacyOperator.Reconcile(ctx, cfg); err != nil {
		return err
	}

	// Persist state
	stored.ActiveWAN = resolvedWAN
	if ip := s.legacyOperator.GetTrackedEndpointIP(tunnelID); ip != "" {
		stored.ResolvedEndpointIP = ip
	}
	if stored.PingCheck != nil && stored.PingCheck.IsDeadByMonitoring {
		stored.PingCheck.IsDeadByMonitoring = false
		stored.PingCheck.DeadSince = nil
	}
	if err := s.store.Save(stored); err != nil {
		s.logWarn("save", stored.ID, "Failed to persist state: "+err.Error())
	}

	s.logInfo("reconcile", tunnelID, "Tunnel reconciled")

	if s.reconcileHooks != nil {
		s.reconcileHooks.OnReconcileStart(tunnelID, stored.Name)
	}

	return nil
}

// Stop stops a tunnel.
func (s *ServiceImpl) Stop(ctx context.Context, tunnelID string) error {
	s.clearReconcileLoop(tunnelID) // reset loop detection on manual stop
	s.suppressReconcile(tunnelID)
	s.lockTunnel(tunnelID)
	defer s.unlockTunnel(tunnelID)

	return s.stopInternal(ctx, tunnelID)
}

// stopInternal stops a tunnel (assumes lock is held).
func (s *ServiceImpl) stopInternal(ctx context.Context, tunnelID string) error {
	// Get stored tunnel
	stored, err := s.store.Get(tunnelID)
	if err != nil {
		return tunnel.ErrNotFound
	}

	// NativeWG dispatch — operator handles everything internally
	if s.isNativeWG(stored) {
		return s.stopNativeWG(ctx, stored)
	}

	// === Kernel path ===

	// Check current state
	stateInfo := s.state.GetState(ctx, tunnelID)

	switch stateInfo.State {
	case tunnel.StateStopped, tunnel.StateNotCreated, tunnel.StateDisabled:
		return tunnel.ErrNotRunning

	case tunnel.StateStarting:
		// Stale starting state — interface exists but link is down.
		// Under per-tunnel lock, a genuine Start can't be running simultaneously.
		// Proceed to stop (operator.Stop handles cleanup).

	case tunnel.StateStopping:
		return tunnel.ErrTransitioning
	}

	// Notify PingCheck before stopping (pause monitoring)
	if s.reconcileHooks != nil {
		s.reconcileHooks.OnReconcileStop(tunnelID)
	}

	// Stop tunnel (handles Running, Broken, NeedsStop, NeedsStart, Starting).
	// NeedsStart (conf: running, no process): operator.Stop() calls InterfaceDown
	// to set conf: disabled — firewall/route removal are no-ops, backend.Stop is harmless.
	if err := s.legacyOperator.Stop(ctx, tunnelID); err != nil {
		s.appLog.Warn("stop", tunnelID, "Failed to stop: "+err.Error())
		return err
	}

	// Notify hook services about tunnel stop
	s.fireStopHooks(ctx, tunnelID)

	// Clear runtime state — user explicitly stopped
	{
		changed := false
		if stored.ActiveWAN != "" {
			stored.ActiveWAN = ""
			changed = true
		}
		if stored.StartedAt != "" {
			stored.StartedAt = ""
			changed = true
		}
		if stored.PingCheck != nil && stored.PingCheck.IsDeadByMonitoring {
			stored.PingCheck.IsDeadByMonitoring = false
			stored.PingCheck.DeadSince = nil
			changed = true
		}
		if changed {
			_ = s.store.Save(stored)
		}
	}

	s.logInfo("stop", tunnelID, "Tunnel stopped")
	s.appLog.Info("stop", tunnelID, "Tunnel stopped")
	return nil
}

// startNativeWG starts a NativeWG tunnel (assumes lock is held).
func (s *ServiceImpl) startNativeWG(ctx context.Context, stored *storage.AWGTunnel) error {
	if s.nwgOperator == nil {
		return fmt.Errorf("NativeWG backend not available")
	}

	// Check current state
	stateInfo := s.nwgOperator.GetState(ctx, stored)
	if stateInfo.State == tunnel.StateRunning {
		s.clearDeadFlag(stored.ID)
		s.appLog.Debug("start", stored.ID, "Already running, skipping")
		return tunnel.ErrAlreadyRunning
	}

	// Start via NativeWG operator
	if err := s.nwgOperator.Start(ctx, stored); err != nil {
		s.appLog.Warn("start", stored.ID, "Failed to start NativeWG: "+err.Error())
		return err
	}

	// Configure NDMS native ping-check if enabled
	if stored.PingCheck != nil && stored.PingCheck.Enabled {
		minSuccess := stored.PingCheck.MinSuccess
		if minSuccess == 0 {
			minSuccess = 1
		}
		pcCfg := ndms.PingCheckConfig{
			Host:           stored.PingCheck.Target,
			Mode:           stored.PingCheck.Method,
			MinSuccess:     minSuccess,
			UpdateInterval: stored.PingCheck.Interval,
			MaxFails:       stored.PingCheck.FailThreshold,
			Timeout:        stored.PingCheck.Timeout,
			Port:           stored.PingCheck.Port,
			Restart:        stored.PingCheck.Restart,
		}
		if err := s.nwgOperator.ConfigurePingCheck(ctx, stored, pcCfg); err != nil {
			s.logWarn("start", stored.ID, "Failed to configure NWG ping-check: "+err.Error())
		}
	}

	// Fire hooks (policy, DNS route, static route)
	names := nwg.NewNWGNames(stored.NWGIndex)
	s.fireStartHooks(ctx, stored.ID, names.IfaceName)

	// Persist state
	stored.StartedAt = time.Now().UTC().Format(time.RFC3339)
	if stored.PingCheck != nil && stored.PingCheck.IsDeadByMonitoring {
		stored.PingCheck.IsDeadByMonitoring = false
		stored.PingCheck.DeadSince = nil
	}
	if err := s.store.Save(stored); err != nil {
		s.logWarn("save", stored.ID, "Failed to persist state: "+err.Error())
	}

	s.logInfo("start", stored.ID, "NativeWG tunnel started")
	s.appLog.Info("start", stored.ID, "NativeWG tunnel started")

	if s.reconcileHooks != nil {
		s.reconcileHooks.OnReconcileStart(stored.ID, stored.Name)
	}

	return nil
}

// stopNativeWG stops a NativeWG tunnel (assumes lock is held).
func (s *ServiceImpl) stopNativeWG(ctx context.Context, stored *storage.AWGTunnel) error {
	if s.nwgOperator == nil {
		return fmt.Errorf("NativeWG backend not available")
	}

	// Check current state
	stateInfo := s.nwgOperator.GetState(ctx, stored)
	switch stateInfo.State {
	case tunnel.StateStopped, tunnel.StateNotCreated, tunnel.StateDisabled:
		return tunnel.ErrNotRunning
	}

	// Notify PingCheck before stopping
	if s.reconcileHooks != nil {
		s.reconcileHooks.OnReconcileStop(stored.ID)
	}

	// Remove NDMS native ping-check profile (only if it was configured)
	if stored.PingCheck != nil && stored.PingCheck.Enabled {
		if err := s.nwgOperator.RemovePingCheck(ctx, stored); err != nil {
			s.logWarn("stop", stored.ID, "Failed to remove NWG ping-check: "+err.Error())
		}
	}

	// Stop via NativeWG operator
	if err := s.nwgOperator.Stop(ctx, stored); err != nil {
		s.appLog.Warn("stop", stored.ID, "Failed to stop NativeWG: "+err.Error())
		return err
	}

	// Fire stop hooks
	s.fireStopHooks(ctx, stored.ID)

	// Clear runtime state
	stored.ActiveWAN = ""
	stored.StartedAt = ""
	if stored.PingCheck != nil && stored.PingCheck.IsDeadByMonitoring {
		stored.PingCheck.IsDeadByMonitoring = false
		stored.PingCheck.DeadSince = nil
	}
	_ = s.store.Save(stored)

	s.logInfo("stop", stored.ID, "NativeWG tunnel stopped")
	s.appLog.Info("stop", stored.ID, "NativeWG tunnel stopped")
	return nil
}

// fireStartHooks notifies all hook services about a tunnel start.
func (s *ServiceImpl) fireStartHooks(ctx context.Context, tunnelID, ifaceName string) {
	if s.dnsRouteHooks != nil {
		if err := s.dnsRouteHooks.OnTunnelStart(ctx); err != nil {
			s.logWarn("start", tunnelID, "DNS route hook failed: "+err.Error())
		}
	}
	if s.staticRouteHooks != nil {
		if err := s.staticRouteHooks.OnTunnelStart(ctx, tunnelID, ifaceName); err != nil {
			s.logWarn("start", tunnelID, "Static route hook failed: "+err.Error())
		}
	}
}

// fireStopHooks notifies all hook services about a tunnel stop.
func (s *ServiceImpl) fireStopHooks(ctx context.Context, tunnelID string) {
	if s.staticRouteHooks != nil {
		if err := s.staticRouteHooks.OnTunnelStop(ctx, tunnelID); err != nil {
			s.logWarn("stop", tunnelID, "Static route hook failed: "+err.Error())
		}
	}
}

// Restart stops and starts a tunnel.
func (s *ServiceImpl) Restart(ctx context.Context, tunnelID string) error {
	s.suppressReconcile(tunnelID)
	s.lockTunnel(tunnelID)
	defer s.unlockTunnel(tunnelID)

	stored, err := s.store.Get(tunnelID)
	if err != nil {
		return tunnel.ErrNotFound
	}

	if s.isNativeWG(stored) {
		// NativeWG: Stop + Start
		_ = s.stopNativeWG(ctx, stored)
		// Re-read stored after stop (it may have modified fields)
		stored, err = s.store.Get(tunnelID)
		if err != nil {
			return tunnel.ErrNotFound
		}
		if err := s.startNativeWG(ctx, stored); err != nil {
			s.appLog.Warn("restart", tunnelID, "Failed to restart NativeWG: "+err.Error())
			return fmt.Errorf("restart start: %w", err)
		}
		s.logInfo("restart", tunnelID, "NativeWG tunnel restarted")
		s.appLog.Info("restart", tunnelID, "Tunnel restarted")
		return nil
	}

	// === Kernel path ===

	// Stop if process might be alive (ignore errors if not running)
	stateInfo := s.state.GetState(ctx, tunnelID)
	if stateInfo.State == tunnel.StateRunning || stateInfo.State == tunnel.StateBroken ||
		stateInfo.State == tunnel.StateNeedsStop || stateInfo.State == tunnel.StateStarting {
		if err := s.legacyOperator.Stop(ctx, tunnelID); err != nil {
			s.logWarn("restart", tunnelID, "Stop failed: "+err.Error())
		}
	}

	// Start
	if err := s.startInternal(ctx, tunnelID); err != nil {
		s.appLog.Warn("restart", tunnelID, "Failed to restart: "+err.Error())
		return fmt.Errorf("restart start: %w", err)
	}

	s.logInfo("restart", tunnelID, "Tunnel restarted")
	s.appLog.Info("restart", tunnelID, "Tunnel restarted")
	return nil
}

// Delete stops (if running) and deletes a tunnel.
func (s *ServiceImpl) Delete(ctx context.Context, tunnelID string) error {
	s.suppressReconcile(tunnelID)
	s.lockTunnel(tunnelID)
	defer func() {
		s.unlockTunnel(tunnelID)
		s.cleanupTunnelLock(tunnelID)
	}()

	// Verify tunnel exists
	if !s.store.Exists(tunnelID) {
		return tunnel.ErrNotFound
	}

	// NativeWG dispatch
	if s.isNativeWGByID(tunnelID) {
		return s.deleteNativeWG(ctx, tunnelID)
	}

	// === Kernel path ===

	// Fire pre-delete hooks
	s.fireDeleteHooks(ctx, tunnelID)

	// Delete via operator (handles stop if needed)
	if err := s.legacyOperator.Delete(ctx, tunnelID); err != nil {
		s.appLog.Warn("delete", tunnelID, "Failed to delete: "+err.Error())
		return err
	}

	// Delete config file
	confPath := tunnel.NewNames(tunnelID).ConfPath
	_ = os.Remove(confPath)

	// Delete from storage
	if err := s.store.Delete(tunnelID); err != nil {
		return fmt.Errorf("delete from storage: %w", err)
	}

	s.logInfo("delete", tunnelID, "Tunnel deleted")
	s.appLog.Info("delete", tunnelID, "Tunnel deleted")
	return nil
}

// deleteNativeWG deletes a NativeWG tunnel (assumes lock is held).
func (s *ServiceImpl) deleteNativeWG(ctx context.Context, tunnelID string) error {
	stored, err := s.store.Get(tunnelID)
	if err != nil {
		return tunnel.ErrNotFound
	}

	// Fire pre-delete hooks
	s.fireDeleteHooks(ctx, tunnelID)

	// Delete via NativeWG operator (handles stop if running)
	if s.nwgOperator != nil {
		if err := s.nwgOperator.Delete(ctx, stored); err != nil {
			s.appLog.Warn("delete", tunnelID, "Failed to delete NativeWG: "+err.Error())
			return err
		}
	}

	// Delete config file
	confPath := filepath.Join(confDir, tunnelID+".conf")
	_ = os.Remove(confPath)

	// Delete from storage
	if err := s.store.Delete(tunnelID); err != nil {
		return fmt.Errorf("delete from storage: %w", err)
	}

	s.logInfo("delete", tunnelID, "NativeWG tunnel deleted")
	s.appLog.Info("delete", tunnelID, "Tunnel deleted")
	return nil
}

// fireDeleteHooks notifies all hook services about a tunnel deletion.
func (s *ServiceImpl) fireDeleteHooks(ctx context.Context, tunnelID string) {
	if s.reconcileHooks != nil {
		s.reconcileHooks.OnTunnelDelete(tunnelID)
	}
	if s.staticRouteHooks != nil {
		if err := s.staticRouteHooks.OnTunnelDelete(ctx, tunnelID); err != nil {
			s.logWarn("delete", tunnelID, "Static route hook failed: "+err.Error())
		}
	}
	if s.dnsRouteHooks != nil {
		if err := s.dnsRouteHooks.OnTunnelDelete(ctx, tunnelID); err != nil {
			s.logWarn("delete", tunnelID, "DNS route hook failed: "+err.Error())
		}
	}
}

// === State Operations ===

// GetState returns the current state of a tunnel.
// During active lifecycle operations (Start/Stop), transient states from the
// state matrix may be misleading (e.g. NeedsStop during Start when process is
// running but InterfaceUp hasn't been called yet). This method overrides such
// states to reflect the actual operation in progress.
func (s *ServiceImpl) GetState(ctx context.Context, tunnelID string) tunnel.StateInfo {
	// NativeWG: use nwgOperator.GetState directly
	if s.nwgOperator != nil && s.isNativeWGByID(tunnelID) {
		stored, err := s.store.Get(tunnelID)
		if err != nil {
			return tunnel.StateInfo{State: tunnel.StateUnknown}
		}
		info := s.nwgOperator.GetState(ctx, stored)
		return info
	}

	// === Kernel path ===
	info := s.state.GetState(ctx, tunnelID)

	s.lifecycleOpsMu.RLock()
	expectedState, inProgress := s.lifecycleOps[tunnelID]
	s.lifecycleOpsMu.RUnlock()

	if inProgress && info.State != expectedState && info.State != tunnel.StateRunning {
		info.State = expectedState
	}

	return info
}

// setLifecycleOp marks a tunnel as undergoing a lifecycle operation.
func (s *ServiceImpl) setLifecycleOp(tunnelID string, state tunnel.State) {
	s.lifecycleOpsMu.Lock()
	s.lifecycleOps[tunnelID] = state
	s.lifecycleOpsMu.Unlock()
}

// clearLifecycleOp removes the lifecycle operation marker for a tunnel.
func (s *ServiceImpl) clearLifecycleOp(tunnelID string) {
	s.lifecycleOpsMu.Lock()
	delete(s.lifecycleOps, tunnelID)
	s.lifecycleOpsMu.Unlock()
}
