package ops

import (
	"context"
	"errors"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// TestOperatorOS4_Create_NoOp verifies Create is a no-op on OS4.
func TestOperatorOS4_Create_NoOp(t *testing.T) {
	backendMock := &MockBackend{}
	wgClient := &MockWGClient{}
	fw := &MockFirewall{}

	op := NewOperatorOS4(nil, wgClient, backendMock, fw, nil)

	cfg := tunnel.Config{
		ID:   "awg0",
		Name: "Test Tunnel",
	}

	err := op.Create(context.Background(), cfg)

	if err != nil {
		t.Fatalf("Create() should be no-op, got error: %v", err)
	}
	// No backend calls should happen
	if len(backendMock.StartCalls) != 0 {
		t.Errorf("Backend should not be started on Create")
	}
}

func TestOperatorOS4_Start_VerifySequence(t *testing.T) {
	// NOTE: Full Start test requires /opt/sbin/ip which isn't available in test env.
	// This test verifies the parts we CAN test: backend start and WG config.

	backendMock := &MockBackend{}
	wgClient := &MockWGClient{}
	fw := &MockFirewall{}

	op := NewOperatorOS4(nil, wgClient, backendMock, fw, nil)

	cfg := tunnel.Config{
		ID:       "awg0",
		Name:     "Test",
		Address:  "10.0.0.1",
		MTU:      1420,
		ConfPath: "/tmp/awg0.conf",
	}

	// Start will fail on ip command, but we can verify:
	// 1. Backend was started
	// 2. Backend was asked to wait ready
	// 3. Then it fails on ip address add

	err := op.Start(context.Background(), cfg)

	// Start will fail because /opt/sbin/ip doesn't exist
	if err == nil {
		t.Log("Start succeeded (running on Keenetic?)")
	} else {
		// Expected failure - verify backend was started before ip config
		if len(backendMock.StartCalls) != 1 {
			t.Errorf("Backend.Start not called before ip command")
		}
		if backendMock.StartCalls[0] != "awg0" {
			t.Errorf("Backend.Start iface = %s, want awg0", backendMock.StartCalls[0])
		}
	}
}

func TestOperatorOS4_Start_BackendFails(t *testing.T) {
	backendMock := &MockBackend{startError: errors.New("process failed")}
	wgClient := &MockWGClient{}
	fw := &MockFirewall{}

	op := NewOperatorOS4(nil, wgClient, backendMock, fw, nil)

	cfg := tunnel.Config{
		ID:       "awg0",
		Address:  "10.0.0.1",
		MTU:      1420,
		ConfPath: "/tmp/awg0.conf",
	}

	err := op.Start(context.Background(), cfg)

	if err == nil {
		t.Fatal("Start() should fail when backend fails")
	}
	// WG config should not be applied if backend fails
	if len(wgClient.SetConfCalls) != 0 {
		t.Errorf("WG.SetConf should not be called on backend failure")
	}
}

func TestOperatorOS4_Start_WGFails_Rollback(t *testing.T) {
	backendMock := &MockBackend{}
	wgClient := &MockWGClient{setConfError: errors.New("WG config failed")}
	fw := &MockFirewall{}

	op := NewOperatorOS4(nil, wgClient, backendMock, fw, nil)

	cfg := tunnel.Config{
		ID:       "awg0",
		Address:  "10.0.0.1",
		MTU:      1420,
		ConfPath: "/tmp/awg0.conf",
	}

	err := op.Start(context.Background(), cfg)

	if err == nil {
		t.Fatal("Start() should fail when WG config fails")
	}
	// Backend should be stopped on WG failure
	if len(backendMock.StopCalls) != 1 {
		t.Errorf("Backend.Stop should be called on WG failure")
	}
}

func TestOperatorOS4_Stop_Success(t *testing.T) {
	backendMock := &MockBackend{running: true}
	fw := &MockFirewall{}

	op := NewOperatorOS4(nil, &MockWGClient{}, backendMock, fw, nil)

	err := op.Stop(context.Background(), "awg0")

	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	// Verify firewall rules removed
	if len(fw.RemoveCalls) != 1 {
		t.Errorf("Firewall.RemoveRules not called")
	}

	// Verify backend stopped
	if len(backendMock.StopCalls) != 1 {
		t.Errorf("Backend.Stop not called")
	}
	if backendMock.StopCalls[0] != "awg0" {
		t.Errorf("Backend.Stop iface = %s, want awg0", backendMock.StopCalls[0])
	}
}

func TestOperatorOS4_Delete_SameAsStop(t *testing.T) {
	backendMock := &MockBackend{running: true}
	fw := &MockFirewall{}

	op := NewOperatorOS4(nil, &MockWGClient{}, backendMock, fw, nil)

	err := op.Delete(context.Background(), "awg0")

	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	// On OS4, Delete is the same as Stop
	if len(backendMock.StopCalls) != 1 {
		t.Errorf("Backend.Stop not called on Delete")
	}
}

func TestOperatorOS4_Recover(t *testing.T) {
	backendMock := &MockBackend{running: true}

	op := NewOperatorOS4(nil, &MockWGClient{}, backendMock, &MockFirewall{}, nil)

	state := tunnel.StateInfo{
		State:          tunnel.StateBroken,
		ProcessRunning: true,
		InterfaceUp:    false,
	}

	err := op.Recover(context.Background(), "awg0", state)

	if err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	// On OS4, recovery just stops everything
	if len(backendMock.StopCalls) != 1 {
		t.Errorf("Backend.Stop should be called for recovery")
	}
}

func TestOperatorOS4_InterfaceUp(t *testing.T) {
	op := NewOperatorOS4(nil, &MockWGClient{}, &MockBackend{}, &MockFirewall{}, nil)

	// This test verifies the method exists and doesn't panic
	// Actual ip command execution can't be tested without mocking exec
	err := op.InterfaceUp(context.Background(), "awg0")

	// Will fail because ip command doesn't exist in test environment
	// but that's expected - we're testing the interface, not the exec
	if err == nil {
		// If somehow ip exists (e.g., running tests on Keenetic), that's fine too
		t.Log("InterfaceUp succeeded (ip command available)")
	}
}

func TestOperatorOS4_InterfaceDown(t *testing.T) {
	op := NewOperatorOS4(nil, &MockWGClient{}, &MockBackend{}, &MockFirewall{}, nil)

	// Same as InterfaceUp - testing interface existence
	err := op.InterfaceDown(context.Background(), "awg0")

	if err == nil {
		t.Log("InterfaceDown succeeded (ip command available)")
	}
}

func TestOperatorOS4_ApplyConfig(t *testing.T) {
	wgClient := &MockWGClient{}
	op := NewOperatorOS4(nil, wgClient, &MockBackend{}, &MockFirewall{}, nil)

	err := op.ApplyConfig(context.Background(), "awg0", "/tmp/new.conf")

	if err != nil {
		t.Fatalf("ApplyConfig() error = %v", err)
	}
	if len(wgClient.SetConfCalls) != 1 {
		t.Errorf("WG.SetConf not called")
	}
	// On OS4, interface name is tunnel ID
	if wgClient.SetConfCalls[0].Iface != "awg0" {
		t.Errorf("SetConf iface = %s, want awg0", wgClient.SetConfCalls[0].Iface)
	}
}

func TestOperatorOS4_UsesDirectTunnelID(t *testing.T) {
	// Verify OS4 uses tunnel ID directly as interface name
	// (unlike OS5 which converts awg0 -> opkgtun0)

	backendMock := &MockBackend{}
	wgClient := &MockWGClient{}
	fw := &MockFirewall{}

	op := NewOperatorOS4(nil, wgClient, backendMock, fw, nil)

	cfg := tunnel.Config{
		ID:       "awg1", // Different ID to verify
		Address:  "10.0.0.1",
		MTU:      1420,
		ConfPath: "/tmp/awg1.conf",
	}

	// Start will fail on ip command, but backend should be started
	_ = op.Start(context.Background(), cfg)

	// Backend should use tunnel ID directly (verified even if Start fails later)
	if len(backendMock.StartCalls) == 0 {
		t.Fatal("Backend.Start not called")
	}
	if backendMock.StartCalls[0] != "awg1" {
		t.Errorf("Backend.Start iface = %s, want awg1 (direct ID)", backendMock.StartCalls[0])
	}

	// WG and Firewall calls may not happen if ip fails early,
	// but we verify the Stop behavior uses direct ID
	_ = op.Stop(context.Background(), "awg1")

	if len(backendMock.StopCalls) == 0 {
		t.Fatal("Backend.Stop not called")
	}
	if backendMock.StopCalls[0] != "awg1" {
		t.Errorf("Backend.Stop iface = %s, want awg1 (direct ID)", backendMock.StopCalls[0])
	}

	// Firewall should use tunnel ID directly (on Stop)
	if len(fw.RemoveCalls) == 0 {
		t.Fatal("Firewall.RemoveRules not called")
	}
	if fw.RemoveCalls[0] != "awg1" {
		t.Errorf("Firewall.RemoveRules iface = %s, want awg1 (direct ID)", fw.RemoveCalls[0])
	}
}

// === Endpoint route no-op tests (routing not managed on OS4) ===

func TestOperatorOS4_SetupEndpointRoute_NoOp(t *testing.T) {
	op := NewOperatorOS4(nil, &MockWGClient{}, &MockBackend{}, &MockFirewall{}, nil)

	ip, err := op.SetupEndpointRoute(context.Background(), "awgm0", "1.2.3.4:51820", "")
	if err != nil {
		t.Fatalf("SetupEndpointRoute() error = %v", err)
	}
	if ip != "" {
		t.Errorf("SetupEndpointRoute() = %q, want empty (no-op on OS4)", ip)
	}
}

func TestOperatorOS4_CleanupEndpointRoute_NoOp(t *testing.T) {
	op := NewOperatorOS4(nil, &MockWGClient{}, &MockBackend{}, &MockFirewall{}, nil)

	err := op.CleanupEndpointRoute(context.Background(), "awgm0")
	if err != nil {
		t.Errorf("CleanupEndpointRoute() error = %v", err)
	}
}

func TestOperatorOS4_GetTrackedEndpointIP_NoOp(t *testing.T) {
	op := NewOperatorOS4(nil, &MockWGClient{}, &MockBackend{}, &MockFirewall{}, nil)

	got := op.GetTrackedEndpointIP("awgm0")
	if got != "" {
		t.Errorf("GetTrackedEndpointIP() = %q, want empty (no-op on OS4)", got)
	}
}

func TestOperatorOS4_KillLink_StopsBackend(t *testing.T) {
	backendMock := &MockBackend{running: true}
	op := NewOperatorOS4(nil, &MockWGClient{}, backendMock, &MockFirewall{}, nil)

	err := op.KillLink(context.Background(), "awgm0")
	if err != nil {
		t.Fatalf("KillLink() error = %v", err)
	}
	if len(backendMock.StopCalls) != 1 {
		t.Errorf("Backend.Stop not called in KillLink")
	}
}
