package diagnostics

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

const v4RouteTable = "default dev ppp0 scope link\n203.0.113.7 via 192.168.1.1 dev eth3\n"

// F586: маршрут до IPv6 endpoint лежит в таблице IPv6, и искать его надо
// там — в `ip route show` его нет, и проверка всегда писала fail.
func TestFindEndpointRoute_IPv6EndpointReadsIPv6Table(t *testing.T) {
	ip := extractEndpointIP("peer: abc=\n  endpoint: [2a01:4f8:c0c:1234::1]:51820\n")
	if ip != "2a01:4f8:c0c:1234::1" {
		t.Fatalf("endpoint ip = %q", ip)
	}
	v6 := "2a01:4f8:c0c:1234::1 via fe80::1 dev eth3 metric 1024 pref medium\nfe80::/64 dev br0 proto kernel metric 256 pref medium\n"
	got := findEndpointRoute(v4RouteTable, ip, func() (string, error) { return v6, nil })
	if got != "2a01:4f8:c0c:1234::1 via fe80::1 dev eth3 metric 1024 pref medium" {
		t.Fatalf("IPv6 endpoint route = %q", got)
	}
}

func TestFindEndpointRoute_IPv4EndpointDoesNotReadIPv6Table(t *testing.T) {
	got := findEndpointRoute(v4RouteTable, "203.0.113.7", func() (string, error) {
		t.Fatal("IPv6 table read for an IPv4 endpoint")
		return "", nil
	})
	if got != "203.0.113.7 via 192.168.1.1 dev eth3" {
		t.Fatalf("IPv4 endpoint route = %q", got)
	}
}

type listOnlyTunnelService struct{ list []service.TunnelWithStatus }

func (f listOnlyTunnelService) List(context.Context) ([]service.TunnelWithStatus, error) {
	return f.list, nil
}
func (listOnlyTunnelService) Start(context.Context, string) error { return nil }
func (listOnlyTunnelService) Stop(context.Context, string) error  { return nil }
func (listOnlyTunnelService) WANModel() *wan.Model                { return nil }
func (listOnlyTunnelService) GetResolvedISP(string) string        { return "" }

// F588: зеркальная запись wdtt-raw в разделе туннелей не появляется — иначе
// её проверки читали бы opkgtun0, интерфейс чужого туннеля.
func TestCollectTunnels_SkipsWdttRawMirror(t *testing.T) {
	r := &Runner{deps: Deps{
		TunnelService: listOnlyTunnelService{list: []service.TunnelWithStatus{
			{ID: "awg0", Backend: "kernel"},
			{ID: "wdttraw-de", Backend: "wdtt-raw"},
		}},
		TunnelStore: storage.NewAWGTunnelStore(t.TempDir()),
	}}
	infos := r.collectTunnels(context.Background())
	if len(infos) != 1 || infos[0].ID != "awg0" {
		ids := make([]string, 0, len(infos))
		for _, ti := range infos {
			ids = append(ids, ti.ID)
		}
		t.Fatalf("tunnels in report = %v, want only awg0", ids)
	}
}
