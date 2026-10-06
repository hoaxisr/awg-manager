package command

import (
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// Commands bundles every NDMS Command group.
type Commands struct {
	// Save — координатор сохранений тех же команд: пакеты, шлющие батчи мимо
	// групп команд (nwg), заказывают сохранение через него (П24).
	Save         *SaveCoordinator
	Interfaces   *InterfaceCommands
	Proxies      *ProxyCommands
	Wireguard    *WireguardCommands
	Policies     *PolicyCommands
	Routes       *RouteCommands
	NAT          *NATCommands
	DNSRoutes    *DNSRouteCommands
	ObjectGroups *ObjectGroupCommands
	PingCheck    *PingCheckCommands
}

// Deps groups the non-Command dependencies NewCommands needs.
type Deps struct {
	Poster  Poster
	Save    *SaveCoordinator
	Queries *query.Queries
	IsOS5   func() bool
}

// NewCommands constructs the full Command registry. Паникует на nil d.Save
// (newMutator через автономные конструкторы).
func NewCommands(d Deps) *Commands {
	return &Commands{
		Save:         d.Save,
		Interfaces:   NewInterfaceCommands(d.Poster, d.Save, d.Queries),
		Proxies:      NewProxyCommands(d.Poster, d.Save, d.Queries),
		Wireguard:    NewWireguardCommands(d.Poster, d.Save, d.Queries),
		Policies:     NewPolicyCommands(d.Poster, d.Save, d.Queries),
		Routes:       NewRouteCommands(d.Poster, d.Save, d.Queries),
		NAT:          NewNATCommands(d.Poster, d.Save, d.Queries),
		DNSRoutes:    NewDNSRouteCommands(d.Poster, d.Save, d.Queries, d.IsOS5),
		ObjectGroups: NewObjectGroupCommands(d.Poster, d.Save, d.Queries),
		PingCheck:    NewPingCheckCommands(d.Poster, d.Save, d.Queries),
	}
}
