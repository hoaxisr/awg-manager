package service

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/netutil"
	"github.com/hoaxisr/awg-manager/internal/tunnel/sysinfo"
)

// === Lifecycle Operations ===

// Start starts a tunnel.
// Safe to call on boot — operator only applies NDMS config when OpkgTun
// was just created (import flow), not on every start.
func (s *ServiceImpl) Start(ctx context.Context, tunnelID string) error {
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

	// Check current state
	stateInfo := s.state.GetState(ctx, tunnelID)

	switch stateInfo.State {
	case tunnel.StateRunning:
		// Clear dead flag — user's manual Start intent overrides monitoring state
		s.clearDeadFlag(tunnelID)
		return tunnel.ErrAlreadyRunning

	case tunnel.StateBroken:
		// Recover first
		s.logInfo("start", tunnelID, "Recovering from broken state before start")
		if err := s.operator.Recover(ctx, tunnelID, stateInfo); err != nil {
			return fmt.Errorf("recover before start: %w", err)
		}
		// Continue with start

	case tunnel.StateStarting:
		// Stale starting state — interface exists but link is down.
		// Happens after kernel KillLink (ip link set down preserves sysfs entry).
		// Under per-tunnel lock, a genuine Start can't be running simultaneously.
		s.logInfo("start", tunnelID, "Recovering from stale starting state")
		if err := s.operator.Recover(ctx, tunnelID, stateInfo); err != nil {
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
	hasIPv6 := sysinfo.HasDefaultIPv6Route() && s.operator.HasWANIPv6(ctx, s.wan.IDFor(resolvedWAN))
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
	if err := s.operator.Start(ctx, cfg); err != nil {
		return err
	}

	// Notify policy service about tunnel start
	if s.policyHooks != nil {
		names := tunnel.NewNames(tunnelID)
		if err := s.policyHooks.OnTunnelStart(ctx, tunnelID, names.IfaceName); err != nil {
			s.logWarn("start", tunnelID, "Policy hook failed: "+err.Error())
		}
	}

	// Notify DNS route service about tunnel start (reconcile routes)
	if s.dnsRouteHooks != nil {
		if err := s.dnsRouteHooks.OnTunnelStart(ctx); err != nil {
			s.logWarn("start", tunnelID, "DNS route hook failed: "+err.Error())
		}
	}

	// Notify static route service about tunnel start
	if s.staticRouteHooks != nil {
		sNames := tunnel.NewNames(tunnelID)
		if err := s.staticRouteHooks.OnTunnelStart(ctx, tunnelID, sNames.IfaceName); err != nil {
			s.logWarn("start", tunnelID, "Static route hook failed: "+err.Error())
		}
	}

	// Persist state after successful start
	stored.ActiveWAN = resolvedWAN
	stored.StartedAt = time.Now().UTC().Format(time.RFC3339)
	changed := true
	if ip := s.operator.GetTrackedEndpointIP(tunnelID); ip != "" {
		stored.ResolvedEndpointIP = ip
	}
	if stored.PingCheck != nil && stored.PingCheck.IsDeadByMonitoring {
		stored.PingCheck.IsDeadByMonitoring = false
		stored.PingCheck.DeadSince = nil
	}
	if changed {
		if err := s.store.Save(stored); err != nil {
			s.logWarn("save", stored.ID, "Failed to persist state: "+err.Error())
		}
	}

	s.logInfo("start", tunnelID, "Tunnel started")

	if s.reconcileHooks != nil {
		s.reconcileHooks.OnReconcileStart(tunnelID, stored.Name)
	}

	return nil
}

// Reconcile re-applies system configuration around an already-running process.
func (s *ServiceImpl) Reconcile(ctx context.Context, tunnelID string) error {
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
	hasIPv6 := sysinfo.HasDefaultIPv6Route() && s.operator.HasWANIPv6(ctx, s.wan.IDFor(resolvedWAN))
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

	if err := s.operator.Reconcile(ctx, cfg); err != nil {
		return err
	}

	// Persist state
	stored.ActiveWAN = resolvedWAN
	changed := true
	if ip := s.operator.GetTrackedEndpointIP(tunnelID); ip != "" {
		stored.ResolvedEndpointIP = ip
	}
	if stored.PingCheck != nil && stored.PingCheck.IsDeadByMonitoring {
		stored.PingCheck.IsDeadByMonitoring = false
		stored.PingCheck.DeadSince = nil
	}
	if changed {
		if err := s.store.Save(stored); err != nil {
			s.logWarn("save", stored.ID, "Failed to persist state: "+err.Error())
		}
	}

	s.logInfo("reconcile", tunnelID, "Tunnel reconciled")

	if s.reconcileHooks != nil {
		s.reconcileHooks.OnReconcileStart(tunnelID, stored.Name)
	}

	return nil
}

// Stop stops a tunnel.
func (s *ServiceImpl) Stop(ctx context.Context, tunnelID string) error {
	s.suppressReconcile(tunnelID)
	s.lockTunnel(tunnelID)
	defer s.unlockTunnel(tunnelID)

	return s.stopInternal(ctx, tunnelID)
}

// stopInternal stops a tunnel (assumes lock is held).
func (s *ServiceImpl) stopInternal(ctx context.Context, tunnelID string) error {
	// Verify tunnel exists in storage
	if !s.store.Exists(tunnelID) {
		return tunnel.ErrNotFound
	}

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
	if err := s.operator.Stop(ctx, tunnelID); err != nil {
		return err
	}

	// Notify policy service about tunnel stop
	if s.policyHooks != nil {
		if err := s.policyHooks.OnTunnelStop(ctx, tunnelID); err != nil {
			s.logWarn("stop", tunnelID, "Policy hook failed: "+err.Error())
		}
	}

	// Notify static route service about tunnel stop
	if s.staticRouteHooks != nil {
		if err := s.staticRouteHooks.OnTunnelStop(ctx, tunnelID); err != nil {
			s.logWarn("stop", tunnelID, "Static route hook failed: "+err.Error())
		}
	}

	// Clear runtime state — user explicitly stopped
	stored, err := s.store.Get(tunnelID)
	if err == nil {
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
	return nil
}

// Restart stops and starts a tunnel.
func (s *ServiceImpl) Restart(ctx context.Context, tunnelID string) error {
	s.suppressReconcile(tunnelID)
	s.lockTunnel(tunnelID)
	defer s.unlockTunnel(tunnelID)

	// Stop if process might be alive (ignore errors if not running)
	stateInfo := s.state.GetState(ctx, tunnelID)
	if stateInfo.State == tunnel.StateRunning || stateInfo.State == tunnel.StateBroken ||
		stateInfo.State == tunnel.StateNeedsStop || stateInfo.State == tunnel.StateStarting {
		if err := s.operator.Stop(ctx, tunnelID); err != nil {
			s.logWarn("restart", tunnelID, "Stop failed: "+err.Error())
		}
	}

	// Start
	if err := s.startInternal(ctx, tunnelID); err != nil {
		return fmt.Errorf("restart start: %w", err)
	}

	s.logInfo("restart", tunnelID, "Tunnel restarted")
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

	// Stop monitoring before deletion
	if s.reconcileHooks != nil {
		s.reconcileHooks.OnTunnelDelete(tunnelID)
	}

	// Notify policy service before deletion
	if s.policyHooks != nil {
		if err := s.policyHooks.OnTunnelDelete(ctx, tunnelID); err != nil {
			s.logWarn("delete", tunnelID, "Policy hook failed: "+err.Error())
		}
	}

	// Notify static route service before deletion
	if s.staticRouteHooks != nil {
		if err := s.staticRouteHooks.OnTunnelDelete(ctx, tunnelID); err != nil {
			s.logWarn("delete", tunnelID, "Static route hook failed: "+err.Error())
		}
	}

	// Notify DNS route service before deletion (cleanup stale references)
	if s.dnsRouteHooks != nil {
		if err := s.dnsRouteHooks.OnTunnelDelete(ctx, tunnelID); err != nil {
			s.logWarn("delete", tunnelID, "DNS route hook failed: "+err.Error())
		}
	}

	// Delete via operator (handles stop if needed)
	if err := s.operator.Delete(ctx, tunnelID); err != nil {
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
	return nil
}

// === State Operations ===

// GetState returns the current state of a tunnel.
// During active lifecycle operations (Start/Stop), transient states from the
// state matrix may be misleading (e.g. NeedsStop during Start when process is
// running but InterfaceUp hasn't been called yet). This method overrides such
// states to reflect the actual operation in progress.
func (s *ServiceImpl) GetState(ctx context.Context, tunnelID string) tunnel.StateInfo {
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
