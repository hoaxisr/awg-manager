package state

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/backend"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wg"
)

// Minimal NDMS "show interface" output templates for tests.
const (
	// conf: running, link: up — fully operational tunnel
	ndmsRunning = `
            state: up
             link: up
        connected: yes
          summary:
                layer:
                     conf: running
`
	// conf: disabled, link: down — admin turned off
	ndmsDisabled = `
            state: down
             link: down
        connected: no
          summary:
                layer:
                     conf: disabled
`
	// conf: running, link: down — needs start (after reboot / kill)
	ndmsNeedsStart = `
            state: up
             link: down
        connected: no
          summary:
                layer:
                     conf: running
`
)

// MockNDMSClient is a mock NDMS client for testing.
type MockNDMSClient struct {
	opkgTunExists       bool
	showInterfaceOutput string
	showInterfaceError  error
}

func (m *MockNDMSClient) ShowInterface(ctx context.Context, name string) (string, error) {
	return m.showInterfaceOutput, m.showInterfaceError
}
func (m *MockNDMSClient) CreateOpkgTun(ctx context.Context, name, description string) error {
	return nil
}
func (m *MockNDMSClient) DeleteOpkgTun(ctx context.Context, name string) error { return nil }
func (m *MockNDMSClient) OpkgTunExists(ctx context.Context, name string) bool {
	return m.opkgTunExists
}
func (m *MockNDMSClient) SetAddress(ctx context.Context, name, address string) error {
	return nil
}
func (m *MockNDMSClient) SetIPv6Address(ctx context.Context, name, address string) error { return nil }
func (m *MockNDMSClient) ClearIPv6Address(ctx context.Context, name string)             {}
func (m *MockNDMSClient) SetMTU(ctx context.Context, name string, mtu int) error         { return nil }
func (m *MockNDMSClient) SetDescription(ctx context.Context, name, description string) error {
	return nil
}
func (m *MockNDMSClient) InterfaceUp(ctx context.Context, name string) error   { return nil }
func (m *MockNDMSClient) InterfaceDown(ctx context.Context, name string) error { return nil }
func (m *MockNDMSClient) SetDefaultRoute(ctx context.Context, name string) error    { return nil }
func (m *MockNDMSClient) RemoveDefaultRoute(ctx context.Context, name string) error { return nil }
func (m *MockNDMSClient) RemoveHostRoute(ctx context.Context, host string) error    { return nil }
func (m *MockNDMSClient) SetIPv6DefaultRoute(ctx context.Context, name string) error {
	return nil
}
func (m *MockNDMSClient) RemoveIPv6DefaultRoute(ctx context.Context, name string) {}
func (m *MockNDMSClient) GetDefaultGatewayInterface(ctx context.Context) (string, error) {
	return "PPPoE1", nil
}
func (m *MockNDMSClient) Save(ctx context.Context) error            { return nil }
func (m *MockNDMSClient) DumpIPv4Routes(ctx context.Context) string { return "" }
func (m *MockNDMSClient) QueryAllWANInterfaces(ctx context.Context) ([]wan.Interface, error) {
	return nil, nil
}
func (m *MockNDMSClient) GetInterfaceAddress(ctx context.Context, iface string) (string, string, error) {
	return "", "", nil
}
func (m *MockNDMSClient) HasWANIPv6(ctx context.Context, ifaceName string) bool { return false }
func (m *MockNDMSClient) GetHotspotClients(ctx context.Context) ([]ndms.HotspotClient, error) {
	return nil, nil
}
func (m *MockNDMSClient) GetSystemName(ctx context.Context, ndmsName string) string {
	return ndmsName
}
func (m *MockNDMSClient) RCIPost(ctx context.Context, payload interface{}) (json.RawMessage, error) {
	return nil, nil
}
func (m *MockNDMSClient) ShowObjectGroupFQDN(ctx context.Context) ([]ndms.ObjectGroupFQDN, error) {
	return nil, nil
}
func (m *MockNDMSClient) ShowDnsProxyRoute(ctx context.Context) ([]ndms.DnsProxyRoute, error) {
	return nil, nil
}
func (m *MockNDMSClient) ListWireguardInterfaces(ctx context.Context) ([]ndms.WireguardInterfaceInfo, error) {
	return nil, nil
}
func (m *MockNDMSClient) QueryAllInterfaces(ctx context.Context) ([]ndms.AllInterface, error) {
	return nil, nil
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
		&MockNDMSClient{opkgTunExists: true, showInterfaceOutput: ndmsDisabled},
		&MockWGClient{},
		&MockBackend{running: false},
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
		&MockNDMSClient{opkgTunExists: true, showInterfaceOutput: ndmsRunning},
		&MockWGClient{hasPeer: true, lastHandshake: time.Now(), rxBytes: 1000, txBytes: 500},
		&MockBackend{running: true, pid: 12345},
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
		&MockNDMSClient{opkgTunExists: true, showInterfaceOutput: ndmsNeedsStart},
		&MockWGClient{},
		&MockBackend{running: true, pid: 12345},
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
		&MockNDMSClient{opkgTunExists: true, showInterfaceOutput: ndmsNeedsStart},
		&MockWGClient{},
		&MockBackend{running: false},
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
		&MockNDMSClient{opkgTunExists: true, showInterfaceOutput: ndmsDisabled},
		&MockWGClient{},
		&MockBackend{running: true, pid: 12345},
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
		&MockNDMSClient{opkgTunExists: true, showInterfaceOutput: ndmsRunning},
		&MockWGClient{hasPeer: false},
		&MockBackend{running: true, pid: 12345},
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
		&MockNDMSClient{opkgTunExists: true, showInterfaceOutput: ""},
		&MockWGClient{},
		&MockBackend{running: false},
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
			ndms:    &MockNDMSClient{opkgTunExists: true, showInterfaceOutput: ndmsDisabled},
			wg:      &MockWGClient{},
			backend: &MockBackend{},
			wantIn:  "disabled",
		},
		{
			name:    "running with handshake",
			ndms:    &MockNDMSClient{opkgTunExists: true, showInterfaceOutput: ndmsRunning},
			wg:      &MockWGClient{hasPeer: true, lastHandshake: time.Now(), rxBytes: 100},
			backend: &MockBackend{running: true},
			wantIn:  "running",
		},
		{
			name:    "needs start",
			ndms:    &MockNDMSClient{opkgTunExists: true, showInterfaceOutput: ndmsNeedsStart},
			wg:      &MockWGClient{},
			backend: &MockBackend{},
			wantIn:  "needs start",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := New(tt.ndms, tt.wg, tt.backend)
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

	mgr := New(&MockNDMSClient{opkgTunExists: true}, &MockWGClient{}, &MockBackend{})
	_ = mgr.GetState(context.Background(), "awg0")
}
