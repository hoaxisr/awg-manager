package service

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/logging"
)

// === Mock implementations ===

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

func (m *MockOperator) Delete(ctx context.Context, stored *storage.AWGTunnel) error {
	m.DeleteCalls = append(m.DeleteCalls, stored.ID)
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

func (m *MockOperator) SyncDNS(ctx context.Context, tunnelID string, dns []string) error {
	return nil
}

func (m *MockOperator) SyncAddress(ctx context.Context, tunnelID string, address, ipv6 string) error {
	return nil
}

func (m *MockOperator) GetDefaultGatewayInterface(ctx context.Context) (string, error) {
	return "PPPoE1", nil
}

func (m *MockOperator) HasWANIPv6(ctx context.Context, ifaceName string) bool { return false }

func (m *MockOperator) GetSystemName(_ context.Context, ndmsID string) string { return ndmsID }

func (m *MockOperator) SetAppLogger(logger logging.AppLogger) {}

// Client VPN routing stubs
func (m *MockOperator) SetupClientRouteTable(ctx context.Context, kernelIface string, tableNum int) error {
	return nil
}
func (m *MockOperator) AddClientRule(ctx context.Context, clientIP string, tableNum int) error {
	return nil
}
func (m *MockOperator) RemoveClientRule(ctx context.Context, clientIP string, tableNum int) error {
	return nil
}
func (m *MockOperator) CleanupClientRouteTable(ctx context.Context, tableNum int) error {
	return nil
}
func (m *MockOperator) ListUsedRoutingTables(ctx context.Context) ([]int, error) {
	return nil, nil
}
