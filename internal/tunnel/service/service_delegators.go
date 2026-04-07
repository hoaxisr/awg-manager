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
	return s.orch.HandleEvent(ctx, orchestrator.Event{
		Type: orchestrator.EventStart, Tunnel: tunnelID,
	})
}

// Stop delegates to orchestrator.
func (s *ServiceImpl) Stop(ctx context.Context, tunnelID string) error {
	if s.orch == nil {
		return fmt.Errorf("orchestrator not initialized")
	}
	return s.orch.HandleEvent(ctx, orchestrator.Event{
		Type: orchestrator.EventStop, Tunnel: tunnelID,
	})
}

// Restart delegates to orchestrator.
func (s *ServiceImpl) Restart(ctx context.Context, tunnelID string) error {
	if s.orch == nil {
		return fmt.Errorf("orchestrator not initialized")
	}
	return s.orch.HandleEvent(ctx, orchestrator.Event{
		Type: orchestrator.EventRestart, Tunnel: tunnelID,
	})
}

// Delete delegates to orchestrator.
func (s *ServiceImpl) Delete(ctx context.Context, tunnelID string) error {
	if s.orch == nil {
		return fmt.Errorf("orchestrator not initialized")
	}
	return s.orch.HandleEvent(ctx, orchestrator.Event{
		Type: orchestrator.EventDelete, Tunnel: tunnelID,
	})
}
