package localdeps

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/clientroute"
	"github.com/hoaxisr/awg-manager/internal/dnsroute"
	"github.com/hoaxisr/awg-manager/internal/events"
	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	"github.com/hoaxisr/awg-manager/internal/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
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

type fakeOrch struct{ events []orchestrator.Event }

func (f *fakeOrch) HandleEvent(_ context.Context, e orchestrator.Event) error {
	f.events = append(f.events, e)
	return nil
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
	enabled map[string]bool
	deleted []string
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

func (f *fakeDNSRoutes) Get(_ context.Context, id string) (*dnsroute.DomainList, error) {
	for i := range f.lists {
		if f.lists[i].ID == id {
			return &f.lists[i], nil
		}
	}
	return nil, errNotFound(id)
}

func (f *fakeDNSRoutes) SetEnabled(_ context.Context, id string, v bool) error {
	f.enabled[id] = v
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
	lists []storage.StaticRouteList
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

	// Старт/стоп/рестарт публикует сам оркестратор — второй раз отсюда
	// была бы лишняя инвалидация на каждое действие.
	t.Run("start stop restart stay silent", func(t *testing.T) {
		h := newHarness(t)
		for _, a := range []string{mcpsrv.ActionStart, mcpsrv.ActionStop, mcpsrv.ActionRestart} {
			if err := h.l.ControlTunnel(ctx, "tn-1", a); err != nil {
				t.Fatal(err)
			}
		}
		if len(h.bus.pub) != 0 {
			t.Fatalf("orchestrator already publishes these; localdeps published %v", h.bus.pub)
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

// TestLocal_AddDNSRouteInputMapping — dnsroute.Create жёстко ставит
// Enabled=true и пересчитывает Domains из ManualDomains, поэтому enabled:false
// надо доводить вторым вызовом, а Domains не передавать вовсе (иначе один и
// тот же слайс лежал бы в двух полях одной структуры).
func TestLocal_AddDNSRouteInputMapping(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	if _, err := h.l.AddDNSRoute(ctx, mcpsrv.DNSRouteInput{Name: "GH", Domains: []string{"github.com"}, TunnelID: "tn-1"}); err != nil {
		t.Fatal(err)
	}
	if h.dns.created.Domains != nil {
		t.Errorf("Domains must not be sent (it is derived from ManualDomains), got %v", h.dns.created.Domains)
	}
	if len(h.dns.created.ManualDomains) != 1 || h.dns.created.ManualDomains[0] != "github.com" {
		t.Errorf("ManualDomains = %v", h.dns.created.ManualDomains)
	}

	off := false
	got, err := h.l.AddDNSRoute(ctx, mcpsrv.DNSRouteInput{Name: "Off", Domains: []string{"example.com"}, TunnelID: "tn-1", Enabled: &off})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := h.dns.enabled["dl-new"]; !ok || v {
		t.Errorf("enabled:false must reach the service as a SetEnabled(false), got %v/%v", v, ok)
	}
	if got.Enabled {
		t.Error("the returned list must not claim to be enabled")
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
