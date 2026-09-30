package command

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/peersubnet"
)

func newPeerRouterFixture(t *testing.T) (*PeerRouter, *query.FakeGetter) {
	t.Helper()
	fg := query.NewFakeGetter()
	fg.SetJSON("/show/rc/ip/route", rcRoutesFixture)
	fg.SetJSON("/show/interface/", `{"Wireguard9":{"id":"Wireguard9","type":"Wireguard"}}`) // кэш интерфейсов: rc читается только по известным (F546)
	fg.SetRC("Wireguard9", `{"wireguard":{"peer":[
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
	nets, err := r.PeerAllowIPs(context.Background(), confirmed(t, "Wireguard9"), "K1=")
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
	if nets, err := r.PeerAllowIPs(context.Background(), confirmed(t, "Wireguard9"), "ABSENT="); !errors.Is(err, peersubnet.ErrPeerNotFound) || nets != nil {
		t.Fatalf("нет пира: %v %v", nets, err)
	}
	fg.SetRC("Wireguard9", `{"wireguard":{"peer":[{"key":"K1=","allow-ips":[{"address":"10.9.9.2","mask":"255.255.255.255"}]}]}}`)
	if nets, err := r.PeerAllowIPs(context.Background(), confirmed(t, "Wireguard9"), "K1="); err != nil || len(nets) != 1 {
		t.Fatalf("после правки: %v %v", nets, err)
	}
	fg.SetError("/show/rc/interface/", errors.New("rci down"))
	if _, err := r.PeerAllowIPs(context.Background(), confirmed(t, "Wireguard9"), "K1="); err == nil {
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

// failOnPoster — оракул, у которого POST с подстрокой failOn отказывает
// (шаг сверки падает — откат).
type failOnPoster struct {
	*query.FakeNDMS
	failOn string
}

func (p failOnPoster) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	b, _ := json.Marshal(payload)
	if p.failOn != "" && strings.Contains(string(b), p.failOn) {
		return nil, errors.New("boom")
	}
	return p.FakeNDMS.Post(ctx, payload)
}

// peerReconcileOracle — Wireguard9 с пиром K= без сетей за клиентом, таблица
// маршрутов пуста; Commands пишут в оракул через failOnPoster.
func peerReconcileOracle(t *testing.T, failOn string) (*PeerRouter, *query.FakeNDMS) {
	t.Helper()
	f := query.NewFakeNDMS(ndms.Interface{ID: "Wireguard9", Type: "Wireguard"})
	f.SetRC("Wireguard9", json.RawMessage(`{"wireguard":{"peer":[{"key":"K=","allow-ips":[{"address":"10.9.9.2","mask":"255.255.255.255"}]}]}}`))
	raw := map[string]string{
		"/show/rc/ip/route": `[]`,
	}
	p := failOnPoster{FakeNDMS: f, failOn: failOn}
	sc := NewSaveCoordinator(p, &fakePublisher{}, time.Hour, time.Hour, 0, nil)
	q := query.NewQueries(query.Deps{Getter: oracleGetter{FakeNDMS: f, raw: raw}, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	return NewPeerRouter(NewCommands(Deps{Poster: p, Save: sc, Queries: q}), q), f
}

// Сверка читает список интерфейсов ОДИН раз на вызов — и откат идёт по тому
// же доказательству (F546, R21): иначе N+1 полных списков на правку пира.
func TestReconcile_OneListRead_WithRollback(t *testing.T) {
	r, f := peerReconcileOracle(t, `"network":"192.168.78.0"`) // второй маршрут падает → откат
	before := f.ListCalls()
	err := peersubnet.Reconcile(context.Background(), r, "Wireguard9", "K=",
		[]net.IP{net.ParseIP("10.9.9.2")}, []string{"192.168.77.0/24", "192.168.78.0/24"})
	if err == nil {
		t.Fatal("ожидали отказ шага")
	}
	if n := f.ListCalls() - before; n != 1 {
		t.Fatalf("чтений списка: %d, want 1", n)
	}
	// Откат действительно был: снимались и маршрут, и allow-ips.
	var rollback int
	for _, p := range f.Posts {
		if strings.Contains(p, `"no":true`) {
			rollback++
		}
	}
	if rollback == 0 || f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("rollback=%d E=%d phantoms=%d posts=%v", rollback, f.E, f.Phantoms, f.Posts)
	}
}

func TestRemoveRoutes_OneListRead(t *testing.T) {
	r, f := peerReconcileOracle(t, "")
	before := f.ListCalls()
	if err := peersubnet.RemoveRoutes(context.Background(), r, "Wireguard9", "K="); err != nil {
		t.Fatal(err)
	}
	if n := f.ListCalls() - before; n != 1 {
		t.Fatalf("чтений списка: %d, want 1", n)
	}
}

// Интерфейса нет: сверка к пустому — успех, постановка — ошибка; список не
// прочитался — ошибка. Ни в одном случае команда не уходит (решение 4).
func TestReconcile_AbsentOrListError_NoCommands(t *testing.T) {
	ctx := context.Background()
	hosts := []net.IP{net.ParseIP("10.9.9.2")}
	r, f := peerReconcileOracle(t, "")
	f.Remove("Wireguard9")
	if err := peersubnet.Reconcile(ctx, r, "Wireguard9", "K=", hosts, []string{"192.168.77.0/24"}); err == nil || !strings.Contains(err.Error(), "Wireguard9") {
		t.Fatalf("постановка на отсутствующий: %v", err)
	}
	if err := peersubnet.Reconcile(ctx, r, "Wireguard9", "K=", hosts, nil); err != nil {
		t.Fatalf("снятие с отсутствующего: %v", err)
	}
	if err := peersubnet.RemoveRoutes(ctx, r, "Wireguard9", "K="); err != nil {
		t.Fatalf("RemoveRoutes с отсутствующего: %v", err)
	}
	f.FailList(errors.New("rci down"))
	if err := peersubnet.RemoveRoutes(ctx, r, "Wireguard9", "K="); err == nil {
		t.Fatal("упавший список: ожидали ошибку")
	}
	if len(f.Posts) != 0 || f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("posts=%v E=%d phantoms=%d", f.Posts, f.E, f.Phantoms)
	}
}
