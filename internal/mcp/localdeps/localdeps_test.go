package localdeps

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/clientroute"
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
