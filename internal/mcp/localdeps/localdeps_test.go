package localdeps

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/clientroute"
	"github.com/hoaxisr/awg-manager/internal/dnsroute"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	"github.com/hoaxisr/awg-manager/internal/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/pingcheck"
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

func (f *fakeDNSRoutes) List(context.Context) ([]dnsroute.DomainList, error) {
	return append([]dnsroute.DomainList(nil), f.lists...), nil
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
func (f *lifecycleTunnels) ReplaceConfig(_ context.Context, _, cfg, _ string) error {
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
