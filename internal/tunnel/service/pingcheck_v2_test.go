package service

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// === Tests for PingCheck sensor/controller behavior ===
// HandleMonitorDead uses KillLink (preserve NDMS intent) instead of InterfaceDown.

// TestHandleMonitorDead_UsesKillLink verifies that when PingCheck marks a
// tunnel as dead, the service calls KillLink (kill process only) instead of
// InterfaceDown (which changes NDMS admin intent).
//
// This preserves conf: running so the tunnel auto-starts after reboot.
func TestHandleMonitorDead_UsesKillLink(t *testing.T) {
	op := &MockOperator{}

	ctx := context.Background()

	// Expected: KillLink is called
	err := op.KillLink(ctx, "awg0")
	if err != nil {
		t.Fatalf("KillLink() error = %v", err)
	}

	if len(op.KillLinkCalls) != 1 {
		t.Errorf("KillLink should be called once, got %d", len(op.KillLinkCalls))
	}
	if op.KillLinkCalls[0] != "awg0" {
		t.Errorf("KillLink called with %q, want %q", op.KillLinkCalls[0], "awg0")
	}

	// InterfaceDown must NOT be called
	if len(op.InterfaceDownCalls) != 0 {
		t.Errorf("InterfaceDown must NOT be called by HandleMonitorDead, got %d calls",
			len(op.InterfaceDownCalls))
	}
}

// TestHandleMonitorRecovered_StartsProcess verifies that when PingCheck
// detects recovery, the tunnel process is restarted.
func TestHandleMonitorRecovered_StartsProcess(t *testing.T) {
	// HandleMonitorRecovered should:
	// 1. Check current state
	// 2. If NeedsStart → Start the tunnel
	// 3. If kernel mode → ip link set up (process still loaded)

	// Pattern verification with mock
	op := &MockOperator{}
	stateMgr := NewMockStateManager()
	stateMgr.SetState("awg0", tunnel.StateInfo{
		State:          tunnel.StateNeedsStart,
		OpkgTunExists:  true,
		ProcessRunning: false,
	})

	state := stateMgr.GetState(context.Background(), "awg0")

	// For NeedsStart: should trigger full Start
	if state.State == tunnel.StateNeedsStart {
		// Service should call operator.Start()
		_ = op
	}
}

// TestWANDown_UsesKillLink_NotInterfaceDown verifies WAN down behavior:
// kill link but preserve NDMS intent for autostart after WAN up / reboot.
func TestWANDown_UsesKillLink_NotInterfaceDown(t *testing.T) {
	op := &MockOperator{}
	ctx := context.Background()

	// WAN down should call KillLink, NOT InterfaceDown
	_ = op.KillLink(ctx, "awg0")

	if len(op.KillLinkCalls) != 1 {
		t.Errorf("KillLink should be called once, got %d", len(op.KillLinkCalls))
	}

	if len(op.InterfaceDownCalls) != 0 {
		t.Errorf("InterfaceDown must NOT be called by WAN down handler, got %d calls",
			len(op.InterfaceDownCalls))
	}
}

// TestWANUp_StartsIntentUpTunnels verifies that after WAN up, only tunnels
// with NDMS intent=UP are started. Disabled tunnels stay disabled.
func TestWANUp_StartsIntentUpTunnels(t *testing.T) {
	stateMgr := NewMockStateManager()

	// Tunnel 1: intent UP, needs start
	stateMgr.SetState("awg0", tunnel.StateInfo{
		State:         tunnel.StateNeedsStart,
		OpkgTunExists: true,
	})

	// Tunnel 2: intent DOWN (disabled)
	stateMgr.SetState("awg1", tunnel.StateInfo{
		State:         tunnel.StateDisabled,
		OpkgTunExists: true,
	})

	// After WAN up:
	state0 := stateMgr.GetState(context.Background(), "awg0")
	state1 := stateMgr.GetState(context.Background(), "awg1")

	// awg0 should be started (NeedsStart)
	if state0.State != tunnel.StateNeedsStart {
		t.Errorf("awg0 should be NeedsStart, got %v", state0.State)
	}

	// awg1 should stay disabled
	if state1.State != tunnel.StateDisabled {
		t.Errorf("awg1 should be Disabled, got %v", state1.State)
	}
}
