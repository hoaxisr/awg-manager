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
	sc := NewSaveCoordinator(poster, pub, 500*time.Millisecond, 5*time.Second, 0, nil)
	q := query.NewQueries(query.Deps{
		Getter: query.NewFakeGetter(),
		Logger: query.NopLogger(),
		IsOS5:  func() bool { return isOS5 },
	})
	return NewDNSRouteCommands(poster, sc, q, func() bool { return isOS5 }), poster
}

// Снос и запись обязаны ехать одним POST и в одном порядке: NDMS применяет
// элементы payload подряд, и именно порядок записи становится приоритетом
// выбора туннеля. Два POST оставили бы группу без правил между ними (#801).
func TestDNSRouteCommands_ReplaceRoutes_OneBatchDeletesThenUpserts(t *testing.T) {
	cmds, poster := newTestDNSRouteCommands(t, true)
	err := cmds.ReplaceRoutes(context.Background(),
		[]DNSRouteSpec{
			{Group: "g1", Interface: "Wireguard1"},
			{Group: "g1", Interface: "Wireguard0"},
		},
		[]DNSRouteSpec{
			{Group: "g1", Interface: "Wireguard0", Reject: false},
			{Group: "g1", Interface: "Wireguard1", Reject: true},
		})
	if err != nil {
		t.Fatalf("ReplaceRoutes: %v", err)
	}
	if poster.Calls() != 1 {
		t.Fatalf("снос и запись обязаны ехать одним POST, POST-ов: %d", poster.Calls())
	}
	routes := poster.Payloads()[0].(map[string]any)["dns-proxy"].(map[string]any)["route"].([]any)
	if len(routes) != 4 {
		t.Fatalf("routes len: %d, want 4", len(routes))
	}
	for i, want := range []string{"Wireguard1", "Wireguard0"} {
		r := routes[i].(map[string]any)
		if r["no"] != true || r["interface"] != want {
			t.Errorf("снос[%d] = %#v, want no:true %s", i, r, want)
		}
	}
	for i, want := range []string{"Wireguard0", "Wireguard1"} {
		r := routes[2+i].(map[string]any)
		if r["auto"] != true || r["interface"] != want {
			t.Errorf("запись[%d] = %#v, want auto:true %s", i, r, want)
		}
		if _, isDelete := r["no"]; isDelete {
			t.Errorf("запись[%d] помечена как снос: %#v", i, r)
		}
	}
	if routes[3].(map[string]any)["reject"] != true {
		t.Errorf("reject потерян: %#v", routes[3])
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
	err := cmds.ReplaceRoutes(context.Background(), nil, []DNSRouteSpec{{Group: "g1", Interface: "w0"}})
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
	if err := cmds.ReplaceRoutes(context.Background(), nil, nil); err != nil {
		t.Errorf("empty replace: %v", err)
	}
	if err := cmds.DeleteRoutes(context.Background(), nil); err != nil {
		t.Errorf("empty delete: %v", err)
	}
	if poster.Calls() != 0 {
		t.Errorf("empty batches must not POST, got %d", poster.Calls())
	}
}
