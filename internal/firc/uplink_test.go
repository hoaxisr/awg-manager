package firc

import (
	"context"
	"errors"
	"net"
	"net/http/httptest"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/uplink"
)

func uplinkFixture(t *testing.T) (*UplinkAdapter, *mockFircServer, string) {
	t.Helper()
	m, h := newMockFircHandler()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	o := NewOperator(Options{ConfigDir: t.TempDir()})
	o.SetClient(NewHTTPClient(s.URL, s.Client()))
	if err := o.EnsureConfigFiles(); err != nil {
		t.Fatal(err)
	}
	interfaces, err := net.Interfaces()
	if err != nil || len(interfaces) == 0 {
		t.Fatal("no local interfaces", err)
	}
	device := interfaces[0].Name
	m.tunnels = []Tunnel{
		{ID: "managed", Device: "tunvless0", Enable: true, Uplink: TunnelUplink{Kind: "iface", Ref: "old"}, Sources: []TunnelSource{{Kind: "link", Link: "vless://11111111-2222-3333-4444-555555555555@192.0.2.1:443?security=tls&sni=example.com"}}, Inbounds: []TunnelInbound{{Listen: "127.0.0.1", Port: 1088, Node: TunnelNodePool}}},
		{ID: "neighbour", Device: "tunvless1", Enable: false, Uplink: TunnelUplink{Kind: "iface", Ref: "untouched"}},
	}
	return &UplinkAdapter{Operator: o, Active: func() bool { return true }}, m, device
}

func TestUplinkActivationPreservesOtherTunnels(t *testing.T) {
	a, m, device := uplinkFixture(t)
	target := uplink.Target{Engine: "firc", Ref: "managed"}
	if err := a.Activate(context.Background(), target, "old", device); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tunnels[0].Uplink.Ref != device || m.tunnels[1].Uplink.Ref != "untouched" || m.tunnels[1].Enable {
		t.Fatal(m.tunnels)
	}
	if m.putTunnelsCount != 1 {
		t.Fatal(m.putTunnelsCount)
	}
}

func TestUplinkActivationRejectsConcurrentBindingChange(t *testing.T) {
	a, m, device := uplinkFixture(t)
	err := a.Activate(context.Background(), uplink.Target{Engine: "firc", Ref: "managed"}, "stale", device)
	if !errors.Is(err, uplink.ErrConflict) {
		t.Fatal(err)
	}
	if m.putTunnelsCount != 0 {
		t.Fatal("stale change mutated runtime")
	}
}

func TestUplinkDomainAndMissingInboundAreUnsupported(t *testing.T) {
	for _, kind := range []string{"domain", "inbound"} {
		t.Run(kind, func(t *testing.T) {
			a, m, _ := uplinkFixture(t)
			if kind == "domain" {
				m.tunnels[0].Sources[0].Link = "vless://id@example.com:443"
			} else {
				m.tunnels[0].Inbounds = nil
			}
			if err := a.Validate(context.Background(), uplink.Target{Engine: "firc", Ref: "managed"}); !errors.Is(err, uplink.ErrUnsupported) {
				t.Fatal(err)
			}
			if m.putTunnelsCount != 0 {
				t.Fatal("validation mutated runtime")
			}
		})
	}
}

func TestUplinkDisabledTunnelCannotBeStarted(t *testing.T) {
	a, m, device := uplinkFixture(t)
	m.tunnels[0].Enable = false
	err := a.Activate(context.Background(), uplink.Target{Engine: "firc", Ref: "managed"}, "old", device)
	if err == nil || m.putTunnelsCount != 0 {
		t.Fatal(err, m.putTunnelsCount)
	}
}

func TestUplinkDomainDNSCapability(t *testing.T) {
	for _, failDNS := range []bool{false, true} {
		t.Run(map[bool]string{false: "bound", true: "unavailable"}[failDNS], func(t *testing.T) {
			a, m, device := uplinkFixture(t)
			m.uplinkDNSVersion = 1
			m.tunnels[0].Sources[0].Link = "vless://id@example.com:443"
			a.DNSForDevice = func(context.Context, string) ([]string, error) {
				if failDNS {
					return nil, errors.New("provider DNS unavailable")
				}
				return []string{"9.9.9.9"}, nil
			}
			err := a.Activate(context.Background(), uplink.Target{Engine: "firc", Ref: "managed"}, "old", device)
			if failDNS {
				if !errors.Is(err, uplink.ErrPathUnavailable) || m.putTunnelsCount != 0 {
					t.Fatal(err, m.putTunnelsCount)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(m.tunnels[0].EndpointDNS) != 1 || m.tunnels[0].EndpointDNS[0] != "9.9.9.9" || m.tunnels[1].Uplink.Ref != "untouched" {
				t.Fatal(m.tunnels)
			}
		})
	}
}

func TestUplinkDNSMetadataSurvivesOlderEditor(t *testing.T) {
	for _, clear := range []bool{false, true} {
		a, m, _ := uplinkFixture(t)
		m.tunnels[0].EndpointDNS = []string{"9.9.9.9"}
		edited := append([]Tunnel(nil), m.tunnels...)
		edited[0].EndpointDNS = nil
		if clear {
			edited[0].EndpointDNS = []string{}
		}
		if _, err := a.Operator.ApplyTunnels(context.Background(), edited, false); err != nil {
			t.Fatal(err)
		}
		if clear && len(m.tunnels[0].EndpointDNS) != 0 {
			t.Fatal("explicit clear was ignored")
		}
		if !clear && (len(m.tunnels[0].EndpointDNS) != 1 || m.tunnels[0].EndpointDNS[0] != "9.9.9.9") {
			t.Fatal("editor dropped strict DNS binding")
		}
	}
}
