package localdeps

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/clientroute"
	"github.com/hoaxisr/awg-manager/internal/connections"
	"github.com/hoaxisr/awg-manager/internal/diagnostics"
	"github.com/hoaxisr/awg-manager/internal/dnsroute"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/managed"
	"github.com/hoaxisr/awg-manager/internal/managed/peerip"
	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/pingcheck"
	"github.com/hoaxisr/awg-manager/internal/singbox"
	singboxorch "github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/storage"
	awgtesting "github.com/hoaxisr/awg-manager/internal/testing"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
)

// Partial fakes: embedding the interface lets us implement only the
// methods under test; anything else panics loudly if called.
type fakeTunnels struct {
	api.TunnelService
	list     []service.TunnelWithStatus
	enabled  map[string]bool
	defRoute map[string]bool
}

func (f *fakeTunnels) List(context.Context) ([]service.TunnelWithStatus, error) { return f.list, nil }
func (f *fakeTunnels) Get(_ context.Context, id string) (*service.TunnelWithStatus, error) {
	for i := range f.list {
		if f.list[i].ID == id {
			return &f.list[i], nil
		}
	}
	return nil, errNotFound(id)
}
func (f *fakeTunnels) SetEnabled(_ context.Context, id string, v bool) error {
	f.enabled[id] = v
	return nil
}
func (f *fakeTunnels) SetDefaultRoute(_ context.Context, id string, v bool) error {
	f.defRoute[id] = v
	return nil
}

type fakeOrch struct {
	events []orchestrator.Event
	err    error // returned from every HandleEvent when set
}

func (f *fakeOrch) HandleEvent(_ context.Context, e orchestrator.Event) error {
	f.events = append(f.events, e)
	return f.err
}

type fakeClientRoutes struct {
	clientroute.Service
	routes []clientroute.ClientRoute
}

func (f *fakeClientRoutes) List() ([]clientroute.ClientRoute, error) { return f.routes, nil }
func (f *fakeClientRoutes) Create(_ context.Context, r clientroute.ClientRoute) (*clientroute.ClientRoute, error) {
	r.ID = "cr-new"
	f.routes = append(f.routes, r)
	return &r, nil
}
func (f *fakeClientRoutes) Update(_ context.Context, r clientroute.ClientRoute) (*clientroute.ClientRoute, error) {
	for i := range f.routes {
		if f.routes[i].ID == r.ID {
			f.routes[i] = r
			return &r, nil
		}
	}
	return nil, errNotFound(r.ID)
}

// SetEnabled mirrors clientroute.ServiceImpl: the stored route is updated.
func (f *fakeClientRoutes) SetEnabled(_ context.Context, id string, v bool) error {
	for i := range f.routes {
		if f.routes[i].ID == id {
			f.routes[i].Enabled = v
			return nil
		}
	}
	return errNotFound(id)
}

func (f *fakeClientRoutes) Delete(_ context.Context, id string) error {
	for i := range f.routes {
		if f.routes[i].ID == id {
			f.routes = append(f.routes[:i], f.routes[i+1:]...)
			return nil
		}
	}
	return errNotFound(id)
}

// recBus records the invalidation hints a mutation publishes. The daemon
// publishes these in its HTTP handlers, so localdeps has to mirror them or
// an open web UI keeps showing pre-MCP data.
type recBus struct{ pub []events.Resource }

func (b *recBus) PublishInvalidated(res events.Resource, _ string) { b.pub = append(b.pub, res) }

func (b *recBus) has(res events.Resource) bool {
	for _, r := range b.pub {
		if r == res {
			return true
		}
	}
	return false
}

type fakeDNSRoutes struct {
	api.DNSRouteService
	lists   []dnsroute.DomainList
	created dnsroute.DomainList
	updated dnsroute.DomainList
	enabled map[string]bool
	// getKnowsHR mirrors the real service: dnsroute.Get scans only the
	// JSON store and never finds an "hr:" list, while List merges them in.
	getKnowsHR bool
	deleted    []string
}

func (f *fakeDNSRoutes) Create(_ context.Context, l dnsroute.DomainList) (*dnsroute.DomainList, error) {
	f.created = l
	l.ID = "dl-new"
	// Mirrors dnsroute.ServiceImpl.Create: Enabled is hard-set and Domains
	// is recomputed from ManualDomains, whatever the payload said.
	l.Enabled = true
	l.Domains = append([]string(nil), l.ManualDomains...)
	f.lists = append(f.lists, l)
	return &l, nil
}

// Update records the sparse payload verbatim: what the adapter leaves
// unset is exactly what dnsroute.ServiceImpl.Update preserves.
func (f *fakeDNSRoutes) Update(_ context.Context, l dnsroute.DomainList) (*dnsroute.DomainList, error) {
	f.updated = l
	for i := range f.lists {
		if f.lists[i].ID != l.ID {
			continue
		}
		merged := f.lists[i]
		if l.Name != "" {
			merged.Name = l.Name
		}
		if l.ManualDomains != nil {
			merged.ManualDomains = l.ManualDomains
			merged.Domains = l.ManualDomains
		}
		if l.Routes != nil {
			merged.Routes = l.Routes
		}
		f.lists[i] = merged
		return &merged, nil
	}
	return nil, errNotFound(l.ID)
}

func (f *fakeDNSRoutes) List(context.Context) ([]dnsroute.DomainList, error) {
	return append([]dnsroute.DomainList(nil), f.lists...), nil
}

func (f *fakeDNSRoutes) Get(_ context.Context, id string) (*dnsroute.DomainList, error) {
	for i := range f.lists {
		if f.lists[i].ID == id {
			if strings.HasPrefix(id, "hr:") && !f.getKnowsHR {
				return nil, errNotFound(id)
			}
			return &f.lists[i], nil
		}
	}
	return nil, errNotFound(id)
}

func (f *fakeDNSRoutes) SetEnabled(_ context.Context, id string, v bool) error {
	f.enabled[id] = v
	// Mirrors dnsroute.ServiceImpl.SetEnabled: the stored list is updated,
	// so a read-back sees the new flag.
	for i := range f.lists {
		if f.lists[i].ID == id {
			f.lists[i].Enabled = v
		}
	}
	return nil
}

func (f *fakeDNSRoutes) Delete(_ context.Context, id string) error {
	for i := range f.lists {
		if f.lists[i].ID == id {
			f.lists = append(f.lists[:i], f.lists[i+1:]...)
			f.deleted = append(f.deleted, id)
			return nil
		}
	}
	return errNotFound(id)
}

type fakeStaticRoutes struct {
	api.StaticRouteService
	lists   []storage.StaticRouteList
	enabled map[string]bool
}

// SetEnabled mirrors staticroute.ServiceImpl: the stored list is updated,
// so a read-back sees the new flag.
func (f *fakeStaticRoutes) SetEnabled(_ context.Context, id string, v bool) error {
	for i := range f.lists {
		if f.lists[i].ID == id {
			f.lists[i].Enabled = v
			if f.enabled == nil {
				f.enabled = map[string]bool{}
			}
			f.enabled[id] = v
			return nil
		}
	}
	return errNotFound(id)
}

func (f *fakeStaticRoutes) Get(id string) (*storage.StaticRouteList, error) {
	for i := range f.lists {
		if f.lists[i].ID == id {
			return &f.lists[i], nil
		}
	}
	return nil, errNotFound(id)
}

func (f *fakeStaticRoutes) Delete(_ context.Context, id string) error {
	for i := range f.lists {
		if f.lists[i].ID == id {
			f.lists = append(f.lists[:i], f.lists[i+1:]...)
			return nil
		}
	}
	return errNotFound(id)
}

func newLocal(t *testing.T) (*Local, *fakeTunnels, *fakeOrch, *fakeClientRoutes) {
	t.Helper()
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, dir+"/locks")
	if err := store.Create(&storage.AWGTunnel{ID: "tn-1", Name: "AMS", Backend: "nativewg", Peer: storage.AWGPeer{Endpoint: "vpn.example.net:51820", AllowedIPs: []string{"0.0.0.0/0"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(&storage.AWGTunnel{ID: "tn-raw", Name: "RAW", Backend: "wdtt-raw"}); err != nil {
		t.Fatal(err)
	}
	ft := &fakeTunnels{
		list: []service.TunnelWithStatus{
			{ID: "tn-1", Name: "AMS", State: tunnel.StateRunning, StateInfo: tunnel.StateInfo{State: tunnel.StateRunning, HasHandshake: true, ProcessPID: 42}, Enabled: true, DefaultRoute: true, InterfaceName: "nwg0", Backend: "nativewg"},
			{ID: "tn-raw", Name: "RAW", State: tunnel.StateStopped, Backend: "wdtt-raw"},
		},
		enabled: map[string]bool{}, defRoute: map[string]bool{},
	}
	fo := &fakeOrch{}
	fc := &fakeClientRoutes{}
	l := New(Config{Tunnels: ft, TunnelStore: store, Orch: fo, ClientRoutes: fc})
	return l, ft, fo, fc
}

// harness is newLocal plus the routing fakes and a recording bus, for the
// tests that assert on published invalidation hints.
type harness struct {
	l      *Local
	tun    *fakeTunnels
	orch   *fakeOrch
	client *fakeClientRoutes
	dns    *fakeDNSRoutes
	static *fakeStaticRoutes
	bus    *recBus
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	l, ft, fo, fc := newLocal(t)
	h := &harness{
		tun: ft, orch: fo, client: fc,
		dns:    &fakeDNSRoutes{enabled: map[string]bool{}},
		static: &fakeStaticRoutes{lists: []storage.StaticRouteList{{ID: "sr-1", Name: "Office", TunnelID: "tn-1", Subnets: []string{"10.20.0.0/16"}, Enabled: true}}},
		bus:    &recBus{},
	}
	cfg := l.c
	cfg.DNSRoutes, cfg.StaticRoutes, cfg.Bus = h.dns, h.static, h.bus
	h.l = New(cfg)
	return h
}

// TestLocal_MutationsPublishInvalidation — записи через MCP идут мимо HTTP-
// обработчиков, которые и публикуют подсказки инвалидации. Без зеркалирования
// открытая вкладка веб-интерфейса показывает состояние до правки.
func TestLocal_MutationsPublishInvalidation(t *testing.T) {
	ctx := context.Background()

	t.Run("dns route create and delete", func(t *testing.T) {
		h := newHarness(t)
		if _, err := h.l.AddDNSRoute(ctx, mcpsrv.DNSRouteInput{Name: "GH", Domains: []string{"github.com"}, TunnelID: "tn-1"}); err != nil {
			t.Fatal(err)
		}
		if !h.bus.has(events.ResourceRoutingDnsRoutes) {
			t.Fatalf("create published %v", h.bus.pub)
		}
		h.bus.pub = nil
		gone, err := h.l.RemoveDNSRoute(ctx, "dl-new")
		if err != nil {
			t.Fatal(err)
		}
		if gone.ID != "dl-new" || gone.Name != "GH" {
			t.Fatalf("RemoveDNSRoute must return the deleted record, got %+v", gone)
		}
		if !h.bus.has(events.ResourceRoutingDnsRoutes) {
			t.Fatalf("delete published %v", h.bus.pub)
		}
	})

	t.Run("static route delete", func(t *testing.T) {
		h := newHarness(t)
		gone, err := h.l.RemoveStaticRoute(ctx, "sr-1")
		if err != nil {
			t.Fatal(err)
		}
		if gone.ID != "sr-1" || gone.Name != "Office" || len(gone.Subnets) != 1 {
			t.Fatalf("RemoveStaticRoute must return the deleted record, got %+v", gone)
		}
		if !h.bus.has(events.ResourceRoutingStaticRoutes) {
			t.Fatalf("published %v", h.bus.pub)
		}
	})

	t.Run("client route upsert and delete", func(t *testing.T) {
		h := newHarness(t)
		if _, err := h.l.SetClientRoute(ctx, mcpsrv.ClientRouteInput{ClientIP: "192.168.1.9", TunnelID: "tn-1"}); err != nil {
			t.Fatal(err)
		}
		if !h.bus.has(events.ResourceRoutingClientRoutes) {
			t.Fatalf("create published %v", h.bus.pub)
		}
		h.bus.pub = nil
		if _, err := h.l.SetClientRoute(ctx, mcpsrv.ClientRouteInput{ClientIP: "192.168.1.9"}); err != nil {
			t.Fatal(err)
		}
		if !h.bus.has(events.ResourceRoutingClientRoutes) {
			t.Fatalf("delete published %v", h.bus.pub)
		}
	})

	t.Run("enable and default route", func(t *testing.T) {
		for _, action := range []string{mcpsrv.ActionDisable, mcpsrv.ActionSetDefaultRoute} {
			h := newHarness(t)
			if err := h.l.ControlTunnel(ctx, "tn-1", action); err != nil {
				t.Fatal(err)
			}
			if !h.bus.has(events.ResourceTunnels) || !h.bus.has(events.ResourceRoutingTunnels) {
				t.Fatalf("%s published %v", action, h.bus.pub)
			}
		}
	})

	// Старт/стоп/рестарт: оркестратор публикует только tunnels, а REST
	// (api.ControlHandler.Start/Stop/Restart) сверх того дёргает
	// publishTunnelList + publishRoutingTunnels — иначе выпадающий список
	// туннелей на странице маршрутизации остаётся устаревшим.
	t.Run("start stop restart publish like REST", func(t *testing.T) {
		for _, a := range []string{mcpsrv.ActionStart, mcpsrv.ActionStop, mcpsrv.ActionRestart} {
			h := newHarness(t)
			if err := h.l.ControlTunnel(ctx, "tn-1", a); err != nil {
				t.Fatal(err)
			}
			want := []events.Resource{events.ResourceTunnels, events.ResourceRoutingTunnels}
			if !reflect.DeepEqual(h.bus.pub, want) {
				t.Fatalf("%s published %v, want %v", a, h.bus.pub, want)
			}
		}
	})

	t.Run("failed action publishes nothing", func(t *testing.T) {
		h := newHarness(t)
		h.orch.err = errors.New("boom")
		if err := h.l.ControlTunnel(ctx, "tn-1", mcpsrv.ActionRestart); err == nil {
			t.Fatal("expected the orchestrator error")
		}
		if len(h.bus.pub) != 0 {
			t.Fatalf("published on failure: %v", h.bus.pub)
		}
	})

	// Bus опционален: половина сборок демона поднимается без него.
	t.Run("nil bus is not a crash", func(t *testing.T) {
		h := newHarness(t)
		cfg := h.l.c
		cfg.Bus = nil
		if err := New(cfg).ControlTunnel(ctx, "tn-1", mcpsrv.ActionDisable); err != nil {
			t.Fatal(err)
		}
	})
}

// TestLocal_ListDNSRoutesCapsExpandedDomains — список по подписке
// разворачивается в десятки тысяч доменов; в вывод идут первые
// MaxDomainsInOutput плюс честный domainCount, а ManualDomains (то, что
// нужно add_dns_route для пересоздания) — целиком.
func TestLocal_ListDNSRoutesCapsExpandedDomains(t *testing.T) {
	h := newHarness(t)
	big := make([]string, 0, 3*mcpsrv.MaxDomainsInOutput)
	for i := range cap(big) {
		big = append(big, fmt.Sprintf("d%d.example", i))
	}
	h.dns.lists = []dnsroute.DomainList{{
		ID: "dl-big", Name: "Geo", Enabled: true, Domains: big, ManualDomains: []string{"my.example"},
		Subnets: []string{"10.0.0.0/8"}, Backend: "ndms",
		Routes:        []dnsroute.RouteTarget{{Interface: "nwg0", TunnelID: "tn-1", Fallback: "bypass"}},
		Subscriptions: []dnsroute.Subscription{{URL: "https://example.invalid/list"}},
	}}
	got, err := h.l.ListDNSRoutes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("routes = %d", len(got))
	}
	r := got[0]
	if len(r.Domains) != mcpsrv.MaxDomainsInOutput || r.DomainCount != len(big) {
		t.Fatalf("domains=%d count=%d, want %d/%d", len(r.Domains), r.DomainCount, mcpsrv.MaxDomainsInOutput, len(big))
	}
	if r.Domains[0] != "d0.example" {
		t.Fatalf("cap must keep the head of the list, got %q first", r.Domains[0])
	}
	if len(r.ManualDomains) != 1 || r.ManualDomains[0] != "my.example" {
		t.Fatalf("manual domains must not be capped: %v", r.ManualDomains)
	}
	want := mcpsrv.RouteTarget{Interface: "nwg0", TunnelID: "tn-1", Fallback: "bypass"}
	if r.ID != "dl-big" || r.Name != "Geo" || !r.Enabled || r.Backend != "ndms" || len(r.Subnets) != 1 || len(r.Routes) != 1 || r.Routes[0] != want {
		t.Fatalf("field mapping: %+v", r)
	}
	// Truncating the output must not touch the service's slice.
	if len(h.dns.lists[0].Domains) != len(big) {
		t.Fatal("the source list was truncated")
	}
}

// TestLocal_AddDNSRouteInputMapping — dnsroute.Create пересчитывает Domains
// из ManualDomains, поэтому Domains не передаётся вовсе (иначе один и тот же
// слайс лежал бы в двух полях одной структуры). Плюс: MCP НЕ отключает
// созданный список вторым вызовом — Create уже поднял маршрутизацию в NDMS,
// и SetEnabled(false) сразу после этого либо лишний, либо (при ошибке)
// оставляет список включённым вопреки запросу.
func TestLocal_AddDNSRouteInputMapping(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	got, err := h.l.AddDNSRoute(ctx, mcpsrv.DNSRouteInput{Name: "GH", Domains: []string{"github.com"}, TunnelID: "tn-1"})
	if err != nil {
		t.Fatal(err)
	}
	if h.dns.created.Domains != nil {
		t.Errorf("Domains must not be sent (it is derived from ManualDomains), got %v", h.dns.created.Domains)
	}
	if len(h.dns.created.ManualDomains) != 1 || h.dns.created.ManualDomains[0] != "github.com" {
		t.Errorf("ManualDomains = %v", h.dns.created.ManualDomains)
	}
	if !got.Enabled {
		t.Error("a list created through MCP is always enabled")
	}
	if len(h.dns.enabled) != 0 {
		t.Errorf("no SetEnabled follow-up may be issued, got %v", h.dns.enabled)
	}
}

// TestLocal_SetClientRoutePreservesEnabled — перенаправление устройства на
// другой туннель не должно молча включать маршрут, который пользователь
// сам выключил.
func TestLocal_SetClientRoutePreservesEnabled(t *testing.T) {
	h := newHarness(t)
	h.client.routes = []clientroute.ClientRoute{{ID: "cr-1", ClientIP: "192.168.1.9", TunnelID: "tn-1", Fallback: "bypass", Enabled: false}}
	got, err := h.l.SetClientRoute(context.Background(), mcpsrv.ClientRouteInput{ClientIP: "192.168.1.9", TunnelID: "tn-1", Fallback: "drop"})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Enabled {
		t.Fatalf("update must keep Enabled=false, got %+v", got)
	}
	if got.Fallback != "drop" {
		t.Fatalf("fallback = %q", got.Fallback)
	}
}

func TestLocal_ListTunnelsMapsStateAndEndpoint(t *testing.T) {
	l, _, _, _ := newLocal(t)
	got, err := l.ListTunnels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "tn-1" || got[0].State != "running" || got[0].Endpoint != "vpn.example.net:51820" || !got[0].HasHandshake {
		t.Fatalf("got = %+v", got)
	}
}

// У обфусцированного туннеля Peer.Endpoint — loopback релея; наружу (как и в
// списке UI) показываем сервер (Q7).
func TestLocal_EndpointOfObfuscatedIsTarget(t *testing.T) {
	l, _, _, _ := newLocal(t)
	if err := l.c.TunnelStore.Create(&storage.AWGTunnel{
		ID: "tn-obf", Name: "PH", Backend: "nativewg",
		Peer:       storage.AWGPeer{Endpoint: "127.0.0.1:39000"},
		Obfuscator: &storage.Obfuscator{Flavor: storage.ObfuscatorFlavorPhobos, Target: "1.2.3.4:51824", LocalPort: 39000},
	}); err != nil {
		t.Fatal(err)
	}
	if got := l.endpointOf("tn-obf"); got != "1.2.3.4:51824" {
		t.Fatalf("endpointOf = %q, want target", got)
	}
	if got := l.endpointOf("tn-1"); got != "vpn.example.net:51820" {
		t.Fatalf("обычный туннель: endpointOf = %q", got)
	}
}

func TestLocal_ControlTunnelDispatch(t *testing.T) {
	l, ft, fo, _ := newLocal(t)
	ctx := context.Background()
	for _, a := range []string{mcpsrv.ActionStart, mcpsrv.ActionStop, mcpsrv.ActionRestart} {
		if err := l.ControlTunnel(ctx, "tn-1", a); err != nil {
			t.Fatal(err)
		}
	}
	if len(fo.events) != 3 || fo.events[0].Type != orchestrator.EventStart || fo.events[1].Type != orchestrator.EventStop || fo.events[2].Type != orchestrator.EventRestart || fo.events[0].Tunnel != "tn-1" {
		t.Fatalf("events = %+v", fo.events)
	}
	if err := l.ControlTunnel(ctx, "tn-1", mcpsrv.ActionDisable); err != nil || ft.enabled["tn-1"] != false {
		t.Fatalf("disable: %v %v", err, ft.enabled)
	}
	if err := l.ControlTunnel(ctx, "tn-1", mcpsrv.ActionSetDefaultRoute); err != nil || !ft.defRoute["tn-1"] {
		t.Fatalf("set_default_route: %v %v", err, ft.defRoute)
	}
	if err := l.ControlTunnel(ctx, "tn-raw", mcpsrv.ActionStart); err == nil {
		t.Fatal("wdtt-raw tunnel must be rejected")
	}
	if err := l.ControlTunnel(ctx, "tn-1", "bogus"); err == nil {
		t.Fatal("bogus action accepted")
	}
}

func TestLocal_SetClientRouteUpsertAndRemove(t *testing.T) {
	l, _, _, fc := newLocal(t)
	ctx := context.Background()
	r, err := l.SetClientRoute(ctx, mcpsrv.ClientRouteInput{ClientIP: "192.168.1.9", TunnelID: "tn-1"})
	if err != nil || r == nil || r.ID != "cr-new" || r.Fallback != "bypass" || !r.Enabled {
		t.Fatalf("create: %+v %v", r, err)
	}
	r, err = l.SetClientRoute(ctx, mcpsrv.ClientRouteInput{ClientIP: "192.168.1.9", TunnelID: "tn-1", Fallback: "drop"})
	if err != nil || r == nil || r.ID != "cr-new" || r.Fallback != "drop" || len(fc.routes) != 1 {
		t.Fatalf("update: %+v %v routes=%d", r, err, len(fc.routes))
	}
	r, err = l.SetClientRoute(ctx, mcpsrv.ClientRouteInput{ClientIP: "192.168.1.9"})
	if err != nil || r != nil || len(fc.routes) != 0 {
		t.Fatalf("remove: %+v %v routes=%d", r, err, len(fc.routes))
	}
}

type notFound string

func (e notFound) Error() string  { return string(e) + " not found" }
func errNotFound(id string) error { return notFound(id) }

// ---- F2: kill-switch ------------------------------------------------------

// TestLocal_SetClientRoutePreservesDropFallback — обновление без fallback
// не должно сбрасывать выставленный «drop» на дефолт «bypass»: устройство
// тогда потечёт в WAN, как только туннель упадёт. Дефолт применяется только
// при СОЗДАНИИ.
func TestLocal_SetClientRoutePreservesDropFallback(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.client.routes = []clientroute.ClientRoute{{ID: "cr-1", ClientIP: "192.168.1.9", TunnelID: "tn-1", Fallback: "drop", Enabled: true}}

	got, err := h.l.SetClientRoute(ctx, mcpsrv.ClientRouteInput{ClientIP: "192.168.1.9", TunnelID: "tn-raw"})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Fallback != "drop" {
		t.Fatalf("an update without fallback reset the kill-switch: %+v", got)
	}
	if h.client.routes[0].Fallback != "drop" {
		t.Fatalf("stored fallback = %q", h.client.routes[0].Fallback)
	}

	// An explicit change is still honoured…
	if got, err = h.l.SetClientRoute(ctx, mcpsrv.ClientRouteInput{ClientIP: "192.168.1.9", TunnelID: "tn-1", Fallback: "bypass"}); err != nil {
		t.Fatal(err)
	}
	if got.Fallback != "bypass" {
		t.Fatalf("explicit fallback ignored: %+v", got)
	}
	// …and a brand-new route still defaults to bypass.
	if got, err = h.l.SetClientRoute(ctx, mcpsrv.ClientRouteInput{ClientIP: "192.168.1.77", TunnelID: "tn-1"}); err != nil {
		t.Fatal(err)
	}
	if got.Fallback != "bypass" {
		t.Fatalf("a new route must default to bypass: %+v", got)
	}
}

// ---- F4/F10: logs ---------------------------------------------------------

// fakeLogs returns entries NEWEST-first, exactly like
// logging.Service.GetLogsMulti (logbuf.Buffer.FilterPage walks the ring
// from the end).
type fakeLogs struct {
	entries  []logging.LogEntry
	gotLevel string
	gotLimit int
	capacity int // 0 → localdeps falls back to logging.DefaultBufferCapacity
}

func (f *fakeLogs) Stats(bucket logging.Bucket) logging.BufferStats {
	return logging.BufferStats{Bucket: bucket, Capacity: f.capacity}
}

func (f *fakeLogs) GetLogsMulti(_ logging.Bucket, groups, _ []string, level string, _ time.Time, limit, _ int) ([]logging.LogEntry, int) {
	f.gotLevel, f.gotLimit = level, limit
	var matched []logging.LogEntry
	for i := len(f.entries) - 1; i >= 0; i-- { // newest first
		e := f.entries[i]
		if len(groups) > 0 {
			ok := false
			for _, g := range groups {
				if g == e.Group {
					ok = true
				}
			}
			if !ok {
				continue
			}
		}
		matched = append(matched, e)
	}
	total := len(matched)
	if limit > 0 && len(matched) > limit {
		matched = matched[:limit]
	}
	return matched, total
}

func logSeq(levels ...string) *fakeLogs {
	f := &fakeLogs{}
	base := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	for i, lv := range levels {
		f.entries = append(f.entries, logging.LogEntry{
			Timestamp: base.Add(time.Duration(i) * time.Minute),
			Level:     lv,
			Group:     logging.GroupTunnel,
			Message:   fmt.Sprintf("m%d", i),
		})
	}
	return f
}

func messages(entries []mcpsrv.LogEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Message)
	}
	return out
}

// TestLocal_GetLogsMasksAndMapsDirectly — зеркало api.logEntryDTO: IP и
// домены по умолчанию маскируются (текст уходит сторонней модели), raw
// отдаёт как есть; repeats/lastSeen доходят до вывода, а не теряются;
// при фильтре на стороне localdeps читается весь буфер по его ёмкости,
// а не константа.
func TestLocal_GetLogsMasksAndMapsDirectly(t *testing.T) {
	ctx := context.Background()
	last := time.Date(2026, 9, 4, 10, 0, 5, 0, time.UTC)
	fl := &fakeLogs{capacity: 777, entries: []logging.LogEntry{{
		Timestamp: time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC), Level: "warn", Group: "tunnel", Subgroup: "awg",
		Action: "start", Target: "vpn.example.net:51820", Message: "handshake with 203.0.113.7 failed", Repeats: 3, LastSeen: &last,
	}}}
	l := New(Config{Logs: fl})

	got, _, err := l.GetLogs(ctx, mcpsrv.LogsQuery{Bucket: "app", Lines: 10, Level: "warn"})
	if err != nil {
		t.Fatal(err)
	}
	if fl.gotLimit != 777 {
		t.Fatalf("client-side filter fetched %d entries, want the buffer capacity 777", fl.gotLimit)
	}
	if len(got) != 1 {
		t.Fatalf("entries = %d, want 1", len(got))
	}
	e := got[0]
	if strings.Contains(e.Message, "203.0.113.7") || strings.Contains(e.Target, "vpn.example.net") {
		t.Fatalf("default output must mask hosts: target=%q message=%q", e.Target, e.Message)
	}
	if e.Repeats != 3 || e.LastSeen != "2026-09-04T10:00:05Z" {
		t.Fatalf("repeats/lastSeen not carried: %+v", e)
	}
	if e.Timestamp != "2026-09-04T10:00:00Z" || e.Group != "tunnel" || e.Subgroup != "awg" || e.Action != "start" || e.Level != "warn" {
		t.Fatalf("field mapping differs from the REST DTO: %+v", e)
	}

	raw, _, err := l.GetLogs(ctx, mcpsrv.LogsQuery{Bucket: "app", Lines: 10, Raw: true})
	if err != nil {
		t.Fatal(err)
	}
	if raw[0].Message != "handshake with 203.0.113.7 failed" || raw[0].Target != "vpn.example.net:51820" {
		t.Fatalf("raw=true must not mask: %+v", raw[0])
	}
	if fl.gotLimit != 10 {
		t.Fatalf("without a client-side filter the buffer should page by Lines, fetched %d", fl.gotLimit)
	}

	// A user-enlarged ring is not copied whole: the scan stops at maxLogScan.
	fl.capacity = 50000
	if _, _, err := l.GetLogs(ctx, mcpsrv.LogsQuery{Bucket: "app", Lines: 10, Level: "warn"}); err != nil {
		t.Fatal(err)
	}
	if fl.gotLimit != maxLogScan {
		t.Fatalf("scan of a 50000-entry ring fetched %d, want the %d cap", fl.gotLimit, maxLogScan)
	}
}

// TestLocal_GetLogsOldestFirstAndKeepsNewest — GetLogsMulti отдаёт записи
// НОВЕЙШИМИ ВПЕРЁД, а get_logs обещает «newest last». Если не развернуть,
// хвостовой срез при фильтре contains оставит САМЫЕ СТАРЫЕ совпадения —
// именно та недавняя ошибка, ради которой агент и полез в логи, не вернётся.
func TestLocal_GetLogsOldestFirstAndKeepsNewest(t *testing.T) {
	ctx := context.Background()
	fl := logSeq("info", "info", "info", "info", "info")
	l := New(Config{Logs: fl})

	got, total, err := l.GetLogs(ctx, mcpsrv.LogsQuery{Lines: 5})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"m0", "m1", "m2", "m3", "m4"}; !reflect.DeepEqual(messages(got), want) {
		t.Fatalf("order = %v, want oldest-first %v", messages(got), want)
	}
	if total != 5 {
		t.Fatalf("total = %d", total)
	}

	// With a filter the cap must keep the NEWEST matches.
	got, _, err = l.GetLogs(ctx, mcpsrv.LogsQuery{Lines: 2, Contains: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"m3", "m4"}; !reflect.DeepEqual(messages(got), want) {
		t.Fatalf("filtered tail = %v, want the newest %v", messages(got), want)
	}
}

// TestLocal_GetLogsLevelIsStrict — localdeps фильтрует уровень сам:
// logging.IsVisible считает warn/error всегда видимыми и схлопывает
// незнакомый уровень в приоритет 0, так что level:"error" приносил бы и
// info-строки. Семантика обязана совпадать с mcptest.Fake.
func TestLocal_GetLogsLevelIsStrict(t *testing.T) {
	ctx := context.Background()
	fl := logSeq("debug", "info", "warn", "error", "full")
	l := New(Config{Logs: fl})

	got, total, err := l.GetLogs(ctx, mcpsrv.LogsQuery{Lines: 100, Level: "error"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"m3"}; !reflect.DeepEqual(messages(got), want) {
		t.Fatalf("level=error → %v, want only %v", messages(got), want)
	}
	if total != 1 {
		t.Fatalf("total = %d, want 1", total)
	}
	if fl.gotLevel != "" {
		t.Fatalf("the level must NOT be delegated to GetLogsMulti (IsVisible semantics), got %q", fl.gotLevel)
	}

	got, _, err = l.GetLogs(ctx, mcpsrv.LogsQuery{Lines: 100, Level: "warn"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"m2", "m3"}; !reflect.DeepEqual(messages(got), want) {
		t.Fatalf("level=warn → %v, want %v", messages(got), want)
	}

	// An entry whose level is outside debug|info|warn|error is dropped —
	// same rule as the fake.
	got, _, err = l.GetLogs(ctx, mcpsrv.LogsQuery{Lines: 100, Level: "debug"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"m0", "m1", "m2", "m3"}; !reflect.DeepEqual(messages(got), want) {
		t.Fatalf("level=debug → %v, want %v", messages(got), want)
	}
}

// ---- F5/F7: replace and import mirror the HTTP handlers -------------------

// lifecycleTunnels records the stop/replace/start sequence and the address
// conflicts the handler surfaces as warnings.
type lifecycleTunnels struct {
	fakeTunnels
	calls       []string
	state       tunnel.State
	stopErr     error
	startErr    error
	replaceErr  error
	conflicts   []string
	imported    *service.TunnelWithStatus
	importedCfg string
}

func (f *lifecycleTunnels) GetState(_ context.Context, _ string) tunnel.StateInfo {
	return tunnel.StateInfo{State: f.state}
}
func (f *lifecycleTunnels) Stop(context.Context, string) error {
	f.calls = append(f.calls, "stop")
	return f.stopErr
}
func (f *lifecycleTunnels) Start(context.Context, string) error {
	f.calls = append(f.calls, "start")
	return f.startErr
}
func (f *lifecycleTunnels) ReplaceConfig(_ context.Context, _, cfg, _ string, _ service.ReplaceOptions) error {
	f.calls = append(f.calls, "replace")
	f.importedCfg = cfg
	return f.replaceErr
}
func (f *lifecycleTunnels) CheckAddressConflicts(context.Context, string) []string {
	return f.conflicts
}
func (f *lifecycleTunnels) Import(_ context.Context, cfg, name, _ string, _ service.ImportLink) (*service.TunnelWithStatus, error) {
	f.importedCfg = cfg
	if f.imported != nil {
		return f.imported, nil
	}
	return &service.TunnelWithStatus{ID: "tn-1", Name: name, Backend: "nativewg", Enabled: false, State: tunnel.StateStopped}, nil
}

func newLifecycle(t *testing.T, state tunnel.State) (*Local, *lifecycleTunnels, *storage.AWGTunnelStore) {
	t.Helper()
	l, _, _, _ := newLocal(t)
	ft := &lifecycleTunnels{state: state, fakeTunnels: fakeTunnels{enabled: map[string]bool{}, defRoute: map[string]bool{}}}
	cfg := l.c
	cfg.Tunnels = ft
	return New(cfg), ft, l.c.TunnelStore
}

// TestLocal_ReplaceTunnelConfigStopsAndStarts зеркалит
// api.TunnelsHandler.ReplaceConfig (internal/api/tunnels_crud.go): один
// `wg setconf` не подхватывает изменившийся Address/DNS/MTU на работающем
// туннеле, поэтому работающий останавливают и поднимают заново.
func TestLocal_ReplaceTunnelConfigStopsAndStarts(t *testing.T) {
	ctx := context.Background()

	t.Run("running tunnel is restarted", func(t *testing.T) {
		l, ft, _ := newLifecycle(t, tunnel.StateRunning)
		ft.conflicts = []string{"address 10.8.0.2/32 conflicts with Wireguard0"}
		warnings, err := l.ReplaceTunnelConfig(ctx, "tn-1", "cfg", "New")
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"stop", "replace", "start"}; !reflect.DeepEqual(ft.calls, want) {
			t.Fatalf("calls = %v, want %v", ft.calls, want)
		}
		if !reflect.DeepEqual(warnings, ft.conflicts) {
			t.Fatalf("warnings = %v, want the address conflicts %v", warnings, ft.conflicts)
		}
	})

	t.Run("stopped tunnel is not started", func(t *testing.T) {
		l, ft, _ := newLifecycle(t, tunnel.StateStopped)
		warnings, err := l.ReplaceTunnelConfig(ctx, "tn-1", "cfg", "")
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"replace"}; !reflect.DeepEqual(ft.calls, want) {
			t.Fatalf("calls = %v, want %v", ft.calls, want)
		}
		if len(warnings) != 0 {
			t.Fatalf("warnings = %v", warnings)
		}
	})

	t.Run("a failed stop leaves the config alone", func(t *testing.T) {
		l, ft, _ := newLifecycle(t, tunnel.StateRunning)
		ft.stopErr = fmt.Errorf("operation in progress")
		if _, err := l.ReplaceTunnelConfig(ctx, "tn-1", "cfg", ""); err == nil {
			t.Fatal("a failed stop must abort the replace")
		}
		if want := []string{"stop"}; !reflect.DeepEqual(ft.calls, want) {
			t.Fatalf("calls = %v, want %v", ft.calls, want)
		}
	})

	t.Run("a failed restart is a warning, not an error", func(t *testing.T) {
		l, ft, _ := newLifecycle(t, tunnel.StateRunning)
		ft.startErr = fmt.Errorf("boom")
		warnings, err := l.ReplaceTunnelConfig(ctx, "tn-1", "cfg", "")
		if err != nil {
			t.Fatalf("the replace itself succeeded: %v", err)
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], "failed to restart") {
			t.Fatalf("warnings = %v", warnings)
		}
	})
}

type fakePingCheck struct {
	api.PingCheckService
	statuses []pingcheck.TunnelStatus
	logs     []pingcheck.LogEntry
	started  chan struct{} // receives once per CheckAllNow (optional)
	release  chan struct{} // CheckAllNow blocks until it is closed (optional)
	disabled bool          // IsEnabled reports the inverse (zero value: enabled)
}

func (f *fakePingCheck) IsEnabled() bool { return !f.disabled }

func (f *fakePingCheck) CheckAllNow() {
	if f.started != nil {
		f.started <- struct{}{}
	}
	if f.release != nil {
		<-f.release
	}
}
func (f *fakePingCheck) GetStatus() []pingcheck.TunnelStatus { return f.statuses }

// GetLogs hands entries out NEWEST first, as logbuf.Buffer.GetAll does
// (buffer.go: "returns all entries, newest first"). The earlier version
// of this fake claimed the opposite, and the adapter reversed a list
// that was already in the right order.
func (f *fakePingCheck) GetLogs() []pingcheck.LogEntry {
	out := make([]pingcheck.LogEntry, 0, len(f.logs))
	for i := len(f.logs) - 1; i >= 0; i-- {
		out = append(out, f.logs[i])
	}
	return out
}

func (f *fakePingCheck) GetTunnelLogs(tunnelID string) []pingcheck.LogEntry {
	var out []pingcheck.LogEntry
	for _, e := range f.GetLogs() {
		if e.TunnelID == tunnelID {
			out = append(out, e)
		}
	}
	return out
}

// TestLocal_RunPingCheckDoesNotBlockOnTheSweep — CheckAllNow пробует все
// туннели синхронно и без контекста запроса; вызов возвращается сразу со
// статусом последней завершённой проверки, а повторный вызов во время
// идущей проверки не запускает вторую.
func TestLocal_RunPingCheckDoesNotBlockOnTheSweep(t *testing.T) {
	ctx := context.Background()
	pc := &fakePingCheck{started: make(chan struct{}, 1), release: make(chan struct{})}
	l := New(Config{PingCheck: pc})
	l.sweepMinInterval = 0 // the in-flight guard is under test here, not the spacing

	first, err := l.RunPingCheck(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Triggered {
		t.Fatal("first call must start a sweep")
	}
	select {
	case <-pc.started:
	case <-time.After(2 * time.Second):
		t.Fatal("sweep did not start in the background")
	}
	second, err := l.RunPingCheck(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second.Triggered {
		t.Fatal("a sweep is in flight; the second call must not stack another")
	}
	close(pc.release)
	deadline := time.Now().Add(2 * time.Second)
	for l.pingSweep.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	third, err := l.RunPingCheck(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !third.Triggered {
		t.Fatal("after the sweep finished a new one must be allowed")
	}
	<-pc.started // release is closed, so the third sweep ends on its own
}

// TestLocal_RunPingCheckIsHonestAboutTriggering — при выключенном
// мониторинге проверять нечего (monitors пусты), и triggered:true заставил
// бы модель ждать результат вечно; подряд идущие вызовы не запускают
// проверку чаще sweepMinInterval.
func TestLocal_RunPingCheckIsHonestAboutTriggering(t *testing.T) {
	ctx := context.Background()

	t.Run("disabled monitoring never triggers", func(t *testing.T) {
		pc := &fakePingCheck{disabled: true, started: make(chan struct{}, 1)}
		l := New(Config{PingCheck: pc})
		run, err := l.RunPingCheck(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if run.Triggered {
			t.Fatal("triggered with monitoring off")
		}
		select {
		case <-pc.started:
			t.Fatal("a sweep started with monitoring off")
		case <-time.After(50 * time.Millisecond):
		}
	})

	t.Run("back-to-back calls are spaced", func(t *testing.T) {
		pc := &fakePingCheck{started: make(chan struct{}, 2)}
		l := New(Config{PingCheck: pc}) // default 10 s spacing
		first, _ := l.RunPingCheck(ctx)
		<-pc.started // the first sweep has finished by the time this returns
		second, _ := l.RunPingCheck(ctx)
		if !first.Triggered || second.Triggered {
			t.Fatalf("triggered = %v, %v; want true then false within the spacing interval", first.Triggered, second.Triggered)
		}
	})
}

// TestLocal_ImportTunnelWritesPingCheckDefaults зеркалит
// api.ImportHandler.ImportConf (internal/api/import.go): без этих умолчаний
// созданный через MCP туннель вообще не имеет записи PingCheck и мониторинг
// обращается с ним иначе, чем с импортированным из веб-интерфейса.
func TestLocal_ImportTunnelWritesPingCheckDefaults(t *testing.T) {
	ctx := context.Background()
	l, _, store := newLifecycle(t, tunnel.StateStopped)
	cfg := l.c
	cfg.PingCheck = &fakePingCheck{}
	cfg.Bus = &recBus{}
	l = New(cfg)

	got, _, err := l.ImportTunnel(ctx, "New", "[Interface]\n[Peer]\n")
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled {
		t.Fatal("service.Import hard-sets Enabled=false; the summary must say so")
	}
	stored, err := store.Get("tn-1")
	if err != nil {
		t.Fatal(err)
	}
	if stored.PingCheck == nil {
		t.Fatal("post-import PingCheck defaults were not written")
	}
	pc := stored.PingCheck
	if pc.Enabled || pc.Method != "icmp" || pc.Target != "8.8.8.8" || pc.Interval != 45 ||
		pc.DeadInterval != 120 || pc.FailThreshold != 3 || pc.MinSuccess != 1 || pc.Timeout != 5 || !pc.Restart {
		t.Fatalf("defaults differ from the REST import handler: %+v", pc)
	}
}

// TestLocal_TunnelListChangesRefreshPingCheckSnapshot — зеркало
// api.TunnelsHandler.publishTunnelList: после create_tunnel /
// replace_tunnel_config / enable снимок мониторинга перепубликуется, иначе
// созданный через MCP туннель не виден на странице мониторинга до
// постороннего обновления. Start/stop идут через оркестратор и снимок не
// трогают — как и в REST.
func TestLocal_TunnelListChangesRefreshPingCheckSnapshot(t *testing.T) {
	ctx := context.Background()
	l, _, _ := newLifecycle(t, tunnel.StateStopped)
	cfg := l.c
	snapshots := 0
	cfg.PingCheckSnapshot = func() { snapshots++ }
	l = New(cfg)

	if _, _, err := l.ImportTunnel(ctx, "New", "[Interface]\n[Peer]\n"); err != nil {
		t.Fatal(err)
	}
	if snapshots != 1 {
		t.Fatalf("after import: snapshots = %d, want 1", snapshots)
	}
	if _, err := l.ReplaceTunnelConfig(ctx, "tn-1", "[Interface]\n[Peer]\n", ""); err != nil {
		t.Fatal(err)
	}
	if snapshots != 2 {
		t.Fatalf("after replace: snapshots = %d, want 2", snapshots)
	}
	if err := l.ControlTunnel(ctx, "tn-1", mcpsrv.ActionEnable); err != nil {
		t.Fatal(err)
	}
	if snapshots != 3 {
		t.Fatalf("after enable: snapshots = %d, want 3", snapshots)
	}
	if err := l.ControlTunnel(ctx, "tn-1", mcpsrv.ActionStart); err != nil {
		t.Fatal(err)
	}
	if snapshots != 4 {
		t.Fatalf("after start: snapshots = %d, want 4 (REST Start refreshes it too)", snapshots)
	}
}

type journalLine struct{ level, group, subgroup, action, target string }

// recJournal records AppLog calls so tests can assert what an admin sees
// on the logs page after an MCP mutation.
type recJournal struct{ lines []journalLine }

func (j *recJournal) AppLog(level logging.Level, group, subgroup, action, target, _ string) {
	j.lines = append(j.lines, journalLine{string(level), group, subgroup, action, target})
}

// TestLocal_MutationsAreJournaledLikeREST — каждое действие через MCP
// оставляет след в журнале под той же группой/подгруппой, что и его
// REST-обработчик: страница логов с фильтром «tunnel» или «routing» иначе
// не показывала бы, что туннель остановил агент.
func TestLocal_MutationsAreJournaledLikeREST(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	j := &recJournal{}
	cfg := h.l.c
	cfg.AppLog = j
	l := New(cfg)

	if err := l.ControlTunnel(ctx, "tn-1", mcpsrv.ActionStop); err != nil {
		t.Fatal(err)
	}
	if _, err := l.AddDNSRoute(ctx, mcpsrv.DNSRouteInput{Name: "GH", Domains: []string{"github.com"}, TunnelID: "tn-1"}); err != nil {
		t.Fatal(err)
	}
	h.orch.err = errors.New("boom")
	if err := l.ControlTunnel(ctx, "tn-1", mcpsrv.ActionRestart); err == nil {
		t.Fatal("expected restart to fail")
	}

	want := []journalLine{
		{"info", logging.GroupTunnel, logging.SubLifecycle, "stop", "tn-1"},
		{"info", logging.GroupRouting, logging.SubDnsRoute, "create", "GH"},
		{"warn", logging.GroupTunnel, logging.SubLifecycle, "restart", "tn-1"},
	}
	if !reflect.DeepEqual(j.lines, want) {
		t.Fatalf("journal =\n%+v\nwant\n%+v", j.lines, want)
	}
}

// TestLocal_ControlTunnelMirrorsRESTLifecycleEdges — две ветки
// api.ControlHandler, без которых агент получает ложный отказ или
// туннель, который «выключили», сам поднимается после перезагрузки.
func TestLocal_ControlTunnelMirrorsRESTLifecycleEdges(t *testing.T) {
	ctx := context.Background()

	t.Run("start on a running tunnel is success", func(t *testing.T) {
		h := newHarness(t)
		h.orch.err = tunnel.ErrAlreadyRunning
		if err := h.l.ControlTunnel(ctx, "tn-1", mcpsrv.ActionStart); err != nil {
			t.Fatalf("ErrAlreadyRunning must read as fulfilled intent, got %v", err)
		}
		if len(h.bus.pub) == 0 {
			t.Fatal("a fulfilled start still publishes")
		}
	})

	t.Run("failed stop records enabled=false", func(t *testing.T) {
		h := newHarness(t)
		h.orch.err = errors.New("wg down failed")
		h.tun.enabled["tn-1"] = true
		if err := h.l.ControlTunnel(ctx, "tn-1", mcpsrv.ActionStop); err == nil {
			t.Fatal("expected the stop error to propagate")
		}
		if v, ok := h.tun.enabled["tn-1"]; !ok || v {
			t.Fatalf("enabled after failed stop = %v/%v, want false (REST syncs the OFF intent)", v, ok)
		}
	})

	t.Run("busy stop leaves enabled alone", func(t *testing.T) {
		h := newHarness(t)
		h.orch.err = tunnel.ErrOperationInProgress
		h.tun.enabled["tn-1"] = true
		if err := h.l.ControlTunnel(ctx, "tn-1", mcpsrv.ActionStop); !errors.Is(err, tunnel.ErrOperationInProgress) {
			t.Fatalf("err = %v", err)
		}
		if !h.tun.enabled["tn-1"] {
			t.Fatal("nothing was attempted; enabled must not flip")
		}
	})
}

// TestLocal_ImportTunnelReportsAddressConflicts — api.ImportHandler
// прикладывает CheckAddressConflicts к ответу; без этого агент включит
// туннель, чей Address совпадает с Wireguard0, со слов модели.
func TestLocal_ImportTunnelReportsAddressConflicts(t *testing.T) {
	l, ft, _ := newLifecycle(t, tunnel.StateStopped)
	ft.conflicts = []string{"address 10.8.0.2/32 conflicts with Wireguard0"}
	got, warnings, err := l.ImportTunnel(context.Background(), "New", "[Interface]\n[Peer]\n")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "tn-1" {
		t.Fatalf("created = %+v", got)
	}
	if len(warnings) != 1 || warnings[0] != ft.conflicts[0] {
		t.Fatalf("warnings = %v, want the conflict from the service", warnings)
	}
}

// Без сервиса PingCheck умолчания не пишутся — ровно как в обработчике,
// где запись стоит под `h.pingCheck != nil`.
func TestLocal_ImportTunnelSkipsPingCheckDefaultsWithoutService(t *testing.T) {
	l, _, store := newLifecycle(t, tunnel.StateStopped)
	if _, _, err := l.ImportTunnel(context.Background(), "New", "[Interface]\n[Peer]\n"); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get("tn-1")
	if err != nil {
		t.Fatal(err)
	}
	if stored.PingCheck != nil {
		t.Fatalf("PingCheck written without the service: %+v", stored.PingCheck)
	}
}

// TestLocal_LockedTunnelRejectsChanges зеркалит защиту #818 из REST-хендлеров:
// запертый туннель не останавливают, не переключают автозапуск и маршрут по
// умолчанию, не меняют конфиг; start и restart проходят, как и по REST.
func TestLocal_LockedTunnelRejectsChanges(t *testing.T) {
	ctx := context.Background()
	l, ft, store := newLifecycle(t, tunnel.StateRunning)
	if err := store.Update("tn-1", func(st *storage.AWGTunnel) error { st.Locked = true; return nil }); err != nil {
		t.Fatal(err)
	}
	fo := l.c.Orch.(*fakeOrch)

	for _, a := range []string{mcpsrv.ActionStop, mcpsrv.ActionEnable, mcpsrv.ActionDisable, mcpsrv.ActionSetDefaultRoute, mcpsrv.ActionUnsetDefaultRoute} {
		if err := l.ControlTunnel(ctx, "tn-1", a); err == nil {
			t.Fatalf("%s on a locked tunnel must be rejected", a)
		}
	}
	if len(fo.events) != 0 || len(ft.enabled) != 0 || len(ft.defRoute) != 0 {
		t.Fatalf("locked tunnel was touched: events=%v enabled=%v defRoute=%v", fo.events, ft.enabled, ft.defRoute)
	}
	if _, err := l.ReplaceTunnelConfig(ctx, "tn-1", "cfg", ""); err == nil || len(ft.calls) != 0 {
		t.Fatalf("replace on a locked tunnel: err=%v calls=%v", err, ft.calls)
	}

	for _, a := range []string{mcpsrv.ActionStart, mcpsrv.ActionRestart} {
		if err := l.ControlTunnel(ctx, "tn-1", a); err != nil {
			t.Fatalf("%s on a locked tunnel must pass: %v", a, err)
		}
	}
	if len(fo.events) != 2 {
		t.Fatalf("events = %+v", fo.events)
	}
}

// TestLocal_GetDNSRouteIsUncappedAndCarriesTheDroppedFields — get_dns_route
// exists precisely because the list view truncates. If this adapter
// applied the list cap too, the tool would answer "that domain is not in
// the list" from a silently cut record.
func TestLocal_GetDNSRouteIsUncappedAndCarriesTheDroppedFields(t *testing.T) {
	h := newHarness(t)
	big := make([]string, 0, 3*mcpsrv.MaxDomainsInOutput)
	for i := range cap(big) {
		big = append(big, fmt.Sprintf("d%d.example", i))
	}
	h.dns.lists = []dnsroute.DomainList{{
		ID: "dl-big", Name: "Geo", Enabled: true, Domains: big, ManualDomains: []string{"my.example"},
		Excludes: []string{"ads.example"}, ExcludeSubnets: []string{"10.1.0.0/16"},
		Subnets: []string{"10.0.0.0/8"}, Backend: "ndms", CreatedAt: "2026-09-01T00:00:00Z",
		Routes:        []dnsroute.RouteTarget{{Interface: "nwg0", TunnelID: "tn-1", Fallback: "bypass"}},
		Subscriptions: []dnsroute.Subscription{{URL: "https://example.invalid/list", Name: "Geo feed", LastCount: 12, LastError: "timeout"}},
	}}

	got, err := h.l.GetDNSRoute(context.Background(), "dl-big")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Domains) != len(big) {
		t.Fatalf("domains = %d, want all %d — the detail view must not cap", len(got.Domains), len(big))
	}
	if len(got.Excludes) != 1 || got.Excludes[0] != "ads.example" {
		t.Errorf("excludes = %v", got.Excludes)
	}
	if len(got.ExcludeSubnets) != 1 {
		t.Errorf("excludeSubnets = %v", got.ExcludeSubnets)
	}
	if len(got.Subscriptions) != 1 {
		t.Fatalf("subscriptions = %v", got.Subscriptions)
	}
	sub := got.Subscriptions[0]
	if sub.URL != "https://example.invalid/list" || sub.Name != "Geo feed" || sub.LastCount != 12 || sub.LastError != "timeout" {
		t.Errorf("subscription mapping lost fields: %+v", sub)
	}
	want := mcpsrv.RouteTarget{Interface: "nwg0", TunnelID: "tn-1", Fallback: "bypass"}
	if got.ID != "dl-big" || got.Name != "Geo" || !got.Enabled || got.Backend != "ndms" || got.CreatedAt == "" || len(got.Routes) != 1 || got.Routes[0] != want {
		t.Errorf("record = %+v", got)
	}

	if _, err := h.l.GetDNSRoute(context.Background(), "nope"); err == nil {
		t.Error("unknown id must be an error")
	}
}

// TestLocal_SetDNSRouteEnabled — переключатель должен возвращать запись
// уже в новом состоянии и публиковать инвалидацию: MCP пишет мимо HTTP-
// обработчиков, которые этим занимаются, и открытая вкладка иначе покажет
// список включённым после выключения.
func TestLocal_SetDNSRouteEnabled(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.dns.lists = []dnsroute.DomainList{{ID: "dl-1", Name: "Video", Enabled: true, Domains: []string{"youtube.com"}}}

	got, err := h.l.SetDNSRouteEnabled(ctx, "dl-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := h.dns.enabled["dl-1"]; !ok || v {
		t.Fatalf("service SetEnabled was called with %v (present=%v), want false", v, ok)
	}
	if got.Enabled {
		t.Error("the returned record must show the state AFTER the change")
	}
	if got.ID != "dl-1" || got.Name != "Video" {
		t.Errorf("record = %+v", got)
	}
	if !h.bus.has(events.ResourceRoutingDnsRoutes) {
		t.Errorf("published %v, want a dns-routes invalidation", h.bus.pub)
	}

	if _, err := h.l.SetDNSRouteEnabled(ctx, "nope", true); err == nil {
		t.Error("unknown id must be an error")
	}
}

// TestLocal_SetStaticRouteEnabled — как и у доменных списков, ответ
// обязан отражать состояние после применения, а веб-интерфейс — узнать
// об изменении.
func TestLocal_SetStaticRouteEnabled(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	got, err := h.l.SetStaticRouteEnabled(ctx, "sr-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled {
		t.Error("the returned record must show the state AFTER the change")
	}
	if got.ID != "sr-1" || got.Name != "Office" || got.TunnelID != "tn-1" {
		t.Errorf("record = %+v", got)
	}
	if !h.bus.has(events.ResourceRoutingStaticRoutes) {
		t.Errorf("published %v, want a static-routes invalidation", h.bus.pub)
	}

	if _, err := h.l.SetStaticRouteEnabled(ctx, "nope", true); err == nil {
		t.Error("unknown id must be an error")
	}
}

// TestLocal_SetClientRouteEnabled — переключатель ищет маршрут по IP
// устройства, потому что это то, чем оперирует агент (list_devices), а
// служба работает по внутреннему id.
func TestLocal_SetClientRouteEnabled(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.client.routes = []clientroute.ClientRoute{{ID: "cr-1", ClientIP: "192.168.1.20", TunnelID: "tn-1", Fallback: "drop", Enabled: true}}

	got, err := h.l.SetClientRouteEnabled(ctx, "192.168.1.20", false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled {
		t.Error("the returned record must show the state AFTER the change")
	}
	if got.ID != "cr-1" || got.TunnelID != "tn-1" || got.Fallback != "drop" {
		t.Errorf("a disabled route keeps its target and fallback: %+v", got)
	}
	if !h.bus.has(events.ResourceRoutingClientRoutes) {
		t.Errorf("published %v, want a client-routes invalidation", h.bus.pub)
	}

	if _, err := h.l.SetClientRouteEnabled(ctx, "192.168.1.99", true); err == nil {
		t.Error("an IP with no route must be an error, not a silent no-op")
	}
}

// TestLocal_UpdateDNSRouteSendsOnlyWhatChanged — dnsroute.Update трактует
// нулевое значение как «поле не прислали» и сохраняет прежнее. Значит
// адаптер обязан отправлять именно разреженную запись: пришли он список
// целиком, любое непереносимое через MCP поле (подписки, excludes) было
// бы затёрто нулём.
func TestLocal_UpdateDNSRouteSendsOnlyWhatChanged(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.dns.lists = []dnsroute.DomainList{{
		ID: "dl-1", Name: "Video", Enabled: true,
		Domains: []string{"youtube.com"}, ManualDomains: []string{"youtube.com"},
		Excludes: []string{"ads.example"}, Backend: "ndms",
		Subscriptions: []dnsroute.Subscription{{URL: "https://example.invalid/list"}},
		Routes:        []dnsroute.RouteTarget{{TunnelID: "tn-1"}},
	}}

	if _, _, err := h.l.UpdateDNSRoute(ctx, mcpsrv.DNSRouteUpdate{RouteID: "dl-1", Name: "Видео"}); err != nil {
		t.Fatal(err)
	}
	sent := h.dns.updated
	if sent.ID != "dl-1" || sent.Name != "Видео" {
		t.Fatalf("sent = %+v", sent)
	}
	if sent.ManualDomains != nil || sent.Routes != nil || sent.Subscriptions != nil || sent.Excludes != nil || sent.Backend != "" {
		t.Fatalf("a rename must send nothing but the name; the service preserves the rest: %+v", sent)
	}

	if _, _, err := h.l.UpdateDNSRoute(ctx, mcpsrv.DNSRouteUpdate{RouteID: "dl-1", ManualDomains: []string{"a.example"}}); err != nil {
		t.Fatal(err)
	}
	sent = h.dns.updated
	if len(sent.ManualDomains) != 1 || sent.ManualDomains[0] != "a.example" {
		t.Fatalf("manualDomains must go into ManualDomains, got %+v", sent)
	}
	if sent.Domains != nil {
		t.Errorf("Domains is derived by the service and must not be sent: %v", sent.Domains)
	}
	if sent.Name != "" {
		t.Errorf("an unchanged name must not be sent: %q", sent.Name)
	}

	if !h.bus.has(events.ResourceRoutingDnsRoutes) {
		t.Errorf("published %v, want a dns-routes invalidation", h.bus.pub)
	}
	if _, _, err := h.l.UpdateDNSRoute(ctx, mcpsrv.DNSRouteUpdate{RouteID: "nope", Name: "x"}); err == nil {
		t.Error("unknown id must be an error")
	}
}

// TestLocal_UpdateDNSRouteWarnsAboutDroppedTargets — tunnelId задаёт ровно
// одну цель, поэтому у списка с несколькими целями остальные пропадают.
func TestLocal_UpdateDNSRouteWarnsAboutDroppedTargets(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.dns.lists = []dnsroute.DomainList{{
		ID: "dl-multi", Name: "Split", Enabled: true,
		Routes: []dnsroute.RouteTarget{{TunnelID: "tn-1"}, {TunnelID: "tn-2"}},
	}}

	_, warnings, err := h.l.UpdateDNSRoute(ctx, mcpsrv.DNSRouteUpdate{RouteID: "dl-multi", TunnelID: "tn-2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) == 0 {
		t.Fatal("dropping a route target must warn")
	}
	if len(h.dns.updated.Routes) != 1 || h.dns.updated.Routes[0].TunnelID != "tn-2" {
		t.Fatalf("sent routes = %+v", h.dns.updated.Routes)
	}

	_, warnings, err = h.l.UpdateDNSRoute(ctx, mcpsrv.DNSRouteUpdate{RouteID: "dl-multi", Name: "Split 2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("a rename touches no targets and must not warn: %v", warnings)
	}
}

// TestLocal_UpdateDNSRouteWarnsAboutDroppedManualSubnets — dnsroute.Update
// пересобирает Domains и Subnets из ManualDomains (impl.go, «Merge
// domains»), поэтому CIDR среди ручных записей живёт ровно до первой
// замены manualDomains, в которой его не повторили. Молча — нельзя.
func TestLocal_UpdateDNSRouteWarnsAboutDroppedManualSubnets(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.dns.lists = []dnsroute.DomainList{{
		ID: "dl-mixed", Name: "Mixed", Enabled: true,
		Domains:       []string{"a.example"},
		ManualDomains: []string{"a.example", "10.0.0.0/8", "192.168.0.0/16"},
		Subnets:       []string{"10.0.0.0/8", "192.168.0.0/16"},
		Routes:        []dnsroute.RouteTarget{{TunnelID: "tn-1"}},
	}}

	_, warnings, err := h.l.UpdateDNSRoute(ctx, mcpsrv.DNSRouteUpdate{RouteID: "dl-mixed", ManualDomains: []string{"b.example", "10.0.0.0/8"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "192.168.0.0/16") || strings.Contains(warnings[0], "10.0.0.0/8") {
		t.Fatalf("warnings = %v, want exactly the dropped subnet named", warnings)
	}

	// The subnets are sent back whole: nothing was lost, nothing to say.
	_, warnings, err = h.l.UpdateDNSRoute(ctx, mcpsrv.DNSRouteUpdate{RouteID: "dl-mixed", ManualDomains: []string{"c.example", "10.0.0.0/8"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("re-sending every subnet must not warn: %v", warnings)
	}
}

// fakeTester records the service URL it was asked for: MCP must not pin
// one, or a single flaky IP-echo host reads as a broken tunnel.
type fakeTester struct {
	askedURL string
	result   *awgtesting.IPResult
	err      error
}

func (f *fakeTester) CheckConnectivity(context.Context, string) (*awgtesting.ConnectivityResult, error) {
	return &awgtesting.ConnectivityResult{Connected: true}, nil
}

func (f *fakeTester) CheckIP(_ context.Context, _ string, serviceURL string) (*awgtesting.IPResult, error) {
	f.askedURL = serviceURL
	return f.result, f.err
}

func TestLocal_CheckIPMapsTheResultAndPinsNoProvider(t *testing.T) {
	ctx := context.Background()
	ft := &fakeTester{result: &awgtesting.IPResult{DirectIP: "203.0.113.7", VpnIP: "198.51.100.42", EndpointIP: "198.51.100.1", IPChanged: true}}
	l := New(Config{Testing: ft})

	got, err := l.CheckIP(ctx, "tn-1")
	if err != nil {
		t.Fatal(err)
	}
	if ft.askedURL != "" {
		t.Errorf("serviceURL = %q, want empty so the service can fall back between providers", ft.askedURL)
	}
	want := mcpsrv.IPCheckResult{TunnelID: "tn-1", DirectIP: "203.0.113.7", VpnIP: "198.51.100.42", EndpointIP: "198.51.100.1", IPChanged: true}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// TestLocal_CheckIPLeakIsReportedNotSwallowed — совпавшие адреса значат,
// что трафик идёт мимо туннеля. Это результат проверки, и потерять его
// нельзя.
func TestLocal_CheckIPLeakIsReportedNotSwallowed(t *testing.T) {
	ft := &fakeTester{result: &awgtesting.IPResult{DirectIP: "203.0.113.7", VpnIP: "203.0.113.7", IPChanged: false}}
	l := New(Config{Testing: ft})

	got, err := l.CheckIP(context.Background(), "tn-1")
	if err != nil {
		t.Fatalf("a leak is a result, not an error: %v", err)
	}
	if got.IPChanged || got.VpnIP != got.DirectIP {
		t.Fatalf("got %+v", got)
	}
}

func TestLocal_CheckIPWithoutATesterSaysSo(t *testing.T) {
	l := New(Config{})
	if _, err := l.CheckIP(context.Background(), "tn-1"); err == nil {
		t.Fatal("a build without the testing service must report that, not return a blank result")
	}
}

// TestLocal_CheckIPNilResultIsAnError — служба может вернуть (nil, nil);
// пустая структура прочиталась бы как «оба адреса неизвестны, утечки нет».
func TestLocal_CheckIPNilResultIsAnError(t *testing.T) {
	l := New(Config{Testing: &fakeTester{result: nil}})
	if _, err := l.CheckIP(context.Background(), "tn-1"); err == nil {
		t.Fatal("a nil result must be an error")
	}
}

// TestLocal_ResolveDomainReturnsIPv4Only — explain_route сверяет адреса с
// CIDR-списками, а те у нас IPv4. Пропущенный AAAA сравнивался бы всегда
// мимо и молча превращался в «ни один список не подходит».
func TestLocal_ResolveDomainReturnsIPv4Only(t *testing.T) {
	l := New(Config{Resolve: func(context.Context, string) ([]string, error) {
		return []string{"2001:db8::1", "203.0.113.9", "not-an-ip", "::ffff:198.51.100.7"}, nil
	}})

	got, err := l.ResolveDomain(context.Background(), "example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"203.0.113.9", "198.51.100.7"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestLocal_ResolveDomainReportsFailure(t *testing.T) {
	l := New(Config{Resolve: func(context.Context, string) ([]string, error) {
		return nil, errors.New("no such host")
	}})
	if _, err := l.ResolveDomain(context.Background(), "nope.invalid"); err == nil {
		t.Fatal("a failed lookup must be reported, so explain_route can say the subnets went unchecked")
	}
}

// TestLocal_ResolveDomainWithoutAResolverSaysSo — пустой список читался бы
// как «домен никуда не резолвится».
func TestLocal_ResolveDomainWithoutAResolverSaysSo(t *testing.T) {
	l := New(Config{})
	if _, err := l.ResolveDomain(context.Background(), "example.invalid"); err == nil {
		t.Fatal("a build without a resolver must report that")
	}
}

// fakeSingboxOp implements the operator surface MCP needs.
type fakeSingboxOp struct {
	tunnels []singbox.TunnelInfo
	delays  map[string]int
	busy    map[string]bool
	asked   []string
	err     error
}

func (f *fakeSingboxOp) GetStatus(context.Context) singbox.Status {
	return singbox.Status{Installed: true, Running: true, TunnelCount: len(f.tunnels)}
}
func (f *fakeSingboxOp) Control(context.Context, string) error { return nil }
func (f *fakeSingboxOp) ListTunnels(context.Context) ([]singbox.TunnelInfo, error) {
	return f.tunnels, f.err
}
func (f *fakeSingboxOp) CheckDelay(_ context.Context, tag string) (int, error) {
	f.asked = append(f.asked, tag)
	if f.busy[tag] {
		return 0, singbox.ErrProbeInFlight
	}
	return f.delays[tag], nil
}

func singboxHarness() *fakeSingboxOp {
	return &fakeSingboxOp{
		tunnels: []singbox.TunnelInfo{
			{Tag: "vless-nl", Protocol: "vless", Server: "nl.example.net", Port: 443, Security: "reality", Transport: "tcp", ListenPort: 2081, ProxyInterface: "Proxy0", SNI: "www.example.com", Username: "secret-user", Running: true},
			{Tag: "hy2-de", Protocol: "hysteria2", Server: "de.example.net", Port: 8443, Running: false},
		},
		delays: map[string]int{"vless-nl": 120, "hy2-de": 0},
	}
}

func TestLocal_ListSingboxTunnelsMapsTheProxy(t *testing.T) {
	op := singboxHarness()
	l := New(Config{Singbox: op})

	got, err := l.ListSingboxTunnels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("tunnels = %d", len(got))
	}
	want := mcpsrv.SingboxTunnel{
		Tag: "vless-nl", Protocol: "vless", Server: "nl.example.net", Port: 443,
		Security: "reality", Transport: "tcp", ListenPort: 2081, ProxyInterface: "Proxy0",
		SNI: "www.example.com", Running: true,
	}
	if got[0] != want {
		t.Fatalf("got %+v, want %+v", got[0], want)
	}
	if !got[1].Running == false && got[1].Tag != "hy2-de" {
		t.Fatalf("a configured but dead proxy must still be listed: %+v", got[1])
	}
}

// TestLocal_ListSingboxTunnelsCarriesNoCredentials — у naive-прокси в
// TunnelInfo лежит имя пользователя. Учётные данные через MCP не отдаём.
func TestLocal_ListSingboxTunnelsCarriesNoCredentials(t *testing.T) {
	op := singboxHarness()
	l := New(Config{Singbox: op})

	got, err := l.ListSingboxTunnels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	blob := fmt.Sprintf("%+v", got)
	if strings.Contains(blob, "secret-user") {
		t.Fatalf("the proxy username must not cross the MCP boundary: %s", blob)
	}
}

// TestLocal_CheckSingboxDelaySeparatesSilenceFromZero — CheckOne отвечает
// нулём и на таймаут; ноль сам по себе читается как «0 мс, отлично».
func TestLocal_CheckSingboxDelaySeparatesSilenceFromZero(t *testing.T) {
	ctx := context.Background()
	op := singboxHarness()
	l := New(Config{Singbox: op})

	got, err := l.CheckSingboxDelay(ctx, "vless-nl")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Reachable || got.DelayMs != 120 {
		t.Fatalf("got %+v", got)
	}

	got, err = l.CheckSingboxDelay(ctx, "hy2-de")
	if err != nil {
		t.Fatalf("silence is a result, not an error: %v", err)
	}
	if got.Reachable || got.DelayMs != 0 {
		t.Fatalf("got %+v, want an explicit unreachable", got)
	}
}

// TestLocal_CheckSingboxDelayRejectsUnknownTag — по чужому тегу проба
// просто не ответит, и опечатка выглядела бы как упавший прокси.
func TestLocal_CheckSingboxDelayRejectsUnknownTag(t *testing.T) {
	op := singboxHarness()
	l := New(Config{Singbox: op})

	if _, err := l.CheckSingboxDelay(context.Background(), "nope"); err == nil {
		t.Fatal("an unknown tag must be an error")
	}
	if len(op.asked) != 0 {
		t.Fatalf("an unknown tag must not reach the delay checker, asked %v", op.asked)
	}
}

func TestLocal_SingboxToolsWithoutTheEngineSaySo(t *testing.T) {
	l := New(Config{})
	if _, err := l.ListSingboxTunnels(context.Background()); err == nil {
		t.Error("a build without sing-box must report that, not an empty list")
	}
	if _, err := l.CheckSingboxDelay(context.Background(), "x"); err == nil {
		t.Error("a build without sing-box must report that")
	}
}

// fakeManaged implements the managed-server surface MCP needs.
type fakeManaged struct {
	servers  []storage.ManagedServer
	added    managed.AddPeerRequest
	addedTo  string
	toggled  []string
	confFor  string
	confErr  error
	addErr   error
	confText string
}

func (f *fakeManaged) List() []storage.ManagedServer { return f.servers }

func (f *fakeManaged) Get(id string) (*storage.ManagedServer, error) {
	for i := range f.servers {
		if f.servers[i].InterfaceName == id {
			cp := f.servers[i]
			return &cp, nil
		}
	}
	return nil, errNotFound(id)
}

func (f *fakeManaged) AddPeer(_ context.Context, id string, req managed.AddPeerRequest) (*storage.ManagedPeer, error) {
	if f.addErr != nil {
		return nil, f.addErr
	}
	f.addedTo, f.added = id, req
	tunnelIP := req.TunnelIP
	if tunnelIP == "" {
		// Mirrors managed.Service.AddPeer: an empty address means "allocate
		// the first free one", and a subnet with none left is ErrNoFreePeerIP.
		for i := range f.servers {
			if f.servers[i].InterfaceName != id {
				continue
			}
			used := make([]string, 0, len(f.servers[i].Peers))
			for _, p := range f.servers[i].Peers {
				used = append(used, p.TunnelIP)
			}
			tunnelIP = peerip.NextFree(f.servers[i].Address, used)
		}
		if tunnelIP == "" {
			return nil, peerip.ErrNoFree
		}
	}
	peer := storage.ManagedPeer{
		PublicKey: "pub-new=", PrivateKey: "secret-private", PresharedKey: "secret-psk",
		Description: req.Description, TunnelIP: tunnelIP, DNS: req.DNS, Enabled: true,
	}
	for i := range f.servers {
		if f.servers[i].InterfaceName == id {
			f.servers[i].Peers = append(f.servers[i].Peers, peer)
		}
	}
	return &peer, nil
}

func (f *fakeManaged) TogglePeer(_ context.Context, id, pubkey string, enabled bool) error {
	for i := range f.servers {
		if f.servers[i].InterfaceName != id {
			continue
		}
		for j := range f.servers[i].Peers {
			if f.servers[i].Peers[j].PublicKey == pubkey {
				f.servers[i].Peers[j].Enabled = enabled
				f.toggled = append(f.toggled, pubkey)
				return nil
			}
		}
	}
	return errNotFound(pubkey)
}

func (f *fakeManaged) GenerateConf(_ context.Context, id, pubkey, _ string) (string, error) {
	f.confFor = pubkey
	return f.confText, f.confErr
}

func managedHarness() *fakeManaged {
	return &fakeManaged{
		servers: []storage.ManagedServer{{
			InterfaceName: "Wireguard3", Description: "Home", Address: "10.0.0.1", Mask: "255.255.255.0",
			Peers: []storage.ManagedPeer{
				{PublicKey: "pub-laptop=", PrivateKey: "secret-private", PresharedKey: "secret-psk", Description: "laptop", TunnelIP: "10.0.0.2/32", Enabled: true},
				{PublicKey: "pub-tv=", PrivateKey: "secret-private2", PresharedKey: "secret-psk2", Description: "tv", TunnelIP: "10.0.0.3/32", Enabled: false},
			},
		}},
		confText: "[Interface]\nPrivateKey = secret-private\n\n[Peer]\nPublicKey = server=\n",
	}
}

// TestLocal_ListServerPeersCarriesNoKeys — приватный ключ и PSK клиента
// лежат в хранилище ради генерации .conf. В списке пиров им делать
// нечего: конфиг выдаётся отдельным инструментом.
func TestLocal_ListServerPeersCarriesNoKeys(t *testing.T) {
	m := managedHarness()
	l := New(Config{Managed: m})

	got, err := l.ListServerPeers(context.Background(), "Wireguard3")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("peers = %d", len(got))
	}
	want := mcpsrv.ServerPeer{PublicKey: "pub-laptop=", Description: "laptop", TunnelIP: "10.0.0.2/32", Enabled: true}
	if got[0] != want {
		t.Fatalf("got %+v, want %+v", got[0], want)
	}
	if !got[1].Enabled == false && got[1].Description != "tv" {
		t.Fatalf("a disabled peer must still be listed: %+v", got[1])
	}
	blob := fmt.Sprintf("%+v", got)
	for _, secret := range []string{"secret-private", "secret-psk"} {
		if strings.Contains(blob, secret) {
			t.Fatalf("the peer listing leaked %q: %s", secret, blob)
		}
	}

	if _, err := l.ListServerPeers(context.Background(), "nope"); err == nil {
		t.Error("an unknown server must be an error")
	}
}

// TestLocal_AddServerPeerLeavesAllocationToTheService — ревью нашло третью
// копию правила «первый свободный адрес» в слое инструментов. Правило
// одно, в managed.AddPeer: адаптер отдаёт пустой TunnelIP как есть и
// переводит ErrNoFreePeerIP в подсказку «спросить пользователя».
func TestLocal_AddServerPeerLeavesAllocationToTheService(t *testing.T) {
	ctx := context.Background()
	m := managedHarness()
	l := New(Config{Managed: m})

	got, err := l.AddServerPeer(ctx, mcpsrv.AddPeerInput{ServerID: "Wireguard3", Description: "phone"})
	if err != nil {
		t.Fatal(err)
	}
	if m.added.TunnelIP != "" {
		t.Fatalf("sent %q, want an empty TunnelIP so the service allocates", m.added.TunnelIP)
	}
	if m.addedTo != "Wireguard3" || m.added.Description != "phone" {
		t.Fatalf("request = %+v to %q", m.added, m.addedTo)
	}
	if got.TunnelIP != "10.0.0.4/32" || got.Description != "phone" || !got.Enabled {
		t.Fatalf("returned peer = %+v, want the address the service allocated", got)
	}
	if got.PublicKey == "" {
		t.Error("the caller needs the public key to address the peer later")
	}

	// An explicit address is passed through untouched.
	if _, err := l.AddServerPeer(ctx, mcpsrv.AddPeerInput{ServerID: "Wireguard3", Description: "x", TunnelIP: "10.0.0.9/32"}); err != nil {
		t.Fatal(err)
	}
	if m.added.TunnelIP != "10.0.0.9/32" {
		t.Fatalf("explicit address was rewritten to %q", m.added.TunnelIP)
	}

	m.addErr = peerip.ErrNoFree
	_, err = l.AddServerPeer(ctx, mcpsrv.AddPeerInput{ServerID: "Wireguard3", Description: "y"})
	if err == nil || !strings.Contains(err.Error(), "ask the user") {
		t.Fatalf("err = %v, want the model told to ask the user for an address", err)
	}
}

// TestLocal_AddServerPeerReturnsNoSecrets — созданный пир возвращается
// вызывающему, а AddPeer отдаёт запись с приватным ключом.
func TestLocal_AddServerPeerReturnsNoSecrets(t *testing.T) {
	m := managedHarness()
	l := New(Config{Managed: m})

	got, err := l.AddServerPeer(context.Background(), mcpsrv.AddPeerInput{ServerID: "Wireguard3", Description: "phone"})
	if err != nil {
		t.Fatal(err)
	}
	if blob := fmt.Sprintf("%+v", got); strings.Contains(blob, "secret-private") || strings.Contains(blob, "secret-psk") {
		t.Fatalf("the created peer leaked key material: %s", blob)
	}
}

func TestLocal_SetServerPeerEnabled(t *testing.T) {
	ctx := context.Background()
	m := managedHarness()
	l := New(Config{Managed: m})

	got, err := l.SetServerPeerEnabled(ctx, "Wireguard3", "pub-laptop=", false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled {
		t.Error("the returned peer must show the state AFTER the change")
	}
	if got.PublicKey != "pub-laptop=" || got.Description != "laptop" {
		t.Fatalf("peer = %+v", got)
	}
	if len(m.toggled) != 1 {
		t.Fatalf("toggled = %v", m.toggled)
	}
	if _, err := l.SetServerPeerEnabled(ctx, "Wireguard3", "nope", true); err == nil {
		t.Error("an unknown peer must be an error")
	}
}

func TestLocal_ServerPeerConfig(t *testing.T) {
	m := managedHarness()
	l := New(Config{Managed: m})

	conf, err := l.ServerPeerConfig(context.Background(), "Wireguard3", "pub-laptop=")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conf, "[Interface]") {
		t.Fatalf("conf = %q", conf)
	}
	if m.confFor != "pub-laptop=" {
		t.Fatalf("asked for %q", m.confFor)
	}
}

// TestLocal_PeerToolsWithoutTheManagedServiceSaySo — на сборке без службы
// пустой список читался бы как «клиентов нет».
func TestLocal_PeerToolsWithoutTheManagedServiceSaySo(t *testing.T) {
	ctx := context.Background()
	l := New(Config{})
	if _, err := l.ListServerPeers(ctx, "Wireguard3"); err == nil {
		t.Error("listing must report the missing service")
	}
	if _, err := l.AddServerPeer(ctx, mcpsrv.AddPeerInput{ServerID: "Wireguard3", Description: "x"}); err == nil {
		t.Error("adding must report the missing service")
	}
	if _, err := l.SetServerPeerEnabled(ctx, "Wireguard3", "k", true); err == nil {
		t.Error("toggling must report the missing service")
	}
	if _, err := l.ServerPeerConfig(ctx, "Wireguard3", "k"); err == nil {
		t.Error("the config must report the missing service")
	}
}

type fakeConns struct {
	asked connections.ListParams
	resp  *connections.ListResponse
	err   error
}

// List applies the service's real Search semantics — a substring match
// over "src:port dst:port clientName" — so a test can show what an
// exact-IP filter must add on top.
func (f *fakeConns) List(_ context.Context, p connections.ListParams) (*connections.ListResponse, error) {
	f.asked = p
	if f.resp == nil || p.Search == "" {
		return f.resp, f.err
	}
	needle := strings.ToLower(p.Search)
	out := &connections.ListResponse{}
	for _, c := range f.resp.Connections {
		hay := strings.ToLower(c.Src + ":" + strconv.Itoa(c.SrcPort) + " " + c.Dst + ":" + strconv.Itoa(c.DstPort) + " " + c.ClientName)
		if strings.Contains(hay, needle) {
			out.Connections = append(out.Connections, c)
		}
	}
	out.Pagination.Total = len(out.Connections)
	return out, f.err
}

// TestLocal_ListConnectionsKeepsTheRealTotal — страница и общее число
// разные вещи: короткая страница, прочитанная как «всего два соединения»,
// это неверный ответ на «что делает устройство».
func TestLocal_ListConnectionsKeepsTheRealTotal(t *testing.T) {
	fc := &fakeConns{resp: &connections.ListResponse{
		Connections: []connections.Connection{{
			Protocol: "tcp", Src: "192.168.1.10", SrcPort: 5123, Dst: "142.250.1.1", DstPort: 443,
			State: "ESTABLISHED", Interface: "nwg0", TunnelID: "tn-1", TunnelName: "Amsterdam",
			ClientName: "laptop", Bytes: 999, TTL: 42,
		}},
		Pagination: connections.PaginationInfo{Total: 37, Returned: 1},
	}}
	l := New(Config{Connections: fc})

	got, total, err := l.ListConnections(context.Background(), mcpsrv.ConnectionsQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 37 {
		t.Fatalf("total = %d, want the count before paging", total)
	}
	want := mcpsrv.Connection{
		Protocol: "tcp", Src: "192.168.1.10", SrcPort: 5123, Dst: "142.250.1.1", DstPort: 443,
		State: "ESTABLISHED", Interface: "nwg0", TunnelID: "tn-1", TunnelName: "Amsterdam", ClientName: "laptop",
	}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	// An empty tunnel filter must reach the service as "all", not as an
	// empty string it would treat as a tunnel named "".
	if fc.asked.Tunnel != "all" {
		t.Errorf("Tunnel = %q, want \"all\"", fc.asked.Tunnel)
	}
	if fc.asked.Limit != 10 {
		t.Errorf("Limit = %d", fc.asked.Limit)
	}
}

func TestLocal_ListConnectionsWithoutTheServiceSaysSo(t *testing.T) {
	l := New(Config{})
	if _, _, err := l.ListConnections(context.Background(), mcpsrv.ConnectionsQuery{}); err == nil {
		t.Fatal("a build without the connections service must report that, not an empty table")
	}
}

// TestLocal_PingCheckLogsAreNewestFirst — ревью нашло: буфер и так отдаёт
// записи от новых к старым, а адаптер их переворачивал и обрезал. С
// лимитом 100 из 800 записей агент получал сотню САМЫХ СТАРЫХ проверок и
// отвечал на «когда начало падать» по данным двухчасовой давности. Здесь
// логи заданы от старых к новым, а фейк отдаёт их как настоящий буфер.
func TestLocal_PingCheckLogsAreNewestFirst(t *testing.T) {
	pc := &fakePingCheck{logs: []pingcheck.LogEntry{
		{Timestamp: time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC), TunnelID: "tn-1", TunnelName: "A", Success: true, Latency: 30},
		{Timestamp: time.Date(2026, 9, 2, 10, 1, 0, 0, time.UTC), TunnelID: "tn-1", TunnelName: "A", Success: false, Error: "timeout"},
		{Timestamp: time.Date(2026, 9, 2, 10, 2, 0, 0, time.UTC), TunnelID: "tn-1", TunnelName: "A", Success: false, Error: "timeout", StateChange: "link_toggle"},
	}}
	l := New(Config{PingCheck: pc})

	got, err := l.PingCheckLogs(context.Background(), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("entries = %d", len(got))
	}
	if got[0].StateChange != "link_toggle" {
		t.Fatalf("first entry = %+v, want the newest", got[0])
	}
	if got[0].Timestamp != "2026-09-02T10:02:00Z" {
		t.Fatalf("timestamp = %q", got[0].Timestamp)
	}

	// The limit must keep the newest entries, not the oldest.
	got, err = l.PingCheckLogs(context.Background(), "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].StateChange != "link_toggle" {
		t.Fatalf("limited = %+v", got)
	}
}

type fakeDiag struct {
	runErr error
	status diagnostics.RunStatus
	report []byte
	repErr error
	runs   int
}

func (f *fakeDiag) Run(context.Context) error     { f.runs++; return f.runErr }
func (f *fakeDiag) Status() diagnostics.RunStatus { return f.status }
func (f *fakeDiag) Result() ([]byte, error)       { return f.report, f.repErr }

// TestLocal_DiagnosticsResultPutsFailuresFirst — агент читает начало
// списка, поэтому там должно быть худшее, а не то, что проверилось первым.
func TestLocal_DiagnosticsResultPutsFailuresFirst(t *testing.T) {
	report := `{"generatedAt":"2026-09-02T10:05:00Z","tests":[
		{"name":"Handshake","status":"warn","detail":"stale","tunnelId":"tn-2","tunnelName":"F"},
		{"name":"Interface","status":"pass","detail":"up"},
		{"name":"Kernel module","status":"fail","detail":"not loaded"},
		{"name":"Speed","status":"skip","detail":"iperf3 missing"},
		{"name":"Routes","status":"error","detail":"ip route failed"}
	]}`
	fd := &fakeDiag{status: diagnostics.RunStatus{Status: "done"}, report: []byte(report)}
	l := New(Config{Diagnostics: fd})

	got, err := l.DiagnosticsResult(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Passed != 1 || got.Failed != 2 || got.Warnings != 1 || got.Skipped != 1 {
		t.Fatalf("counts = %+v", got)
	}
	if len(got.Problems) != 3 {
		t.Fatalf("problems = %+v", got.Problems)
	}
	if got.Problems[0].Status == "warn" {
		t.Fatalf("a warning came before the failures: %+v", got.Problems)
	}
	if got.Problems[2].Status != "warn" {
		t.Fatalf("the warning must come last: %+v", got.Problems)
	}
	if got.GeneratedAt != "2026-09-02T10:05:00Z" || got.Status != "done" {
		t.Fatalf("result = %+v", got)
	}
}

// TestLocal_DiagnosticsResultWithoutAReportIsExplicit — пустой отчёт
// читался бы как «всё в порядке».
func TestLocal_DiagnosticsResultWithoutAReportIsExplicit(t *testing.T) {
	fd := &fakeDiag{repErr: errors.New("no report available")}
	l := New(Config{Diagnostics: fd})

	if _, err := l.DiagnosticsResult(context.Background()); err == nil {
		t.Fatal("with no report the call must fail loudly, not return an all-clear")
	}
}

// TestLocal_RunDiagnosticsAlreadyRunningIsNotAnError — но и «запустил» в
// этом случае говорить нельзя.
func TestLocal_RunDiagnosticsAlreadyRunning(t *testing.T) {
	fd := &fakeDiag{runErr: errors.New("diagnostics already running"), status: diagnostics.RunStatus{Status: "running"}}
	l := New(Config{Diagnostics: fd})

	got, err := l.RunDiagnostics(context.Background())
	if err != nil {
		t.Fatalf("a sweep already in progress is not a failure: %v", err)
	}
	if got.Started {
		t.Fatal("started must be false — this call started nothing")
	}
	if got.Status != "running" {
		t.Fatalf("status = %q", got.Status)
	}
}

func TestLocal_RunDiagnosticsStarts(t *testing.T) {
	fd := &fakeDiag{}
	l := New(Config{Diagnostics: fd})

	got, err := l.RunDiagnostics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Started || fd.runs != 1 {
		t.Fatalf("got %+v after %d runs", got, fd.runs)
	}
}

// fakeRouter implements the router surface MCP uses.
type fakeRouter struct {
	router.Service
	rules     []router.Rule
	outbounds []router.CompositeOutboundView
	staging   router.StagingStatus
	bulkIdx   []int
	bulkTag   string
	bulkErr   error
	applied   int
	discarded int
	applyErr  error
	applyRes  singboxorch.ValidationResult
}

func (f *fakeRouter) ListRules(context.Context) ([]router.Rule, error) { return f.rules, nil }
func (f *fakeRouter) ListCompositeOutbounds(context.Context) ([]router.CompositeOutboundView, error) {
	return f.outbounds, nil
}
func (f *fakeRouter) StagingStatus(context.Context) router.StagingStatus { return f.staging }
func (f *fakeRouter) BulkSetRuleOutbound(_ context.Context, idx []int, tag string) error {
	f.bulkIdx, f.bulkTag = idx, tag
	return f.bulkErr
}
func (f *fakeRouter) ApplyStaging(context.Context) (singboxorch.ValidationResult, error) {
	f.applied++
	return f.applyRes, f.applyErr
}
func (f *fakeRouter) DiscardStaging(context.Context) error { f.discarded++; return nil }

func routerHarness() *fakeRouter {
	yes := true
	return &fakeRouter{
		rules: []router.Rule{
			{DomainSuffix: []string{"youtube.com", "googlevideo.com"}, Action: "route", Outbound: "vless-nl"},
			{RuleSet: []string{"geosite-ru"}, Outbound: "direct"},
			{Protocol: "dns", Action: "hijack-dns", IPIsPrivate: &yes, AwgmManaged: "selective-ip"},
		},
		outbounds: []router.CompositeOutboundView{
			{Outbound: router.Outbound{Tag: "auto", Type: "urltest"}, Source: "user"},
		},
	}
}

// TestLocal_ListSingboxRulesRendersAMatchSummary — модель выбирает
// правило по смыслу («правило про ютуб»), а не по сырым полям матчера.
func TestLocal_ListSingboxRulesRendersAMatchSummary(t *testing.T) {
	fr := routerHarness()
	l := New(Config{Router: fr})

	got, hasDraft, err := l.ListSingboxRules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if hasDraft {
		t.Error("no draft was staged")
	}
	if len(got) != 3 {
		t.Fatalf("rules = %d", len(got))
	}
	if got[0].Index != 0 || got[1].Index != 1 {
		t.Fatalf("indices must be the positions: %+v", got)
	}
	if !strings.Contains(got[0].Match, "youtube.com") {
		t.Fatalf("match = %q", got[0].Match)
	}
	if !strings.Contains(got[1].Match, "geosite-ru") {
		t.Fatalf("a rule-set rule must name the set: %q", got[1].Match)
	}
	// An empty action is "route" in sing-box; reporting it blank would
	// read as "this rule does nothing".
	if got[1].Action != "route" {
		t.Fatalf("action = %q, want route for an omitted action", got[1].Action)
	}
	if !got[2].Managed {
		t.Error("a rule carrying awgm_managed must be marked")
	}
	if got[0].Managed {
		t.Error("a user rule must not be marked managed")
	}
}

// TestLocal_ListSingboxRulesReportsTheDraft — правила читаются из
// черновика, и агент, принявший их за действующие, отчитается «сделано»
// там, где ничего не применено.
func TestLocal_ListSingboxRulesReportsTheDraft(t *testing.T) {
	fr := routerHarness()
	fr.staging = router.StagingStatus{HasDraft: true, DraftedAt: time.Date(2026, 9, 2, 10, 4, 0, 0, time.UTC)}
	l := New(Config{Router: fr})

	_, hasDraft, err := l.ListSingboxRules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !hasDraft {
		t.Fatal("with a draft staged the caller must be told the rules are not live")
	}
}

func TestLocal_SingboxStaging(t *testing.T) {
	ctx := context.Background()
	fr := routerHarness()
	l := New(Config{Router: fr})

	got, err := l.SingboxStaging(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.HasDraft {
		t.Fatalf("got %+v", got)
	}

	fr.staging = router.StagingStatus{HasDraft: true, DraftedAt: time.Date(2026, 9, 2, 10, 4, 0, 0, time.UTC)}
	got, err = l.SingboxStaging(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !got.HasDraft || got.DraftedAt != "2026-09-02T10:04:00Z" {
		t.Fatalf("got %+v", got)
	}
}

// TestLocal_SetSingboxRuleOutboundRefusesManagedRules — правило,
// созданное демоном, будет переписано ближайшим reconcile: правка
// «применится» и молча исчезнет.
func TestLocal_SetSingboxRuleOutboundRefusesManagedRules(t *testing.T) {
	ctx := context.Background()
	fr := routerHarness()
	l := New(Config{Router: fr})

	if err := l.SetSingboxRuleOutbound(ctx, 2, "auto"); err == nil {
		t.Fatal("editing a managed rule must be refused")
	}
	if fr.bulkTag != "" {
		t.Fatalf("the refused edit still reached the service: %v %q", fr.bulkIdx, fr.bulkTag)
	}

	if err := l.SetSingboxRuleOutbound(ctx, 5, "auto"); err == nil {
		t.Error("an out-of-range index must be refused")
	}

	if err := l.SetSingboxRuleOutbound(ctx, 0, "auto"); err != nil {
		t.Fatal(err)
	}
	if len(fr.bulkIdx) != 1 || fr.bulkIdx[0] != 0 || fr.bulkTag != "auto" {
		t.Fatalf("service got %v %q", fr.bulkIdx, fr.bulkTag)
	}
}

// TestLocal_ApplySingboxStagingSurfacesValidationFailure — sing-box может
// отвергнуть черновик. Сказать «применено» в этом случае значит соврать о
// состоянии роутера.
func TestLocal_ApplySingboxStagingSurfacesValidationFailure(t *testing.T) {
	ctx := context.Background()
	fr := routerHarness()
	fr.applyErr = errors.New("sing-box check failed: unknown outbound")
	l := New(Config{Router: fr})

	if err := l.ApplySingboxStaging(ctx); err == nil {
		t.Fatal("a rejected draft must be an error")
	}

	// A draft can also come back without an error but with blocking
	// validation errors. Reporting that as applied is the same lie.
	fr.applyErr = nil
	fr.applyRes = singboxorch.ValidationResult{Errors: []singboxorch.ValidationError{
		{Kind: "unknown-outbound", Tag: "gone", Message: "outbound not found"},
	}}
	if err := l.ApplySingboxStaging(ctx); err == nil {
		t.Fatal("a draft that fails validation must be an error, not a silent success")
	}

	// An advisory warning does not block a reload, so it must not block
	// this call either.
	fr.applyRes = singboxorch.ValidationResult{Errors: []singboxorch.ValidationError{
		{Kind: "dns-final-conflict", Severity: singboxorch.SeverityWarning, Message: "advisory"},
	}}
	if err := l.ApplySingboxStaging(ctx); err != nil {
		t.Fatalf("an advisory warning must not block the apply: %v", err)
	}
	if fr.applied != 3 {
		t.Fatalf("applied %d times", fr.applied)
	}
}

func TestLocal_DiscardSingboxStaging(t *testing.T) {
	fr := routerHarness()
	l := New(Config{Router: fr})
	if err := l.DiscardSingboxStaging(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fr.discarded != 1 {
		t.Fatalf("discarded %d times", fr.discarded)
	}
}

func TestLocal_RouterToolsWithoutTheServiceSaySo(t *testing.T) {
	ctx := context.Background()
	l := New(Config{})
	if _, _, err := l.ListSingboxRules(ctx); err == nil {
		t.Error("listing rules must report the missing service")
	}
	if _, err := l.ListSingboxOutbounds(ctx); err == nil {
		t.Error("listing outbounds must report the missing service")
	}
	if _, err := l.SingboxStaging(ctx); err == nil {
		t.Error("staging status must report the missing service")
	}
	if err := l.SetSingboxRuleOutbound(ctx, 0, "auto"); err == nil {
		t.Error("the edit must report the missing service")
	}
	if err := l.ApplySingboxStaging(ctx); err == nil {
		t.Error("apply must report the missing service")
	}
	if err := l.DiscardSingboxStaging(ctx); err == nil {
		t.Error("discard must report the missing service")
	}
}

// TestLocal_ListManagedServersUnitesBothIdSpaces — список NDMS-серверов
// (api.ServersHandler.ListServers) намеренно исключает серверы,
// заведённые awg-manager, а инструменты пиров знают только их. Без
// объединения агенту показывали id, которые пиры не принимали, и прятали
// те, которые принимали.
func TestLocal_ListManagedServersUnitesBothIdSpaces(t *testing.T) {
	m := managedHarness()
	l := New(Config{
		Managed: m,
		ListServers: func(context.Context) ([]ndms.WireguardServer, error) {
			return []ndms.WireguardServer{{ID: "Wireguard0", InterfaceName: "nwg0", Description: "Built-in", Status: "up", ListenPort: 51820}}, nil
		},
	})

	got, err := l.ListManagedServers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("servers = %+v, want the NDMS one and the managed one", got)
	}
	byID := map[string]mcpsrv.ManagedServer{}
	for _, s := range got {
		byID[s.ID] = s
	}
	builtIn, ok := byID["Wireguard0"]
	if !ok || builtIn.Managed || builtIn.Status != "up" {
		t.Fatalf("built-in = %+v", builtIn)
	}
	managed, ok := byID["Wireguard3"]
	if !ok || !managed.Managed {
		t.Fatalf("managed = %+v, want the awg-manager server flagged", managed)
	}
	if managed.PeerCount != 2 || managed.Description != "Home" {
		t.Fatalf("managed = %+v", managed)
	}
}

// TestLocal_PeerToolsRefuseAnUnmanagedServerWithTheReason — «не найден»
// отправил бы агента искать опечатку в id, который он только что получил
// из list_managed_servers.
func TestLocal_PeerToolsRefuseAnUnmanagedServerWithTheReason(t *testing.T) {
	m := managedHarness()
	l := New(Config{
		Managed: m,
		ListServers: func(context.Context) ([]ndms.WireguardServer, error) {
			return []ndms.WireguardServer{{ID: "Wireguard0", InterfaceName: "nwg0"}}, nil
		},
	})
	_, err := l.ListServerPeers(context.Background(), "Wireguard0")
	if err == nil {
		t.Fatal("an unmanaged server must be refused")
	}
	if !strings.Contains(err.Error(), "not managed") {
		t.Fatalf("error = %q, want the reason", err)
	}
}

// TestLocal_DNSRouteReadsResolveHydraRouteIDs — ревью нашло: списки
// HydraRoute приходят из List с id «hr:…», а Get их не знает. Получить,
// переключить или поправить такой список через MCP было нельзя, а
// explain_route помечал каждый как «не удалось прочитать».
func TestLocal_DNSRouteReadsResolveHydraRouteIDs(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.dns.lists = []dnsroute.DomainList{
		{ID: "dl-1", Name: "Video", Enabled: true, Domains: []string{"youtube.com"}},
		{ID: "hr:youtube", Name: "YouTube (HR)", Enabled: true, Backend: "hydraroute", Domains: []string{"youtube.com"}, Routes: []dnsroute.RouteTarget{{TunnelID: "tn-1"}}},
	}
	h.dns.getKnowsHR = false

	got, err := h.l.GetDNSRoute(ctx, "hr:youtube")
	if err != nil {
		t.Fatalf("GetDNSRoute(hr:…) = %v; the list is in List and must be readable", err)
	}
	if got.Name != "YouTube (HR)" || got.Backend != "hydraroute" {
		t.Fatalf("got %+v", got)
	}

	if _, err := h.l.SetDNSRouteEnabled(ctx, "hr:youtube", false); err != nil {
		t.Fatalf("SetDNSRouteEnabled(hr:…) = %v", err)
	}
	if v, ok := h.dns.enabled["hr:youtube"]; !ok || v {
		t.Fatalf("SetEnabled was not called for the HR list: %v %v", v, ok)
	}

	if _, _, err := h.l.UpdateDNSRoute(ctx, mcpsrv.DNSRouteUpdate{RouteID: "hr:youtube", Name: "YT"}); err != nil {
		t.Fatalf("UpdateDNSRoute(hr:…) = %v", err)
	}
	if h.dns.updated.ID != "hr:youtube" {
		t.Fatalf("update went to %q", h.dns.updated.ID)
	}

	if _, err := h.l.GetDNSRoute(ctx, "nope"); err == nil {
		t.Error("an unknown id must still be an error")
	}
}

// TestLocal_ListConnectionsFiltersByExactSourceIP — ревью нашло: clientIp
// уходил в службу как подстрочный поиск по «src dst clientName». Фильтр
// «только потоки этого устройства» захватывал соседей по подсети,
// потоки К этому адресу и совпадения по имени клиента, и общее число
// считалось по той же рыхлой выборке.
func TestLocal_ListConnectionsFiltersByExactSourceIP(t *testing.T) {
	fc := &fakeConns{resp: &connections.ListResponse{Connections: []connections.Connection{
		{Protocol: "tcp", Src: "192.168.1.1", SrcPort: 1, Dst: "1.1.1.1", DstPort: 443, ClientName: "router"},
		{Protocol: "tcp", Src: "192.168.1.10", SrcPort: 2, Dst: "1.1.1.1", DstPort: 443, ClientName: "laptop"},
		{Protocol: "tcp", Src: "192.168.1.100", SrcPort: 3, Dst: "1.1.1.1", DstPort: 443, ClientName: "tv"},
		{Protocol: "udp", Src: "192.168.1.20", SrcPort: 4, Dst: "192.168.1.1", DstPort: 53, ClientName: "phone"},
		{Protocol: "tcp", Src: "10.0.0.5", SrcPort: 5, Dst: "1.1.1.1", DstPort: 443, ClientName: "host-192.168.1.1-x"},
	}}}
	l := New(Config{Connections: fc})

	got, total, err := l.ListConnections(context.Background(), mcpsrv.ConnectionsQuery{ClientIP: "192.168.1.1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Src != "192.168.1.1" {
		t.Fatalf("got %+v, want only the flow FROM 192.168.1.1", got)
	}
	if total != 1 {
		t.Fatalf("total = %d, want it to count the exact matches, not the substring ones", total)
	}
}

// TestLocal_DiagnosticsResultWhileRunningSaysSo — ревью нашло: Run
// обнуляет прошлый отчёт на старте, и на протяжении всего прогона
// get_diagnostics отвечал «отчёта нет, вызовите run_diagnostics», а тот —
// «уже идёт, вызовите get_diagnostics». Агент ходил по кругу или решал,
// что диагностика не запускалась.
func TestLocal_DiagnosticsResultWhileRunningSaysSo(t *testing.T) {
	fd := &fakeDiag{status: diagnostics.RunStatus{Status: "running", Progress: "checking tunnels"}, repErr: errors.New("no report available")}
	l := New(Config{Diagnostics: fd})

	got, err := l.DiagnosticsResult(context.Background())
	if err != nil {
		t.Fatalf("a sweep in progress is a state, not a missing report: %v", err)
	}
	if got.Status != "running" {
		t.Fatalf("status = %q, want running", got.Status)
	}
	if got.Problems == nil {
		t.Fatal("problems must be an empty list, not null, so the shape stays stable")
	}
}

// TestLocal_CheckSingboxDelayReportsABusyProbeAsBusy — ревью нашло: MCP
// делит проверяющий задержку с периодическим обходом, и пока тот держит
// тег занятым, ответ 0 читался как «не ответил». Живой прокси
// объявлялся упавшим. Занятость — отдельное состояние, а не молчание.
func TestLocal_CheckSingboxDelayReportsABusyProbeAsBusy(t *testing.T) {
	op := singboxHarness()
	op.busy = map[string]bool{"vless-nl": true}
	l := New(Config{Singbox: op})

	got, err := l.CheckSingboxDelay(context.Background(), "vless-nl")
	if err != nil {
		t.Fatalf("a busy probe is a result to retry, not an error: %v", err)
	}
	if !got.Busy {
		t.Fatalf("got %+v, want busy=true", got)
	}
	if got.Reachable {
		t.Fatalf("got %+v: reachable carries no information while busy and must not claim the proxy answered", got)
	}
}

// TestLocal_ListDNSRouteDetailsIsOneListCall — для explain_route: все
// списки целиком за один List, без Get на каждый.
func TestLocal_ListDNSRouteDetailsIsOneListCall(t *testing.T) {
	h := newHarness(t)
	h.dns.lists = []dnsroute.DomainList{
		{ID: "dl-1", Name: "A", Domains: []string{"a.example"}},
		{ID: "hr:b", Name: "B", Backend: "hydraroute", Domains: []string{"b.example"}},
	}
	got, err := h.l.ListDNSRouteDetails(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].ID != "hr:b" || got[1].Domains[0] != "b.example" {
		t.Fatalf("got %+v", got)
	}
}

// TestLocal_DNSRouteDetailRedactsSubscriptionSecrets — URL подписки часто
// несёт токен в query или userinfo. Ключ только для чтения не должен
// уносить его вместе со списком.
func TestLocal_DNSRouteDetailRedactsSubscriptionSecrets(t *testing.T) {
	h := newHarness(t)
	h.dns.lists = []dnsroute.DomainList{{
		ID: "dl-1", Name: "A",
		Subscriptions: []dnsroute.Subscription{
			{URL: "https://user:s3cret@lists.example/a.txt?token=abc123&x=1", Name: "A"},
			{URL: "https://lists.example/plain.txt", Name: "B"},
		},
	}}
	got, err := h.l.GetDNSRoute(context.Background(), "dl-1")
	if err != nil {
		t.Fatal(err)
	}
	if u := got.Subscriptions[0].URL; strings.Contains(u, "s3cret") || strings.Contains(u, "abc123") {
		t.Fatalf("subscription URL leaked credentials: %q", u)
	}
	if u := got.Subscriptions[0].URL; !strings.HasPrefix(u, "https://lists.example/a.txt") {
		t.Fatalf("the host and path must survive redaction: %q", u)
	}
	if got.Subscriptions[1].URL != "https://lists.example/plain.txt" {
		t.Fatalf("a plain URL must pass unchanged: %q", got.Subscriptions[1].URL)
	}
}
