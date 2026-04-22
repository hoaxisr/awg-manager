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

func TestDNSRouteCommands_SetDisabled_OS5(t *testing.T) {
	cmds, poster := newTestDNSRouteCommands(t, true)

	// Each SetDisabled POSTs the disable command AND flushes save
	// synchronously (no debounce), so payloads arrive as:
	//   [0] disable command
	//   [1] system-configuration-save
	//   [2] disable command (second call)
	//   [3] system-configuration-save (second call)

	// disabled=true → "no": false (apply the disable)
	if err := cmds.SetDisabled(context.Background(), "abc123", true); err != nil {
		t.Fatalf("SetDisabled true: %v", err)
	}
	d := poster.Payloads()[0].(map[string]any)["dns-proxy"].(map[string]any)["route"].(map[string]any)["disable"].(map[string]any)
	if d["index"] != "abc123" || d["no"] != false {
		t.Errorf("disable true payload: %#v", d)
	}
	// Flush should have sent save immediately, not via debounce.
	save := poster.Payloads()[1].(map[string]any)["system"].(map[string]any)["configuration"].(map[string]any)
	if _, ok := save["save"]; !ok {
		t.Errorf("save payload missing: %#v", poster.Payloads()[1])
	}

	// disabled=false → "no": true (negate the disable)
	if err := cmds.SetDisabled(context.Background(), "abc123", false); err != nil {
		t.Fatalf("SetDisabled false: %v", err)
	}
	d2 := poster.Payloads()[2].(map[string]any)["dns-proxy"].(map[string]any)["route"].(map[string]any)["disable"].(map[string]any)
	if d2["no"] != true {
		t.Errorf("disable false payload: %#v", d2)
	}
}

func TestDNSRouteCommands_SetDisabled_EmptyIndexNoOp(t *testing.T) {
	cmds, poster := newTestDNSRouteCommands(t, true)
	if err := cmds.SetDisabled(context.Background(), "", true); err != nil {
		t.Errorf("empty index: %v", err)
	}
	if poster.Calls() != 0 {
		t.Errorf("empty index must not POST, got %d", poster.Calls())
	}
}

func TestDNSRouteCommands_SetDisabled_OS4(t *testing.T) {
	cmds, _ := newTestDNSRouteCommands(t, false)
	if err := cmds.SetDisabled(context.Background(), "abc", true); !errors.Is(err, query.ErrNotSupportedOnOS4) {
		t.Errorf("OS4 err: %v", err)
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
