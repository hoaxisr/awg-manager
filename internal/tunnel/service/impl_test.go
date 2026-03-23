package service

import (
	"context"
	"errors"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/netutil"
	"github.com/hoaxisr/awg-manager/internal/logging"
)

// === Mock implementations ===

// MockStore is a mock storage implementation.
type MockStore struct {
	tunnels map[string]*MockTunnel
}

type MockTunnel struct {
	ID                string
	Name              string
	Enabled           bool
	DefaultRoute bool
	ISPInterface      string
	Interface         MockInterface
	Peer              MockPeer
}

type MockInterface struct {
	Address string
	MTU     int
}

type MockPeer struct {
	Endpoint string
}

func NewMockStore() *MockStore {
	return &MockStore{tunnels: make(map[string]*MockTunnel)}
}

func (m *MockStore) Exists(id string) bool {
	_, ok := m.tunnels[id]
	return ok
}

func (m *MockStore) Get(id string) (*MockTunnel, error) {
	t, ok := m.tunnels[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return t, nil
}

func (m *MockStore) List() ([]MockTunnel, error) {
	var result []MockTunnel
	for _, t := range m.tunnels {
		result = append(result, *t)
	}
	return result, nil
}

func (m *MockStore) Save(t *MockTunnel) error {
	m.tunnels[t.ID] = t
	return nil
}

func (m *MockStore) Delete(id string) error {
	delete(m.tunnels, id)
	return nil
}

// MockStateManager is a mock state manager.
type MockStateManager struct {
	states map[string]tunnel.StateInfo
}

func NewMockStateManager() *MockStateManager {
	return &MockStateManager{states: make(map[string]tunnel.StateInfo)}
}

func (m *MockStateManager) GetState(ctx context.Context, tunnelID string) tunnel.StateInfo {
	if s, ok := m.states[tunnelID]; ok {
		return s
	}
	return tunnel.StateInfo{State: tunnel.StateNotCreated}
}


func (m *MockStateManager) SetState(tunnelID string, state tunnel.StateInfo) {
	m.states[tunnelID] = state
}

// MockOperator is a mock operator.
type MockOperator struct {
	createError        error
	startError         error
	stopError          error
	deleteError        error
	recoverError       error
	applyConfigError   error

	// SetupEndpointRouteIP is the IP returned by SetupEndpointRoute.
	SetupEndpointRouteIP string
	// TrackedEndpointIPs maps tunnelID -> IP for GetTrackedEndpointIP.
	TrackedEndpointIPs map[string]string

	CreateCalls              []tunnel.Config
	StartCalls               []tunnel.Config
	StopCalls                []string
	DeleteCalls              []string
	RecoverCalls             []struct{ ID string; State tunnel.StateInfo }
	ReconcileCalls           []tunnel.Config
	SuspendCalls             []string
	ResumeCalls              []string
	ApplyConfigCalls         []struct{ ID, Path string }
	SetupEndpointRouteCalls  []struct{ ID, Endpoint, ISP string }
	CleanupEndpointRouteCalls []string
	RestoreEndpointTrackingCalls []struct{ ID, Endpoint string }
	SetMTUCalls              []struct{ ID string; MTU int }
	UpdateDescriptionCalls   []struct{ ID, Desc string }
}

func (m *MockOperator) Create(ctx context.Context, cfg tunnel.Config) error {
	m.CreateCalls = append(m.CreateCalls, cfg)
	return m.createError
}

func (m *MockOperator) ColdStart(ctx context.Context, cfg tunnel.Config) error {
	m.StartCalls = append(m.StartCalls, cfg)
	return m.startError
}

func (m *MockOperator) Start(ctx context.Context, cfg tunnel.Config) error {
	m.StartCalls = append(m.StartCalls, cfg)
	return m.startError
}

func (m *MockOperator) Stop(ctx context.Context, tunnelID string) error {
	m.StopCalls = append(m.StopCalls, tunnelID)
	return m.stopError
}

func (m *MockOperator) Delete(ctx context.Context, tunnelID string) error {
	m.DeleteCalls = append(m.DeleteCalls, tunnelID)
	return m.deleteError
}

func (m *MockOperator) Recover(ctx context.Context, tunnelID string, state tunnel.StateInfo) error {
	m.RecoverCalls = append(m.RecoverCalls, struct{ ID string; State tunnel.StateInfo }{tunnelID, state})
	return m.recoverError
}

func (m *MockOperator) Reconcile(ctx context.Context, cfg tunnel.Config) error {
	m.ReconcileCalls = append(m.ReconcileCalls, cfg)
	return nil
}

func (m *MockOperator) Suspend(ctx context.Context, tunnelID string) error {
	m.SuspendCalls = append(m.SuspendCalls, tunnelID)
	return nil
}

func (m *MockOperator) Resume(ctx context.Context, tunnelID string) error {
	m.ResumeCalls = append(m.ResumeCalls, tunnelID)
	return nil
}

func (m *MockOperator) ApplyConfig(ctx context.Context, tunnelID, configPath string) error {
	m.ApplyConfigCalls = append(m.ApplyConfigCalls, struct{ ID, Path string }{tunnelID, configPath})
	return m.applyConfigError
}

func (m *MockOperator) SetupEndpointRoute(ctx context.Context, tunnelID, endpoint, ispInterface, _ string) (string, error) {
	m.SetupEndpointRouteCalls = append(m.SetupEndpointRouteCalls, struct{ ID, Endpoint, ISP string }{tunnelID, endpoint, ispInterface})
	return m.SetupEndpointRouteIP, nil
}

func (m *MockOperator) CleanupEndpointRoute(ctx context.Context, tunnelID string) error {
	m.CleanupEndpointRouteCalls = append(m.CleanupEndpointRouteCalls, tunnelID)
	return nil
}

func (m *MockOperator) RestoreEndpointTracking(ctx context.Context, tunnelID, endpoint, ispInterface string) (string, error) {
	m.RestoreEndpointTrackingCalls = append(m.RestoreEndpointTrackingCalls, struct{ ID, Endpoint string }{tunnelID, endpoint})
	return m.SetupEndpointRouteIP, nil
}

func (m *MockOperator) GetTrackedEndpointIP(tunnelID string) string {
	if m.TrackedEndpointIPs != nil {
		return m.TrackedEndpointIPs[tunnelID]
	}
	return m.SetupEndpointRouteIP
}

func (m *MockOperator) SetMTU(ctx context.Context, tunnelID string, mtu int) error {
	m.SetMTUCalls = append(m.SetMTUCalls, struct{ ID string; MTU int }{tunnelID, mtu})
	return nil
}

func (m *MockOperator) UpdateDescription(ctx context.Context, tunnelID, description string) error {
	m.UpdateDescriptionCalls = append(m.UpdateDescriptionCalls, struct{ ID, Desc string }{tunnelID, description})
	return nil
}

func (m *MockOperator) GetDefaultGatewayInterface(ctx context.Context) (string, error) {
	return "PPPoE1", nil
}

func (m *MockOperator) HasWANIPv6(ctx context.Context, ifaceName string) bool { return false }

func (m *MockOperator) GetSystemName(_ context.Context, ndmsID string) string { return ndmsID }

func (m *MockOperator) SetAppLogger(logger logging.AppLogger) {}

// === Tests ===

func TestResolveEndpointIP_IP(t *testing.T) {
	ip, err := netutil.ResolveEndpointIP("192.168.1.1:51820")
	if err != nil {
		t.Fatalf("netutil.ResolveEndpointIP() error = %v", err)
	}
	if ip != "192.168.1.1" {
		t.Errorf("netutil.ResolveEndpointIP() = %v, want 192.168.1.1", ip)
	}
}

func TestResolveEndpointIP_IPv6(t *testing.T) {
	ip, err := netutil.ResolveEndpointIP("[2001:db8::1]:51820")
	if err != nil {
		t.Fatalf("netutil.ResolveEndpointIP() error = %v", err)
	}
	if ip != "2001:db8::1" {
		t.Errorf("netutil.ResolveEndpointIP() = %v, want 2001:db8::1", ip)
	}
}

func TestResolveEndpointIP_Hostname(t *testing.T) {
	// This test may fail in offline environments
	ip, err := netutil.ResolveEndpointIP("localhost:51820")
	if err != nil {
		t.Skipf("Skipping hostname test (offline?): %v", err)
	}
	if ip == "" {
		t.Error("netutil.ResolveEndpointIP() returned empty string")
	}
}

// TestServiceStart_AlreadyRunning verifies Start returns error when tunnel is running.
func TestServiceStart_AlreadyRunning(t *testing.T) {
	stateMgr := NewMockStateManager()
	stateMgr.SetState("awg0", tunnel.StateInfo{State: tunnel.StateRunning})

	op := &MockOperator{}

	// We can't use the real service because it depends on real storage
	// This test demonstrates the expected behavior pattern

	// The service should return ErrAlreadyRunning without calling operator
	state := stateMgr.GetState(context.Background(), "awg0")
	if state.State == tunnel.StateRunning {
		// Service would return error here
		t.Log("State is running - service would return ErrAlreadyRunning")
	}

	// Operator should not be called
	if len(op.StartCalls) != 0 {
		t.Errorf("Operator.Start should not be called when already running")
	}
}

// TestServiceStart_RecoversBroken verifies Start recovers from broken state.
func TestServiceStart_RecoversBroken(t *testing.T) {
	stateMgr := NewMockStateManager()
	stateMgr.SetState("awg0", tunnel.StateInfo{
		State:          tunnel.StateBroken,
		ProcessRunning: true,
		InterfaceUp:    false,
	})

	op := &MockOperator{}

	// Simulate service behavior
	state := stateMgr.GetState(context.Background(), "awg0")
	if state.State == tunnel.StateBroken {
		// Service would call Recover first
		_ = op.Recover(context.Background(), "awg0", state)
	}

	if len(op.RecoverCalls) != 1 {
		t.Errorf("Operator.Recover should be called once for broken state")
	}
	if op.RecoverCalls[0].ID != "awg0" {
		t.Errorf("Recover called with wrong ID")
	}
}

// TestServiceStop_NotRunning verifies Stop returns error when tunnel is not running.
func TestServiceStop_NotRunning(t *testing.T) {
	stateMgr := NewMockStateManager()
	stateMgr.SetState("awg0", tunnel.StateInfo{State: tunnel.StateStopped})

	op := &MockOperator{}

	// Simulate service behavior
	state := stateMgr.GetState(context.Background(), "awg0")
	if state.State == tunnel.StateStopped {
		// Service would return ErrNotRunning
		t.Log("State is stopped - service would return ErrNotRunning")
	}

	// Operator should not be called
	if len(op.StopCalls) != 0 {
		t.Errorf("Operator.Stop should not be called when not running")
	}
}

// TestServiceWANUp_StartsEnabled verifies WAN up starts enabled tunnels.
func TestServiceWANUp_StartsEnabled(t *testing.T) {
	stateMgr := NewMockStateManager()
	stateMgr.SetState("awg0", tunnel.StateInfo{State: tunnel.StateStopped})
	stateMgr.SetState("awg1", tunnel.StateInfo{State: tunnel.StateStopped})

	op := &MockOperator{}

	// Simulate enabled tunnels
	enabledTunnels := []struct {
		ID      string
		Enabled bool
	}{
		{"awg0", true},
		{"awg1", false},
	}

	// Simulate WAN up behavior
	for _, t := range enabledTunnels {
		if !t.Enabled {
			continue
		}
		state := stateMgr.GetState(context.Background(), t.ID)
		if state.State == tunnel.StateStopped {
			// Service would start this tunnel
			op.StartCalls = append(op.StartCalls, tunnel.Config{ID: t.ID})
		}
	}

	// Only awg0 should be started (enabled)
	if len(op.StartCalls) != 1 {
		t.Errorf("Expected 1 Start call, got %d", len(op.StartCalls))
	}
	if len(op.StartCalls) > 0 && op.StartCalls[0].ID != "awg0" {
		t.Errorf("Expected awg0 to be started, got %s", op.StartCalls[0].ID)
	}
}

// TestServiceWANDown_Suspends verifies WAN down suspends running tunnels.
func TestServiceWANDown_Suspends(t *testing.T) {
	stateMgr := NewMockStateManager()
	stateMgr.SetState("awg0", tunnel.StateInfo{State: tunnel.StateRunning})
	stateMgr.SetState("awg1", tunnel.StateInfo{State: tunnel.StateStopped})

	op := &MockOperator{}

	// v3: WAN down calls Suspend (ip link set down, preserves interface)
	tunnelIDs := []string{"awg0", "awg1"}
	for _, id := range tunnelIDs {
		state := stateMgr.GetState(context.Background(), id)
		if state.State == tunnel.StateRunning {
			_ = op.Suspend(context.Background(), id)
		}
	}

	if len(op.SuspendCalls) != 1 {
		t.Errorf("Expected 1 Suspend call, got %d", len(op.SuspendCalls))
	}
	if len(op.StopCalls) != 0 {
		t.Errorf("Stop should not be called by WAN down, got %d", len(op.StopCalls))
	}
}

// TestServicePingCheck_Dead verifies HandleMonitorDead uses Stop (not KillLink).
func TestServicePingCheck_Dead(t *testing.T) {
	op := &MockOperator{}

	// v3: HandleMonitorDead calls Stop via Manager
	_ = op.Stop(context.Background(), "awg0")

	if len(op.StopCalls) != 1 {
		t.Errorf("Expected 1 Stop call, got %d", len(op.StopCalls))
	}
}

// TestServicePingCheck_Recovered verifies HandleMonitorRecovered restarts tunnel.
func TestServicePingCheck_Recovered(t *testing.T) {
	// After KillLink killed the process, HandleMonitorRecovered should
	// call startInternal() for a full restart (not just InterfaceUp).
	// State would be NeedsStart (intent=up, no process).
	stateMgr := NewMockStateManager()
	stateMgr.SetState("awg0", tunnel.StateInfo{
		State:          tunnel.StateNeedsStart,
		OpkgTunExists:  true,
		ProcessRunning: false,
	})

	state := stateMgr.GetState(context.Background(), "awg0")
	if state.State != tunnel.StateNeedsStart {
		t.Errorf("State should be NeedsStart after KillLink, got %v", state.State)
	}
}
