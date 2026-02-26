package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// === WAN Event Handlers ===

// HandleWANUp is called when a WAN interface comes up.
// Starts tunnels bound to this WAN. Auto-mode tunnels may switch
// to the new default gateway if it changed.
func (s *ServiceImpl) HandleWANUp(ctx context.Context, iface string) {
	s.logInfo("wan", "event", fmt.Sprintf("WAN up: %s", iface))

	tunnels, err := s.store.List()
	if err != nil {
		s.logWarn("wan_up", "list", "Failed to list tunnels: "+err.Error())
		return
	}

	// Separate tunnels into direct (non-chained) and chained (tunnel:xxx).
	// Direct tunnels must start first so chained tunnels can resolve parent's WAN.
	var direct, chained []storage.AWGTunnel
	for _, t := range tunnels {
		if !t.Enabled {
			continue
		}

		// Clear dead-by-monitoring flag (WAN recovery = fresh start)
		if t.PingCheck != nil && t.PingCheck.IsDeadByMonitoring {
			t.PingCheck.IsDeadByMonitoring = false
			t.PingCheck.DeadSince = nil
			if err := s.store.Save(&t); err != nil {
				s.logWarn("wan_up", t.ID, "Failed to clear dead flag: "+err.Error())
			}
		}

		stateInfo := s.state.GetState(ctx, t.ID)

		if stateInfo.State == tunnel.StateRunning {
			// Running tunnel: check if auto-mode should switch gateway
			if t.ISPInterface == "" {
				s.handleAutoGatewaySwitch(ctx, &t)
			}
			continue
		}

		// Not running — determine if this WAN event should start the tunnel
		shouldStart := false
		switch {
		case t.ISPInterface == "":
			shouldStart = true // auto: any WAN up triggers start attempt
		case t.ISPInterface == iface:
			shouldStart = true // explicit: exact WAN match
		case tunnel.IsTunnelRoute(t.ISPInterface):
			// Chained tunnels are handled in the second phase
			chained = append(chained, t)
			continue
		}

		if shouldStart {
			direct = append(direct, t)
		}
	}

	// Phase 1: Start direct tunnels in parallel, wait for all to complete.
	// This populates resolvedISP so chained tunnels can resolve parent WAN.
	var wg sync.WaitGroup
	for _, t := range direct {
		t := t
		wg.Add(1)
		go func() {
			defer wg.Done()
			wanCtx := s.newWANOp(t.ID)
			defer s.clearWANOp(t.ID)

			s.suppressReconcile(t.ID)
			s.lockTunnel(t.ID)
			defer s.unlockTunnel(t.ID)

			startCtx, startCancel := context.WithTimeout(wanCtx, 30*time.Second)
			defer startCancel()

			if err := s.startInternal(startCtx, t.ID); err != nil {
				s.logWarn("wan_up", t.ID, "Failed to start: "+err.Error())
				return
			}
			s.logInfo("wan_up", t.ID, "Tunnel started")
		}()
	}
	wg.Wait()

	// Phase 2: Start chained tunnels (parent's ActiveWAN is now populated).
	for _, t := range chained {
		parentID := tunnel.TunnelRouteID(t.ISPInterface)
		var parentWAN string
		if parentStored, err := s.store.Get(parentID); err == nil {
			parentWAN = parentStored.ActiveWAN
		}
		if parentWAN != iface {
			continue // parent's WAN doesn't match this event
		}

		t := t
		go func() {
			wanCtx := s.newWANOp(t.ID)
			defer s.clearWANOp(t.ID)

			s.suppressReconcile(t.ID)
			s.lockTunnel(t.ID)
			defer s.unlockTunnel(t.ID)

			startCtx, startCancel := context.WithTimeout(wanCtx, 30*time.Second)
			defer startCancel()

			if err := s.startInternal(startCtx, t.ID); err != nil {
				s.logWarn("wan_up", t.ID, "Failed to start chained: "+err.Error())
				return
			}
			s.logInfo("wan_up", t.ID, "Chained tunnel started")
		}()
	}
}

// handleAutoGatewaySwitch checks if an auto-mode running tunnel should
// switch to the current default gateway (e.g., PPPoE restored priority over LTE).
func (s *ServiceImpl) handleAutoGatewaySwitch(ctx context.Context, t *storage.AWGTunnel) {
	// Prefer WAN model (priority-based), fallback to route table
	currentGW, ok := s.wan.PreferredUp()
	if !ok {
		gwCtx, gwCancel := context.WithTimeout(ctx, 5*time.Second)
		defer gwCancel()
		var err error
		currentGW, err = s.operator.GetDefaultGatewayInterface(gwCtx)
		if err != nil {
			return // can't determine gateway, leave tunnel as-is
		}
	}

	resolvedWAN := t.ActiveWAN // t is *storage.AWGTunnel from store.List()
	if currentGW == resolvedWAN {
		return // already on correct gateway
	}

	s.logInfo("wan_up", t.ID, fmt.Sprintf("Auto gateway switch: %s → %s", resolvedWAN, currentGW))

	go func() {
		wanCtx := s.newWANOp(t.ID)
		defer s.clearWANOp(t.ID)

		s.suppressReconcile(t.ID)
		s.lockTunnel(t.ID)
		defer s.unlockTunnel(t.ID)

		if s.reconcileHooks != nil {
			s.reconcileHooks.OnReconcileStop(t.ID)
		}

		switchCtx, switchCancel := context.WithTimeout(wanCtx, 15*time.Second)
		defer switchCancel()

		if err := s.operator.KillLink(switchCtx, t.ID); err != nil {
			s.logWarn("wan_up", t.ID, "Gateway switch KillLink failed: "+err.Error())
			return
		}
		s.clearActiveWAN(t.ID)
		if err := s.startInternal(switchCtx, t.ID); err != nil {
			s.logWarn("wan_up", t.ID, "Gateway switch Start failed: "+err.Error())
		}
	}()
}

// HandleWANDown is called when a WAN interface goes down.
// Kills only tunnels bound to this specific WAN. Auto-mode tunnels
// attempt immediate failover to another available gateway.
func (s *ServiceImpl) HandleWANDown(ctx context.Context, iface string) {
	s.logInfo("wan", "event", fmt.Sprintf("WAN down: %s", iface))

	tunnels, err := s.store.List()
	if err != nil {
		s.logWarn("wan_down", "list", "Failed to list tunnels: "+err.Error())
		return
	}

	for _, t := range tunnels {
		resolvedWAN := t.ActiveWAN // persisted, reliable

		// Match tunnels bound to this WAN.
		// Empty iface = "all WANs down" (boot with no gateway) — kill all with ActiveWAN set.
		if iface != "" && (resolvedWAN == "" || resolvedWAN != iface) {
			continue
		}
		if iface == "" && resolvedWAN == "" {
			continue
		}

		t := t // capture for goroutine
		go func() {
			wanCtx := s.newWANOp(t.ID)
			defer s.clearWANOp(t.ID)

			s.suppressReconcile(t.ID)
			s.lockTunnel(t.ID)
			defer s.unlockTunnel(t.ID)

			if s.reconcileHooks != nil {
				s.reconcileHooks.OnReconcileStop(t.ID)
			}

			// Kill the tunnel (preserves NDMS intent for reboot recovery)
			killCtx, killCancel := context.WithTimeout(wanCtx, 10*time.Second)
			defer killCancel()
			if err := s.operator.KillLink(killCtx, t.ID); err != nil {
				s.logWarn("wan_down", t.ID, "KillLink failed: "+err.Error())
				return
			}
			s.clearActiveWAN(t.ID)
			s.logInfo("wan_down", t.ID, fmt.Sprintf("Tunnel killed (WAN %s down)", iface))

			// Auto mode: attempt immediate failover to another gateway.
			// Stop fully first to clean up KillLink state — kernel KillLink
			// leaves sysfs entry (link down), startInternal would see
			// StateStarting → Recover → destructively changes NDMS intent.
			if iface != "" && t.ISPInterface == "" {
				failoverCtx, failoverCancel := context.WithTimeout(wanCtx, 30*time.Second)
				defer failoverCancel()

				// Prefer WAN model (priority-based), fallback to route table
				hasGW := false
				if _, ok := s.wan.PreferredUp(); ok {
					hasGW = true
				} else if _, err := s.operator.GetDefaultGatewayInterface(failoverCtx); err == nil {
					hasGW = true
				}
				if hasGW {
					s.logInfo("wan_down", t.ID, "Auto failover: another gateway available, restarting")
					_ = s.operator.Stop(failoverCtx, t.ID)
					if err := s.startInternal(failoverCtx, t.ID); err != nil {
						s.logWarn("wan_down", t.ID, "Auto failover failed: "+err.Error())
					}
				}
			}
		}()
	}
}

// === PingCheck Integration ===

// HandleMonitorDead is called when PingCheck detects a dead tunnel.
// Persists dead state in storage and kills the tunnel process via KillLink.
// KillLink preserves NDMS conf: running → tunnel auto-starts after reboot.
func (s *ServiceImpl) HandleMonitorDead(ctx context.Context, tunnelID string) error {
	s.lockTunnel(tunnelID)
	defer s.unlockTunnel(tunnelID)

	stored, err := s.store.Get(tunnelID)
	if err != nil {
		return nil // tunnel deleted, ignore
	}

	// Persist dead state in storage
	if stored.PingCheck != nil {
		now := time.Now().Format(time.RFC3339)
		stored.PingCheck.IsDeadByMonitoring = true
		stored.PingCheck.DeadSince = &now
		_ = s.store.Save(stored)
	}

	// KillLink kills the process but preserves NDMS intent (conf: running).
	// After this, state becomes NeedsStart — recoverable by HandleMonitorRecovered.
	if err := s.operator.KillLink(ctx, tunnelID); err != nil {
		s.logWarn("monitor", tunnelID, "KillLink failed: "+err.Error())
		return err
	}
	s.clearActiveWAN(tunnelID)

	s.logInfo("monitor", tunnelID, "Tunnel marked as dead by monitoring")
	return nil
}

// HandleForcedRestart is called by PingCheck when the dead interval timer fires.
// Does a full Stop + Start (like Restart) to ensure the tunnel is fully rebuilt.
// Preserves IsDeadByMonitoring — recovery is confirmed only when a subsequent
// handshake check succeeds and triggers HandleMonitorRecovered.
func (s *ServiceImpl) HandleForcedRestart(ctx context.Context, tunnelID string) error {
	if !s.wan.AnyUp() {
		return fmt.Errorf("WAN down, cannot restart")
	}

	s.suppressReconcile(tunnelID)
	s.lockTunnel(tunnelID)
	defer s.unlockTunnel(tunnelID)

	if !s.store.Exists(tunnelID) {
		return nil
	}

	// Full stop — tear down interface completely (ignore errors, might not be fully running).
	// This is needed because KillLink in kernel mode only does `ip link set down`,
	// leaving the interface present but inactive. reconcileInternal wouldn't bring it back up.
	_ = s.operator.Stop(ctx, tunnelID)
	s.clearActiveWAN(tunnelID)

	// Full start from scratch
	if err := s.startInternal(ctx, tunnelID); err != nil {
		s.logWarn("monitor", tunnelID, "Forced restart failed: "+err.Error())
		return err
	}

	// startInternal cleared IsDeadByMonitoring — re-set it because
	// connectivity hasn't been confirmed by a handshake check yet.
	stored, storeErr := s.store.Get(tunnelID)
	if storeErr == nil && stored.PingCheck != nil {
		now := time.Now().Format(time.RFC3339)
		stored.PingCheck.IsDeadByMonitoring = true
		stored.PingCheck.DeadSince = &now
		_ = s.store.Save(stored)
	}

	s.logInfo("monitor", tunnelID, "Forced restart (pending handshake verification)")
	return nil
}

// HandleMonitorRecovered is called when PingCheck detects tunnel recovery.
// Attempts a full restart. Returns error if restart fails — pingcheck stays
// in dead state and retries after DeadInterval.
func (s *ServiceImpl) HandleMonitorRecovered(ctx context.Context, tunnelID string) error {
	if !s.wan.AnyUp() {
		return fmt.Errorf("WAN down, cannot restart")
	}

	s.suppressReconcile(tunnelID)
	s.lockTunnel(tunnelID)
	defer s.unlockTunnel(tunnelID)

	if !s.store.Exists(tunnelID) {
		return nil // tunnel deleted
	}

	stateInfo := s.state.GetState(ctx, tunnelID)

	var err error
	if stateInfo.ProcessRunning {
		// Process alive but no firewall/routes after KillLink — reconcile
		err = s.reconcileInternal(ctx, tunnelID)
	} else {
		// Process dead — full start
		err = s.startInternal(ctx, tunnelID)
	}

	if err != nil {
		s.logWarn("monitor", tunnelID, "Recovery failed: "+err.Error())
		return err // pingcheck stays dead, retries after DeadInterval
	}

	s.logInfo("monitor", tunnelID, "Tunnel recovered from dead state")
	return nil
}

// RestoreEndpointTracking restores endpoint route tracking on daemon restart.
func (s *ServiceImpl) RestoreEndpointTracking(ctx context.Context) error {
	tunnels, err := s.store.List()
	if err != nil {
		return fmt.Errorf("list tunnels: %w", err)
	}

	restored := 0
	for _, t := range tunnels {
		// Skip if no endpoint
		if t.Peer.Endpoint == "" {
			continue
		}
		// Skip if not running
		stateInfo := s.state.GetState(ctx, t.ID)
		if stateInfo.State != tunnel.StateRunning {
			continue
		}

		// Restore tracking (route already exists in system)
		isp := t.ActiveWAN
		if isp == "" {
			// Migration: tunnel from older version without ActiveWAN
			if resolved, err := s.resolveWAN(ctx, t.ISPInterface); err == nil {
				isp = resolved
			} else {
				s.logWarn("restore", t.ID, "No stored ActiveWAN, resolve failed: "+err.Error())
			}
		}
		ip, err := s.operator.RestoreEndpointTracking(ctx, t.ID, t.Peer.Endpoint, isp)
		if err != nil {
			s.logWarn("restore", t.ID, "Failed to restore endpoint tracking: "+err.Error())
			continue
		}

		// Migration: fill ResolvedEndpointIP for tunnels from older versions
		if ip != "" && t.ResolvedEndpointIP == "" {
			t.ResolvedEndpointIP = ip
			if err := s.store.Save(&t); err != nil {
				s.logWarn("save", t.ID, "Failed to persist state: "+err.Error())
			}
			s.logInfo("restore", t.ID, "Migrated: persisted resolved endpoint IP "+ip)
		}
		restored++
	}

	if restored > 0 {
		s.logInfo("restore", "daemon", fmt.Sprintf("Restored endpoint tracking for %d tunnel(s)", restored))
	}

	// Clean up stale ActiveWAN/StartedAt for dead tunnels (daemon restart with dead processes)
	for _, t := range tunnels {
		if t.ActiveWAN == "" && t.StartedAt == "" {
			continue
		}
		stateInfo := s.state.GetState(ctx, t.ID)
		if !stateInfo.ProcessRunning {
			s.logInfo("restore", t.ID, "Clearing stale ActiveWAN/StartedAt (process dead)")
			t.ActiveWAN = ""
			t.StartedAt = ""
			_ = s.store.Save(&t)
		}
	}

	return nil
}

// TeardownForBackendSwitch fully removes OS-side resources for all tunnels
// (Stop + Delete OpkgTun) while keeping storage intact.
// Clears stale state (IsDeadByMonitoring, ResolvedEndpointIP) so tunnels
// start clean after daemon restart with the new backend.
func (s *ServiceImpl) TeardownForBackendSwitch(ctx context.Context) error {
	tunnels, err := s.store.List()
	if err != nil {
		return fmt.Errorf("list tunnels: %w", err)
	}

	for _, t := range tunnels {
		s.lockTunnel(t.ID)

		// Delete OS-side: Stop (firewall, routes, backend, InterfaceDown) + DeleteOpkgTun
		s.suppressReconcile(t.ID)
		if err := s.operator.Delete(ctx, t.ID); err != nil {
			s.logWarn("teardown", t.ID, "Failed to delete OS resources: "+err.Error())
			// Continue with other tunnels
		}

		// Clear stale state in storage
		changed := false
		if t.ActiveWAN != "" {
			t.ActiveWAN = ""
			changed = true
		}
		if t.ResolvedEndpointIP != "" {
			t.ResolvedEndpointIP = ""
			changed = true
		}
		if t.PingCheck != nil && t.PingCheck.IsDeadByMonitoring {
			t.PingCheck.IsDeadByMonitoring = false
			t.PingCheck.DeadSince = nil
			changed = true
		}
		if changed {
			if err := s.store.Save(&t); err != nil {
				s.logWarn("save", t.ID, "Failed to persist state: "+err.Error())
			}
		}

		s.unlockTunnel(t.ID)
		s.logInfo("teardown", t.ID, "OS resources removed for backend switch")
	}

	return nil
}

// ReconcileInterface handles an NDMS interface state change event.
// Called by iflayerchanged.d hook when user toggles interface in router UI.
func (s *ServiceImpl) ReconcileInterface(ctx context.Context, ndmsName, layer, level string) error {
	if layer != "conf" {
		return nil
	}

	// Map NDMS name (OpkgTun0) -> tunnel ID (awg0)
	tunnelID, stored := s.findTunnelByNDMSName(ndmsName)
	if tunnelID == "" {
		return nil // Not our interface
	}

	// Skip self-triggered hooks from our own Start/Stop/Restart/Delete.
	// These operations call InterfaceUp/InterfaceDown which fire NDMS hooks back to us.
	if s.isReconcileSuppressed(tunnelID) {
		s.logInfo("reconcile", tunnelID, fmt.Sprintf("Skipping self-triggered hook (level=%s)", level))
		return nil
	}

	switch level {
	case "running":
		// Intent UP — start if not running
		if !s.wan.AnyUp() {
			s.logInfo("reconcile", tunnelID, "Skipping start — WAN is down")
			return nil
		}
		if stored.PingCheck != nil && stored.PingCheck.IsDeadByMonitoring {
			s.logInfo("reconcile", tunnelID, "Skipping start — dead by monitoring")
			return nil
		}

		s.lockTunnel(tunnelID)
		defer s.unlockTunnel(tunnelID)

		// Tunnel may have been deleted while waiting for lock
		if !s.store.Exists(tunnelID) {
			return nil
		}

		// Re-check state after lock
		stateInfo := s.state.GetState(ctx, tunnelID)
		if stateInfo.State == tunnel.StateRunning {
			return nil
		}

		s.logInfo("reconcile", tunnelID, "Interface enabled in router UI, starting")
		if err := s.startInternal(ctx, tunnelID); err != nil {
			s.logWarn("reconcile", tunnelID, "Failed to start: "+err.Error())
			// Roll back NDMS state so tunnel doesn't stay in needs_start
			if rollbackErr := s.operator.InterfaceDown(ctx, tunnelID); rollbackErr != nil {
				s.logWarn("reconcile", tunnelID, "Failed to roll back InterfaceDown: "+rollbackErr.Error())
			}
			return err
		}
		stored.Enabled = true
		_ = s.store.Save(stored)

	case "disabled":
		// Intent DOWN — full stop (not KillLink).
		// KillLink for kernel does ip link set down (interface stays in sysfs),
		// so IsRunning returns true and dashboard shows "running".
		// Full Stop removes the interface entirely.
		// stopInternal fires OnReconcileStop hook itself.
		s.lockTunnel(tunnelID)
		defer s.unlockTunnel(tunnelID)

		// Tunnel may have been deleted while waiting for lock
		if !s.store.Exists(tunnelID) {
			return nil
		}

		s.logInfo("reconcile", tunnelID, "Interface disabled in router UI, stopping")
		s.suppressReconcile(tunnelID) // Stop calls InterfaceDown → suppress self-triggered hook
		if err := s.stopInternal(ctx, tunnelID); err != nil && err != tunnel.ErrNotRunning {
			s.logWarn("reconcile", tunnelID, "Failed to stop: "+err.Error())
		}

		// Re-read: stopInternal may have modified stored data (dead flag clear)
		if fresh, err := s.store.Get(tunnelID); err == nil {
			fresh.Enabled = false
			_ = s.store.Save(fresh)
		}
	}

	return nil
}

// findTunnelByNDMSName maps an NDMS interface name (e.g. "OpkgTun0")
// to its tunnel ID (e.g. "awg0") and stored data.
func (s *ServiceImpl) findTunnelByNDMSName(ndmsName string) (string, *storage.AWGTunnel) {
	tunnels, err := s.store.List()
	if err != nil {
		return "", nil
	}
	for i := range tunnels {
		names := tunnel.NewNames(tunnels[i].ID)
		if names.NDMSName == ndmsName {
			return tunnels[i].ID, &tunnels[i]
		}
	}
	return "", nil
}
