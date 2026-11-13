package awgm

import (
	"context"
	"fmt"
	"strings"
)

// DirectPath is the external path used by curl and UDP probes. Empty Interface
// means the OS default route of a direct outbound without bind_interface.
type DirectPath struct {
	Interface string
	Outbound  string
}

type RouteClient interface {
	RouterStatus(context.Context) (RouterStatus, error)
	Outbounds(context.Context) ([]Outbound, error)
	ProxyGroups(context.Context) ([]ProxyGroup, error)
	TunnelCatalog(context.Context) ([]Tunnel, error)
}

type RouteResolver struct {
	client RouteClient
}

func NewRouteResolver(client RouteClient) *RouteResolver { return &RouteResolver{client: client} }

func (r *RouteResolver) Resolve(ctx context.Context) (DirectPath, error) {
	status, err := r.client.RouterStatus(ctx)
	if err != nil {
		return DirectPath{}, err
	}
	if status.Final == "" {
		return DirectPath{}, fmt.Errorf("sing-box route.final is empty")
	}
	return r.ResolveOutbound(ctx, status.Final)
}

func (r *RouteResolver) ResolveOutbound(ctx context.Context, tag string) (DirectPath, error) {
	if tag == "direct" {
		return DirectPath{Outbound: "direct"}, nil
	}
	outbounds, err := r.client.Outbounds(ctx)
	if err != nil {
		return DirectPath{}, err
	}
	groups, err := r.client.ProxyGroups(ctx)
	if err != nil {
		return DirectPath{}, err
	}
	tunnels, err := r.client.TunnelCatalog(ctx)
	if err != nil {
		return DirectPath{}, err
	}
	state := routeState{outbounds: map[string]Outbound{}, groups: map[string]ProxyGroup{}, tunnels: map[string]Tunnel{}, members: map[string]Tunnel{}}
	ambiguous := map[string]bool{}
	for _, outbound := range outbounds {
		if outbound.Tag != "" {
			state.outbounds[outbound.Tag] = outbound
		}
	}
	for _, group := range groups {
		if group.Tag != "" {
			state.groups[group.Tag] = group
		}
	}
	for _, tunnel := range tunnels {
		if tunnel.Outbound != "" {
			if _, exists := state.tunnels[tunnel.Outbound]; exists {
				ambiguous[tunnel.Outbound] = true
			} else {
				state.tunnels[tunnel.Outbound] = tunnel
			}
		}
		for _, member := range tunnel.Members {
			if _, exists := state.members[member]; exists {
				ambiguous[member] = true
			} else {
				state.members[member] = tunnel
			}
		}
	}
	state.ambiguous = ambiguous
	return state.resolve(tag, map[string]bool{})
}

type routeState struct {
	outbounds map[string]Outbound
	groups    map[string]ProxyGroup
	tunnels   map[string]Tunnel
	members   map[string]Tunnel
	ambiguous map[string]bool
}

func (s routeState) resolve(tag string, visited map[string]bool) (DirectPath, error) {
	if tag == "" {
		return DirectPath{}, fmt.Errorf("empty outbound tag")
	}
	if tag == "direct" {
		return DirectPath{Outbound: tag}, nil
	}
	if visited[tag] {
		return DirectPath{}, fmt.Errorf("outbound cycle at %q", tag)
	}
	visited[tag] = true
	defer delete(visited, tag)
	if s.ambiguous[tag] {
		return DirectPath{}, fmt.Errorf("multiple interface mappings for outbound %q", tag)
	}
	if outbound, ok := s.outbounds[tag]; ok {
		switch strings.ToLower(outbound.Type) {
		case "direct":
			return DirectPath{Interface: outbound.BindInterface, Outbound: tag}, nil
		case "selector", "urltest", "loadbalance", "fallback":
			selected := ""
			if group, ok := s.groups[tag]; ok {
				selected = group.Now
			}
			if selected == "" {
				selected = outbound.Default
			}
			if selected == "" && len(outbound.Outbounds) == 1 {
				selected = outbound.Outbounds[0]
			}
			if selected == "" {
				return DirectPath{}, fmt.Errorf("outbound %q has no unambiguous runtime/default selection", tag)
			}
			return s.resolve(selected, visited)
		default:
			return DirectPath{}, fmt.Errorf("unsupported outbound type %q for %q", outbound.Type, tag)
		}
	}
	if tunnel, ok := s.tunnels[tag]; ok {
		if !tunnel.Enabled || tunnel.Interface == "" {
			return DirectPath{}, fmt.Errorf("tunnel outbound %q is unavailable", tag)
		}
		return DirectPath{Interface: tunnel.Interface, Outbound: tunnel.Outbound}, nil
	}
	if tunnel, ok := s.members[tag]; ok {
		if !tunnel.Enabled || tunnel.Interface == "" {
			return DirectPath{}, fmt.Errorf("tunnel member %q is unavailable", tag)
		}
		return DirectPath{Interface: tunnel.Interface, Outbound: tunnel.Outbound}, nil
	}
	return DirectPath{}, fmt.Errorf("no unambiguous direct/interface mapping for outbound %q", tag)
}

// FirstTunnel implements the setting default without persisting over a saved
// choice. Disabled or incompletely mapped tunnels are not eligible.
func FirstTunnel(ctx context.Context, client RouteClient) (Tunnel, error) {
	tunnels, err := client.TunnelCatalog(ctx)
	if err != nil {
		return Tunnel{}, err
	}
	for _, tunnel := range tunnels {
		if tunnel.Enabled && tunnel.Interface != "" && tunnel.Outbound != "" {
			return tunnel, nil
		}
	}
	return Tunnel{}, fmt.Errorf("no available tunnel interfaces")
}
