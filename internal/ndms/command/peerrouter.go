package command

import (
	"context"
	"fmt"
	"net"

	"github.com/hoaxisr/awg-manager/internal/peersubnet"
)

// PeerRouter — адаптер peersubnet.Router над Commands: переводит *net.IPNet в
// формы address/mask RCI. Один на оба пути (системный сервер и managed) — формы
// allow-ips у них одинаковые. /32 уезжает host-формой маршрута, чтобы запись в
// running-config совпадала с тем, что потом ищет NetworkRouteOwner. Чтения —
// свежие, мимо кэша: по ним сверка решает, что снимать.
type PeerRouter struct{ cmds *Commands }

var _ peersubnet.Router = (*PeerRouter)(nil)

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

func (r *PeerRouter) PeerAllowIPs(ctx context.Context, iface, pubkey string) ([]*net.IPNet, error) {
	q := r.cmds.Wireguard.queries
	if q == nil || q.WGServers == nil {
		return nil, fmt.Errorf("wireguard server store not wired")
	}
	peers, err := q.WGServers.PeersRCFresh(ctx, iface)
	if err != nil {
		return nil, err
	}
	var out []*net.IPNet
	for _, p := range peers {
		if p.PublicKey != pubkey {
			continue
		}
		for _, a := range p.AllowedIPs {
			if _, n, err := net.ParseCIDR(a); err == nil {
				out = append(out, n)
			}
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
		var n *net.IPNet
		if e.Host != "" {
			if ip := net.ParseIP(e.Host).To4(); ip != nil {
				n = &net.IPNet{IP: ip, Mask: net.CIDRMask(32, 32)}
			}
		} else if ip, m := net.ParseIP(e.Network).To4(), net.ParseIP(e.Mask).To4(); ip != nil && m != nil {
			mask := net.IPMask(m)
			if ones, bits := mask.Size(); bits != 0 || ones != 0 {
				n = &net.IPNet{IP: ip.Mask(mask), Mask: mask}
			}
		}
		if n != nil {
			out = append(out, peersubnet.Route{Net: n, Comment: e.Comment})
		}
	}
	return out, nil
}

func (r *PeerRouter) AddNetworkRoute(ctx context.Context, n *net.IPNet, iface, comment string) error {
	return r.cmds.Routes.AddStaticRoute(ctx, routeSpec(n, iface, comment))
}

func (r *PeerRouter) RemoveOwnNetworkRoute(ctx context.Context, n *net.IPNet, iface, comment string) (bool, error) {
	return r.cmds.Routes.RemoveOwnNetworkRoute(ctx, routeSpec(n, iface, comment))
}
