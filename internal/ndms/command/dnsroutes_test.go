package command

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

func newTestDNSRouteCommands(_ *testing.T, isOS5 bool) (*DNSRouteCommands, *fakePoster) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 500*time.Millisecond, 5*time.Second)
	q := query.NewQueries(query.Deps{
		Getter: query.NewFakeGetter(),
		Logger: query.NopLogger(),
		IsOS5:  func() bool { return isOS5 },
	})
	return NewDNSRouteCommands(poster, sc, q, func() bool { return isOS5 }), poster
}

func TestDNSRouteCommands_UpsertRoutes_OS5(t *testing.T) {
	cmds, poster := newTestDNSRouteCommands(t, true)
	err := cmds.UpsertRoutes(context.Background(), []DNSRouteSpec{
		{Group: "g1", Interface: "Wireguard0", Reject: false},
		{Group: "g2", Interface: "Wireguard1", Reject: true},
	})
	if err != nil {
		t.Fatalf("UpsertRoutes: %v", err)
	}
	p := poster.Payloads()[0].(map[string]any)
	routes := p["dns-proxy"].(map[string]any)["route"].([]any)
	if len(routes) != 2 {
		t.Fatalf("routes len: %d", len(routes))
	}
	r2 := routes[1].(map[string]any)
	if r2["reject"] != true || r2["auto"] != true || r2["group"] != "g2" {
		t.Errorf("route[1]: %#v", r2)
	}
}

func TestDNSRouteCommands_DeleteRoutes_OS5(t *testing.T) {
	cmds, poster := newTestDNSRouteCommands(t, true)
	_ = cmds.DeleteRoutes(context.Background(), []DNSRouteSpec{
		{Group: "g1", Interface: "Wireguard0"},
	})
	r := poster.Payloads()[0].(map[string]any)["dns-proxy"].(map[string]any)["route"].([]any)[0].(map[string]any)
	if r["no"] != true {
		t.Errorf("delete: %#v", r)
	}
}

func TestDNSRouteCommands_OS4_ReturnsErrNotSupported(t *testing.T) {
	cmds, poster := newTestDNSRouteCommands(t, false)
	err := cmds.UpsertRoutes(context.Background(), []DNSRouteSpec{{Group: "g1", Interface: "w0"}})
	if !errors.Is(err, query.ErrNotSupportedOnOS4) {
		t.Errorf("err: want ErrNotSupportedOnOS4, got %v", err)
	}
	if poster.Calls() != 0 {
		t.Errorf("no POST must occur on OS4, got %d", poster.Calls())
	}

	err = cmds.DeleteRoutes(context.Background(), []DNSRouteSpec{{Group: "g1", Interface: "w0"}})
	if !errors.Is(err, query.ErrNotSupportedOnOS4) {
		t.Errorf("Delete err: %v", err)
	}
}

func TestDNSRouteCommands_EmptyBatch_NoOp(t *testing.T) {
	cmds, poster := newTestDNSRouteCommands(t, true)
	if err := cmds.UpsertRoutes(context.Background(), nil); err != nil {
		t.Errorf("empty upsert: %v", err)
	}
	if err := cmds.DeleteRoutes(context.Background(), nil); err != nil {
		t.Errorf("empty delete: %v", err)
	}
	if poster.Calls() != 0 {
		t.Errorf("empty batches must not POST, got %d", poster.Calls())
	}
}
