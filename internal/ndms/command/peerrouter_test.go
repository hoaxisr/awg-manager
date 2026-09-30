package command

import (
	"context"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/peersubnet"
)

func newPeerRouterFixture(t *testing.T) (*PeerRouter, *query.FakeGetter) {
	t.Helper()
	fg := query.NewFakeGetter()
	fg.SetJSON("/show/rc/ip/route", rcRoutesFixture)
	fg.SetJSON("/show/interface/", `{"Wireguard9":{"id":"Wireguard9","type":"Wireguard"}}`) // кэш интерфейсов: rc читается только по известным (F546)
	fg.SetJSON("/show/rc/interface/Wireguard9", `{"wireguard":{"peer":[
		{"key":"K1=","allow-ips":[{"address":"10.9.9.2","mask":"255.255.255.255"},{"address":"192.168.77.0","mask":"255.255.255.0"}]},
		{"key":"K2=","allow-ips":[{"address":"10.9.9.3","mask":"255.255.255.255"}]}]}}`)
	poster := &fakePoster{}
	sc := NewSaveCoordinator(poster, &fakePublisher{}, time.Hour, time.Hour, 0, nil)
	q := query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger()})
	return NewPeerRouter(NewCommands(Deps{Poster: poster, Save: sc, Queries: q}), q), fg
}

func TestPeerRouter_InterfaceRoutes(t *testing.T) {
	r, fg := newPeerRouterFixture(t)
	routes, err := r.InterfaceRoutes(context.Background(), "Wireguard9")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, rt := range routes {
		got = append(got, rt.Net.String()+" "+rt.Comment)
	}
	want := []string{"192.168.77.0/24 awgm-peer:5+0I/P0V", "192.168.78.0/24 manual", "192.168.81.0/24 awgm-peer:5+0I/P0Vx", "192.168.79.5/32 awgm-peer:5+0I/P0V"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("routes = %v", got)
	}
	if routes, err := r.InterfaceRoutes(context.Background(), "Wireguard8"); err != nil || len(routes) != 0 {
		t.Fatalf("другой интерфейс: %v %v", routes, err)
	}
	// Свежее чтение: правка мимо панели видна сразу, отказ — ошибка, а не
	// прежний снимок кэша.
	fg.SetJSON("/show/rc/ip/route", `[]`)
	if routes, err := r.InterfaceRoutes(context.Background(), "Wireguard9"); err != nil || len(routes) != 0 {
		t.Fatalf("после правки: %v %v", routes, err)
	}
	fg.SetError("/show/rc/ip/route", errors.New("rci down"))
	if _, err := r.InterfaceRoutes(context.Background(), "Wireguard9"); err == nil {
		t.Fatal("отказ чтения проглочен")
	}
}

func TestPeerRouter_PeerAllowIPs(t *testing.T) {
	r, fg := newPeerRouterFixture(t)
	nets, err := r.PeerAllowIPs(context.Background(), "Wireguard9", "K1=")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, n := range nets {
		got = append(got, n.String())
	}
	if want := []string{"10.9.9.2/32", "192.168.77.0/24"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("allow = %v", got)
	}
	// Пира нет — ErrPeerNotFound, не пустой список: allow-ips на отсутствующий
	// ключ NDMS создаёт пира.
	if nets, err := r.PeerAllowIPs(context.Background(), "Wireguard9", "ABSENT="); !errors.Is(err, peersubnet.ErrPeerNotFound) || nets != nil {
		t.Fatalf("нет пира: %v %v", nets, err)
	}
	fg.SetJSON("/show/rc/interface/Wireguard9", `{"wireguard":{"peer":[{"key":"K1=","allow-ips":[{"address":"10.9.9.2","mask":"255.255.255.255"}]}]}}`)
	if nets, err := r.PeerAllowIPs(context.Background(), "Wireguard9", "K1="); err != nil || len(nets) != 1 {
		t.Fatalf("после правки: %v %v", nets, err)
	}
	fg.SetError("/show/rc/interface/Wireguard9", errors.New("rci down"))
	if _, err := r.PeerAllowIPs(context.Background(), "Wireguard9", "K1="); err == nil {
		t.Fatal("отказ чтения проглочен")
	}
}

// M6: стор маршрутов не подключён — ошибка, не nil-паника.
func TestPeerRouter_InterfaceRoutes_NotWired(t *testing.T) {
	poster := &fakePoster{}
	sc := NewSaveCoordinator(poster, &fakePublisher{}, time.Hour, time.Hour, 0, nil)
	r := NewPeerRouter(NewCommands(Deps{Poster: poster, Save: sc, Queries: &query.Queries{}}), &query.Queries{})
	if _, err := r.InterfaceRoutes(context.Background(), "Wireguard9"); err == nil {
		t.Fatal("ожидали ошибку")
	}
}

// Имя строкой подтверждается свежим списком до команды: интерфейса нет —
// постановка отказывает, снятие успешно; список не прочитался — ошибка.
// Ни в одном случае команда не уходит.
func TestPeerRouter_ConfirmsBeforeCommand(t *testing.T) {
	ctx := context.Background()
	_, n, _ := net.ParseCIDR("192.168.77.0/24")

	cmds, f, q := newOracleCommands(t, nil)
	r := NewPeerRouter(cmds, q)
	if err := r.AddAllowIP(ctx, "Wireguard9", "K=", n); err == nil || !strings.Contains(err.Error(), "Wireguard9") {
		t.Fatalf("AddAllowIP: %v", err)
	}
	if err := r.AddNetworkRoute(ctx, n, "Wireguard9", "L"); err == nil {
		t.Fatal("AddNetworkRoute: ожидали ошибку")
	}
	if err := r.RemoveAllowIP(ctx, "Wireguard9", "K=", n); err != nil {
		t.Fatalf("RemoveAllowIP: %v", err)
	}
	if removed, err := r.RemoveOwnNetworkRoute(ctx, n, "Wireguard9", "L"); removed || err != nil {
		t.Fatalf("RemoveOwnNetworkRoute: %v %v", removed, err)
	}
	f.FailList(errors.New("rci down"))
	if err := r.RemoveAllowIP(ctx, "Wireguard9", "K=", n); err == nil {
		t.Fatal("RemoveAllowIP при упавшем списке: ожидали ошибку")
	}
	if len(f.Posts) != 0 || f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("posts=%v E=%d phantoms=%d", f.Posts, f.E, f.Phantoms)
	}
}
