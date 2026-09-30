package command

import (
	"context"
	"net"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/peersubnet"
)

// PeerRouter — адаптер peersubnet.Router над Commands: переводит *net.IPNet в
// формы address/mask RCI. Один на оба пути (системный сервер и managed) — формы
// allow-ips у них одинаковые. /32 уезжает host-формой маршрута, чтобы запись в
// running-config совпадала с тем, что потом ищет NetworkRouteOwner. Чтения —
// свежие, мимо кэша: по ним сверка решает, что снимать.
//
// Интерфейс подтверждает Confirm — peersubnet зовёт его один раз на сверку
// (F546); мутации принимают только доказательство.
type PeerRouter struct {
	cmds    *Commands
	queries *query.Queries
}

var _ peersubnet.Router = (*PeerRouter)(nil)

func NewPeerRouter(c *Commands, q *query.Queries) *PeerRouter {
	return &PeerRouter{cmds: c, queries: q}
}

// Confirm — iface по свежему списку интерфейсов; ok=false — интерфейса нет.
func (r *PeerRouter) Confirm(ctx context.Context, iface string) (query.Confirmed, bool, error) {
	conf, _, ok, err := r.queries.Interfaces.Confirm(ctx, iface)
	return conf, ok, err
}

func dotted(n *net.IPNet) (address, mask string) {
	return n.IP.String(), net.IP(n.Mask).String()
}

func routeSpec(n *net.IPNet, iface query.Confirmed, comment string) StaticRouteSpec {
	address, mask := dotted(n)
	if ones, bits := n.Mask.Size(); bits != 0 && ones == bits {
		return StaticRouteSpec{Host: address, Interface: iface, Comment: comment}
	}
	return StaticRouteSpec{Network: address, Mask: mask, Interface: iface, Comment: comment}
}

func (r *PeerRouter) AddAllowIP(ctx context.Context, iface query.Confirmed, pubkey string, n *net.IPNet) error {
	a, m := dotted(n)
	return r.cmds.Wireguard.AddPeerAllowIP(ctx, iface, pubkey, a, m)
}

func (r *PeerRouter) RemoveAllowIP(ctx context.Context, iface query.Confirmed, pubkey string, n *net.IPNet) error {
	a, m := dotted(n)
	return r.cmds.Wireguard.RemovePeerAllowIP(ctx, iface, pubkey, a, m)
}

func (r *PeerRouter) PeerAllowIPs(ctx context.Context, iface query.Confirmed, pubkey string) ([]*net.IPNet, error) {
	// allow-ips на отсутствующий ключ NDMS создаёт пира — сверка обязана
	// остановиться на ErrPeerNotFound.
	p, err := r.cmds.Wireguard.freshPeerIn(ctx, iface, pubkey)
	if err != nil {
		return nil, err
	}
	var out []*net.IPNet
	for _, a := range p.AllowedIPs {
		if _, n, err := net.ParseCIDR(a); err == nil {
			out = append(out, n)
		}
	}
	return out, nil
}

// InterfaceRoutes — записи /show/rc/ip/route на iface; host-форма — /32.
// Запись без читаемой IPv4-сети (неканоническая маска) пропускается.
func (r *PeerRouter) InterfaceRoutes(ctx context.Context, iface string) ([]peersubnet.Route, error) {
	entries, err := r.cmds.Routes.freshStaticRoutes(ctx)
	if err != nil {
		return nil, err
	}
	var out []peersubnet.Route
	for _, e := range entries {
		if e.Interface != iface {
			continue
		}
		if n := e.IPv4Net(); n != nil {
			out = append(out, peersubnet.Route{Net: n, Comment: e.Comment})
		}
	}
	return out, nil
}

func (r *PeerRouter) NetworkRouteOwner(ctx context.Context, n *net.IPNet, iface, comment string) (bool, bool, error) {
	a, m := dotted(n)
	return r.cmds.Routes.NetworkRouteOwner(ctx, a, m, iface, comment)
}

func (r *PeerRouter) AddNetworkRoute(ctx context.Context, n *net.IPNet, iface query.Confirmed, comment string) error {
	return r.cmds.Routes.AddStaticRoute(ctx, routeSpec(n, iface, comment))
}

func (r *PeerRouter) RemoveOwnNetworkRoute(ctx context.Context, n *net.IPNet, iface query.Confirmed, comment string) (bool, error) {
	return r.cmds.Routes.RemoveOwnNetworkRoute(ctx, routeSpec(n, iface, comment))
}
