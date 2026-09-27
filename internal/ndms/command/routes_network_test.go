package command

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

const rcRoutesFixture = `[
 {"network":"192.168.77.0","mask":"255.255.255.0","interface":"Wireguard9","auto":true,"comment":"awgm-peer:5+0I/P0V"},
 {"network":"192.168.78.0","mask":"255.255.255.0","interface":"Wireguard9","auto":true,"comment":"manual"},
 {"network":"192.168.81.0","mask":"255.255.255.0","interface":"Wireguard9","auto":true,"comment":"awgm-peer:5+0I/P0Vx"},
 {"host":"192.168.79.5","interface":"Wireguard9","auto":true,"comment":"awgm-peer:5+0I/P0V"}]`

func newRouteCommandsWithRC(t *testing.T, rc string) (*RouteCommands, *fakePoster) {
	t.Helper()
	fg := query.NewFakeGetter()
	fg.SetJSON("/show/rc/ip/route", rc)
	poster := &fakePoster{}
	sc := NewSaveCoordinator(poster, &fakePublisher{}, time.Hour, time.Hour, 0, nil)
	q := query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger()})
	return NewRouteCommands(poster, sc, q), poster
}

func TestNetworkRouteOwner(t *testing.T) {
	cmds, _ := newRouteCommandsWithRC(t, rcRoutesFixture)
	ctx := context.Background()
	cases := []struct {
		name, network, mask, comment string
		exists, own                  bool
	}{
		{"ours", "192.168.77.0", "255.255.255.0", "awgm-peer:5+0I/P0V", true, true},
		{"ours, other label", "192.168.77.0", "255.255.255.0", "awgm-peer:XXXXXXXX", true, false},
		{"foreign", "192.168.78.0", "255.255.255.0", "awgm-peer:5+0I/P0V", true, false},
		// Метка сверяется целиком: чужая, начинающаяся с нашей, — не наша.
		{"prefix label", "192.168.81.0", "255.255.255.0", "awgm-peer:5+0I/P0V", true, false},
		{"absent", "192.168.80.0", "255.255.255.0", "awgm-peer:5+0I/P0V", false, false},
		// /32 хранится host-формой и обязан узнаваться как свой.
		{"host form", "192.168.79.5", "255.255.255.255", "awgm-peer:5+0I/P0V", true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			exists, own, err := cmds.NetworkRouteOwner(ctx, c.network, c.mask, "Wireguard9", c.comment)
			if err != nil || exists != c.exists || own != c.own {
				t.Fatalf("exists=%v own=%v err=%v", exists, own, err)
			}
		})
	}
	// Та же сеть на другом интерфейсе — другая запись: пары (сеть, интерфейс) нет.
	exists, own, err := cmds.NetworkRouteOwner(ctx, "192.168.77.0", "255.255.255.0", "Wireguard8", "awgm-peer:5+0I/P0V")
	if err != nil || exists || own {
		t.Fatalf("другой интерфейс: exists=%v own=%v err=%v", exists, own, err)
	}
}

func TestRemoveOwnNetworkRoute_OnlyOwn(t *testing.T) {
	cmds, poster := newRouteCommandsWithRC(t, rcRoutesFixture)
	ctx := context.Background()
	removed, err := cmds.RemoveOwnNetworkRoute(ctx, StaticRouteSpec{Network: "192.168.78.0", Mask: "255.255.255.0", Interface: "Wireguard9", Comment: "awgm-peer:5+0I/P0V"})
	if err != nil || removed || len(poster.Payloads()) != 0 {
		t.Fatalf("чужая запись: removed=%v err=%v posts=%d", removed, err, len(poster.Payloads()))
	}
	removed, err = cmds.RemoveOwnNetworkRoute(ctx, StaticRouteSpec{Network: "192.168.77.0", Mask: "255.255.255.0", Interface: "Wireguard9", Comment: "awgm-peer:5+0I/P0V"})
	if err != nil || !removed || len(poster.Payloads()) != 1 {
		t.Fatalf("своя запись: removed=%v err=%v posts=%d", removed, err, len(poster.Payloads()))
	}
	b, _ := json.Marshal(poster.Payloads()[0])
	if want := `{"ip":{"route":{"interface":"Wireguard9","mask":"255.255.255.0","network":"192.168.77.0","no":true}}}`; string(b) != want {
		t.Fatalf("payload:\n got %s\nwant %s", b, want)
	}
}

// Host-форма снимается host-формой: иначе network+mask /32 промахнулся бы
// мимо записи, которую ставит адаптер.
func TestRemoveOwnNetworkRoute_HostForm(t *testing.T) {
	cmds, poster := newRouteCommandsWithRC(t, rcRoutesFixture)
	removed, err := cmds.RemoveOwnNetworkRoute(context.Background(), StaticRouteSpec{Host: "192.168.79.5", Interface: "Wireguard9", Comment: "awgm-peer:5+0I/P0V"})
	if err != nil || !removed || len(poster.Payloads()) != 1 {
		t.Fatalf("removed=%v err=%v posts=%d", removed, err, len(poster.Payloads()))
	}
	b, _ := json.Marshal(poster.Payloads()[0])
	if want := `{"ip":{"route":{"host":"192.168.79.5","interface":"Wireguard9","no":true}}}`; string(b) != want {
		t.Fatalf("payload:\n got %s\nwant %s", b, want)
	}
}

// Кэш /show/rc/ip/route сбрасывается мутацией маршрута: иначе проверка
// владения сразу после добавления видела бы снимок без новой записи (TTL 30 с).
func TestRouteMutation_InvalidatesStaticRoutes(t *testing.T) {
	fg := query.NewFakeGetter()
	fg.SetJSON("/show/rc/ip/route", `[]`)
	poster := &fakePoster{}
	sc := NewSaveCoordinator(poster, &fakePublisher{}, time.Hour, time.Hour, 0, nil)
	cmds := NewRouteCommands(poster, sc, query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger()}))
	ctx := context.Background()
	const label = "awgm-peer:5+0I/P0V"
	if exists, _, err := cmds.NetworkRouteOwner(ctx, "192.168.77.0", "255.255.255.0", "Wireguard9", label); err != nil || exists {
		t.Fatalf("до: exists=%v err=%v", exists, err)
	}
	fg.SetJSON("/show/rc/ip/route", rcRoutesFixture)
	if err := cmds.AddStaticRoute(ctx, StaticRouteSpec{Network: "192.168.77.0", Mask: "255.255.255.0", Interface: "Wireguard9", Comment: label}); err != nil {
		t.Fatal(err)
	}
	if exists, own, err := cmds.NetworkRouteOwner(ctx, "192.168.77.0", "255.255.255.0", "Wireguard9", label); err != nil || !exists || !own {
		t.Fatalf("после: exists=%v own=%v err=%v — кэш не сброшен", exists, own, err)
	}
}

// Адаптер ставит /32 host-формой, остальное — network+mask: ровно то, что
// потом узнаёт NetworkRouteOwner.
func TestPeerRouter_AddNetworkRouteForms(t *testing.T) {
	cases := []struct{ cidr, want string }{
		{"192.168.77.0/24", `{"ip":{"route":{"auto":true,"comment":"L","interface":"Wireguard9","mask":"255.255.255.0","network":"192.168.77.0"}}}`},
		{"192.168.79.5/32", `{"ip":{"route":{"auto":true,"comment":"L","host":"192.168.79.5","interface":"Wireguard9"}}}`},
	}
	for _, c := range cases {
		cmds, poster := newRouteCommandsWithRC(t, `[]`)
		_, n, _ := net.ParseCIDR(c.cidr)
		if err := NewPeerRouter(&Commands{Routes: cmds}).AddNetworkRoute(context.Background(), n, "Wireguard9", "L"); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(poster.Payloads()[0])
		if string(b) != c.want {
			t.Fatalf("%s:\n got %s\nwant %s", c.cidr, b, c.want)
		}
	}
}

// Владение решается по свежему чтению: правка роутера мимо нас (веб-морда)
// кэш не сбрасывает, а решать по снимку — переподписать или снять чужое.
func TestNetworkRouteOwner_ReadsFresh(t *testing.T) {
	fg := query.NewFakeGetter()
	fg.SetJSON("/show/rc/ip/route", rcRoutesFixture)
	poster := &fakePoster{}
	sc := NewSaveCoordinator(poster, &fakePublisher{}, time.Hour, time.Hour, 0, nil)
	cmds := NewRouteCommands(poster, sc, query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger()}))
	ctx := context.Background()
	const label = "awgm-peer:5+0I/P0V"
	if _, own, err := cmds.NetworkRouteOwner(ctx, "192.168.77.0", "255.255.255.0", "Wireguard9", label); err != nil || !own {
		t.Fatalf("снимок: own=%v err=%v", own, err)
	}
	// Пользователь перехватил запись своим комментарием — без нашей мутации.
	fg.SetJSON("/show/rc/ip/route", `[{"network":"192.168.77.0","mask":"255.255.255.0","interface":"Wireguard9","comment":"mine"}]`)
	if exists, own, err := cmds.NetworkRouteOwner(ctx, "192.168.77.0", "255.255.255.0", "Wireguard9", label); err != nil || !exists || own {
		t.Fatalf("после чужой правки: exists=%v own=%v err=%v", exists, own, err)
	}
}

// RCI молчит — ошибка, а не устаревший own=true; снятие не уходит.
func TestNetworkRouteOwner_FetchErrorFailsClosed(t *testing.T) {
	fg := query.NewFakeGetter()
	fg.SetJSON("/show/rc/ip/route", rcRoutesFixture)
	poster := &fakePoster{}
	sc := NewSaveCoordinator(poster, &fakePublisher{}, time.Hour, time.Hour, 0, nil)
	cmds := NewRouteCommands(poster, sc, query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger()}))
	ctx := context.Background()
	const label = "awgm-peer:5+0I/P0V"
	if _, own, err := cmds.NetworkRouteOwner(ctx, "192.168.77.0", "255.255.255.0", "Wireguard9", label); err != nil || !own {
		t.Fatalf("снимок: own=%v err=%v", own, err)
	}
	fg.SetError("/show/rc/ip/route", errors.New("rci timeout"))
	if _, own, err := cmds.NetworkRouteOwner(ctx, "192.168.77.0", "255.255.255.0", "Wireguard9", label); err == nil || own {
		t.Fatalf("отказ RCI: own=%v err=%v", own, err)
	}
	removed, err := cmds.RemoveOwnNetworkRoute(ctx, StaticRouteSpec{Network: "192.168.77.0", Mask: "255.255.255.0", Interface: "Wireguard9", Comment: label})
	if err == nil || removed || len(poster.Payloads()) != 0 {
		t.Fatalf("снятие при отказе RCI: removed=%v err=%v posts=%d", removed, err, len(poster.Payloads()))
	}
}

// Пустая маска: Size() даёт (0, 0) — это не /32 и не host-форма.
func TestRouteSpec_ZeroMaskNotHost(t *testing.T) {
	spec := routeSpec(&net.IPNet{IP: net.IPv4(192, 168, 77, 0).To4()}, "Wireguard9", "L")
	if spec.Host != "" {
		t.Fatalf("пустая маска ушла host-формой: %+v", spec)
	}
}
