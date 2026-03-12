package ops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/backend"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wg"
)

// === Mock implementations ===

// MockNDMSClient tracks calls and allows configuring behavior.
type MockNDMSClient struct {
	opkgTunExists       bool
	interfaceUp         bool
	showInterfaceOutput string
	showInterfaceError  error
	createError         error
	deleteError         error
	setAddrError        error
	setMTUError         error
	ifUpError           error
	ifDownError         error
	setRouteError       error
	saveError           error
	defaultGateway string
	gatewayError   error

	// Call tracking
	CreateCalls          []string
	DeleteCalls          []string
	SetAddrCalls         []struct{ Name, Addr string }
	SetMTUCalls          []struct{ Name string; MTU int }
	IfUpCalls            []string
	IfDownCalls          []string
	SetRouteCalls        []string
	RemoveRouteCalls     []string
	RemoveHostRouteCalls []string
	SaveCalls            int
}

func (m *MockNDMSClient) ShowInterface(ctx context.Context, name string) (string, error) {
	return m.showInterfaceOutput, m.showInterfaceError
}

func (m *MockNDMSClient) CreateOpkgTun(ctx context.Context, name, description string) error {
	m.CreateCalls = append(m.CreateCalls, name)
	return m.createError
}

func (m *MockNDMSClient) DeleteOpkgTun(ctx context.Context, name string) error {
	m.DeleteCalls = append(m.DeleteCalls, name)
	return m.deleteError
}

func (m *MockNDMSClient) OpkgTunExists(ctx context.Context, name string) bool {
	return m.opkgTunExists
}

func (m *MockNDMSClient) SetAddress(ctx context.Context, name, address string) error {
	m.SetAddrCalls = append(m.SetAddrCalls, struct{ Name, Addr string }{name, address})
	return m.setAddrError
}

func (m *MockNDMSClient) SetIPv6Address(ctx context.Context, name, address string) error {
	return nil
}

func (m *MockNDMSClient) ClearIPv6Address(ctx context.Context, name string) {}

func (m *MockNDMSClient) SetMTU(ctx context.Context, name string, mtu int) error {
	m.SetMTUCalls = append(m.SetMTUCalls, struct{ Name string; MTU int }{name, mtu})
	return m.setMTUError
}

func (m *MockNDMSClient) SetDescription(ctx context.Context, name, description string) error {
	return nil
}

func (m *MockNDMSClient) InterfaceUp(ctx context.Context, name string) error {
	m.IfUpCalls = append(m.IfUpCalls, name)
	return m.ifUpError
}

func (m *MockNDMSClient) InterfaceDown(ctx context.Context, name string) error {
	m.IfDownCalls = append(m.IfDownCalls, name)
	return m.ifDownError
}

func (m *MockNDMSClient) SetDefaultRoute(ctx context.Context, name string) error {
	m.SetRouteCalls = append(m.SetRouteCalls, name)
	return m.setRouteError
}

func (m *MockNDMSClient) RemoveDefaultRoute(ctx context.Context, name string) error {
	m.RemoveRouteCalls = append(m.RemoveRouteCalls, name)
	return nil
}

func (m *MockNDMSClient) RemoveHostRoute(ctx context.Context, host string) error {
	m.RemoveHostRouteCalls = append(m.RemoveHostRouteCalls, host)
	return nil
}

func (m *MockNDMSClient) SetIPv6DefaultRoute(ctx context.Context, name string) error {
	return nil
}

func (m *MockNDMSClient) RemoveIPv6DefaultRoute(ctx context.Context, name string) {}

func (m *MockNDMSClient) GetDefaultGatewayInterface(ctx context.Context) (string, error) {
	if m.gatewayError != nil {
		return "", m.gatewayError
	}
	if m.defaultGateway == "" {
		return "PPPoE1", nil
	}
	return m.defaultGateway, nil
}

func (m *MockNDMSClient) Save(ctx context.Context) error {
	m.SaveCalls++
	return m.saveError
}

func (m *MockNDMSClient) QueryAllWANInterfaces(ctx context.Context) ([]wan.Interface, error) {
	return nil, nil
}

func (m *MockNDMSClient) GetInterfaceAddress(ctx context.Context, iface string) (string, string, error) {
	return "", "", nil
}

func (m *MockNDMSClient) HasWANIPv6(ctx context.Context, ifaceName string) bool {
	return false
}

func (m *MockNDMSClient) GetHotspotClients(ctx context.Context) ([]ndms.HotspotClient, error) {
	return nil, nil
}
func (m *MockNDMSClient) DumpIPv4Routes(ctx context.Context) string { return "" }
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

// MockWGClient for WireGuard operations.
type MockWGClient struct {
	setConfError error
	showError    error
	hasPeer      bool

	SetConfCalls []struct{ Iface, Path string }
}

func (m *MockWGClient) SetConf(ctx context.Context, iface, confPath string) error {
	m.SetConfCalls = append(m.SetConfCalls, struct{ Iface, Path string }{iface, confPath})
	return m.setConfError
}

func (m *MockWGClient) Show(ctx context.Context, iface string) (*wg.ShowResult, error) {
	if m.showError != nil {
		return nil, m.showError
	}
	return &wg.ShowResult{HasPeer: m.hasPeer}, nil
}

func (m *MockWGClient) RemovePeer(ctx context.Context, iface, publicKey string) error {
	return nil
}

func (m *MockWGClient) GetPeerPublicKey(ctx context.Context, iface string) (string, error) {
	if m.hasPeer {
		return "mock-key", nil
	}
	return "", nil
}

// MockBackend for kernel backend operations.
type MockBackend struct {
	running      bool
	pid          int
	startError   error
	stopError    error
	waitReadyErr error

	StartCalls []string
	StopCalls  []string
}

func (m *MockBackend) Type() backend.Type {
	return backend.TypeKernel
}

func (m *MockBackend) Start(ctx context.Context, ifaceName string) error {
	m.StartCalls = append(m.StartCalls, ifaceName)
	if m.startError == nil {
		m.running = true
		m.pid = 12345
	}
	return m.startError
}

func (m *MockBackend) Stop(ctx context.Context, ifaceName string) error {
	m.StopCalls = append(m.StopCalls, ifaceName)
	m.running = false
	m.pid = 0
	return m.stopError
}

func (m *MockBackend) IsRunning(ctx context.Context, ifaceName string) (bool, int) {
	return m.running, m.pid
}

func (m *MockBackend) WaitReady(ctx context.Context, ifaceName string, timeout time.Duration) error {
	return m.waitReadyErr
}

// MockFirewall for firewall operations.
type MockFirewall struct {
	addError    error
	removeError error
	hasRules    bool

	AddCalls    []string
	RemoveCalls []string
}

func (m *MockFirewall) AddRules(ctx context.Context, iface string) error {
	m.AddCalls = append(m.AddCalls, iface)
	return m.addError
}

func (m *MockFirewall) RemoveRules(ctx context.Context, iface string) error {
	m.RemoveCalls = append(m.RemoveCalls, iface)
	return m.removeError
}

func (m *MockFirewall) HasRules(ctx context.Context, iface string) bool {
	return m.hasRules
}

// === Test helpers ===

// mockIPRun is a no-op ip command runner for tests.
// Returns success for all ip commands so tests don't need /opt/sbin/ip.
func mockIPRun(_ context.Context, _ string, _ ...string) (*exec.Result, error) {
	return &exec.Result{}, nil
}

// ipRunRecorder records ip command calls for assertion.
type ipRunRecorder struct {
	Calls []string
}

func (r *ipRunRecorder) run(_ context.Context, name string, args ...string) (*exec.Result, error) {
	r.Calls = append(r.Calls, name+" "+strings.Join(args, " "))
	return &exec.Result{}, nil
}

// newTestOperator creates an operator with mocked ipRun for unit tests.
func newTestOperator(ndmsClient *MockNDMSClient, wgClient *MockWGClient, backendMock *MockBackend, fw *MockFirewall) *OperatorOS5Impl {
	op := NewOperatorOS5(ndmsClient, wgClient, backendMock, fw, nil)
	op.ipRun = mockIPRun
	return op
}

// === Tests ===

func TestOperatorOS5_Create_Success(t *testing.T) {
	ndms := &MockNDMSClient{opkgTunExists: false}
	op := newTestOperator(ndms, &MockWGClient{}, &MockBackend{}, &MockFirewall{})

	cfg := tunnel.Config{
		ID:      "awg0",
		Name:    "Test Tunnel",
		Address: "10.0.0.1",
		MTU:     1420,
	}

	err := op.Create(context.Background(), cfg)

	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if len(ndms.CreateCalls) != 1 || ndms.CreateCalls[0] != "OpkgTun0" {
		t.Errorf("Expected CreateOpkgTun(OpkgTun0), got %v", ndms.CreateCalls)
	}
	// Create now sets address and MTU in NDMS
	if len(ndms.SetAddrCalls) != 1 {
		t.Errorf("Expected SetAddress to be called once, got %d", len(ndms.SetAddrCalls))
	}
	if len(ndms.SetMTUCalls) != 1 {
		t.Errorf("Expected SetMTU to be called once, got %d", len(ndms.SetMTUCalls))
	}
	if ndms.SaveCalls != 1 {
		t.Errorf("Expected Save() to be called once, got %d", ndms.SaveCalls)
	}
}

func TestOperatorOS5_Create_AlreadyExists(t *testing.T) {
	ndms := &MockNDMSClient{opkgTunExists: true}
	op := newTestOperator(ndms, &MockWGClient{}, &MockBackend{}, &MockFirewall{})

	cfg := tunnel.Config{ID: "awg0"}

	err := op.Create(context.Background(), cfg)

	if !errors.Is(err, tunnel.ErrAlreadyExists) {
		t.Errorf("Create() error = %v, want ErrAlreadyExists", err)
	}
}

func TestOperatorOS5_Start_Success(t *testing.T) {
	ndms := &MockNDMSClient{opkgTunExists: true}
	wgClient := &MockWGClient{}
	backendMock := &MockBackend{}
	fw := &MockFirewall{}

	op := newTestOperator(ndms, wgClient, backendMock, fw)

	cfg := tunnel.Config{
		ID:           "awg0",
		Name:         "Test",
		Address:      "10.0.0.1",
		MTU:          1420,
		ConfPath:     "/tmp/awg0.conf",
		DefaultRoute: true,
	}

	err := op.Start(context.Background(), cfg)

	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	// Verify phases: backend → kernel config → WG → ip link up → NDMS up → firewall
	// When OpkgTun exists + kernel: SetAddress/SetMTU skipped (MTU set via ip link set)
	if len(backendMock.StartCalls) != 1 {
		t.Errorf("Backend.Start not called")
	}
	if len(wgClient.SetConfCalls) != 1 {
		t.Errorf("WG.SetConf not called")
	}
	if len(ndms.SetAddrCalls) != 0 {
		t.Errorf("NDMS.SetAddress should NOT be called when OpkgTun exists, got %d", len(ndms.SetAddrCalls))
	}
	if len(ndms.SetMTUCalls) != 0 {
		t.Errorf("NDMS.SetMTU should NOT be called for kernel backend (MTU set via ip), got %d", len(ndms.SetMTUCalls))
	}
	// Start always calls InterfaceUp (after Stop, conf is disabled)
	if len(ndms.IfUpCalls) != 1 {
		t.Errorf("NDMS.InterfaceUp not called, got %d", len(ndms.IfUpCalls))
	}
	if len(fw.AddCalls) != 1 {
		t.Errorf("Firewall.AddRules not called")
	}
	// NDMS default route is set when DefaultRoute=true (Phase 6 only)
	if len(ndms.SetRouteCalls) != 1 {
		t.Errorf("NDMS.SetDefaultRoute expected 1 call (phase 6), got %d", len(ndms.SetRouteCalls))
	}
}

func TestOperatorOS5_Start_JustCreated_SetsNDMSConfig(t *testing.T) {
	// When OpkgTun does NOT exist, Start creates it and sets address/MTU
	ndms := &MockNDMSClient{opkgTunExists: false}
	wgClient := &MockWGClient{}
	backendMock := &MockBackend{}
	fw := &MockFirewall{}

	op := newTestOperator(ndms, wgClient, backendMock, fw)

	cfg := tunnel.Config{
		ID:       "awg0",
		Name:     "Test",
		Address:  "10.0.0.1",
		MTU:      1420,
		ConfPath: "/tmp/awg0.conf",
	}

	err := op.Start(context.Background(), cfg)

	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	// OpkgTun was just created -> NDMS SetAddress/SetMTU SHOULD be called
	if len(ndms.CreateCalls) != 1 {
		t.Errorf("NDMS.CreateOpkgTun not called")
	}
	if len(ndms.SetAddrCalls) != 1 {
		t.Errorf("NDMS.SetAddress should be called when OpkgTun just created, got %d", len(ndms.SetAddrCalls))
	}
	if len(ndms.SetMTUCalls) != 1 {
		t.Errorf("NDMS.SetMTU should be called when OpkgTun just created, got %d", len(ndms.SetMTUCalls))
	}
	// Save should be called twice: after NDMS config (justCreated) + final save
	if ndms.SaveCalls != 2 {
		t.Errorf("NDMS.Save should be called twice (config + final), got %d", ndms.SaveCalls)
	}
}

func TestOperatorOS5_Start_VerifyIPCommands(t *testing.T) {
	// Verify the specific ip commands called during Start
	ndms := &MockNDMSClient{opkgTunExists: true}
	backendMock := &MockBackend{}
	recorder := &ipRunRecorder{}

	op := NewOperatorOS5(ndms, &MockWGClient{}, backendMock, &MockFirewall{}, nil)
	op.ipRun = recorder.run

	cfg := tunnel.Config{
		ID:       "awg0",
		Name:     "Test",
		Address:  "10.0.0.1",
		MTU:      1420,
		ConfPath: "/tmp/awg0.conf",
	}

	err := op.Start(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	// Should have: (1) ip link set dev ... mtu/qlen, (2) ip link set up
	if len(recorder.Calls) < 2 {
		t.Fatalf("Expected at least 2 ip calls, got %d: %v", len(recorder.Calls), recorder.Calls)
	}

	// First call: configure interface MTU + txqueuelen
	if !strings.Contains(recorder.Calls[0], "link set dev opkgtun0 txqueuelen 1000 mtu 1420") {
		t.Errorf("First ip call should configure MTU/qlen, got: %s", recorder.Calls[0])
	}
	// Second call: bring link up
	if !strings.Contains(recorder.Calls[1], "link set up dev opkgtun0") {
		t.Errorf("Second ip call should bring link up, got: %s", recorder.Calls[1])
	}
}

func TestOperatorOS5_Start_BackendFails_Rollback(t *testing.T) {
	ndms := &MockNDMSClient{opkgTunExists: true}
	backendMock := &MockBackend{startError: errors.New("process failed")}

	op := newTestOperator(ndms, &MockWGClient{}, backendMock, &MockFirewall{})

	cfg := tunnel.Config{
		ID:       "awg0",
		Address:  "10.0.0.1",
		MTU:      1420,
		ConfPath: "/tmp/awg0.conf",
	}

	err := op.Start(context.Background(), cfg)

	if err == nil {
		t.Fatal("Start() should fail")
	}
	// OpkgTun exists + kernel -> justCreated=false -> no SetAddress, no NDMS SetMTU
	if len(ndms.SetAddrCalls) != 0 {
		t.Errorf("SetAddress should NOT be called when OpkgTun exists, got %d", len(ndms.SetAddrCalls))
	}
	if len(ndms.SetMTUCalls) != 0 {
		t.Errorf("SetMTU should NOT be called for kernel backend (MTU set via ip), got %d", len(ndms.SetMTUCalls))
	}
}

func TestOperatorOS5_Start_WGFails_Rollback(t *testing.T) {
	ndms := &MockNDMSClient{opkgTunExists: true}
	backendMock := &MockBackend{}
	wgClient := &MockWGClient{setConfError: errors.New("WG config failed")}

	op := newTestOperator(ndms, wgClient, backendMock, &MockFirewall{})

	cfg := tunnel.Config{
		ID:       "awg0",
		Address:  "10.0.0.1",
		MTU:      1420,
		ConfPath: "/tmp/awg0.conf",
	}

	err := op.Start(context.Background(), cfg)

	if err == nil {
		t.Fatal("Start() should fail")
	}
	// Backend should be stopped on rollback
	if len(backendMock.StopCalls) != 1 {
		t.Errorf("Backend.Stop should be called on WG failure")
	}
}

func TestOperatorOS5_Start_FirewallFails_Rollback(t *testing.T) {
	ndms := &MockNDMSClient{opkgTunExists: true}
	backendMock := &MockBackend{}
	fw := &MockFirewall{addError: errors.New("firewall failed")}

	op := newTestOperator(ndms, &MockWGClient{}, backendMock, fw)

	cfg := tunnel.Config{
		ID:       "awg0",
		Address:  "10.0.0.1",
		MTU:      1420,
		ConfPath: "/tmp/awg0.conf",
	}

	err := op.Start(context.Background(), cfg)

	if err == nil {
		t.Fatal("Start() should fail")
	}
	// Rollback: backend stopped, but InterfaceDown NOT called (OpkgTun already existed)
	if len(backendMock.StopCalls) != 1 {
		t.Errorf("Backend.Stop should be called on firewall failure")
	}
	if len(ndms.IfDownCalls) != 0 {
		t.Errorf("InterfaceDown should NOT be called when OpkgTun already existed (preserves conf: running)")
	}
}

func TestOperatorOS5_Stop_Success(t *testing.T) {
	ndms := &MockNDMSClient{}
	backendMock := &MockBackend{running: true}
	fw := &MockFirewall{}

	op := newTestOperator(ndms, &MockWGClient{}, backendMock, fw)

	err := op.Stop(context.Background(), "awg0")

	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	// Kernel stop order: InterfaceDown -> firewall -> routes -> backend.Stop (ip link del)
	if len(ndms.IfDownCalls) != 1 {
		t.Errorf("NDMS.InterfaceDown not called")
	}
	if len(fw.RemoveCalls) != 1 || fw.RemoveCalls[0] != "opkgtun0" {
		t.Errorf("Firewall.RemoveRules not called correctly, got: %v", fw.RemoveCalls)
	}
	if len(backendMock.StopCalls) != 1 {
		t.Errorf("Backend.Stop not called")
	}
	// Kernel stop calls RemoveDefaultRoute (Phase 3: routes cleanup)
}

func TestOperatorOS5_Delete_Success(t *testing.T) {
	ndms := &MockNDMSClient{opkgTunExists: true}
	backendMock := &MockBackend{running: true}
	fw := &MockFirewall{}

	op := newTestOperator(ndms, &MockWGClient{}, backendMock, fw)

	err := op.Delete(context.Background(), "awg0")

	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	// Should stop first, then delete OpkgTun
	if len(backendMock.StopCalls) != 1 {
		t.Errorf("Backend.Stop not called")
	}
	if len(ndms.DeleteCalls) != 1 || ndms.DeleteCalls[0] != "OpkgTun0" {
		t.Errorf("NDMS.DeleteOpkgTun not called correctly")
	}
}

func TestOperatorOS5_Recover_ZombieProcess(t *testing.T) {
	ndms := &MockNDMSClient{opkgTunExists: true}
	backendMock := &MockBackend{running: true}

	op := newTestOperator(ndms, &MockWGClient{}, backendMock, &MockFirewall{})

	state := tunnel.StateInfo{
		State:          tunnel.StateBroken,
		ProcessRunning: true,
		InterfaceUp:    false,
	}

	err := op.Recover(context.Background(), "awg0", state)

	if err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	if len(backendMock.StopCalls) != 1 {
		t.Errorf("Backend.Stop should be called for zombie process")
	}
	if len(ndms.DeleteCalls) != 0 {
		t.Errorf("DeleteOpkgTun should NOT be called (preserves Policy bindings)")
	}
	if len(ndms.IfDownCalls) != 1 {
		t.Errorf("InterfaceDown should be called instead of delete")
	}
}

func TestOperatorOS5_Recover_OrphanedInterface(t *testing.T) {
	ndms := &MockNDMSClient{opkgTunExists: true, interfaceUp: true}
	backendMock := &MockBackend{running: false}

	op := newTestOperator(ndms, &MockWGClient{}, backendMock, &MockFirewall{})

	state := tunnel.StateInfo{
		State:          tunnel.StateBroken,
		ProcessRunning: false,
		InterfaceUp:    true,
	}

	err := op.Recover(context.Background(), "awg0", state)

	if err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	if len(ndms.DeleteCalls) != 0 {
		t.Errorf("DeleteOpkgTun should NOT be called (process was not running, OpkgTun preserved)")
	}
	if len(ndms.IfDownCalls) != 1 {
		t.Errorf("InterfaceDown should be called to bring down stale interface")
	}
}

func TestOperatorOS5_InterfaceUp(t *testing.T) {
	ndms := &MockNDMSClient{}
	recorder := &ipRunRecorder{}

	op := NewOperatorOS5(ndms, &MockWGClient{}, &MockBackend{}, &MockFirewall{}, nil)
	op.ipRun = recorder.run

	err := op.InterfaceUp(context.Background(), "awg0")

	if err != nil {
		t.Fatalf("InterfaceUp() error = %v", err)
	}
	// Kernel: NDMS InterfaceUp is skipped (intent already persisted)
	if len(ndms.IfUpCalls) != 0 {
		t.Errorf("NDMS.InterfaceUp should NOT be called for kernel, got %d", len(ndms.IfUpCalls))
	}
	// Kernel: ip link set up must be called
	if len(recorder.Calls) != 1 {
		t.Fatalf("Expected 1 ip call, got %d", len(recorder.Calls))
	}
	if !strings.Contains(recorder.Calls[0], "link set up dev opkgtun0") {
		t.Errorf("ip link set up not called, got: %s", recorder.Calls[0])
	}
}

func TestOperatorOS5_InterfaceDown(t *testing.T) {
	ndms := &MockNDMSClient{}
	op := newTestOperator(ndms, &MockWGClient{}, &MockBackend{}, &MockFirewall{})

	err := op.InterfaceDown(context.Background(), "awg0")

	if err != nil {
		t.Fatalf("InterfaceDown() error = %v", err)
	}
	if len(ndms.IfDownCalls) != 1 || ndms.IfDownCalls[0] != "OpkgTun0" {
		t.Errorf("NDMS.InterfaceDown not called correctly")
	}
}

func TestOperatorOS5_ApplyConfig(t *testing.T) {
	wgClient := &MockWGClient{}
	op := newTestOperator(&MockNDMSClient{}, wgClient, &MockBackend{}, &MockFirewall{})

	err := op.ApplyConfig(context.Background(), "awg0", "/tmp/new.conf")

	if err != nil {
		t.Fatalf("ApplyConfig() error = %v", err)
	}
	if len(wgClient.SetConfCalls) != 1 {
		t.Errorf("WG.SetConf not called")
	}
	if wgClient.SetConfCalls[0].Iface != "opkgtun0" {
		t.Errorf("SetConf iface = %s, want opkgtun0", wgClient.SetConfCalls[0].Iface)
	}
}

func TestOperatorOS5_SetupEndpointRoute_Success(t *testing.T) {
	ndmsMock := &MockNDMSClient{}
	wgClient := &MockWGClient{showError: errors.New("no wg show")}

	// Mock ip commands: ip route get returns gateway, ip route del/add succeed
	ipMock := func(_ context.Context, name string, args ...string) (*exec.Result, error) {
		cmd := name + " " + strings.Join(args, " ")
		if strings.Contains(cmd, "route get 1.2.3.4") {
			return &exec.Result{Stdout: "1.2.3.4 via 10.0.0.1 dev eth3 src 192.168.1.2"}, nil
		}
		return &exec.Result{}, nil
	}

	op := NewOperatorOS5(ndmsMock, wgClient, &MockBackend{}, &MockFirewall{}, nil)
	op.ipRun = ipMock

	// SetupEndpointRoute with IP endpoint (so DNS fallback resolves immediately)
	ip, err := op.SetupEndpointRoute(context.Background(), "awg0", "1.2.3.4:51820", "eth3", "ISP")

	if err != nil {
		t.Fatalf("SetupEndpointRoute() error = %v", err)
	}
	if ip != "1.2.3.4" {
		t.Errorf("SetupEndpointRoute() returned ip = %q, want %q", ip, "1.2.3.4")
	}

	// Verify tracked in map
	tracked := op.GetTrackedEndpointIP("awg0")
	if tracked != "1.2.3.4" {
		t.Errorf("GetTrackedEndpointIP() = %q, want %q", tracked, "1.2.3.4")
	}
}

func TestOperatorOS5_SetupEndpointRoute_PPPoE(t *testing.T) {
	ndmsMock := &MockNDMSClient{}
	wgClient := &MockWGClient{showError: errors.New("no wg show")}

	// PPPoE: ip route get returns device only, no gateway
	ipMock := func(_ context.Context, name string, args ...string) (*exec.Result, error) {
		cmd := name + " " + strings.Join(args, " ")
		if strings.Contains(cmd, "route get 5.6.7.8") {
			return &exec.Result{Stdout: "5.6.7.8 dev ppp0 src 10.64.0.2"}, nil
		}
		return &exec.Result{}, nil
	}

	op := NewOperatorOS5(ndmsMock, wgClient, &MockBackend{}, &MockFirewall{}, nil)
	op.ipRun = ipMock

	ip, err := op.SetupEndpointRoute(context.Background(), "awg0", "5.6.7.8:51820", "ppp0", "PPPoE1")
	if err != nil {
		t.Fatalf("SetupEndpointRoute() error = %v", err)
	}
	if ip != "5.6.7.8" {
		t.Errorf("SetupEndpointRoute() returned ip = %q, want %q", ip, "5.6.7.8")
	}
}

func TestOperatorOS5_SetupEndpointRoute_NoOifConstraint(t *testing.T) {
	ndmsMock := &MockNDMSClient{}
	wgClient := &MockWGClient{showError: errors.New("no wg show")}

	// Verify that empty kernelDevice means no oif in ip route get
	var routeGetCmd string
	ipMock := func(_ context.Context, name string, args ...string) (*exec.Result, error) {
		cmd := name + " " + strings.Join(args, " ")
		if strings.Contains(cmd, "route get") {
			routeGetCmd = cmd
			return &exec.Result{Stdout: "9.8.7.6 via 10.0.0.1 dev eth3 src 192.168.1.2"}, nil
		}
		return &exec.Result{}, nil
	}

	op := NewOperatorOS5(ndmsMock, wgClient, &MockBackend{}, &MockFirewall{}, nil)
	op.ipRun = ipMock

	_, err := op.SetupEndpointRoute(context.Background(), "awg0", "9.8.7.6:51820", "", "")
	if err != nil {
		t.Fatalf("SetupEndpointRoute() error = %v", err)
	}
	if strings.Contains(routeGetCmd, "oif") {
		t.Errorf("ip route get should NOT contain oif when kernelDevice is empty, got: %s", routeGetCmd)
	}
}

func TestOperatorOS5_CleanupEndpointRoute_RefCounting(t *testing.T) {
	ndmsMock := &MockNDMSClient{}
	recorder := &ipRunRecorder{}

	op := NewOperatorOS5(ndmsMock, &MockWGClient{showError: errors.New("no wg")}, &MockBackend{}, &MockFirewall{}, nil)
	op.ipRun = recorder.run

	// Setup two tunnels pointing to the same IP
	op.endpointRoutes["awg0"] = "1.2.3.4"
	op.endpointRoutes["awg1"] = "1.2.3.4"

	// Cleanup awg0 -- should NOT remove route (awg1 still uses it)
	_ = op.CleanupEndpointRoute(context.Background(), "awg0")

	// awg0 should be removed from map
	if ip := op.GetTrackedEndpointIP("awg0"); ip != "" {
		t.Errorf("awg0 should be removed from tracking, got %q", ip)
	}
	// awg1 should still be tracked
	if ip := op.GetTrackedEndpointIP("awg1"); ip != "1.2.3.4" {
		t.Errorf("awg1 should still be tracked, got %q", ip)
	}
	// No ip route del should be called yet (still in use)
	for _, call := range recorder.Calls {
		if strings.Contains(call, "route del 1.2.3.4") {
			t.Errorf("ip route del should not be called while IP is still in use, got: %s", call)
		}
	}

	// Reset recorder
	recorder.Calls = nil

	// Cleanup awg1 -- should remove route (last reference)
	_ = op.CleanupEndpointRoute(context.Background(), "awg1")

	if ip := op.GetTrackedEndpointIP("awg1"); ip != "" {
		t.Errorf("awg1 should be removed from tracking, got %q", ip)
	}
	// Kernel: ip route del should be called (last reference removed)
	foundRouteDel := false
	for _, call := range recorder.Calls {
		if strings.Contains(call, "route del 1.2.3.4/32") {
			foundRouteDel = true
		}
	}
	if !foundRouteDel {
		t.Errorf("ip route del should be called for last reference, got: %v", recorder.Calls)
	}
}

func TestOperatorOS5_CleanupEndpointRoute_EmptyMap(t *testing.T) {
	op := newTestOperator(&MockNDMSClient{}, &MockWGClient{}, &MockBackend{}, &MockFirewall{})

	// Cleanup when nothing is tracked -- should return nil without errors
	err := op.CleanupEndpointRoute(context.Background(), "awg0")
	if err != nil {
		t.Fatalf("CleanupEndpointRoute() error = %v", err)
	}
}

func TestOperatorOS5_RestoreEndpointTracking(t *testing.T) {
	ndmsMock := &MockNDMSClient{}
	wgClient := &MockWGClient{showError: errors.New("no wg")}
	op := newTestOperator(ndmsMock, wgClient, &MockBackend{}, &MockFirewall{})

	// Restore tracking for IP endpoint
	ip, err := op.RestoreEndpointTracking(context.Background(), "awg0", "5.6.7.8:51820", "ISP")

	if err != nil {
		t.Fatalf("RestoreEndpointTracking() error = %v", err)
	}
	if ip != "5.6.7.8" {
		t.Errorf("RestoreEndpointTracking() returned ip = %q, want %q", ip, "5.6.7.8")
	}

	// Verify tracked
	tracked := op.GetTrackedEndpointIP("awg0")
	if tracked != "5.6.7.8" {
		t.Errorf("GetTrackedEndpointIP() = %q, want %q", tracked, "5.6.7.8")
	}
}

func TestOperatorOS5_GetTrackedEndpointIP_Empty(t *testing.T) {
	op := newTestOperator(&MockNDMSClient{}, &MockWGClient{}, &MockBackend{}, &MockFirewall{})

	ip := op.GetTrackedEndpointIP("awg0")
	if ip != "" {
		t.Errorf("GetTrackedEndpointIP() = %q, want empty string", ip)
	}
}

func TestOperatorOS5_NamesConversion(t *testing.T) {
	// Verify tunnel ID "awg0" maps to:
	// - NDMSName: "OpkgTun0"
	// - IfaceName: "opkgtun0"

	// Use opkgTunExists=false so Start creates OpkgTun and calls SetAddress
	ndms := &MockNDMSClient{opkgTunExists: false}
	backendMock := &MockBackend{}
	wgClient := &MockWGClient{}

	op := newTestOperator(ndms, wgClient, backendMock, &MockFirewall{})

	cfg := tunnel.Config{
		ID:       "awg0",
		Name:     "Test",
		Address:  "10.0.0.1",
		MTU:      1420,
		ConfPath: "/tmp/awg0.conf",
	}

	_ = op.Start(context.Background(), cfg)

	// Backend uses IfaceName (opkgtun0)
	if backendMock.StartCalls[0] != "opkgtun0" {
		t.Errorf("Backend.Start iface = %s, want opkgtun0", backendMock.StartCalls[0])
	}

	// NDMS uses NDMSName (OpkgTun0) -- verified via SetAddress (only called for justCreated)
	if ndms.SetAddrCalls[0].Name != "OpkgTun0" {
		t.Errorf("NDMS.SetAddress name = %s, want OpkgTun0", ndms.SetAddrCalls[0].Name)
	}
}
