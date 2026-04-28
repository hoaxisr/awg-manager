package command

import (
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// HookNotifier is the subset of the existing tunnel.HookNotifier used by
// Commands that change interface state. Commands call ExpectHook before
// the mutating POST so the orchestrator can filter the self-triggered
// hook event. Optional — nil means no hook filtering.
type HookNotifier interface {
	ExpectHook(ndmsName, level string)
}

// Commands bundles every NDMS Command group.
type Commands struct {
	Interfaces   *InterfaceCommands
	Proxies      *ProxyCommands
	Wireguard    *WireguardCommands
	Policies     *PolicyCommands
	Routes       *RouteCommands
	DNSRoutes    *DNSRouteCommands
	ObjectGroups *ObjectGroupCommands
	PingCheck    *PingCheckCommands
	Save         *SaveCoordinator
}

// Deps groups the non-Command dependencies NewCommands needs.
type Deps struct {
	Poster       Poster
	Save         *SaveCoordinator
	Queries      *query.Queries
	HookNotifier HookNotifier
	IsOS5        func() bool
}

// SetHookNotifier fans the HookNotifier out to every Command group that
// uses it (currently Interfaces + Policies). Used to break the construction
// cycle between Commands and the Orchestrator.
func (c *Commands) SetHookNotifier(hn HookNotifier) {
	if c.Interfaces != nil {
		c.Interfaces.SetHookNotifier(hn)
	}
	if c.Policies != nil {
		c.Policies.SetHookNotifier(hn)
	}
}

// NewCommands constructs the full Command registry.
func NewCommands(d Deps) *Commands {
	return &Commands{
		Interfaces:   NewInterfaceCommands(d.Poster, d.Save, d.Queries, d.HookNotifier),
		Proxies:      NewProxyCommands(d.Poster, d.Save, d.Queries),
		Wireguard:    NewWireguardCommands(d.Poster, d.Save, d.Queries),
		Policies:     NewPolicyCommands(d.Poster, d.Save, d.Queries, d.HookNotifier),
		Routes:       NewRouteCommands(d.Poster, d.Save, d.Queries),
		DNSRoutes:    NewDNSRouteCommands(d.Poster, d.Save, d.Queries, d.IsOS5),
		ObjectGroups: NewObjectGroupCommands(d.Poster, d.Save, d.Queries),
		PingCheck:    NewPingCheckCommands(d.Poster, d.Save, d.Queries),
		Save:         d.Save,
	}
}
