package service

import (
	"context"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
)

// === WAN Event Handlers ===

// HandleWANUp is called when a WAN interface comes up.
// Kernel tunnels: delegated to lifecycle Manager.
// NativeWG+proxy (< 5.01.A.3): resume proxy via startProxy().
// NativeWG+native ASC (>= 5.01.A.3): NDMS manages reconnect, skipped.
func (s *ServiceImpl) HandleWANUp(ctx context.Context, iface string) {
	if s.nwgOperator != nil && !ndmsinfo.SupportsWireguardASC() {
		if s.lifecycleManager.IsBootInProgress() {
			s.logInfo("wan_up", "nwg", "NativeWG proxy resume deferred — boot in progress")
		} else {
			s.resumeNativeWGProxies(ctx)
		}
	}
	s.lifecycleManager.HandleWANUp(ctx, iface)
}

// HandleWANDown is called when a WAN interface goes down.
// Kernel tunnels: delegated to lifecycle Manager.
// NativeWG+proxy (< 5.01.A.3): suspend proxy (kill kmod entry, disconnect peer).
// NativeWG+native ASC (>= 5.01.A.3): NDMS manages, skipped.
func (s *ServiceImpl) HandleWANDown(ctx context.Context, iface string) {
	if s.nwgOperator != nil && !ndmsinfo.SupportsWireguardASC() {
		if s.lifecycleManager.IsBootInProgress() {
			s.logInfo("wan_down", "nwg", "NativeWG proxy suspend deferred — boot in progress")
		} else {
			s.suspendNativeWGProxies(ctx)
			// If another WAN is still up, immediately resume proxies through
			// the surviving WAN. Without this, tunnels stay suspended until
			// a wan-up event fires — which never happens for an already-up
			// backup interface.
			if _, ok := s.wan.PreferredUp(); ok {
				s.logInfo("wan_down", "nwg", "Another WAN available — resuming NativeWG proxies")
				s.resumeNativeWGProxies(ctx)
			}
		}
	}
	s.lifecycleManager.HandleWANDown(ctx, iface)
}

// === PingCheck Integration ===

// HandleMonitorDead is called when PingCheck detects a dead tunnel.
// Kernel tunnels: delegated to lifecycle Manager.
func (s *ServiceImpl) HandleMonitorDead(ctx context.Context, tunnelID string) error {
	if s.isNativeWGByID(tunnelID) {
		return nil
	}
	return s.lifecycleManager.HandlePingDead(ctx, tunnelID)
}

// HandleForcedRestart is called by PingCheck when the dead interval timer fires.
// Kernel tunnels: delegated to lifecycle Manager as PingRetry event.
func (s *ServiceImpl) HandleForcedRestart(ctx context.Context, tunnelID string) error {
	if s.isNativeWGByID(tunnelID) {
		return nil
	}
	return s.lifecycleManager.HandlePingRetry(ctx, tunnelID)
}

// HandleMonitorRecovered is called when PingCheck detects tunnel recovery.
// Kernel tunnels: delegated to lifecycle Manager as PingRetry event.
func (s *ServiceImpl) HandleMonitorRecovered(ctx context.Context, tunnelID string) error {
	if s.isNativeWGByID(tunnelID) {
		return nil
	}
	return s.lifecycleManager.HandlePingRetry(ctx, tunnelID)
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
			s.store.ClearRuntimeState(t.ID)
		}
	}

	return nil
}

// ReconcileInterface handles an NDMS interface state change event.
// Called by iflayerchanged.d hook when user toggles interface in router UI.
// Handles three cases:
//  1. Managed NativeWG tunnel → reconcileNativeWG
//  2. Managed kernel tunnel → lifecycle Manager
//  3. System interface with client routes → OnTunnelStart/OnTunnelStop
func (s *ServiceImpl) ReconcileInterface(ctx context.Context, ndmsName, layer, level string) error {
	if layer != "conf" {
		return nil
	}

	// Case 1 & 2: Managed tunnel (NativeWG or kernel).
	tunnelID, stored := s.findTunnelByNDMSName(ndmsName)
	if tunnelID != "" {
		if stored.Backend == "nativewg" {
			return s.reconcileNativeWG(ctx, tunnelID, stored, level)
		}
		s.lifecycleManager.HandleUserToggle(ctx, tunnelID, level)
		return nil
	}

	// Case 3: System interface — check if any client routes reference it.
	if s.clientRouteHooks == nil {
		return nil
	}
	systemTunnelID := tunnel.SystemTunnelPrefix + ndmsName
	if !s.clientRouteHooks.HasRoutesForTunnel(systemTunnelID) {
		return nil
	}

	switch level {
	case "running":
		kernelIface := s.legacyOperator.GetSystemName(ctx, ndmsName)
		if kernelIface == "" || kernelIface == ndmsName {
			return nil
		}
		s.logInfo("reconcile", systemTunnelID, "System interface up, applying client routes")
		if err := s.clientRouteHooks.OnTunnelStart(ctx, systemTunnelID, kernelIface); err != nil {
			s.logWarn("reconcile", systemTunnelID, "OnTunnelStart failed: "+err.Error())
		}
	case "disabled":
		s.logInfo("reconcile", systemTunnelID, "System interface down, removing client routes")
		if err := s.clientRouteHooks.OnTunnelStop(ctx, systemTunnelID); err != nil {
			s.logWarn("reconcile", systemTunnelID, "OnTunnelStop failed: "+err.Error())
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

// suspendNativeWGProxies suspends all running NativeWG+proxy tunnels on WAN down.
// Kills kmod proxy entry and disconnects peer (conf stays "running" = intent preserved).
func (s *ServiceImpl) suspendNativeWGProxies(ctx context.Context) {
	tunnels, err := s.store.List()
	if err != nil {
		s.logWarn("wan_down", "nwg", "Failed to list tunnels: "+err.Error())
		return
	}
	for i := range tunnels {
		t := &tunnels[i]
		if t.Backend != "nativewg" || !t.Enabled {
			continue
		}
		info := s.nwgOperator.GetState(ctx, t)
		if info.State != tunnel.StateRunning {
			continue
		}
		s.beginOperation(t.ID)
		s.lockTunnel(t.ID)
		s.logInfo("wan_down", t.ID, "Suspending NativeWG proxy")
		if err := s.nwgOperator.SuspendProxy(ctx, t); err != nil {
			s.logWarn("wan_down", t.ID, "SuspendProxy failed: "+err.Error())
		}
		s.unlockTunnel(t.ID)
		s.endOperation(t.ID)
	}
}

// resumeNativeWGProxies resumes all enabled NativeWG+proxy tunnels on WAN up.
// Calls startNativeWG which routes through startProxy() — creates new kmod proxy, reconnects peer.
func (s *ServiceImpl) resumeNativeWGProxies(ctx context.Context) {
	tunnels, err := s.store.List()
	if err != nil {
		s.logWarn("wan_up", "nwg", "Failed to list tunnels: "+err.Error())
		return
	}
	for i := range tunnels {
		t := &tunnels[i]
		if t.Backend != "nativewg" || !t.Enabled {
			continue
		}
		// Only resume tunnels that were suspended (not already running).
		info := s.nwgOperator.GetState(ctx, t)
		if info.State == tunnel.StateRunning {
			continue
		}
		s.beginOperation(t.ID)
		s.lockTunnel(t.ID)
		s.logInfo("wan_up", t.ID, "Resuming NativeWG proxy")
		if err := s.startNativeWG(ctx, t); err != nil {
			s.logWarn("wan_up", t.ID, "Resume proxy failed: "+err.Error())
		}
		s.unlockTunnel(t.ID)
		s.endOperation(t.ID)
	}
}
