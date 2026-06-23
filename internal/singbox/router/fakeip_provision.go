package router

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// StaticRouteSpec mirrors internal/ndms/command.StaticRouteSpec so the router
// stays decoupled from concrete ndms command types (DIP), consistent with the
// other consumer-owned router interfaces. The cmd/awg-manager adapter translates
// field-for-field.
type StaticRouteSpec struct {
	Interface string
	Host      string
	Network   string
	Mask      string
	Reject    bool
	Comment   string
	// V6 selects the IPv6 route form (bare network+interface, no mask/host/
	// reject/comment). Mirrors ndmscommand.StaticRouteSpec.V6.
	V6 bool
}

// OpkgTunProvisioner manages the fakeip-tun kernel interface lifecycle via NDMS.
type OpkgTunProvisioner interface {
	CreateOpkgTunWithSecurityLevel(ctx context.Context, name, description, securityLevel string) error
	SetIPGlobal(ctx context.Context, name string) error
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
}

// FakeIPTunParams holds the static fakeip-tun provisioning knobs not derivable
// at runtime. Defaults are spec §3.3/3.4/3.6 values; wired in cmd/awg-manager.
// (RealServer + cache path are sourced by the lifecycle layer in Slice 1D.)
type FakeIPTunParams struct {
	Inet4Range string // fakeip v4 pool (default "198.18.0.0/15", per sing-box docs)
	Inet6Range string // fakeip v6 pool (default "fc00::/18", per sing-box docs; empty disables v6)
	TunAddr4   string // tun gw /30 CIDR (default "172.18.0.1/30"); client DNS = other /30 host
	TunAddr6   string // tun gw /126 CIDR (default "fdfe:dcba:9876::1/126"; empty disables v6)
	MTU        int    // tun MTU (default 1500)
	// RealServer is the true upstream resolver the fakeip config's "real" DNS
	// server forwards to (proxy-endpoint hostnames + non-fakeip queries).
	// Default "1.1.1.1" — v1 fixed upstream; made configurable in a later slice.
	RealServer string
	// CachePath is the sing-box experimental.cache_file path (store_fakeip).
	// Not a spec-default — wired by cmd/awg-manager from singbox.DefaultCacheDBPath
	// so the router stays decoupled from the operator's path layout.
	CachePath string
}

// DefaultFakeIPTunParams returns the spec-default fakeip-tun provisioning knobs
// (spec §3.3 fakeip pools, §3.4 tun gw addresses + MTU).
// Single source of truth for the wiring site in cmd/awg-manager and tests.
func DefaultFakeIPTunParams() FakeIPTunParams {
	return FakeIPTunParams{
		Inet4Range: "198.18.0.0/15",
		Inet6Range: "fc00::/18",
		TunAddr4:   "172.18.0.1/30",
		TunAddr6:   "fdfe:dcba:9876::1/126",
		MTU:        1500,
		RealServer: "1.1.1.1", // v1 default upstream; configurable later
		// CachePath left empty — wired by main.go from singbox.DefaultCacheDBPath.
	}
}

// resolveFakeIPParams overlays the user-editable engine settings (pool4/6, MTU)
// from sr onto the wired static params base, returning the effective
// FakeIPTunParams. Single source of truth shared by enableFakeIPTun and the
// fakeip config overlay so the live tun/cache/pool can never diverge from what
// the user is editing. Mirrors the merge formerly inlined in enableFakeIPTun.
func resolveFakeIPParams(base FakeIPTunParams, sr storage.SingboxRouterSettings) FakeIPTunParams {
	p := base
	if sr.FakeIPPool4 != "" {
		p.Inet4Range = sr.FakeIPPool4
	}
	p.Inet6Range = sr.FakeIPPool6
	if sr.FakeIPPool6 == "" {
		p.TunAddr6 = ""
	}
	if sr.FakeIPMTU != 0 {
		p.MTU = sr.FakeIPMTU
	}
	return p
}
