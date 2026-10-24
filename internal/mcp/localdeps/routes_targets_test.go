package localdeps

import (
	"context"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/dnsroute"
	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	"github.com/hoaxisr/awg-manager/internal/routing"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

type fakeCatalog []routing.TunnelEntry

func (f fakeCatalog) ListAll(context.Context) []routing.TunnelEntry { return f }

func catalogHarness() fakeCatalog {
	return fakeCatalog{
		{ID: "tn-1", Name: "AMS", Type: "managed", Status: "running", Available: true},
		{ID: "system:Proxy0", Name: "Proxy0", Type: "system", Status: "up", Available: true},
		{ID: "system:Wireguard1", Name: "Home server", Type: "system", Status: "up", Available: true, Server: true},
		{ID: "wan:ppp0", Name: "ISP", Type: "wan", Status: "down", Available: false, Warning: "нет адреса\nв NDMS"},
	}
}

// Create and Update mirror staticroute.ServiceImpl closely enough for the
// adapter: Update takes the fallback and the on/off state as sent.
func (f *fakeStaticRoutes) Create(_ context.Context, rl storage.StaticRouteList) (*storage.StaticRouteList, error) {
	rl.ID = "srl-new"
	f.lists = append(f.lists, rl)
	return &rl, nil
}

func (f *fakeStaticRoutes) Update(_ context.Context, rl storage.StaticRouteList) (*storage.StaticRouteList, error) {
	for i := range f.lists {
		if f.lists[i].ID == rl.ID {
			f.lists[i] = rl
			return &rl, nil
		}
	}
	return nil, errNotFound(rl.ID)
}

// TestLocal_ListRoutingTargets — те же цели, что в выборе веб-интерфейса,
// кроме WireGuard-серверов: это вход для клиентов, а не выход.
func TestLocal_ListRoutingTargets(t *testing.T) {
	l := New(Config{Targets: catalogHarness()})
	got, err := l.ListRoutingTargets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[1].ID != "system:Proxy0" || got[2].ID != "wan:ppp0" || got[2].Available {
		t.Fatalf("got %+v", got)
	}
	for _, g := range got {
		if g.ID == "system:Wireguard1" {
			t.Fatal("a WireGuard server is not a routing target")
		}
		if strings.Contains(g.Warning, "\n") {
			t.Fatalf("a warning must be one line: %q", g.Warning)
		}
	}
	if _, err := New(Config{}).ListRoutingTargets(context.Background()); err == nil {
		t.Fatal("without the catalog the tool must say it is unavailable")
	}
}

// TestLocal_AddDNSRouteWithFailover — цели идут в порядке приоритета, Kill
// Switch ложится на последнюю (NDMS применяет его, когда легли все),
// исключения и подписки доходят до сервиса.
func TestLocal_AddDNSRouteWithFailover(t *testing.T) {
	fd := &fakeDNSRoutes{}
	l := New(Config{DNSRoutes: fd, Targets: catalogHarness()})

	_, err := l.AddDNSRoute(context.Background(), mcpsrv.DNSRouteInput{
		Name: "video", Domains: []string{"youtube.com"}, Targets: []string{"tn-1", "system:Proxy0", "wan:ppp0"},
		KillSwitch: true, Excludes: []string{"music.youtube.com"}, SubscriptionURLs: []string{"https://lists.example.net/u/TOKEN/video.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := fd.created
	if len(c.Routes) != 3 || c.Routes[0].TunnelID != "tn-1" || c.Routes[2].TunnelID != "wan:ppp0" {
		t.Fatalf("routes = %+v", c.Routes)
	}
	if c.Routes[2].Fallback != "reject" || c.Routes[0].Fallback != "" {
		t.Fatalf("the kill switch rides on the last target only: %+v", c.Routes)
	}
	if len(c.Excludes) != 1 || len(c.Subscriptions) != 1 || c.Subscriptions[0].URL != "https://lists.example.net/u/TOKEN/video.txt" {
		t.Fatalf("excludes and subscriptions must reach the service whole: %+v", c)
	}

	for name, in := range map[string]mcpsrv.DNSRouteInput{
		"unknown target":   {Name: "x", Domains: []string{"a.com"}, Targets: []string{"tn-ghost"}},
		"server as target": {Name: "x", Domains: []string{"a.com"}, Targets: []string{"system:Wireguard1"}},
		"unknown tunnel":   {Name: "x", Domains: []string{"a.com"}, TunnelID: "tn-ghost"},
	} {
		fd.created = dnsroute.DomainList{}
		if _, err := l.AddDNSRoute(context.Background(), in); err == nil || !strings.Contains(err.Error(), "list_routing_targets") {
			t.Errorf("%s: err = %v", name, err)
		}
		if fd.created.Name != "" {
			t.Errorf("%s: an unknown target must be refused before the service is asked", name)
		}
	}
}

// TestLocal_UpdateDNSRouteTargetsAndSubscriptions — новые цели сохраняют
// Kill Switch, если его не трогали; подписки удаляются по позиции (агент
// видит только хост URL) и добавляются в конец.
func TestLocal_UpdateDNSRouteTargetsAndSubscriptions(t *testing.T) {
	fd := &fakeDNSRoutes{lists: []dnsroute.DomainList{{
		ID: "dl-1", Name: "video",
		Routes:        []dnsroute.RouteTarget{{TunnelID: "tn-1", Fallback: "reject"}},
		Subscriptions: []dnsroute.Subscription{{URL: "https://a.example/u/T1"}, {URL: "https://b.example/u/T2"}},
		Excludes:      []string{"x.com"},
	}}}
	l := New(Config{DNSRoutes: fd, Targets: catalogHarness()})
	ctx := context.Background()

	if _, _, err := l.UpdateDNSRoute(ctx, mcpsrv.DNSRouteUpdate{RouteID: "dl-1", Targets: []string{"system:Proxy0", "tn-1"}}); err != nil {
		t.Fatal(err)
	}
	if r := fd.updated.Routes; len(r) != 2 || r[0].TunnelID != "system:Proxy0" || r[1].Fallback != "reject" {
		t.Fatalf("new targets must keep the kill switch: %+v", r)
	}
	off := false
	if _, _, err := l.UpdateDNSRoute(ctx, mcpsrv.DNSRouteUpdate{RouteID: "dl-1", KillSwitch: &off}); err != nil {
		t.Fatal(err)
	}
	if r := fd.updated.Routes; len(r) != 2 || r[0].TunnelID != "system:Proxy0" || r[1].TunnelID != "tn-1" || r[1].Fallback != "" {
		t.Fatalf("the kill switch alone keeps the stored targets: %+v", r)
	}
	if _, _, err := l.UpdateDNSRoute(ctx, mcpsrv.DNSRouteUpdate{RouteID: "dl-1", RemoveSubscriptions: []int{0}, AddSubscriptionURLs: []string{"https://c.example/l"}}); err != nil {
		t.Fatal(err)
	}
	if s := fd.updated.Subscriptions; len(s) != 2 || s[0].URL != "https://b.example/u/T2" || s[1].URL != "https://c.example/l" {
		t.Fatalf("subscriptions = %+v", s)
	}
	if _, _, err := l.UpdateDNSRoute(ctx, mcpsrv.DNSRouteUpdate{RouteID: "dl-1", AddSubscriptionURLs: []string{"https://b.example/u/T2"}}); err != nil {
		t.Fatal(err)
	}
	if s := fd.updated.Subscriptions; len(s) != 2 {
		t.Fatalf("a URL the list has must not be added twice: %+v", s)
	}
	if fd.updated.Routes != nil || fd.updated.Excludes != nil {
		t.Fatalf("fields not named must stay out of the sparse patch: %+v", fd.updated)
	}
	if _, _, err := l.UpdateDNSRoute(ctx, mcpsrv.DNSRouteUpdate{RouteID: "dl-1", Excludes: []string{}}); err != nil {
		t.Fatal(err)
	}
	if fd.updated.Excludes == nil || len(fd.updated.Excludes) != 0 {
		t.Fatal("[] must reach the service as an empty, non-nil slice: that is what clears excludes")
	}
	if _, _, err := l.UpdateDNSRoute(ctx, mcpsrv.DNSRouteUpdate{RouteID: "dl-1", RemoveSubscriptions: []int{5}}); err == nil {
		t.Fatal("a position past the end must be refused")
	}
	if _, _, err := l.UpdateDNSRoute(ctx, mcpsrv.DNSRouteUpdate{RouteID: "dl-1", Targets: []string{"tn-ghost"}}); err == nil {
		t.Fatal("an unknown target must be refused")
	}
}

// TestLocal_UpdateDNSRouteRefusesWhatHydraRouteLacks — HydraRoute берёт одну
// цель и не знает исключений, подписок и Kill Switch: запись молча их
// потеряла бы.
func TestLocal_UpdateDNSRouteRefusesWhatHydraRouteLacks(t *testing.T) {
	fd := &fakeDNSRoutes{getKnowsHR: true, lists: []dnsroute.DomainList{{
		ID: "hr:1:awgm0:0", Name: "hr", Backend: "hydraroute", HRRouteMode: "interface",
		ManualDomains: []string{"a.com"}, Routes: []dnsroute.RouteTarget{{TunnelID: "tn-1"}},
	}}}
	l := New(Config{DNSRoutes: fd, Targets: catalogHarness()})
	on := true
	for name, in := range map[string]mcpsrv.DNSRouteUpdate{
		"two targets":  {RouteID: "hr:1:awgm0:0", Targets: []string{"tn-1", "system:Proxy0"}},
		"kill switch":  {RouteID: "hr:1:awgm0:0", KillSwitch: &on},
		"excludes":     {RouteID: "hr:1:awgm0:0", Excludes: []string{"b.com"}},
		"subscription": {RouteID: "hr:1:awgm0:0", AddSubscriptionURLs: []string{"https://a.example/l"}},
	} {
		if _, _, err := l.UpdateDNSRoute(context.Background(), in); err == nil || !strings.Contains(err.Error(), "HydraRoute") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, _, err := l.UpdateDNSRoute(context.Background(), mcpsrv.DNSRouteUpdate{RouteID: "hr:1:awgm0:0", Targets: []string{"system:Proxy0"}}); err != nil {
		t.Fatalf("one target is fine: %v", err)
	}
	if r := fd.updated.Routes; len(r) != 1 || r[0].TunnelID != "system:Proxy0" {
		t.Fatalf("routes = %+v", r)
	}

	// A policy-mode list routes through a policy, not a target: one target
	// given as targets is refused just as tunnelId is.
	fd.lists[0].HRRouteMode, fd.lists[0].HRPolicyName = "policy", "VPN"
	if _, _, err := l.UpdateDNSRoute(context.Background(), mcpsrv.DNSRouteUpdate{RouteID: "hr:1:awgm0:0", Targets: []string{"system:Proxy0"}}); err == nil || !strings.Contains(err.Error(), "policy") {
		t.Fatalf("err = %v", err)
	}
}

// TestLocal_StaticRouteKillSwitchSurvivesAnEdit — staticroute.Update берёт
// fallback и включённость как прислали: разреженная правка выключила бы
// Kill Switch и сам список.
func TestLocal_StaticRouteKillSwitchSurvivesAnEdit(t *testing.T) {
	fs := &fakeStaticRoutes{}
	l := New(Config{StaticRoutes: fs, Targets: catalogHarness()})
	ctx := context.Background()

	created, err := l.AddStaticRoute(ctx, mcpsrv.StaticRouteInput{Name: "corp", TunnelID: "system:Proxy0", Subnets: []string{"10.0.0.0/8"}, KillSwitch: true})
	if err != nil || created.Fallback != "reject" || !created.Enabled {
		t.Fatalf("created = %+v, %v", created, err)
	}
	fs.lists[0].IconURL = "/icons/corp.svg"
	got, err := l.UpdateStaticRoute(ctx, mcpsrv.StaticRouteUpdate{RouteID: "srl-new", Subnets: []string{"10.1.0.0/16"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Fallback != "reject" || !got.Enabled || got.TunnelID != "system:Proxy0" || got.Name != "corp" || fs.lists[0].IconURL != "/icons/corp.svg" {
		t.Fatalf("an edit of the subnets must keep the rest: %+v / %+v", got, fs.lists[0])
	}
	off := false
	got, err = l.UpdateStaticRoute(ctx, mcpsrv.StaticRouteUpdate{RouteID: "srl-new", KillSwitch: &off, TunnelID: "wan:ppp0"})
	if err != nil || got.Fallback != "" || got.TunnelID != "wan:ppp0" || len(got.Subnets) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := l.UpdateStaticRoute(ctx, mcpsrv.StaticRouteUpdate{RouteID: "srl-new", TunnelID: "tn-ghost"}); err == nil {
		t.Fatal("an unknown target must be refused")
	}
	if _, err := l.UpdateStaticRoute(ctx, mcpsrv.StaticRouteUpdate{RouteID: "ghost", Name: "x"}); err == nil {
		t.Fatal("an unknown list must be refused")
	}
}
