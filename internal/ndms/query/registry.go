package query

import (
	"context"
	"fmt"
)

// Queries bundles every NDMS Query Store.
type Queries struct {
	Interfaces       *InterfaceStore
	Peers            *PeerStore
	Policies         *PolicyStore
	Hotspot          *HotspotStore
	Routes           *RouteStore
	ObjectGroups     *ObjectGroupStore
	DNSProxy         *DNSProxyStore
	PingCheckProfile *PingCheckProfileStore
	PingCheckStatus  *PingCheckStatusStore
	RunningConfig    *RunningConfigStore
	SystemInfo       *SystemInfoStore
	WGServers        *WGServerStore
}

// Deps groups the non-Store dependencies NewQueries needs.
type Deps struct {
	Getter Getter
	Logger Logger
	// IsOS5 reports whether we're running on OS 5.x. Called lazily from
	// OS-gated Stores (DNSProxyStore). Inject a fake in tests.
	IsOS5 func() bool
}

// WANInterfaceAddress returns the IPv4 address configured on the
// interface carrying the default route. Used as a local fallback when
// external WAN-IP probes (ipinfo.io etc.) fail due to DNS / upstream
// outage. Not truly "external" behind CGNAT, but accurate for plain
// PPPoE / DHCP WAN deployments.
func (q *Queries) WANInterfaceAddress(ctx context.Context) (string, error) {
	if q == nil || q.Routes == nil || q.Interfaces == nil {
		return "", fmt.Errorf("queries not wired")
	}
	name, err := q.Routes.GetDefaultGatewayInterface(ctx)
	if err != nil {
		return "", fmt.Errorf("default gateway: %w", err)
	}
	iface, err := q.Interfaces.Get(ctx, name)
	if err != nil {
		return "", fmt.Errorf("get interface %s: %w", name, err)
	}
	if iface == nil || iface.Address == "" {
		return "", fmt.Errorf("interface %s has no IPv4 address", name)
	}
	return iface.Address, nil
}

// NewQueries constructs the full registry.
func NewQueries(d Deps) *Queries {
	isOS5 := d.IsOS5
	if isOS5 == nil {
		isOS5 = func() bool { return false }
	}
	ifaces := NewInterfaceStore(d.Getter, d.Logger)
	return &Queries{
		Interfaces:       ifaces,
		Peers:            NewPeerStore(d.Getter, d.Logger),
		Policies:         NewPolicyStore(d.Getter, d.Logger),
		Hotspot:          NewHotspotStore(d.Getter, d.Logger),
		Routes:           NewRouteStore(d.Getter, d.Logger),
		ObjectGroups:     NewObjectGroupStore(d.Getter, d.Logger),
		DNSProxy:         NewDNSProxyStore(d.Getter, d.Logger, isOS5),
		PingCheckProfile: NewPingCheckProfileStore(d.Getter, d.Logger),
		PingCheckStatus:  NewPingCheckStatusStore(d.Getter, d.Logger),
		RunningConfig:    NewRunningConfigStore(d.Getter, d.Logger),
		SystemInfo:       NewSystemInfoStore(d.Getter, d.Logger),
		WGServers:        NewWGServerStore(d.Getter, d.Logger, ifaces),
	}
}
