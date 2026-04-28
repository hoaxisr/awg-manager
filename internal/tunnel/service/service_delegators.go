package service

import (
	"context"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/orchestrator"
)

// Start delegates to orchestrator.
func (s *ServiceImpl) Start(ctx context.Context, tunnelID string) error {
	if s.orch == nil {
		return fmt.Errorf("orchestrator not initialized")
	}
	err := s.orch.HandleEvent(ctx, orchestrator.Event{
		Type: orchestrator.EventStart, Tunnel: tunnelID,
	})
	if err == nil {
		s.notifyAWGSyncer(ctx)
	}
	return err
}

// Stop delegates to orchestrator.
func (s *ServiceImpl) Stop(ctx context.Context, tunnelID string) error {
	if s.orch == nil {
		return fmt.Errorf("orchestrator not initialized")
	}
	err := s.orch.HandleEvent(ctx, orchestrator.Event{
		Type: orchestrator.EventStop, Tunnel: tunnelID,
	})
	if err == nil {
		s.notifyAWGSyncer(ctx)
	}
	return err
}

// Restart delegates to orchestrator.
func (s *ServiceImpl) Restart(ctx context.Context, tunnelID string) error {
	if s.orch == nil {
		return fmt.Errorf("orchestrator not initialized")
	}
	err := s.orch.HandleEvent(ctx, orchestrator.Event{
		Type: orchestrator.EventRestart, Tunnel: tunnelID,
	})
	if err == nil {
		s.notifyAWGSyncer(ctx)
	}
	return err
}

// Delete delegates to orchestrator. Refuses with ErrTunnelReferenced if
// the tunnel's tag is in the deviceproxy selector or referenced by any
// router rule — the deletion would otherwise leave dangling references
// that FATAL sing-box on next reload.
func (s *ServiceImpl) Delete(ctx context.Context, tunnelID string) error {
	if err := checkTunnelReferences(tunnelID, s.deviceProxyRefs, s.routerRefs); err != nil {
		return err
	}
	if s.orch == nil {
		return fmt.Errorf("orchestrator not initialized")
	}
	err := s.orch.HandleEvent(ctx, orchestrator.Event{
		Type: orchestrator.EventDelete, Tunnel: tunnelID,
	})
	if err == nil {
		s.notifyAWGSyncer(ctx)
	}
	return err
}
