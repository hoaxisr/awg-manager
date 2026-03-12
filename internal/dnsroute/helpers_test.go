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

func (n *noopNDMS) CreateOpkgTun(ctx context.Context, name, description string) error {
	return nil
}
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
func (n *noopNDMS) InterfaceUp(ctx context.Context, name string) error         { return nil }
func (n *noopNDMS) InterfaceDown(ctx context.Context, name string) error       { return nil }
func (n *noopNDMS) SetDefaultRoute(ctx context.Context, name string) error     { return nil }
func (n *noopNDMS) RemoveDefaultRoute(ctx context.Context, name string) error  { return nil }
func (n *noopNDMS) RemoveHostRoute(ctx context.Context, host string) error     { return nil }
func (n *noopNDMS) SetIPv6DefaultRoute(ctx context.Context, name string) error { return nil }
func (n *noopNDMS) RemoveIPv6DefaultRoute(ctx context.Context, name string)    {}
func (n *noopNDMS) GetDefaultGatewayInterface(ctx context.Context) (string, error) {
	return "", nil
}
func (n *noopNDMS) GetInterfaceAddress(ctx context.Context, iface string) (string, string, error) {
	return "", "", nil
}
func (n *noopNDMS) QueryAllWANInterfaces(ctx context.Context) ([]wan.Interface, error) {
	return nil, nil
}
func (n *noopNDMS) DumpIPv4Routes(ctx context.Context) string { return "" }
func (n *noopNDMS) HasWANIPv6(ctx context.Context, ifaceName string) bool {
	return false
}
func (n *noopNDMS) GetHotspotClients(ctx context.Context) ([]ndms.HotspotClient, error) {
	return nil, nil
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
