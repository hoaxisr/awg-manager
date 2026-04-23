package query

import (
	"context"
	"errors"
	"testing"
	"time"
)

const routesPath = "/show/ip/route"

const sampleRoutesJSON = `[
	{"destination": "0.0.0.0/0", "gateway": "1.2.3.4", "interface": "PPPoE0"},
	{"destination": "10.0.0.0/24", "gateway": "", "interface": "Wireguard0"}
]`

func TestRouteStore_List_ParsesAndCaches(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(routesPath, sampleRoutesJSON)
	s := NewRouteStore(fg, NopLogger())

	got, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len: want 2, got %d", len(got))
	}
	if got[0].Destination != "0.0.0.0/0" || got[0].Gateway != "1.2.3.4" || got[0].Interface != "PPPoE0" {
		t.Errorf("got[0]: %#v", got[0])
	}
	_, _ = s.List(context.Background())
	if fg.Calls(routesPath) != 1 {
		t.Errorf("calls: want 1, got %d", fg.Calls(routesPath))
	}
}

func TestRouteStore_List_ServesStaleOnError(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(routesPath, sampleRoutesJSON)
	s := NewRouteStoreWithTTL(fg, NopLogger(), 20*time.Millisecond)
	_, _ = s.List(context.Background())
	time.Sleep(30 * time.Millisecond)
	fg.SetError(routesPath, errors.New("boom"))
	got, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("stale-ok: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("len: %d", len(got))
	}
}

func TestRouteStore_GetDefaultGatewayInterface(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(routesPath, sampleRoutesJSON)
	s := NewRouteStore(fg, NopLogger())

	got, err := s.GetDefaultGatewayInterface(context.Background())
	if err != nil {
		t.Fatalf("GetDefaultGatewayInterface: %v", err)
	}
	if got != "PPPoE0" {
		t.Errorf("want PPPoE0, got %q", got)
	}
}

func TestRouteStore_GetDefaultGatewayInterface_NoneFound(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(routesPath, `[{"destination":"10.0.0.0/24","gateway":"","interface":"br0"}]`)
	s := NewRouteStore(fg, NopLogger())

	_, err := s.GetDefaultGatewayInterface(context.Background())
	if !errors.Is(err, ErrNoDefaultRoute) {
		t.Errorf("want ErrNoDefaultRoute, got %v", err)
	}
}

func TestRouteStore_GetDefaultGatewayInterface_DefaultLiteral(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(routesPath, `[{"destination":"default","gateway":"1.2.3.4","interface":"eth0"}]`)
	s := NewRouteStore(fg, NopLogger())

	got, err := s.GetDefaultGatewayInterface(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "eth0" {
		t.Errorf("want eth0, got %q", got)
	}
}

func TestRouteStore_InvalidateAllForcesRefetch(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(routesPath, sampleRoutesJSON)
	s := NewRouteStore(fg, NopLogger())
	_, _ = s.List(context.Background())
	s.InvalidateAll()
	_, _ = s.List(context.Background())
	if fg.Calls(routesPath) != 2 {
		t.Errorf("calls: want 2, got %d", fg.Calls(routesPath))
	}
}
