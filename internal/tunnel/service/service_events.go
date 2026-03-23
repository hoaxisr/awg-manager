package service

import (
	"context"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
)

// === WAN Event Handlers ===

// HandleWANUp is called when a WAN interface comes up.
// Kernel tunnels: delegated to lifecycle Manager.
// NativeWG: NDMS manages routing natively, skipped.
func (s *ServiceImpl) HandleWANUp(ctx context.Context, iface string) {
	s.lifecycleManager.HandleWANUp(ctx, iface)
}


// HandleWANDown is called when a WAN interface goes down.
// Kernel tunnels: delegated to lifecycle Manager.
func (s *ServiceImpl) HandleWANDown(ctx context.Context, iface string) {
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
func (s *ServiceImpl) ReconcileInterface(ctx context.Context, ndmsName, layer, level string) error {
	if layer != "conf" {
		return nil
	}

	// Map NDMS name (OpkgTun0) -> tunnel ID (awg0)
	tunnelID, stored := s.findTunnelByNDMSName(ndmsName)
	if tunnelID == "" {
		return nil // Not our interface
	}

	// NativeWG: keep own reconcile logic (lifecycle Manager is kernel-only).
	if stored.Backend == "nativewg" {
		return s.reconcileNativeWG(ctx, tunnelID, stored, level)
	}

	// Kernel: delegate to lifecycle Manager.
	s.lifecycleManager.HandleUserToggle(ctx, tunnelID, level)
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
