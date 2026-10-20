package command

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

func newTestDNSRouteCommands(_ *testing.T, isOS5 bool) (*DNSRouteCommands, *fakePoster) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 500*time.Millisecond, 5*time.Second, 0, nil)
	sc.SetSaveTimings(SaveEventCap, 0, SaveAfterRemoval)
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
		[]DNSRouteRef{
			{Group: "g1", Interface: "Wireguard1"},
			{Group: "g1", Interface: "Wireguard0"},
		},
		[]DNSRouteSpec{
			{Group: "g1", Interface: confirmed(t, "Wireguard0"), Reject: false},
			{Group: "g1", Interface: confirmed(t, "Wireguard1"), Reject: true},
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
	_ = cmds.DeleteRoutes(context.Background(), []DNSRouteRef{
		{Group: "g1", Interface: "Wireguard0"},
	})
	r := poster.Payloads()[0].(map[string]any)["dns-proxy"].(map[string]any)["route"].([]any)[0].(map[string]any)
	if r["no"] != true {
		t.Errorf("delete: %#v", r)
	}
}

func TestDNSRouteCommands_OS4_ReturnsErrNotSupported(t *testing.T) {
	cmds, poster := newTestDNSRouteCommands(t, false)
	err := cmds.ReplaceRoutes(context.Background(), nil, []DNSRouteSpec{{Group: "g1", Interface: confirmed(t, "w0")}})
	if !errors.Is(err, query.ErrNotSupportedOnOS4) {
		t.Errorf("err: want ErrNotSupportedOnOS4, got %v", err)
	}
	if poster.Calls() != 0 {
		t.Errorf("no POST must occur on OS4, got %d", poster.Calls())
	}

	err = cmds.DeleteRoutes(context.Background(), []DNSRouteRef{{Group: "g1", Interface: "w0"}})
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

// Снос — строкой из выдачи роутера, постановка — подтверждённым интерфейсом;
// обе половины одним POST.
func TestReplaceRoutes_DeleteByRefUpsertByConfirmed(t *testing.T) {
	cmds, f, q := newOracleCommands(t, nil, ndms.Interface{ID: "Wireguard0"}, ndms.Interface{ID: "Wireguard1"})
	c, _, ok, err := q.Interfaces.Confirm(context.Background(), "Wireguard0")
	if err != nil || !ok {
		t.Fatalf("confirm: ok=%v err=%v", ok, err)
	}
	err = cmds.DNSRoutes.ReplaceRoutes(context.Background(),
		[]DNSRouteRef{{Group: "g", Interface: "Wireguard1"}},
		[]DNSRouteSpec{{Group: "g", Interface: c}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"dns-proxy":{"route":[{"group":"g","interface":"Wireguard1","no":true},{"auto":true,"group":"g","interface":"Wireguard0"}]}}`
	if len(f.Posts) != 1 || f.Posts[0] != want {
		t.Fatalf("posts=%v", f.Posts)
	}
	if f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("E=%d phantoms=%d", f.E, f.Phantoms)
	}
}

// Нулевой Confirmed в upsert — отказ всего батча без POST (F546).
func TestDNSRouteCommands_ReplaceRoutes_ZeroInterface_Refused(t *testing.T) {
	cmds, poster := newTestDNSRouteCommands(t, true)
	err := cmds.ReplaceRoutes(context.Background(),
		[]DNSRouteRef{{Group: "g1", Interface: "Wireguard1"}},
		[]DNSRouteSpec{{Group: "g1", Interface: confirmed(t, "Wireguard0")}, {Group: "g2"}})
	if err == nil {
		t.Fatal("upsert без интерфейса обязан отказывать")
	}
	if poster.Calls() != 0 {
		t.Fatalf("в NDMS ушло %d POST", poster.Calls())
	}
}

// F568 по эпохам (Н10b): sc-вид чист, когда завершённое сохранение начато
// после нашей правки dns-proxy route; чужие ожидающие правки Flush не
// вызывают. Мутация: гейт по PendingCount → Flush при чистом sc-виде, красный.
func TestFlushPendingSave_ByEpoch(t *testing.T) {
	poster := &flightPoster{}
	sc := newFlightSC(t, poster)
	sc.SetSaveTimings(5*time.Second, 5*time.Second, 0)
	q := query.NewQueries(query.Deps{Getter: query.NewFakeGetter(), Logger: query.NopLogger(),
		IsOS5: func() bool { return true }})
	c := NewDNSRouteCommands(poster, sc, q, func() bool { return true })

	if err := c.DeleteRoutes(context.Background(), []DNSRouteRef{{Group: "g_p1", Interface: "OpkgTun0"}}); err != nil {
		t.Fatal(err)
	}
	poster.waitCalls(t, 2) // снос + сохранение
	waitFlight(t, sc)
	sc.OnConfigurationSaved(104)
	waitEpochSaved(t, sc, 1)

	sc.mu.Lock()
	sc.debounce = time.Hour // чужая правка ждёт своего сохранения
	sc.mu.Unlock()
	sc.Request()
	if err := c.FlushPendingSave(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(poster.calls()); n != 2 {
		t.Fatalf("POST %d: Flush при чистом sc-виде", n)
	}

	if err := c.DeleteRoutes(context.Background(), []DNSRouteRef{{Group: "g_p1", Interface: "OpkgTun1"}}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.FlushPendingSave(context.Background()) }()
	poster.waitCalls(t, 4) // снос + Flush
	waitFlight(t, sc)
	sc.OnConfigurationSaved(105)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func waitEpochSaved(t *testing.T, sc *SaveCoordinator, want uint64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, saved := sc.Epoch(); saved >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("saved < %d", want)
}

func dnsRoutePayload(t *testing.T, poster *fakePoster) []any {
	t.Helper()
	return poster.Payloads()[0].(map[string]any)["dns-proxy"].(map[string]any)["route"].([]any)
}

// Снос, правка по index и добавление — один POST в этом порядке: сносы
// первыми освобождают место, правка держит позицию строки (#1023).
func TestRewriteInPlace_OneBatchDeletesUpdatesAdds(t *testing.T) {
	cmds, poster := newTestDNSRouteCommands(t, true)
	err := cmds.RewriteInPlace(context.Background(),
		[]string{"i9"},
		[]DNSRouteUpdate{{Index: "i1", Spec: DNSRouteSpec{Group: "g1", Interface: confirmed(t, "Wireguard2")}}},
		[]DNSRouteSpec{{Group: "g1", Interface: confirmed(t, "Wireguard3"), Reject: true}})
	if err != nil {
		t.Fatalf("RewriteInPlace: %v", err)
	}
	if poster.Calls() != 1 {
		t.Fatalf("POST-ов: %d, want 1", poster.Calls())
	}
	route := dnsRoutePayload(t, poster)
	if len(route) != 3 {
		t.Fatalf("route len: %d, want 3", len(route))
	}
	del := route[0].(map[string]any)
	if del["index"] != "i9" || del["no"] != true || len(del) != 2 {
		t.Errorf("снос: %#v", del)
	}
	upd := route[1].(map[string]any)
	if upd["group"] != "g1" || upd["interface"] != "Wireguard2" || upd["auto"] != true || upd["index"] != "i1" {
		t.Errorf("правка: %#v", upd)
	}
	if _, has := upd["reject"]; has {
		t.Errorf("правка без reject несёт ключ reject: %#v", upd)
	}
	add := route[2].(map[string]any)
	if add["interface"] != "Wireguard3" || add["reject"] != true {
		t.Errorf("добавление: %#v", add)
	}
	if _, has := add["index"]; has {
		t.Errorf("добавление несёт index: %#v", add)
	}
}

func TestMoveRoutes_SequentialRules(t *testing.T) {
	cmds, poster := newTestDNSRouteCommands(t, true)
	err := cmds.MoveRoutes(context.Background(), []DNSRouteMove{{Index: "i3", Position: 4}, {Index: "i1", Position: 5}})
	if err != nil {
		t.Fatalf("MoveRoutes: %v", err)
	}
	if poster.Calls() != 1 {
		t.Fatalf("POST-ов: %d, want 1", poster.Calls())
	}
	route := dnsRoutePayload(t, poster)
	if len(route) != 2 {
		t.Fatalf("route len: %d", len(route))
	}
	for i, want := range []struct {
		idx string
		pos int
	}{{"i3", 4}, {"i1", 5}} {
		r := route[i].(map[string]any)["rule"].(map[string]any)
		if r["index"] != want.idx || r["new-index"] != want.pos || len(r) != 2 {
			t.Errorf("rule[%d] = %#v", i, r)
		}
	}
}

// Любой status:"error" — ошибка: «unable to find … with index» одинаков у
// сноса и правки, безобидный отказ не отличить.
func TestRewriteInPlace_RouterErrorSurfaces(t *testing.T) {
	cmds, poster := newTestDNSRouteCommands(t, true)
	poster.SetResponse(`{"dns-proxy":{"route":[{"status":[{"status":"error","code":"4457098","message":"unable to find the DNS route rule with index \"i1\"."}]}]}}`)
	err := cmds.RewriteInPlace(context.Background(), nil,
		[]DNSRouteUpdate{{Index: "i1", Spec: DNSRouteSpec{Group: "g1", Interface: confirmed(t, "Wireguard2")}}}, nil)
	if err == nil || !strings.Contains(err.Error(), `unable to find the DNS route rule with index "i1"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestRewriteInPlace_ZeroInterface_Refused(t *testing.T) {
	cmds, poster := newTestDNSRouteCommands(t, true)
	if err := cmds.RewriteInPlace(context.Background(), nil,
		[]DNSRouteUpdate{{Index: "i1", Spec: DNSRouteSpec{Group: "g1"}}}, nil); err == nil {
		t.Fatal("правка без интерфейса обязана отказывать")
	}
	if err := cmds.RewriteInPlace(context.Background(), []string{"i9"}, nil,
		[]DNSRouteSpec{{Group: "g1"}}); err == nil {
		t.Fatal("добавление без интерфейса обязано отказывать")
	}
	if poster.Calls() != 0 {
		t.Fatalf("в NDMS ушло %d POST", poster.Calls())
	}
}

func TestRewriteInPlaceAndMove_OS4_NotSupported(t *testing.T) {
	cmds, poster := newTestDNSRouteCommands(t, false)
	if err := cmds.RewriteInPlace(context.Background(), []string{"i1"}, nil, nil); !errors.Is(err, query.ErrNotSupportedOnOS4) {
		t.Errorf("RewriteInPlace err: %v", err)
	}
	if err := cmds.MoveRoutes(context.Background(), []DNSRouteMove{{Index: "i1", Position: 0}}); !errors.Is(err, query.ErrNotSupportedOnOS4) {
		t.Errorf("MoveRoutes err: %v", err)
	}
	if poster.Calls() != 0 {
		t.Errorf("POST на OS4: %d", poster.Calls())
	}
}

func TestRewriteInPlaceAndMove_Empty_NoOp(t *testing.T) {
	cmds, poster := newTestDNSRouteCommands(t, true)
	if err := cmds.RewriteInPlace(context.Background(), nil, nil, nil); err != nil {
		t.Errorf("RewriteInPlace: %v", err)
	}
	if err := cmds.MoveRoutes(context.Background(), nil); err != nil {
		t.Errorf("MoveRoutes: %v", err)
	}
	if poster.Calls() != 0 {
		t.Errorf("POST на пустом входе: %d", poster.Calls())
	}
}

// F692: пауза едет в строке записи — `disable:true` у записи ставит строку
// выключенной сразу (стенд 5.02.B.1). Без паузы ключа нет вовсе:
// `disable:false` не шлём.
func TestReplaceRoutes_DisabledSpecCarriesDisable(t *testing.T) {
	cmds, poster := newTestDNSRouteCommands(t, true)
	err := cmds.ReplaceRoutes(context.Background(), nil, []DNSRouteSpec{
		{Group: "g1", Interface: confirmed(t, "Wireguard0"), Disabled: true},
		{Group: "g1", Interface: confirmed(t, "Wireguard1")},
	})
	if err != nil {
		t.Fatalf("ReplaceRoutes: %v", err)
	}
	route := dnsRoutePayload(t, poster)
	if r := route[0].(map[string]any); r["disable"] != true {
		t.Errorf("запись на паузе без disable:true: %#v", r)
	}
	if r := route[1].(map[string]any); r["disable"] != nil {
		t.Errorf("запись без паузы несёт disable: %#v", r)
	}
}

// F692: добавление RewriteInPlace несёт disable так же; правка по index —
// никогда: disable правкой не трогается, его наводит planMoves.
func TestRewriteInPlace_DisableOnlyOnAdds(t *testing.T) {
	cmds, poster := newTestDNSRouteCommands(t, true)
	err := cmds.RewriteInPlace(context.Background(), nil,
		[]DNSRouteUpdate{{Index: "i1", Spec: DNSRouteSpec{Group: "g1", Interface: confirmed(t, "Wireguard2"), Disabled: true}}},
		[]DNSRouteSpec{
			{Group: "g1", Interface: confirmed(t, "Wireguard3"), Disabled: true},
			{Group: "g1", Interface: confirmed(t, "Wireguard1")},
		})
	if err != nil {
		t.Fatalf("RewriteInPlace: %v", err)
	}
	route := dnsRoutePayload(t, poster)
	if r := route[0].(map[string]any); r["disable"] != nil {
		t.Errorf("правка по index несёт disable: %#v", r)
	}
	if r := route[1].(map[string]any); r["disable"] != true {
		t.Errorf("добавление на паузе без disable:true: %#v", r)
	}
	if r := route[2].(map[string]any); r["disable"] != nil {
		t.Errorf("добавление без паузы несёт disable: %#v", r)
	}
}
