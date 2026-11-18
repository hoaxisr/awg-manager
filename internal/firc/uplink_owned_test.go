package firc

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/uplink"
)

func TestManagedEditRejectsAutoBeforePersistence(t *testing.T) {
	a, m, _ := uplinkFixture(t)
	if err := a.Operator.SaveTunnelsFile(m.tunnels); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(a.Operator.TunnelsPath())
	if err != nil {
		t.Fatal(err)
	}
	policies := []uplink.Policy{{Target: uplink.Target{Engine: "firc", Ref: "managed"}, Enabled: true, Providers: []string{"isp"}}}
	a.Operator.SetManagedUplinkConfig(func(ctx context.Context, tunnels []Tunnel) ([]Tunnel, error) {
		return MaterializeOwnedUplinks(ctx, tunnels, policies, []uplink.Provider{{ID: "isp", Device: "old"}}, nil, a.Operator.Client())
	})
	edited := append([]Tunnel(nil), m.tunnels...)
	edited[0].Uplink = TunnelUplink{Kind: "auto"}
	if _, err := a.Operator.ApplyTunnels(context.Background(), edited, false); !errors.Is(err, uplink.ErrConflict) {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(a.Operator.TunnelsPath())
	if string(before) != string(after) || m.putTunnelsCount != 0 {
		t.Fatal("rejected managed edit mutated durable or live configuration")
	}
}

func TestManagedDomainEditRejectsOldRuntime(t *testing.T) {
	a, m, _ := uplinkFixture(t)
	edited := append([]Tunnel(nil), m.tunnels...)
	edited[0].Sources = append([]TunnelSource(nil), edited[0].Sources...)
	edited[0].Sources[0].Link = "vless://id@new.example:443"
	_, err := MaterializeOwnedUplinks(context.Background(), edited, []uplink.Policy{{Target: uplink.Target{Engine: "firc", Ref: "managed"}, Enabled: true, Providers: []string{"isp"}}}, []uplink.Provider{{ID: "isp", Device: "old"}}, func(context.Context, string) ([]string, error) { return []string{"9.9.9.9"}, nil }, a.Operator.Client())
	if !errors.Is(err, uplink.ErrUnsupported) || m.putTunnelsCount != 0 {
		t.Fatal(err)
	}
}
