package dnsroute

import (
	"context"
	"encoding/json"

	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// noopLogger returns a logger for tests.
func noopLogger() *logger.Logger {
	return logger.New().WithComponent("dnsroute-test")
}

// noopNDMS is a minimal ndms.Client mock that does nothing.
// Reconcile calls ShowObjectGroupFQDN, ShowDnsProxyRoute, RCIPost, Save — all no-op here.
type noopNDMS struct{}

func (n *noopNDMS) CreateOpkgTun(ctx context.Context, name, description string) error { return nil }
func (n *noopNDMS) SetIPGlobal(ctx context.Context, name string) error                 { return nil }
func (n *noopNDMS) DeleteOpkgTun(ctx context.Context, name string) error      { return nil }
func (n *noopNDMS) OpkgTunExists(ctx context.Context, name string) bool       { return false }
func (n *noopNDMS) ShowInterface(ctx context.Context, name string) (string, error) {
	return "", nil
}
func (n *noopNDMS) SetAddress(ctx context.Context, name, address string) error { return nil }
func (n *noopNDMS) SetIPv6Address(ctx context.Context, name, address string) error {
	return nil
}
func (n *noopNDMS) ClearIPv6Address(ctx context.Context, name string)          {}
func (n *noopNDMS) SetMTU(ctx context.Context, name string, mtu int) error     { return nil }
func (n *noopNDMS) SetDescription(ctx context.Context, name, description string) error {
	return nil
}
func (n *noopNDMS) InterfaceUp(ctx context.Context, name string) error        { return nil }
func (n *noopNDMS) InterfaceDown(ctx context.Context, name string) error      { return nil }
func (n *noopNDMS) SetDefaultRoute(ctx context.Context, name string) error    { return nil }
func (n *noopNDMS) RemoveDefaultRoute(ctx context.Context, name string) error { return nil }
func (n *noopNDMS) RemoveHostRoute(ctx context.Context, host string) error    { return nil }
func (n *noopNDMS) SetIPv6DefaultRoute(ctx context.Context, name string) error { return nil }
func (n *noopNDMS) RemoveIPv6DefaultRoute(ctx context.Context, name string)   {}
func (n *noopNDMS) GetInterfaceAddress(ctx context.Context, iface string) (string, string, error) {
	return "", "", nil
}
func (n *noopNDMS) GetDefaultGatewayInterface(ctx context.Context) (string, error) {
	return "", nil
}
func (n *noopNDMS) QueryAllWANInterfaces(ctx context.Context) ([]wan.Interface, error) {
	return nil, nil
}
func (n *noopNDMS) DumpIPv4Routes(ctx context.Context) string { return "" }
func (n *noopNDMS) HasWANIPv6(ctx context.Context, ifaceName string) bool {
	return false
}
func (n *noopNDMS) GetSystemName(ctx context.Context, ndmsName string) string { return ndmsName }
func (n *noopNDMS) Save(ctx context.Context) error                           { return nil }
func (n *noopNDMS) RCIPost(ctx context.Context, payload interface{}) (json.RawMessage, error) {
	return nil, nil
}
func (n *noopNDMS) ShowObjectGroupFQDN(ctx context.Context) ([]ndms.ObjectGroupFQDN, error) {
	return nil, nil
}
func (n *noopNDMS) ShowDnsProxyRoute(ctx context.Context) ([]ndms.DnsProxyRoute, error) {
	return nil, nil
}
func (n *noopNDMS) ListWireguardInterfaces(ctx context.Context) ([]ndms.WireguardInterfaceInfo, error) {
	return nil, nil
}
func (n *noopNDMS) QueryAllInterfaces(ctx context.Context) ([]ndms.AllInterface, error) {
	return nil, nil
}
func (n *noopNDMS) SetDNS(ctx context.Context, name string, servers []string) error { return nil }
func (n *noopNDMS) ClearDNS(ctx context.Context, name string, servers []string) error {
	return nil
}
func (n *noopNDMS) ListSystemWireguardTunnels(ctx context.Context) ([]ndms.SystemWireguardTunnel, error) {
	return nil, nil
}
func (n *noopNDMS) GetSystemWireguardTunnel(ctx context.Context, name string) (*ndms.SystemWireguardTunnel, error) {
	return nil, nil
}
func (n *noopNDMS) GetASCParams(ctx context.Context, name string) (json.RawMessage, error) {
	return nil, nil
}
func (n *noopNDMS) SetASCParams(ctx context.Context, name string, params json.RawMessage) error {
	return nil
}
func (n *noopNDMS) GetWireguardServer(ctx context.Context, name string) (*ndms.WireguardServer, error) {
	return nil, nil
}
func (n *noopNDMS) GetWireguardServerConfig(ctx context.Context, name string) (*ndms.WireguardServerConfig, error) {
	return nil, nil
}
func (n *noopNDMS) ListAllWireguardServers(ctx context.Context) ([]ndms.WireguardServer, error) {
	return nil, nil
}
func (n *noopNDMS) FindFreeWireguardIndex(ctx context.Context) (int, error)  { return 0, nil }
func (n *noopNDMS) ConfigurePingCheck(ctx context.Context, profile, ifaceName string, cfg ndms.PingCheckConfig) error {
	return nil
}
func (n *noopNDMS) RemovePingCheck(ctx context.Context, profile, ifaceName string) error {
	return nil
}
func (n *noopNDMS) ShowPingCheck(ctx context.Context, profile string) (*ndms.PingCheckStatus, error) {
	return nil, nil
}
func (n *noopNDMS) RCIGet(ctx context.Context, path string) (json.RawMessage, error) {
	return nil, nil
}

func (n *noopNDMS) CreateProxy(ctx context.Context, name, description, upstreamHost string, upstreamPort int, socks5UDP bool) error {
	return nil
}

func (n *noopNDMS) DeleteProxy(ctx context.Context, name string) error {
	return nil
}

func (n *noopNDMS) ProxyUp(ctx context.Context, name string) error {
	return nil
}

func (n *noopNDMS) ProxyDown(ctx context.Context, name string) error {
	return nil
}

func (n *noopNDMS) ShowProxy(ctx context.Context, name string) (*ndms.ProxyInfo, error) {
	return nil, nil
}
