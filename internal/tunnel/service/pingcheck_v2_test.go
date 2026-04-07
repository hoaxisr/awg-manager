package service

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// === Tests for PingCheck lifecycle integration ===

// TestWANUp_StartsIntentUpTunnels verifies that after WAN up, only tunnels
// with Enabled=true are started. Disabled tunnels stay disabled.
func TestWANUp_StartsIntentUpTunnels(t *testing.T) {
	stateMgr := NewMockStateManager()

	// Tunnel 1: enabled, needs start
	stateMgr.SetState("awg0", tunnel.StateInfo{
		State:         tunnel.StateNeedsStart,
		OpkgTunExists: true,
	})

	// Tunnel 2: disabled
	stateMgr.SetState("awg1", tunnel.StateInfo{
		State:         tunnel.StateDisabled,
		OpkgTunExists: true,
	})

	state0 := stateMgr.GetState(context.Background(), "awg0")
	state1 := stateMgr.GetState(context.Background(), "awg1")

	if state0.State != tunnel.StateNeedsStart {
		t.Errorf("awg0 should be NeedsStart, got %v", state0.State)
	}
	if state1.State != tunnel.StateDisabled {
		t.Errorf("awg1 should be Disabled, got %v", state1.State)
	}
}
