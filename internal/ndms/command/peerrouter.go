package command

import (
	"context"
	"net"
)

// PeerRouter — адаптер peersubnet.Router над Commands: переводит *net.IPNet в
// формы address/mask RCI. Один на оба пути (системный сервер и managed) — формы
// allow-ips у них одинаковые. /32 уезжает host-формой маршрута, чтобы запись в
// running-config совпадала с тем, что потом ищет NetworkRouteOwner.
type PeerRouter struct{ cmds *Commands }

func NewPeerRouter(c *Commands) *PeerRouter { return &PeerRouter{cmds: c} }

func dotted(n *net.IPNet) (address, mask string) {
	return n.IP.String(), net.IP(n.Mask).String()
}

func routeSpec(n *net.IPNet, iface, comment string) StaticRouteSpec {
	address, mask := dotted(n)
	if ones, bits := n.Mask.Size(); bits != 0 && ones == bits {
		return StaticRouteSpec{Host: address, Interface: iface, Comment: comment}
	}
	return StaticRouteSpec{Network: address, Mask: mask, Interface: iface, Comment: comment}
}

func (r *PeerRouter) AddAllowIP(ctx context.Context, iface, pubkey string, n *net.IPNet) error {
	a, m := dotted(n)
	return r.cmds.Wireguard.AddPeerAllowIP(ctx, iface, pubkey, a, m)
}

func (r *PeerRouter) RemoveAllowIP(ctx context.Context, iface, pubkey string, n *net.IPNet) error {
	a, m := dotted(n)
	return r.cmds.Wireguard.RemovePeerAllowIP(ctx, iface, pubkey, a, m)
}

func (r *PeerRouter) NetworkRouteOwner(ctx context.Context, n *net.IPNet, iface, comment string) (bool, bool, error) {
	a, m := dotted(n)
	return r.cmds.Routes.NetworkRouteOwner(ctx, a, m, iface, comment)
}

func (r *PeerRouter) AddNetworkRoute(ctx context.Context, n *net.IPNet, iface, comment string) error {
	return r.cmds.Routes.AddStaticRoute(ctx, routeSpec(n, iface, comment))
}

func (r *PeerRouter) RemoveOwnNetworkRoute(ctx context.Context, n *net.IPNet, iface, comment string) (bool, error) {
	return r.cmds.Routes.RemoveOwnNetworkRoute(ctx, routeSpec(n, iface, comment))
}
