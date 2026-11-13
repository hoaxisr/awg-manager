package awgm

import (
	"context"
	"testing"
)

type fakeRouteClient struct {
	final     string
	outbounds []Outbound
	groups    []ProxyGroup
	tunnels   []Tunnel
}

func (f fakeRouteClient) RouterStatus(context.Context) (RouterStatus, error) {
	return RouterStatus{Final: f.final}, nil
}
func (f fakeRouteClient) Outbounds(context.Context) ([]Outbound, error)     { return f.outbounds, nil }
func (f fakeRouteClient) ProxyGroups(context.Context) ([]ProxyGroup, error) { return f.groups, nil }
func (f fakeRouteClient) TunnelCatalog(context.Context) ([]Tunnel, error)   { return f.tunnels, nil }

func TestRouteResolverDirect(t *testing.T) {
	path, err := NewRouteResolver(fakeRouteClient{final: "direct"}).Resolve(context.Background())
	if err != nil || path.Interface != "" || path.Outbound != "direct" {
		t.Fatalf("path=%+v err=%v", path, err)
	}
}

func TestRouteResolverUsesRuntimeSelectorLeaf(t *testing.T) {
	client := fakeRouteClient{
		final:     "stabilization",
		outbounds: []Outbound{{Type: "selector", Tag: "stabilization", Outbounds: []string{"awg-one"}, Default: "awg-two"}},
		groups:    []ProxyGroup{{Tag: "stabilization", Type: "selector", Now: "awg-one"}},
		tunnels:   []Tunnel{{Interface: "nwg0", Outbound: "awg-one", Enabled: true}, {Interface: "nwg1", Outbound: "awg-two", Enabled: true}},
	}
	path, err := NewRouteResolver(client).Resolve(context.Background())
	if err != nil || path.Interface != "nwg0" || path.Outbound != "awg-one" {
		t.Fatalf("path=%+v err=%v", path, err)
	}
}

func TestRouteResolverDefaultAndSubscriptionMember(t *testing.T) {
	client := fakeRouteClient{
		final:     "selector",
		outbounds: []Outbound{{Type: "selector", Tag: "selector", Default: "node-a"}},
		tunnels:   []Tunnel{{Interface: "t2s3", Outbound: "sub-selector", Enabled: true, Members: []string{"node-a"}}},
	}
	path, err := NewRouteResolver(client).Resolve(context.Background())
	if err != nil || path.Interface != "t2s3" || path.Outbound != "sub-selector" {
		t.Fatalf("path=%+v err=%v", path, err)
	}
}

func TestRouteResolverRejectsAmbiguousAndCycles(t *testing.T) {
	for name, client := range map[string]fakeRouteClient{
		"ambiguous":   {final: "selector", outbounds: []Outbound{{Type: "selector", Tag: "selector", Outbounds: []string{"a", "b"}}}},
		"cycle":       {final: "a", outbounds: []Outbound{{Type: "selector", Tag: "a", Default: "b"}, {Type: "urltest", Tag: "b", Default: "a"}}},
		"unavailable": {final: "vpn", tunnels: []Tunnel{{Interface: "nwg0", Outbound: "vpn", Enabled: false}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewRouteResolver(client).Resolve(context.Background()); err == nil {
				t.Fatal("invalid route was accepted")
			}
		})
	}
}

func TestFirstTunnel(t *testing.T) {
	client := fakeRouteClient{tunnels: []Tunnel{{Interface: "nwg0", Outbound: "off", Enabled: false}, {Interface: "nocli0", Outbound: "on", Enabled: true}}}
	tunnel, err := FirstTunnel(context.Background(), client)
	if err != nil || tunnel.Interface != "nocli0" {
		t.Fatalf("tunnel=%+v err=%v", tunnel, err)
	}
}
