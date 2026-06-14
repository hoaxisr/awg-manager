package router

import "context"

// StaticRouteSpec mirrors internal/ndms/command.StaticRouteSpec — router cannot
// import internal/ndms (cycle). The cmd/awg-manager adapter translates field-for-field.
type StaticRouteSpec struct {
	Interface string
	Host      string
	Network   string
	Mask      string
	Reject    bool
	Comment   string
}

// OpkgTunProvisioner manages the fakeip-tun kernel interface lifecycle via NDMS.
type OpkgTunProvisioner interface {
	CreateOpkgTunWithSecurityLevel(ctx context.Context, name, description, securityLevel string) error
	DeleteOpkgTun(ctx context.Context, name string) error
	SetAddress(ctx context.Context, name, address, mask string) error
	SetIPv6Address(ctx context.Context, name, address string) error
	ClearIPv6Address(ctx context.Context, name string) error
	SetMTU(ctx context.Context, name string, mtu int) error
	InterfaceUp(ctx context.Context, name string) error
	InterfaceDown(ctx context.Context, name string) error
}

// StaticRouteProvider manages NDMS auto static routes for the fakeip pool + reject route.
type StaticRouteProvider interface {
	AddStaticRoute(ctx context.Context, route StaticRouteSpec) error
	RemoveStaticRoute(ctx context.Context, route StaticRouteSpec) error
	AddStaticRoute6(ctx context.Context, network, iface string) error
	RemoveStaticRoute6(ctx context.Context, network, iface string) error
}

// DHCPProvider delivers the tun DNS to LAN segments via DHCP pool dns-server.
type DHCPProvider interface {
	SetPoolDNS(ctx context.Context, pool string, servers []string) error
	ClearPoolDNS(ctx context.Context, pool string) error
}

// FakeIPTunParams holds the static fakeip-tun provisioning knobs not derivable
// at runtime. Defaults are spec §3.3/3.4/3.6 values; wired in cmd/awg-manager.
// (RealServer + cache path are sourced by the lifecycle layer in Slice 1D.)
type FakeIPTunParams struct {
	Inet4Range string // fakeip v4 pool (default "10.128.0.0/10")
	Inet6Range string // fakeip v6 pool (default "3f80::/10"; empty disables v6)
	TunAddr4   string // tun gw /30 CIDR (default "172.18.0.1/30"); DHCP DNS = other /30 host
	TunAddr6   string // tun gw /126 CIDR (default "fdfe:dcba:9876::1/126"; empty disables v6)
	MTU        int    // tun MTU (default 1500)
	DHCPPool   string // default DHCP pool for DNS delivery (default "_WEBADMIN")
}

// DefaultFakeIPTunParams returns the spec-default fakeip-tun provisioning knobs
// (spec §3.3 fakeip pools, §3.4 tun gw addresses + MTU, §3.6 DHCP DNS delivery).
// Single source of truth for the wiring site in cmd/awg-manager and tests.
func DefaultFakeIPTunParams() FakeIPTunParams {
	return FakeIPTunParams{
		Inet4Range: "10.128.0.0/10",
		Inet6Range: "3f80::/10",
		TunAddr4:   "172.18.0.1/30",
		TunAddr6:   "fdfe:dcba:9876::1/126",
		MTU:        1500,
		DHCPPool:   "_WEBADMIN",
	}
}
