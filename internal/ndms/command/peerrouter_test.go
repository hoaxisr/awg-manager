package command

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

func newPeerRouterFixture(t *testing.T) (*PeerRouter, *query.FakeGetter) {
	t.Helper()
	fg := query.NewFakeGetter()
	fg.SetJSON("/show/rc/ip/route", rcRoutesFixture)
	fg.SetJSON("/show/rc/interface/Wireguard9", `{"wireguard":{"peer":[
		{"key":"K1=","allow-ips":[{"address":"10.9.9.2","mask":"255.255.255.255"},{"address":"192.168.77.0","mask":"255.255.255.0"}]},
		{"key":"K2=","allow-ips":[{"address":"10.9.9.3","mask":"255.255.255.255"}]}]}}`)
	poster := &fakePoster{}
	sc := NewSaveCoordinator(poster, &fakePublisher{}, time.Hour, time.Hour, 0, nil)
	q := query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger()})
	return NewPeerRouter(NewCommands(Deps{Poster: poster, Save: sc, Queries: q})), fg
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
	if nets, err := r.PeerAllowIPs(context.Background(), "Wireguard9", "ABSENT="); err != nil || len(nets) != 0 {
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
