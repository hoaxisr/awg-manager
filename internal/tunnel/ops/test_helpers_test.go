package ops

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
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
	deleteError  error
	setAddrError error
	setMTUError  error
	ifUpError    error
	setRouteError error
	saveError           error
	defaultGateway      string
	gatewayError        error

	// Call tracking
	DeleteCalls      []string
	SetAddrCalls     []struct{ Name, Addr string }
	SetMTUCalls      []struct{ Name string; MTU int }
	IfUpCalls        []string
	SetRouteCalls    []string
	RemoveRouteCalls []string
	SaveCalls        int
}

func (m *MockNDMSClient) ShowInterface(ctx context.Context, name string) (string, error) {
	return m.showInterfaceOutput, m.showInterfaceError
}

func (m *MockNDMSClient) CreateOpkgTun(ctx context.Context, name, description string) error {
	return nil
}

func (m *MockNDMSClient) SetIPGlobal(ctx context.Context, name string) error {
	return nil
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

func (m *MockNDMSClient) SetDNS(ctx context.Context, name string, servers []string) error {
	return nil
}

func (m *MockNDMSClient) ClearDNS(ctx context.Context, name string, servers []string) error {
	return nil
}

func (m *MockNDMSClient) SetDescription(ctx context.Context, name, description string) error {
	return nil
}

func (m *MockNDMSClient) InterfaceUp(ctx context.Context, name string) error {
	m.IfUpCalls = append(m.IfUpCalls, name)
	return m.ifUpError
}

func (m *MockNDMSClient) InterfaceDown(ctx context.Context, name string) error {
	return nil
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
	return nil
}

func (m *MockNDMSClient) SetIPv6DefaultRoute(ctx context.Context, name string) error {
	return nil
}

func (m *MockNDMSClient) RemoveIPv6DefaultRoute(ctx context.Context, name string) {}

func (m *MockNDMSClient) GetInterfaceAddress(ctx context.Context, iface string) (string, string, error) {
	return "", "", nil
}

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

func (m *MockNDMSClient) HasWANIPv6(ctx context.Context, ifaceName string) bool {
	return false
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
func (m *MockNDMSClient) ListSystemWireguardTunnels(ctx context.Context) ([]ndms.SystemWireguardTunnel, error) {
	return nil, nil
}
func (m *MockNDMSClient) GetSystemWireguardTunnel(ctx context.Context, name string) (*ndms.SystemWireguardTunnel, error) {
	return nil, nil
}
func (m *MockNDMSClient) GetASCParams(ctx context.Context, name string) (json.RawMessage, error) {
	return nil, nil
}
func (m *MockNDMSClient) SetASCParams(ctx context.Context, name string, params json.RawMessage) error {
	return nil
}
func (m *MockNDMSClient) GetWireguardServer(ctx context.Context, name string) (*ndms.WireguardServer, error) {
	return nil, nil
}
func (m *MockNDMSClient) GetWireguardServerConfig(ctx context.Context, name string) (*ndms.WireguardServerConfig, error) {
	return nil, nil
}
func (m *MockNDMSClient) ListAllWireguardServers(ctx context.Context) ([]ndms.WireguardServer, error) {
	return nil, nil
}
func (m *MockNDMSClient) FindFreeWireguardIndex(ctx context.Context) (int, error) {
	return 0, nil
}
func (m *MockNDMSClient) ConfigurePingCheck(ctx context.Context, profile, ifaceName string, cfg ndms.PingCheckConfig) error {
	return nil
}
func (m *MockNDMSClient) RemovePingCheck(ctx context.Context, profile, ifaceName string) error {
	return nil
}
func (m *MockNDMSClient) ShowPingCheck(ctx context.Context, profile string) (*ndms.PingCheckStatus, error) {
	return nil, nil
}
func (m *MockNDMSClient) RCIGet(ctx context.Context, path string) (json.RawMessage, error) {
	return nil, nil
}

func (m *MockNDMSClient) CreateProxy(ctx context.Context, name, description, upstreamHost string, upstreamPort int, socks5UDP bool) error {
	return nil
}

func (m *MockNDMSClient) DeleteProxy(ctx context.Context, name string) error {
	return nil
}

func (m *MockNDMSClient) ProxyUp(ctx context.Context, name string) error {
	return nil
}

func (m *MockNDMSClient) ProxyDown(ctx context.Context, name string) error {
	return nil
}

func (m *MockNDMSClient) ShowProxy(ctx context.Context, name string) (*ndms.ProxyInfo, error) {
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
