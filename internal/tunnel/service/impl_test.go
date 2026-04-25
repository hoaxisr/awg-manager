package service

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
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
	setMTUError        error

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
	SyncDNSCalls             [][]string
	SyncAddressCalls         []struct{ ID, Addr, IPv6 string }
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
	return m.setMTUError
}

func (m *MockOperator) Suspend(ctx context.Context, tunnelID string) error {
	return nil
}

func (m *MockOperator) Resume(ctx context.Context, tunnelID string) error {
	return nil
}

func (m *MockOperator) SetDefaultRoute(ctx context.Context, tunnelID string) error {
	return nil
}

func (m *MockOperator) RemoveDefaultRoute(ctx context.Context, tunnelID string) error {
	return nil
}

func (m *MockOperator) GetResolvedISP(tunnelID string) string {
	return ""
}

func (m *MockOperator) UpdateDescription(ctx context.Context, tunnelID, description string) error {
	m.UpdateDescriptionCalls = append(m.UpdateDescriptionCalls, struct{ ID, Desc string }{tunnelID, description})
	return nil
}

// SyncDNSCalls / SyncAddressCalls live on the MockOperator instance
// (not as package-level globals) so tests stay safe under t.Parallel().
func (m *MockOperator) SyncDNS(ctx context.Context, tunnelID string, dns []string) error {
	m.SyncDNSCalls = append(m.SyncDNSCalls, dns)
	return nil
}

func (m *MockOperator) SyncAddress(ctx context.Context, tunnelID string, address, ipv6 string) error {
	m.SyncAddressCalls = append(m.SyncAddressCalls, struct{ ID, Addr, IPv6 string }{tunnelID, address, ipv6})
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

// === Update tests (Bug F + diff sanity) ===

// newTestUpdateService spins up a minimal ServiceImpl suitable for
// Update precondition checks. Storage is not wired — we only need the
// branches that fail before any storage access.
func newTestUpdateService() *ServiceImpl {
	return &ServiceImpl{}
}

func TestUpdate_RejectsEmptyAddress(t *testing.T) {
	s := newTestUpdateService()
	old := &storage.AWGTunnel{ID: "awg0", Interface: storage.AWGInterface{Address: "10.0.0.1", MTU: 1420}}
	new_ := &storage.AWGTunnel{ID: "awg0", Interface: storage.AWGInterface{Address: "", MTU: 1420}}
	if err := s.Update(context.Background(), old, new_); err == nil {
		t.Fatal("expected error for empty Address")
	}
}

func TestUpdate_RejectsZeroMTU(t *testing.T) {
	s := newTestUpdateService()
	old := &storage.AWGTunnel{ID: "awg0", Interface: storage.AWGInterface{Address: "10.0.0.1", MTU: 1420}}
	new_ := &storage.AWGTunnel{ID: "awg0", Interface: storage.AWGInterface{Address: "10.0.0.1", MTU: 0}}
	if err := s.Update(context.Background(), old, new_); err == nil {
		t.Fatal("expected error for zero MTU")
	}
}

func TestUpdate_RejectsNilStored(t *testing.T) {
	s := newTestUpdateService()
	if err := s.Update(context.Background(), nil, nil); err == nil {
		t.Fatal("expected error for nil snapshots")
	}
}

func TestUpdate_RejectsIDMismatch(t *testing.T) {
	s := newTestUpdateService()
	old := &storage.AWGTunnel{ID: "awg0", Interface: storage.AWGInterface{Address: "10.0.0.1", MTU: 1420}}
	new_ := &storage.AWGTunnel{ID: "awg1", Interface: storage.AWGInterface{Address: "10.0.0.1", MTU: 1420}}
	if err := s.Update(context.Background(), old, new_); err == nil {
		t.Fatal("expected error for id mismatch")
	}
}

// === Diff helper tests ===

func TestAWGInterfaceEqual_SameValues(t *testing.T) {
	a := storage.AWGInterface{Address: "10.0.0.1", MTU: 1420, DNS: "1.1.1.1", AWGObfuscation: storage.AWGObfuscation{Qlen: 1000, Jc: 5}}
	b := a
	if !awgInterfaceEqual(a, b) {
		t.Fatal("expected equal")
	}
}

func TestAWGInterfaceEqual_DifferentDNS(t *testing.T) {
	a := storage.AWGInterface{Address: "10.0.0.1", MTU: 1420, DNS: "1.1.1.1"}
	b := a
	b.DNS = "8.8.8.8"
	if awgInterfaceEqual(a, b) {
		t.Fatal("expected not equal when DNS differs")
	}
}

func TestAWGPeerEqual_AllowedIPsOrderIgnored(t *testing.T) {
	// AllowedIPs is a set — order does not matter semantically.
	a := storage.AWGPeer{PublicKey: "k", AllowedIPs: []string{"10.0.0.0/24", "192.168.1.0/24"}}
	b := storage.AWGPeer{PublicKey: "k", AllowedIPs: []string{"192.168.1.0/24", "10.0.0.0/24"}}
	if !awgPeerEqual(a, b) {
		t.Fatal("expected equal when AllowedIPs differ only in order")
	}
}

func TestAWGPeerEqual_AllowedIPsContent(t *testing.T) {
	a := storage.AWGPeer{PublicKey: "k", AllowedIPs: []string{"10.0.0.0/24"}}
	b := storage.AWGPeer{PublicKey: "k", AllowedIPs: []string{"192.168.1.0/24"}}
	if awgPeerEqual(a, b) {
		t.Fatal("expected not equal when AllowedIPs content differs")
	}
}

func TestAWGPeerEqual_PSKChange(t *testing.T) {
	a := storage.AWGPeer{PublicKey: "k", PresharedKey: "psk1"}
	b := storage.AWGPeer{PublicKey: "k", PresharedKey: "psk2"}
	if awgPeerEqual(a, b) {
		t.Fatal("expected not equal when PSK differs")
	}
}

func TestAWGParamsEqual_Identical(t *testing.T) {
	a := storage.AWGInterface{AWGObfuscation: storage.AWGObfuscation{Qlen: 1000, Jc: 5, Jmin: 50, Jmax: 1000, S1: 100, H1: "h1"}}
	b := a
	if !awgParamsEqual(a, b) {
		t.Fatal("expected AWG params equal")
	}
}

func TestAWGParamsEqual_DifferentJc(t *testing.T) {
	a := storage.AWGInterface{AWGObfuscation: storage.AWGObfuscation{Qlen: 1000, Jc: 5}}
	b := a
	b.Jc = 7
	if awgParamsEqual(a, b) {
		t.Fatal("expected not equal when Jc differs")
	}
}

func TestAWGParamsEqual_IgnoresNonAWGFields(t *testing.T) {
	// Address/MTU/DNS/PrivateKey differ but AWG params are identical.
	a := storage.AWGInterface{Address: "10.0.0.1", MTU: 1420, DNS: "1.1.1.1", AWGObfuscation: storage.AWGObfuscation{Jc: 5, Qlen: 1000}}
	b := storage.AWGInterface{Address: "10.0.0.2", MTU: 1280, DNS: "8.8.8.8", AWGObfuscation: storage.AWGObfuscation{Jc: 5, Qlen: 1000}}
	if !awgParamsEqual(a, b) {
		t.Fatal("AWG params helper should ignore Address/MTU/DNS")
	}
}

// === applyDiffKernel integration tests (Bug A/B/C/D/E coverage) ===
//
// These tests call applyDiffKernel directly with a MockOperator to verify
// that field-level diffs trigger the correct Sync* dispatch. If any
// regression removes a per-field branch, one of these tests fails.

// newDiffTestService builds a minimal ServiceImpl wired to a MockOperator.
// state.Manager and nwgOperator are nil — applyDiffKernel only touches
// legacyOperator + lockTunnel/resolveWAN, which we drive through stub
// fields. resolveWAN is not called for non-endpoint changes.
func newDiffTestService(mockOp *MockOperator) *ServiceImpl {
	return &ServiceImpl{
		legacyOperator: mockOp,
	}
}

func TestApplyDiffKernel_DNSChange_CallsSyncDNS(t *testing.T) {
	mockOp := &MockOperator{}
	s := newDiffTestService(mockOp)
	old := &storage.AWGTunnel{ID: "awg0", Interface: storage.AWGInterface{Address: "10.0.0.1", MTU: 1420, DNS: "1.1.1.1"}}
	new_ := &storage.AWGTunnel{ID: "awg0", Interface: storage.AWGInterface{Address: "10.0.0.1", MTU: 1420, DNS: "8.8.8.8"}}

	if err := s.applyDiffKernel(context.Background(), old, new_); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(mockOp.SyncDNSCalls) != 1 {
		t.Fatalf("expected 1 SyncDNS call, got %d", len(mockOp.SyncDNSCalls))
	}
	if len(mockOp.SyncDNSCalls[0]) != 1 || mockOp.SyncDNSCalls[0][0] != "8.8.8.8" {
		t.Fatalf("expected SyncDNS([8.8.8.8]), got %v", mockOp.SyncDNSCalls[0])
	}
}

func TestApplyDiffKernel_DNSCleared_CallsSyncDNSWithEmpty(t *testing.T) {
	mockOp := &MockOperator{}
	s := newDiffTestService(mockOp)
	old := &storage.AWGTunnel{ID: "awg0", Interface: storage.AWGInterface{Address: "10.0.0.1", MTU: 1420, DNS: "1.1.1.1"}}
	new_ := &storage.AWGTunnel{ID: "awg0", Interface: storage.AWGInterface{Address: "10.0.0.1", MTU: 1420, DNS: ""}}

	if err := s.applyDiffKernel(context.Background(), old, new_); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(mockOp.SyncDNSCalls) != 1 {
		t.Fatalf("expected 1 SyncDNS call, got %d", len(mockOp.SyncDNSCalls))
	}
	if mockOp.SyncDNSCalls[0] != nil {
		t.Fatalf("expected SyncDNS(nil) for cleared DNS, got %v", mockOp.SyncDNSCalls[0])
	}
}

func TestApplyDiffKernel_NoOpWhenIdentical(t *testing.T) {
	mockOp := &MockOperator{}
	s := newDiffTestService(mockOp)
	old := &storage.AWGTunnel{
		ID:           "awg0",
		Interface:    storage.AWGInterface{Address: "10.0.0.1", MTU: 1420, DNS: "1.1.1.1"},
		Peer:         storage.AWGPeer{Endpoint: "1.2.3.4:51820"},
		ISPInterface: "PPPoE0",
	}
	new_ := *old // identical

	if err := s.applyDiffKernel(context.Background(), old, &new_); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(mockOp.SyncDNSCalls) != 0 {
		t.Fatalf("expected 0 SyncDNS for no-op, got %d", len(mockOp.SyncDNSCalls))
	}
	if len(mockOp.SyncAddressCalls) != 0 {
		t.Fatalf("expected 0 SyncAddress for no-op, got %d", len(mockOp.SyncAddressCalls))
	}
	if len(mockOp.ApplyConfigCalls) != 0 {
		t.Fatalf("expected 0 ApplyConfig for no-op, got %d", len(mockOp.ApplyConfigCalls))
	}
	if len(mockOp.SetMTUCalls) != 0 {
		t.Fatalf("expected 0 SetMTU for no-op, got %d", len(mockOp.SetMTUCalls))
	}
	if len(mockOp.SetupEndpointRouteCalls) != 0 {
		t.Fatalf("expected 0 SetupEndpointRoute for no-op, got %d", len(mockOp.SetupEndpointRouteCalls))
	}
	if len(mockOp.CleanupEndpointRouteCalls) != 0 {
		t.Fatalf("expected 0 CleanupEndpointRoute for no-op, got %d", len(mockOp.CleanupEndpointRouteCalls))
	}
}

func TestApplyDiffKernel_MTUChange_CallsSetMTU(t *testing.T) {
	mockOp := &MockOperator{}
	s := newDiffTestService(mockOp)
	old := &storage.AWGTunnel{ID: "awg0", Interface: storage.AWGInterface{Address: "10.0.0.1", MTU: 1420}}
	new_ := &storage.AWGTunnel{ID: "awg0", Interface: storage.AWGInterface{Address: "10.0.0.1", MTU: 1280}}

	if err := s.applyDiffKernel(context.Background(), old, new_); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(mockOp.SetMTUCalls) != 1 || mockOp.SetMTUCalls[0].MTU != 1280 {
		t.Fatalf("expected SetMTU(1280), got %v", mockOp.SetMTUCalls)
	}
	if len(mockOp.ApplyConfigCalls) != 1 {
		t.Fatalf("expected 1 ApplyConfig (Interface differs), got %d", len(mockOp.ApplyConfigCalls))
	}
}

func TestApplyDiffKernel_PeerChange_CallsApplyConfig(t *testing.T) {
	mockOp := &MockOperator{}
	s := newDiffTestService(mockOp)
	old := &storage.AWGTunnel{
		ID:        "awg0",
		Interface: storage.AWGInterface{Address: "10.0.0.1", MTU: 1420},
		Peer:      storage.AWGPeer{PublicKey: "k1", AllowedIPs: []string{"0.0.0.0/0"}},
	}
	new_ := &storage.AWGTunnel{
		ID:        "awg0",
		Interface: storage.AWGInterface{Address: "10.0.0.1", MTU: 1420},
		Peer:      storage.AWGPeer{PublicKey: "k1", AllowedIPs: []string{"10.0.0.0/8"}},
	}

	if err := s.applyDiffKernel(context.Background(), old, new_); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(mockOp.ApplyConfigCalls) != 1 {
		t.Fatalf("expected ApplyConfig on peer change, got %d", len(mockOp.ApplyConfigCalls))
	}
}

func TestApplyDiffKernel_AggregatesErrors(t *testing.T) {
	mockOp := &MockOperator{
		setMTUError:      errStub("mtu boom"),
		applyConfigError: errStub("apply boom"),
	}
	s := newDiffTestService(mockOp)
	old := &storage.AWGTunnel{ID: "awg0", Interface: storage.AWGInterface{Address: "10.0.0.1", MTU: 1420}}
	new_ := &storage.AWGTunnel{ID: "awg0", Interface: storage.AWGInterface{Address: "10.0.0.1", MTU: 1280}}

	err := s.applyDiffKernel(context.Background(), old, new_)
	if err == nil {
		t.Fatal("expected aggregated error")
	}
	// Both ApplyConfig and SetMTU should have run despite errors.
	if len(mockOp.ApplyConfigCalls) != 1 || len(mockOp.SetMTUCalls) != 1 {
		t.Fatalf("expected all dispatches to run; got ApplyConfig=%d SetMTU=%d",
			len(mockOp.ApplyConfigCalls), len(mockOp.SetMTUCalls))
	}
}

// errStub builds a sentinel error for mocked failures.
type errStub string

func (e errStub) Error() string { return string(e) }
