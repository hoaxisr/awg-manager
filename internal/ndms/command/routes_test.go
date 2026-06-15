package command

import (
	"context"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

func newTestRouteCommands(_ *testing.T) (*RouteCommands, *fakePoster) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 500*time.Millisecond, 5*time.Second, 0, nil)
	q := query.NewQueries(query.Deps{Getter: query.NewFakeGetter(), Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	return NewRouteCommands(poster, sc, q), poster
}

func TestRouteCommands_SetDefaultRoute(t *testing.T) {
	cmds, poster := newTestRouteCommands(t)
	_ = cmds.SetDefaultRoute(context.Background(), "PPPoE0")
	r := poster.Payloads()[0].(map[string]any)["ip"].(map[string]any)["route"].(map[string]any)
	if r["default"] != true || r["interface"] != "PPPoE0" {
		t.Errorf("set default: %#v", r)
	}
	if _, ok := r["no"]; ok {
		t.Errorf("no must be absent on set")
	}
}

func TestRouteCommands_RemoveDefaultRoute(t *testing.T) {
	cmds, poster := newTestRouteCommands(t)
	_ = cmds.RemoveDefaultRoute(context.Background(), "PPPoE0")
	r := poster.Payloads()[0].(map[string]any)["ip"].(map[string]any)["route"].(map[string]any)
	if r["no"] != true {
		t.Errorf("remove default: %#v", r)
	}
}

func TestRouteCommands_SetIPv6DefaultRoute(t *testing.T) {
	cmds, poster := newTestRouteCommands(t)
	_ = cmds.SetIPv6DefaultRoute(context.Background(), "PPPoE0")
	p := poster.Payloads()[0].(map[string]any)
	if _, ok := p["ipv6"]; !ok {
		t.Errorf("ipv6 key missing: %#v", p)
	}
}

func TestRouteCommands_RemoveIPv6DefaultRoute(t *testing.T) {
	cmds, poster := newTestRouteCommands(t)
	_ = cmds.RemoveIPv6DefaultRoute(context.Background(), "PPPoE0")
	r := poster.Payloads()[0].(map[string]any)["ipv6"].(map[string]any)["route"].(map[string]any)
	if r["no"] != true {
		t.Errorf("remove ipv6 default: %#v", r)
	}
}

func TestRouteCommands_RemoveHostRoute(t *testing.T) {
	cmds, poster := newTestRouteCommands(t)
	_ = cmds.RemoveHostRoute(context.Background(), "1.2.3.4")
	r := poster.Payloads()[0].(map[string]any)["ip"].(map[string]any)["route"].(map[string]any)
	if r["host"] != "1.2.3.4" || r["no"] != true {
		t.Errorf("remove host: %#v", r)
	}
}

func TestRouteCommands_AddStaticRoute_Network(t *testing.T) {
	cmds, poster := newTestRouteCommands(t)
	_ = cmds.AddStaticRoute(context.Background(), StaticRouteSpec{
		Interface: "Wireguard0",
		Network:   "10.0.0.0",
		Mask:      "255.255.255.0",
		Reject:    true,
		Comment:   "test route",
	})
	r := poster.Payloads()[0].(map[string]any)["ip"].(map[string]any)["route"].(map[string]any)
	if r["network"] != "10.0.0.0" || r["mask"] != "255.255.255.0" {
		t.Errorf("network/mask: %#v", r)
	}
	if r["auto"] != true || r["reject"] != true {
		t.Errorf("flags: %#v", r)
	}
	if r["comment"] != "test route" {
		t.Errorf("comment: %#v", r)
	}
	if _, ok := r["host"]; ok {
		t.Errorf("host must be absent for network route")
	}
}

func TestRouteCommands_AddStaticRoute_Host(t *testing.T) {
	cmds, poster := newTestRouteCommands(t)
	_ = cmds.AddStaticRoute(context.Background(), StaticRouteSpec{
		Interface: "Wireguard0",
		Host:      "8.8.8.8",
	})
	r := poster.Payloads()[0].(map[string]any)["ip"].(map[string]any)["route"].(map[string]any)
	if r["host"] != "8.8.8.8" {
		t.Errorf("host: %#v", r)
	}
	if _, ok := r["network"]; ok {
		t.Errorf("network must be absent for host route")
	}
}

func TestRouteCommands_RemoveStaticRoute(t *testing.T) {
	cmds, poster := newTestRouteCommands(t)
	_ = cmds.RemoveStaticRoute(context.Background(), StaticRouteSpec{
		Interface: "Wireguard0",
		Network:   "10.0.0.0",
		Mask:      "255.255.255.0",
	})
	r := poster.Payloads()[0].(map[string]any)["ip"].(map[string]any)["route"].(map[string]any)
	if r["no"] != true || r["network"] != "10.0.0.0" {
		t.Errorf("remove static: %#v", r)
	}
}

// TestRouteCommands_RemoveStaticRoute_RejectPayload documents the CURRENT
// behavior of RemoveStaticRoute when given a Reject:true spec (the fakeip drain
// removal): RemoveStaticRoute drops the `reject` key entirely and emits only
// {network, mask, no:true}. This is the payload the fakeip drain sends to take
// down the temporary reject route.
//
// STAND-GATE (Task 1F.1): whether this no:true / reject-key-less form actually
// MATCHES a reject:true route on live Keenetic RCI is UNVERIFIED and MUST be
// checked at the stand. If it does NOT match, the startup sweep in
// ReapOrphanedFakeIPTun (router Fix 1) is the safety net that removes the stale
// reject route on the next boot. This test pins current behavior only — do NOT
// change RemoveStaticRoute's payload speculatively; that is a stand decision.
func TestRouteCommands_RemoveStaticRoute_RejectPayload(t *testing.T) {
	cmds, poster := newTestRouteCommands(t)
	_ = cmds.RemoveStaticRoute(context.Background(), StaticRouteSpec{
		Network: "10.128.0.0",
		Mask:    "255.192.0.0",
		Reject:  true,
	})
	r := poster.Payloads()[0].(map[string]any)["ip"].(map[string]any)["route"].(map[string]any)
	if r["network"] != "10.128.0.0" || r["mask"] != "255.192.0.0" {
		t.Errorf("network/mask: %#v", r)
	}
	if r["no"] != true {
		t.Errorf("no must be true on remove: %#v", r)
	}
	// Current behavior: the reject key is DROPPED on remove (UNVERIFIED match —
	// see the stand-gate note above).
	if _, ok := r["reject"]; ok {
		t.Errorf("reject key is currently expected to be ABSENT on remove (documents current behavior): %#v", r)
	}
}

func TestRouteCommands_AddStaticRoute6(t *testing.T) {
	cmds, poster := newTestRouteCommands(t)
	if err := cmds.AddStaticRoute6(context.Background(), "3f80::/10", "OpkgTun10"); err != nil {
		t.Fatalf("add6: %v", err)
	}
	r := poster.Payloads()[0].(map[string]any)["ipv6"].(map[string]any)["route"].(map[string]any)
	if r["network"] != "3f80::/10" || r["interface"] != "OpkgTun10" || r["auto"] != true {
		t.Errorf("ipv6 route: %#v", r)
	}
}

func TestRouteCommands_RemoveStaticRoute6(t *testing.T) {
	cmds, poster := newTestRouteCommands(t)
	if err := cmds.RemoveStaticRoute6(context.Background(), "3f80::/10", "OpkgTun10"); err != nil {
		t.Fatalf("rm6: %v", err)
	}
	r := poster.Payloads()[0].(map[string]any)["ipv6"].(map[string]any)["route"].(map[string]any)
	if r["network"] != "3f80::/10" || r["no"] != true {
		t.Errorf("ipv6 route remove: %#v", r)
	}
}
