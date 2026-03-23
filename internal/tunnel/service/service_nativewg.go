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
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
)

// === NativeWG lifecycle operations ===
// NativeWG tunnels use NDMS-managed WireGuard interfaces.
// Lifecycle is simpler than kernel tunnels — NDMS handles most state management.

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
	stored.Enabled = true
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
	stored.Enabled = false
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

// deleteNativeWG deletes a NativeWG tunnel (assumes lock is held).
func (s *ServiceImpl) deleteNativeWG(ctx context.Context, stored *storage.AWGTunnel) error {
	s.fireDeleteHooks(ctx, stored.ID)

	if s.nwgOperator != nil {
		if err := s.nwgOperator.Delete(ctx, stored); err != nil {
			s.appLog.Warn("delete", stored.ID, "Failed to delete NativeWG: "+err.Error())
			return err
		}
	}

	confPath := filepath.Join(confDir, stored.ID+".conf")
	_ = os.Remove(confPath)
	_ = s.store.Delete(stored.ID)

	s.logInfo("delete", stored.ID, "NativeWG tunnel deleted")
	s.appLog.Info("delete", stored.ID, "Tunnel deleted")
	return nil
}

// reconcileNativeWG handles NDMS hook for NativeWG tunnels.
// Called when user toggles interface in router UI (iflayerchanged.d hook).
func (s *ServiceImpl) reconcileNativeWG(ctx context.Context, tunnelID string, stored *storage.AWGTunnel, level string) error {
	// Skip self-triggered hooks (our Start/Stop sets beginOperation flag).
	if s.isOperating(tunnelID) {
		return nil
	}

	switch level {
	case "running":
		if !s.wan.AnyUp() {
			return nil
		}
		s.lockTunnel(tunnelID)
		defer s.unlockTunnel(tunnelID)
		if !s.store.Exists(tunnelID) {
			return nil
		}
		stored, err := s.store.Get(tunnelID)
		if err != nil {
			return nil
		}
		if s.nwgOperator != nil {
			if info := s.nwgOperator.GetState(ctx, stored); info.State == tunnel.StateRunning {
				return nil
			}
		}
		if err := s.startInternal(ctx, tunnelID); err != nil {
			s.logWarn("reconcile", tunnelID, "NativeWG start failed: "+err.Error())
			return err
		}
	case "disabled":
		s.lockTunnel(tunnelID)
		defer s.unlockTunnel(tunnelID)
		if !s.store.Exists(tunnelID) {
			return nil
		}
		if err := s.stopInternal(ctx, tunnelID); err != nil && err != tunnel.ErrNotRunning {
			s.logWarn("reconcile", tunnelID, "NativeWG stop failed: "+err.Error())
		}
	}
	return nil
}
