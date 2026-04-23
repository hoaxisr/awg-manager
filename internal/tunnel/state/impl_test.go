package state

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/backend"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wg"
)

// Pre-built InterfaceDetails fixtures matching the legacy "show interface"
// output templates. Used across state tests.
var (
	// conf: running, link: up — fully operational tunnel
	detailsRunning = &ndms.InterfaceDetails{
		State: "up", Link: "up", Connected: true, ConfLayer: "running",
	}
	// conf: disabled, link: down — admin turned off
	detailsDisabled = &ndms.InterfaceDetails{
		State: "down", Link: "down", Connected: false, ConfLayer: "disabled",
	}
	// conf: running, link: down — needs start (after reboot / kill)
	detailsNeedsStart = &ndms.InterfaceDetails{
		State: "up", Link: "down", Connected: false, ConfLayer: "running",
	}
)

// MockNDMSClient is a mock InterfaceQueries for state tests.
// It satisfies the narrow state.InterfaceQueries interface
// (Get + GetDetails).
type MockNDMSClient struct {
	// opkgTunExists controls whether Get returns a non-nil Interface.
	opkgTunExists bool
	// details is what GetDetails returns. If nil, GetDetails returns
	// (nil, error) to simulate an unreachable NDMS (legacy: ShowInterface
	// returning empty / parse error).
	details *ndms.InterfaceDetails
}

func (m *MockNDMSClient) Get(_ context.Context, name string) (*ndms.Interface, error) {
	if !m.opkgTunExists {
		return nil, nil
	}
	return &ndms.Interface{ID: name}, nil
}

func (m *MockNDMSClient) GetDetails(_ context.Context, _ string) (*ndms.InterfaceDetails, error) {
	if m.details == nil {
		return nil, errors.New("show interface failed")
	}
	return m.details, nil
}

// MockWGClient is a mock WireGuard client for testing.
type MockWGClient struct {
	hasPeer       bool
	lastHandshake time.Time
	rxBytes       int64
	txBytes       int64
	showError     error
}

func (m *MockWGClient) SetConf(ctx context.Context, iface, confPath string) error { return nil }
func (m *MockWGClient) Show(ctx context.Context, iface string) (*wg.ShowResult, error) {
	if m.showError != nil {
		return nil, m.showError
	}
	return &wg.ShowResult{
		HasPeer:       m.hasPeer,
		LastHandshake: m.lastHandshake,
		RxBytes:       m.rxBytes,
		TxBytes:       m.txBytes,
	}, nil
}
func (m *MockWGClient) RemovePeer(ctx context.Context, iface, publicKey string) error { return nil }
func (m *MockWGClient) GetPeerPublicKey(ctx context.Context, iface string) (string, error) {
	if m.hasPeer {
		return "mock-peer-key", nil
	}
	return "", nil
}

// MockBackend is a mock backend for testing.
type MockBackend struct {
	running bool
	pid     int
}

func (m *MockBackend) Type() backend.Type { return backend.TypeKernel }
func (m *MockBackend) Start(ctx context.Context, ifaceName string) error {
	return nil
}
func (m *MockBackend) Stop(ctx context.Context, ifaceName string) error { return nil }
func (m *MockBackend) IsRunning(ctx context.Context, ifaceName string) (bool, int) {
	return m.running, m.pid
}
func (m *MockBackend) WaitReady(ctx context.Context, ifaceName string, timeout time.Duration) error {
	return nil
}

func TestManagerImpl_GetState_NotCreated(t *testing.T) {
	mgr := New(
		&MockNDMSClient{opkgTunExists: false},
		&MockWGClient{},
		&MockBackend{},
		nil,
	)

	state := mgr.GetState(context.Background(), "awg0")

	if state.State != tunnel.StateNotCreated {
		t.Errorf("State = %v, want StateNotCreated", state.State)
	}
	if state.OpkgTunExists {
		t.Error("OpkgTunExists should be false")
	}
}

// TestManagerImpl_GetState_Disabled tests: OpkgTun exists, conf: disabled, no process.
// v1 called this "Stopped". v2 calls it "Disabled" (NDMS intent = down).
func TestManagerImpl_GetState_Disabled(t *testing.T) {
	mgr := New(
		&MockNDMSClient{opkgTunExists: true, details: detailsDisabled},
		&MockWGClient{},
		&MockBackend{running: false},
		nil,
	)

	state := mgr.GetState(context.Background(), "awg0")

	if state.State != tunnel.StateDisabled {
		t.Errorf("State = %v, want StateDisabled", state.State)
	}
	if !state.OpkgTunExists {
		t.Error("OpkgTunExists should be true")
	}
	if state.InterfaceUp {
		t.Error("InterfaceUp should be false")
	}
	if state.ProcessRunning {
		t.Error("ProcessRunning should be false")
	}
}

func TestManagerImpl_GetState_Running(t *testing.T) {
	mgr := New(
		&MockNDMSClient{opkgTunExists: true, details: detailsRunning},
		&MockWGClient{hasPeer: true, lastHandshake: time.Now(), rxBytes: 1000, txBytes: 500},
		&MockBackend{running: true, pid: 12345},
		nil,
	)
	mgr.deviceExists = func(string) bool { return true }

	state := mgr.GetState(context.Background(), "awg0")

	if state.State != tunnel.StateRunning {
		t.Errorf("State = %v, want StateRunning", state.State)
	}
	if !state.OpkgTunExists {
		t.Error("OpkgTunExists should be true")
	}
	if !state.InterfaceUp {
		t.Error("InterfaceUp should be true")
	}
	if !state.ProcessRunning {
		t.Error("ProcessRunning should be true")
	}
	if state.ProcessPID != 12345 {
		t.Errorf("ProcessPID = %d, want 12345", state.ProcessPID)
	}
	if !state.HasPeer {
		t.Error("HasPeer should be true")
	}
	if !state.HasHandshake {
		t.Error("HasHandshake should be true")
	}
	if state.RxBytes != 1000 {
		t.Errorf("RxBytes = %d, want 1000", state.RxBytes)
	}
	if state.TxBytes != 500 {
		t.Errorf("TxBytes = %d, want 500", state.TxBytes)
	}
}

// TestManagerImpl_GetState_Starting tests: conf: running, process alive, link not up yet.
// v1 called this "Broken". v2 calls it "Starting".
func TestManagerImpl_GetState_Starting(t *testing.T) {
	mgr := New(
		&MockNDMSClient{opkgTunExists: true, details: detailsNeedsStart},
		&MockWGClient{},
		&MockBackend{running: true, pid: 12345},
		nil,
	)

	state := mgr.GetState(context.Background(), "awg0")

	if state.State != tunnel.StateStarting {
		t.Errorf("State = %v, want StateStarting", state.State)
	}
	if !state.ProcessRunning {
		t.Error("ProcessRunning should be true")
	}
	if state.InterfaceUp {
		t.Error("InterfaceUp should be false")
	}
}

// TestManagerImpl_GetState_NeedsStart tests: conf: running, no process (after reboot).
// v1 called this "Broken" (interfaceUp=true from stale NDMS, process=false).
// v2 correctly identifies this as NeedsStart via conf layer.
func TestManagerImpl_GetState_NeedsStart(t *testing.T) {
	mgr := New(
		&MockNDMSClient{opkgTunExists: true, details: detailsNeedsStart},
		&MockWGClient{},
		&MockBackend{running: false},
		nil,
	)

	state := mgr.GetState(context.Background(), "awg0")

	if state.State != tunnel.StateNeedsStart {
		t.Errorf("State = %v, want StateNeedsStart", state.State)
	}
}

// TestManagerImpl_GetState_NeedsStop tests: conf: disabled, process still alive.
// Happens when user toggles off in router UI.
func TestManagerImpl_GetState_NeedsStop(t *testing.T) {
	mgr := New(
		&MockNDMSClient{opkgTunExists: true, details: detailsDisabled},
		&MockWGClient{},
		&MockBackend{running: true, pid: 12345},
		nil,
	)

	state := mgr.GetState(context.Background(), "awg0")

	if state.State != tunnel.StateNeedsStop {
		t.Errorf("State = %v, want StateNeedsStop", state.State)
	}
}

// TestManagerImpl_GetState_RunningNoPeer tests: link up, process alive, no peer.
// v1 called this "Broken". v2 calls it "Running" (peer is not required for Running).
func TestManagerImpl_GetState_RunningNoPeer(t *testing.T) {
	mgr := New(
		&MockNDMSClient{opkgTunExists: true, details: detailsRunning},
		&MockWGClient{hasPeer: false},
		&MockBackend{running: true, pid: 12345},
		nil,
	)
	mgr.deviceExists = func(string) bool { return true }

	state := mgr.GetState(context.Background(), "awg0")

	if state.State != tunnel.StateRunning {
		t.Errorf("State = %v, want StateRunning", state.State)
	}
}

// TestManagerImpl_GetState_ShowInterfaceFails tests graceful degradation:
// when ShowInterface fails, intent defaults to IntentDown (safe),
// so with no process → Disabled.
func TestManagerImpl_GetState_ShowInterfaceFails(t *testing.T) {
	mgr := New(
		&MockNDMSClient{opkgTunExists: true, details: nil},
		&MockWGClient{},
		&MockBackend{running: false},
		nil,
	)

	state := mgr.GetState(context.Background(), "awg0")

	// IntentDown (zero value) + no process → Disabled (safe default)
	if state.State != tunnel.StateDisabled {
		t.Errorf("State = %v, want StateDisabled (safe fallback)", state.State)
	}
}

func TestManagerImpl_GetState_Details(t *testing.T) {
	tests := []struct {
		name    string
		ndms    *MockNDMSClient
		wg      *MockWGClient
		backend *MockBackend
		wantIn  string // substring expected in Details
	}{
		{
			name:    "not created",
			ndms:    &MockNDMSClient{opkgTunExists: false},
			wg:      &MockWGClient{},
			backend: &MockBackend{},
			wantIn:  "not been created",
		},
		{
			name:    "disabled",
			ndms:    &MockNDMSClient{opkgTunExists: true, details: detailsDisabled},
			wg:      &MockWGClient{},
			backend: &MockBackend{},
			wantIn:  "disabled",
		},
		{
			name:    "running with handshake",
			ndms:    &MockNDMSClient{opkgTunExists: true, details: detailsRunning},
			wg:      &MockWGClient{hasPeer: true, lastHandshake: time.Now(), rxBytes: 100},
			backend: &MockBackend{running: true},
			wantIn:  "running",
		},
		{
			name:    "needs start",
			ndms:    &MockNDMSClient{opkgTunExists: true, details: detailsNeedsStart},
			wg:      &MockWGClient{},
			backend: &MockBackend{},
			wantIn:  "needs start",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := New(tt.ndms, tt.wg, tt.backend, nil)
			mgr.deviceExists = func(string) bool { return true }
			state := mgr.GetState(context.Background(), "awg0")

			if !containsSubstring(state.Details, tt.wantIn) {
				t.Errorf("Details = %q, want to contain %q", state.Details, tt.wantIn)
			}
		})
	}
}

func containsSubstring(s, substr string) bool {
	if len(substr) > len(s) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestManagerImpl_GetState_NamesConversion(t *testing.T) {
	names := tunnel.NewNames("awg0")
	if names.NDMSName != "OpkgTun0" {
		t.Errorf("NDMSName = %q, want OpkgTun0", names.NDMSName)
	}
	if names.IfaceName != "opkgtun0" {
		t.Errorf("IfaceName = %q, want opkgtun0", names.IfaceName)
	}

	mgr := New(&MockNDMSClient{opkgTunExists: true}, &MockWGClient{}, &MockBackend{}, nil)
	_ = mgr.GetState(context.Background(), "awg0")
}
