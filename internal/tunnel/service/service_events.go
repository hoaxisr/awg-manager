package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
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

	// Count enabled kernel tunnels for UI log
	evalCount := 0
	for _, t := range tunnels {
		if t.Enabled && t.Backend != "nativewg" {
			evalCount++
		}
	}
	s.appLog.Full("wan-up", iface, fmt.Sprintf("Processing WAN up, %d tunnels to evaluate", evalCount))

	// Separate tunnels into direct (non-chained) and chained (tunnel:xxx).
	// Direct tunnels must start first so chained tunnels can resolve parent's WAN.
	var direct, chained []storage.AWGTunnel
	for _, t := range tunnels {
		if !t.Enabled {
			continue
		}

		// NativeWG: NDMS manages WAN routing natively, skip
		if t.Backend == "nativewg" {
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
			// Running tunnel with default route: ensure route is set
			// (may be missing if Start succeeded but SetDefaultRoute failed at boot)
			if t.DefaultRoute {
				s.ensureDefaultRoute(ctx, &t)
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

			s.appLog.Full("wan-up", t.ID, "Starting tunnel on WAN up")
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

			s.appLog.Full("wan-up", t.ID, "Starting chained tunnel on WAN up")
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
	// Prefer WAN model (priority-based, returns kernel name), fallback to route table
	currentGW, ok := s.wan.PreferredUp()
	if !ok {
		gwCtx, gwCancel := context.WithTimeout(ctx, 5*time.Second)
		defer gwCancel()
		// GetDefaultGatewayInterface returns NDMS ID → translate to kernel name
		ndmsID, err := s.legacyOperator.GetDefaultGatewayInterface(gwCtx)
		if err != nil {
			return // can't determine gateway, leave tunnel as-is
		}
		if kernelName := s.wan.NameForID(ndmsID); kernelName != "" {
			currentGW = kernelName
		} else {
			currentGW = s.legacyOperator.GetSystemName(gwCtx, ndmsID)
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

		if err := s.legacyOperator.KillLink(switchCtx, t.ID); err != nil {
			s.logWarn("wan_up", t.ID, "Gateway switch KillLink failed: "+err.Error())
			return
		}
		s.clearActiveWAN(t.ID)
		if err := s.startInternal(switchCtx, t.ID); err != nil {
			s.logWarn("wan_up", t.ID, "Gateway switch Start failed: "+err.Error())
		}
	}()
}

// ensureDefaultRoute attempts to set the default route for a running tunnel
// that has DefaultRoute=true. This handles the case where SetDefaultRoute
// failed during Start (e.g., boot race condition with NDMS not ready).
// The ndmc "ip route default" command is idempotent — safe to call even if
// the route already exists.
func (s *ServiceImpl) ensureDefaultRoute(ctx context.Context, t *storage.AWGTunnel) {
	routeCtx, routeCancel := context.WithTimeout(ctx, 10*time.Second)
	defer routeCancel()

	if err := s.legacyOperator.SetDefaultRoute(routeCtx, t.ID); err != nil {
		s.logWarn("wan_up", t.ID, "Failed to ensure default route: "+err.Error())
	} else {
		s.logInfo("wan_up", t.ID, "Default route ensured")
	}
}

// HandleWANDown is called when a WAN interface goes down.
// Kills only tunnels bound to this specific WAN. Auto-mode tunnels
// attempt immediate failover to another available gateway.
func (s *ServiceImpl) HandleWANDown(ctx context.Context, iface string) {
	s.logInfo("wan", "event", fmt.Sprintf("WAN down: %s", iface))
	s.appLog.Full("wan-down", iface, "Processing WAN down")

	tunnels, err := s.store.List()
	if err != nil {
		s.logWarn("wan_down", "list", "Failed to list tunnels: "+err.Error())
		return
	}

	for _, t := range tunnels {
		// NativeWG: NDMS manages tunnel lifecycle, skip
		if t.Backend == "nativewg" {
			continue
		}

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
			s.appLog.Full("wan-down", t.ID, "Killing tunnel (WAN down)")
			killCtx, killCancel := context.WithTimeout(wanCtx, 10*time.Second)
			defer killCancel()
			if err := s.legacyOperator.KillLink(killCtx, t.ID); err != nil {
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
				} else if _, err := s.legacyOperator.GetDefaultGatewayInterface(failoverCtx); err == nil {
					hasGW = true
				}
				if hasGW {
					s.logInfo("wan_down", t.ID, "Auto failover: another gateway available, restarting")
					_ = s.legacyOperator.Stop(failoverCtx, t.ID)
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
	// NativeWG: NDMS native ping-check handles dead detection
	if s.isNativeWGByID(tunnelID) {
		return nil
	}

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
	if err := s.legacyOperator.KillLink(ctx, tunnelID); err != nil {
		s.logWarn("monitor", tunnelID, "KillLink failed: "+err.Error())
		return err
	}
	s.clearActiveWAN(tunnelID)

	s.logInfo("monitor", tunnelID, "Tunnel marked as dead by monitoring")
	s.appLog.Warn("monitor-dead", tunnelID, "Tunnel marked dead by monitoring")
	return nil
}

// HandleForcedRestart is called by PingCheck when the dead interval timer fires.
// Executes exactly stopInternal + startInternal — identical to manual Stop + Start
// from the UI/API. stopInternal pauses the monitor goroutine (PauseMonitoring),
// startInternal resumes monitoring with a fresh goroutine (StartMonitoring).
func (s *ServiceImpl) HandleForcedRestart(ctx context.Context, tunnelID string) error {
	// NativeWG: NDMS native ping-check handles restart
	if s.isNativeWGByID(tunnelID) {
		return nil
	}

	if !s.wan.AnyUp() {
		return fmt.Errorf("WAN down, cannot restart")
	}

	s.suppressReconcile(tunnelID)
	s.lockTunnel(tunnelID)
	defer s.unlockTunnel(tunnelID)

	stored, err := s.store.Get(tunnelID)
	if err != nil {
		return nil // tunnel deleted
	}

	// Another operation (HandleWANUp, manual Start) already restarted the tunnel
	// and cleared the dead flag. Skip to avoid disrupting a healthy tunnel.
	if stored.PingCheck == nil || !stored.PingCheck.IsDeadByMonitoring {
		s.logInfo("monitor", tunnelID, "Skipping forced restart — dead flag already cleared")
		return nil
	}

	s.appLog.Info("forced-restart", tunnelID, "Forced restart initiated")

	// Full Stop — exactly like manual Stop from UI/API.
	if err := s.stopInternal(ctx, tunnelID); err != nil && err != tunnel.ErrNotRunning {
		s.logWarn("monitor", tunnelID, "Forced restart stop failed: "+err.Error())
	}

	// Full Start — exactly like manual Start from UI/API.
	if err := s.startInternal(ctx, tunnelID); err != nil {
		s.logWarn("monitor", tunnelID, "Forced restart start failed: "+err.Error())

		// Start failed — restore dead state and resume monitoring for automatic retry.
		// stopInternal already cleared IsDeadByMonitoring and paused the monitor.
		s.restoreDeadMonitoring(tunnelID)

		return err
	}

	s.logInfo("monitor", tunnelID, "Forced restart complete")
	return nil
}

// restoreDeadMonitoring re-marks a tunnel as dead and resumes monitoring.
// Used when forced restart's startInternal fails — ensures monitoring retries
// instead of dying permanently.
func (s *ServiceImpl) restoreDeadMonitoring(tunnelID string) {
	stored, err := s.store.Get(tunnelID)
	if err != nil {
		return
	}

	// Re-mark as dead in storage
	if stored.PingCheck != nil {
		now := time.Now().Format(time.RFC3339)
		stored.PingCheck.IsDeadByMonitoring = true
		stored.PingCheck.DeadSince = &now
		_ = s.store.Save(stored)
	}

	// Resume monitoring — will detect tunnel is down, re-enter dead state,
	// and retry forced restart after deadInterval.
	if s.reconcileHooks != nil {
		s.reconcileHooks.OnReconcileStart(tunnelID, stored.Name)
	}
}

// HandleMonitorRecovered is called when PingCheck detects tunnel recovery.
// Attempts a full restart. Returns error if restart fails — pingcheck stays
// in dead state and retries after DeadInterval.
func (s *ServiceImpl) HandleMonitorRecovered(ctx context.Context, tunnelID string) error {
	// NativeWG: NDMS native ping-check handles recovery
	if s.isNativeWGByID(tunnelID) {
		return nil
	}

	if !s.wan.AnyUp() {
		return fmt.Errorf("WAN down, cannot restart")
	}

	s.appLog.Info("monitor-recovered", tunnelID, "Tunnel recovered, restarting")

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
		// NativeWG: NDMS manages endpoint routing natively
		if t.Backend == "nativewg" {
			continue
		}
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
		ip, err := s.legacyOperator.RestoreEndpointTracking(ctx, t.ID, t.Peer.Endpoint, isp)
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
		// NativeWG: skip stale WAN cleanup (NDMS manages state)
		if t.Backend == "nativewg" {
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
		s.appLog.Debug("reconcile", tunnelID, "Skipping self-triggered hook")
		return nil
	}

	// Loop detection: block reconcile if NDMS is cycling the interface
	if s.isReconcileLoopBlocked(tunnelID) {
		s.logWarn("reconcile", tunnelID, fmt.Sprintf("Reconcile blocked (loop detected, level=%s). Manual start/stop from UI to reset.", level))
		s.appLog.Warn("reconcile", tunnelID, "Reconcile blocked (loop detected)")
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

		// Re-read stored after lock (may have been modified while waiting)
		var err error
		stored, err = s.store.Get(tunnelID)
		if err != nil {
			return nil
		}

		// Re-check state after lock using correct backend
		var stateInfo tunnel.StateInfo
		if stored.Backend == "nativewg" && s.nwgOperator != nil {
			stateInfo = s.nwgOperator.GetState(ctx, stored)
		} else {
			stateInfo = s.state.GetState(ctx, tunnelID)
		}
		if stateInfo.State == tunnel.StateRunning {
			return nil
		}

		s.logInfo("reconcile", tunnelID, "Interface enabled in router UI, starting")
		s.appLog.Full("reconcile", tunnelID, "Interface enabled in router UI, starting")
		s.suppressReconcile(tunnelID) // Start triggers NDMS hooks → suppress self-triggered reconcile
		if err := s.startInternal(ctx, tunnelID); err != nil {
			s.logWarn("reconcile", tunnelID, "Failed to start: "+err.Error())
			// Roll back NDMS state so tunnel doesn't stay in needs_start
			if stored.Backend != "nativewg" {
				if rollbackErr := s.legacyOperator.InterfaceDown(ctx, tunnelID); rollbackErr != nil {
					s.logWarn("reconcile", tunnelID, "Failed to roll back InterfaceDown: "+rollbackErr.Error())
				}
			}
			return err
		}
		// Re-read: startInternal may have modified stored data
		if fresh, err := s.store.Get(tunnelID); err == nil {
			fresh.Enabled = true
			_ = s.store.Save(fresh)
		}

	case "disabled":
		// Loop detection: record disabled event
		if s.recordDisabledEvent(tunnelID) {
			s.logWarn("reconcile", tunnelID, "Loop detected: too many disabled events. Blocking reconcile for 5 minutes. Manual start/stop from UI to reset.")
			return nil
		}

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
		s.appLog.Full("reconcile", tunnelID, "Interface disabled in router UI, stopping")
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
		t := &tunnels[i]
		if t.Backend == "nativewg" {
			// NativeWG: match by WireguardX name
			names := nwg.NewNWGNames(t.NWGIndex)
			if names.NDMSName == ndmsName {
				return t.ID, t
			}
		} else {
			// Kernel: match by OpkgTunX name
			names := tunnel.NewNames(t.ID)
			if names.NDMSName == ndmsName {
				return t.ID, t
			}
		}
	}
	return "", nil
}
