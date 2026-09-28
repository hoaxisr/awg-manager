package managed

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/peersubnet"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

func postsJSON(p *recordingPoster) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.posts))
	for i, m := range p.posts {
		b, _ := json.Marshal(m)
		out[i] = string(b)
	}
	return out
}

func indexOf(posts []string, sub string) int {
	for i, p := range posts {
		if strings.Contains(p, sub) {
			return i
		}
	}
	return -1
}

const (
	allow77    = `"allow-ips":[{"address":"192.168.77.0","mask":"255.255.255.0"}]`
	allow77Off = `"allow-ips":[{"address":"192.168.77.0","mask":"255.255.255.0","no":true}]`
	allow78    = `"allow-ips":[{"address":"192.168.78.0","mask":"255.255.255.0"}]`
	route77    = `"route":{"auto":true,"comment":"awgm-peer:pub-1","interface":"Wireguard1","mask":"255.255.255.0","network":"192.168.77.0"}`
	route77Off = `"route":{"interface":"Wireguard1","mask":"255.255.255.0","network":"192.168.77.0","no":true}`
	route78    = `"route":{"auto":true,"comment":"awgm-peer:PEER1","interface":"Wireguard1","mask":"255.255.255.0","network":"192.168.78.0"}`
	rcOurs77   = `[{"network":"192.168.77.0","mask":"255.255.255.0","interface":"Wireguard1","auto":true,"comment":"awgm-peer:PEER1"}]`
)

// simRouter — роутер в памяти поверх FakeGetter для сверки (#713): применяет
// посты allow-ips, добавления/снятия пира и маршрутов и перерисовывает
// /show/rc/interface/<iface> и /show/rc/ip/route — Reconcile читает то, что
// сделано. Пост, отвергнутый failOn, состояние не меняет. after — хук теста
// после применения поста.
type simRouter struct {
	mu     sync.Mutex
	fg     *query.FakeGetter
	poster *recordingPoster
	peers  map[string]map[string][]string // iface → key → "address/mask"
	routes []map[string]any
	after  func(m map[string]any)
}

func newSimRouter(t *testing.T, fg *query.FakeGetter, poster *recordingPoster, rcRoutes string) *simRouter {
	t.Helper()
	r := &simRouter{fg: fg, poster: poster, peers: map[string]map[string][]string{"Wireguard1": {}}}
	if err := json.Unmarshal([]byte(rcRoutes), &r.routes); err != nil {
		t.Fatal(err)
	}
	poster.onPost = r.apply
	r.render()
	return r
}

// seed — пир на роутере с allow-ips (CIDR).
func (r *simRouter) seed(iface, key string, cidrs ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.peers[iface] == nil {
		r.peers[iface] = map[string][]string{}
	}
	if _, ok := r.peers[iface][key]; !ok {
		r.peers[iface][key] = []string{} // пир есть и без адресов
	}
	for _, c := range cidrs {
		_, n, _ := net.ParseCIDR(c)
		r.peers[iface][key] = append(r.peers[iface][key], n.IP.String()+"/"+net.IP(n.Mask).String())
	}
	r.renderLocked()
}

func (r *simRouter) render() { r.mu.Lock(); defer r.mu.Unlock(); r.renderLocked() }

func (r *simRouter) renderLocked() {
	for iface, peers := range r.peers {
		keys := make([]string, 0, len(peers))
		for k := range peers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var list []map[string]any
		for _, k := range keys {
			var allow []map[string]string
			for _, a := range peers[k] {
				am := strings.SplitN(a, "/", 2)
				allow = append(allow, map[string]string{"address": am[0], "mask": am[1]})
			}
			list = append(list, map[string]any{"key": k, "allow-ips": allow})
		}
		b, _ := json.Marshal(map[string]any{"wireguard": map[string]any{"peer": list}})
		r.fg.SetJSON("/show/rc/interface/"+iface, string(b))
	}
	b, _ := json.Marshal(r.routes)
	if r.routes == nil {
		b = []byte(`[]`)
	}
	r.fg.SetJSON("/show/rc/ip/route", string(b))
}

func sameRoute(a, b map[string]any) bool {
	return a["interface"] == b["interface"] && a["network"] == b["network"] && a["mask"] == b["mask"] && a["host"] == b["host"]
}

func (r *simRouter) apply(orig map[string]interface{}) {
	if r.poster.failOn != nil && r.poster.failOn(orig) != nil {
		return
	}
	var m map[string]any
	b, _ := json.Marshal(orig)
	_ = json.Unmarshal(b, &m)
	r.mu.Lock()
	if ifs, ok := m["interface"].(map[string]any); ok {
		for iface, v := range ifs {
			wg, _ := v.(map[string]any)["wireguard"].(map[string]any)
			peerList, _ := wg["peer"].([]any)
			for _, pv := range peerList {
				p := pv.(map[string]any)
				key, _ := p["key"].(string)
				allow, hasAllow := p["allow-ips"].([]any)
				if p["no"] == true && !hasAllow {
					delete(r.peers[iface], key)
					continue
				}
				if r.peers[iface] == nil {
					r.peers[iface] = map[string][]string{}
				}
				// Как настоящий NDMS (стенд 5.02.A.11, 28.09): ЛЮБАЯ операция
				// allow-ips на неизвестном ключе создаёт пира — и снятие тоже.
				cur := r.peers[iface][key]
				for _, av := range allow {
					a := av.(map[string]any)
					e := a["address"].(string) + "/" + a["mask"].(string)
					cur = slices.DeleteFunc(cur, func(x string) bool { return x == e })
					if a["no"] != true {
						cur = append(cur, e)
					}
				}
				r.peers[iface][key] = cur
			}
		}
	}
	if ip, ok := m["ip"].(map[string]any); ok {
		if rt, ok := ip["route"].(map[string]any); ok {
			r.routes = slices.DeleteFunc(r.routes, func(x map[string]any) bool { return sameRoute(x, rt) })
			if rt["no"] != true {
				r.routes = append(r.routes, rt)
			}
		}
	}
	r.renderLocked()
	after := r.after
	r.mu.Unlock()
	if after != nil {
		after(orig)
	}
}

// state — allow-ips пира и сети маршрутов с меткой, для проверок итога.
func (r *simRouter) state(iface, key, comment string) (allow, routes []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	allow = append(allow, r.peers[iface][key]...)
	for _, rt := range r.routes {
		if rt["interface"] == iface && rt["comment"] == comment {
			n, _ := rt["network"].(string)
			if h, ok := rt["host"].(string); ok && h != "" {
				n = h
			}
			routes = append(routes, n)
		}
	}
	sort.Strings(allow)
	sort.Strings(routes)
	return allow, routes
}

func seedPeer(t *testing.T, store *storage.SettingsStore, subnets ...string) {
	t.Helper()
	if err := store.UpdateManagedServer("Wireguard1", func(sv *storage.ManagedServer) error {
		sv.Peers = append(sv.Peers, storage.ManagedPeer{PublicKey: "PEER1", PrivateKey: "p", Description: "branch", TunnelIP: "10.66.66.2/32", Enabled: true, RemoteSubnets: subnets})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// Порядок: пир → allow-ips → маршрут; запись только после успеха.
func TestAddPeer_RemoteSubnets_AllowIPsThenRoute(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	sim := newSimRouter(t, fg, poster, `[]`)
	peer, err := svc.AddPeer(context.Background(), "Wireguard1", AddPeerRequest{Description: "branch", TunnelIP: "10.66.66.2/32",
		ClientAllowedIPs: "10.66.66.0/24,192.168.1.0/24", RemoteSubnets: []string{"192.168.77.5/24"}})
	if err != nil {
		t.Fatal(err)
	}
	posts := postsJSON(poster)
	iPeer, iAllow, iRoute := indexOf(posts, `"preshared-key"`), indexOf(posts, allow77), indexOf(posts, route77)
	if iPeer < 0 || iAllow < 0 || iRoute < 0 || !(iPeer < iAllow && iAllow < iRoute) {
		t.Fatalf("порядок: peer=%d allow=%d route=%d\n%s", iPeer, iAllow, iRoute, strings.Join(posts, "\n"))
	}
	if peer.ClientAllowedIPs != "10.66.66.0/24, 192.168.1.0/24" || len(peer.RemoteSubnets) != 1 || peer.RemoteSubnets[0] != "192.168.77.0/24" {
		t.Fatalf("peer = %+v", peer)
	}
	sv, _ := store.GetManagedServerByID("Wireguard1")
	if sv.Peers[0].RemoteSubnets[0] != "192.168.77.0/24" {
		t.Fatalf("store = %+v", sv.Peers[0])
	}
	// /32 нового пира сверка сетью не считает и не снимает.
	if allow, _ := sim.state("Wireguard1", "pub-1", ""); !slices.Equal(allow, []string{"10.66.66.2/255.255.255.255", "192.168.77.0/255.255.255.0"}) {
		t.Fatalf("allow = %v", allow)
	}
}

func TestAddPeer_OverlapRejectedBeforeRCI(t *testing.T) {
	svc, store, poster, _ := newPeerSubnetTestService(t, `[]`)
	for _, c := range []struct{ subnet, label string }{
		{"172.16.5.0/25", "office"}, {"192.168.1.0/24", "Home"}, {"10.9.0.0/16", "Wireguard VPN Server"},
	} {
		_, err := svc.AddPeer(context.Background(), "Wireguard1", AddPeerRequest{Description: "x", TunnelIP: "10.66.66.2/32", RemoteSubnets: []string{c.subnet}})
		if !errors.Is(err, peersubnet.ErrRemoteSubnetOverlap) || !strings.Contains(err.Error(), c.label) {
			t.Fatalf("%s: err = %v", c.subnet, err)
		}
	}
	_, err := svc.AddPeer(context.Background(), "Wireguard1", AddPeerRequest{TunnelIP: "10.66.66.2/32", ClientAllowedIPs: "10.0.0.1"})
	if !errors.Is(err, peersubnet.ErrInvalidClientAllowedIPs) {
		t.Fatalf("err = %v", err)
	}
	if len(postsJSON(poster)) != 0 {
		t.Fatalf("RCI дёрнут при отказе валидации: %v", postsJSON(poster))
	}
	if sv, _ := store.GetManagedServerByID("Wireguard1"); len(sv.Peers) != 0 {
		t.Fatal("пир записан")
	}
}

func TestAddPeer_RouteFailure_RollsBackAllowIPsAndPeer(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	_ = newSimRouter(t, fg, poster, `[]`)
	poster.failOn = func(m map[string]interface{}) error {
		b, _ := json.Marshal(m)
		if strings.Contains(string(b), `"comment":"awgm-peer:`) {
			return errors.New("route refused")
		}
		return nil
	}
	_, err := svc.AddPeer(context.Background(), "Wireguard1", AddPeerRequest{Description: "branch", TunnelIP: "10.66.66.2/32", RemoteSubnets: []string{"192.168.77.0/24"}})
	if err == nil || !strings.Contains(err.Error(), "route refused") {
		t.Fatalf("err = %v", err)
	}
	posts := postsJSON(poster)
	if indexOf(posts, allow77Off) < 0 || indexOf(posts, `{"key":"pub-1","no":true}`) < 0 {
		t.Fatalf("нет отката allow-ips/пира:\n%s", strings.Join(posts, "\n"))
	}
	if sv, _ := store.GetManagedServerByID("Wireguard1"); len(sv.Peers) != 0 {
		t.Fatal("пир записан после отказа")
	}
}

// seedSimPeer — PEER1 в записи и на роутере: /32 туннеля плюс subnets в allow-ips.
func seedSimPeer(t *testing.T, store *storage.SettingsStore, sim *simRouter, subnets ...string) {
	t.Helper()
	seedPeer(t, store, subnets...)
	sim.seed("Wireguard1", "PEER1", append([]string{"10.66.66.2/32"}, subnets...)...)
}

func TestUpdatePeer_DiffAddsAndRemoves(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, rcOurs77)
	sim := newSimRouter(t, fg, poster, rcOurs77)
	seedSimPeer(t, store, sim, "192.168.77.0/24")
	err := svc.UpdatePeer(context.Background(), "Wireguard1", "PEER1", UpdatePeerRequest{Description: "branch", TunnelIP: "10.66.66.2/32", RemoteSubnets: &[]string{"192.168.78.0/24"}})
	if err != nil {
		t.Fatal(err)
	}
	posts := postsJSON(poster)
	for _, want := range []string{allow78, allow77Off, route78, route77Off} {
		if indexOf(posts, want) < 0 {
			t.Fatalf("нет %s:\n%s", want, strings.Join(posts, "\n"))
		}
	}
	if indexOf(posts, allow78) > indexOf(posts, route78) || indexOf(posts, allow77Off) > indexOf(posts, route77Off) {
		t.Fatalf("allow-ips обязаны идти до маршрутов:\n%s", strings.Join(posts, "\n"))
	}
	sv, _ := store.GetManagedServerByID("Wireguard1")
	if len(sv.Peers[0].RemoteSubnets) != 1 || sv.Peers[0].RemoteSubnets[0] != "192.168.78.0/24" {
		t.Fatalf("store = %v", sv.Peers[0].RemoteSubnets)
	}
}

// Review Focus 3: пустой список снимает всё.
func TestUpdatePeer_EmptyRemoteSubnetsRemovesAll(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, rcOurs77)
	sim := newSimRouter(t, fg, poster, rcOurs77)
	seedSimPeer(t, store, sim, "192.168.77.0/24")
	if err := svc.UpdatePeer(context.Background(), "Wireguard1", "PEER1", UpdatePeerRequest{Description: "branch", TunnelIP: "10.66.66.2/32", RemoteSubnets: &[]string{}}); err != nil {
		t.Fatal(err)
	}
	posts := postsJSON(poster)
	if indexOf(posts, allow77Off) < 0 || indexOf(posts, route77Off) < 0 {
		t.Fatalf("сети не сняты:\n%s", strings.Join(posts, "\n"))
	}
	if sv, _ := store.GetManagedServerByID("Wireguard1"); len(sv.Peers[0].RemoteSubnets) != 0 {
		t.Fatalf("store = %v", sv.Peers[0].RemoteSubnets)
	}
	if allow, routes := sim.state("Wireguard1", "PEER1", "awgm-peer:PEER1"); len(allow) != 1 || len(routes) != 0 {
		t.Fatalf("роутер: allow=%v routes=%v", allow, routes)
	}
}

// Правило 2: чужая запись на (N, I) — не добавляем и потом не снимаем.
func TestUpdatePeer_ForeignRouteNeverTouched(t *testing.T) {
	rc := `[{"network":"192.168.78.0","mask":"255.255.255.0","interface":"Wireguard1","auto":true,"comment":"manual"}]`
	svc, store, poster, fg := newPeerSubnetTestService(t, rc)
	sim := newSimRouter(t, fg, poster, rc)
	seedSimPeer(t, store, sim)
	if err := svc.UpdatePeer(context.Background(), "Wireguard1", "PEER1", UpdatePeerRequest{Description: "branch", TunnelIP: "10.66.66.2/32", RemoteSubnets: &[]string{"192.168.78.0/24"}}); err != nil {
		t.Fatal(err)
	}
	if err := svc.UpdatePeer(context.Background(), "Wireguard1", "PEER1", UpdatePeerRequest{Description: "branch", TunnelIP: "10.66.66.2/32", RemoteSubnets: &[]string{}}); err != nil {
		t.Fatal(err)
	}
	if posts := postsJSON(poster); indexOf(posts, `"route"`) >= 0 {
		t.Fatalf("маршрут тронут при чужой записи:\n%s", strings.Join(posts, "\n"))
	}
}

func TestDeletePeer_RemovesOwnRoutesBeforePeer_FailClosed(t *testing.T) {
	svc, store, poster, _ := newPeerSubnetTestService(t, rcOurs77)
	seedPeer(t, store, "192.168.77.0/24")
	if err := svc.DeletePeer(context.Background(), "Wireguard1", "PEER1"); err != nil {
		t.Fatal(err)
	}
	posts := postsJSON(poster)
	iRoute, iPeer := indexOf(posts, route77Off), indexOf(posts, `{"key":"PEER1","no":true}`)
	if iRoute < 0 || iPeer < 0 || iRoute > iPeer {
		t.Fatalf("порядок: route=%d peer=%d\n%s", iRoute, iPeer, strings.Join(posts, "\n"))
	}
	if indexOf(posts, allow77Off) >= 0 {
		t.Fatal("allow-ips при удалении пира не снимаются отдельно")
	}

	svc, store, poster, _ = newPeerSubnetTestService(t, rcOurs77)
	seedPeer(t, store, "192.168.77.0/24")
	poster.failOn = func(m map[string]interface{}) error {
		b, _ := json.Marshal(m)
		if strings.Contains(string(b), `"route"`) {
			return errors.New("route stuck")
		}
		return nil
	}
	if err := svc.DeletePeer(context.Background(), "Wireguard1", "PEER1"); err == nil {
		t.Fatal("удаление обязано отказать")
	}
	if posts := postsJSON(poster); indexOf(posts, `{"key":"PEER1","no":true}`) >= 0 {
		t.Fatal("пир снят при неснятом маршруте")
	}
	if sv, _ := store.GetManagedServerByID("Wireguard1"); len(sv.Peers) != 1 {
		t.Fatal("пир пропал из хранилища")
	}
}

// Fix round 1 / IMPORTANT 1: смена tunnel IP и сетей одним запросом, маршрут
// отвергнут — /32 на роутере возвращается к записанному, хранилище не тронуто.
func TestUpdatePeer_TunnelIPAndSubnets_RouteFailure_RestoresTunnelIP(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	sim := newSimRouter(t, fg, poster, `[]`)
	seedSimPeer(t, store, sim)
	poster.failOn = func(m map[string]interface{}) error {
		b, _ := json.Marshal(m)
		if strings.Contains(string(b), `"comment":"awgm-peer:`) {
			return errors.New("route refused")
		}
		return nil
	}
	err := svc.UpdatePeer(context.Background(), "Wireguard1", "PEER1", UpdatePeerRequest{Description: "branch", TunnelIP: "10.66.66.3/32", RemoteSubnets: &[]string{"192.168.77.0/24"}})
	if err == nil || !strings.Contains(err.Error(), "route refused") {
		t.Fatalf("err = %v", err)
	}
	posts := postsJSON(poster)
	const (
		old32    = `"allow-ips":[{"address":"10.66.66.2","mask":"255.255.255.255"}]`
		new32Off = `"allow-ips":[{"address":"10.66.66.3","mask":"255.255.255.255","no":true}]`
	)
	iRoute, iNewOff, iOld := indexOf(posts, `"comment":"awgm-peer:PEER1"`), -1, -1
	for i, p := range posts {
		if i > iRoute && strings.Contains(p, new32Off) && iNewOff < 0 {
			iNewOff = i
		}
		if i > iRoute && strings.Contains(p, old32) {
			iOld = i
		}
	}
	if iRoute < 0 || iNewOff < 0 || iOld < 0 || iNewOff > iOld {
		t.Fatalf("старый /32 не возвращён: route=%d newOff=%d old=%d\n%s", iRoute, iNewOff, iOld, strings.Join(posts, "\n"))
	}
	sv, _ := store.GetManagedServerByID("Wireguard1")
	if sv.Peers[0].TunnelIP != "10.66.66.2/32" || len(sv.Peers[0].RemoteSubnets) != 0 {
		t.Fatalf("хранилище записано при отказе: %+v", sv.Peers[0])
	}
}

// Fix round 1 / IMPORTANT 1(б): старого /32 на роутере уже нет — `no such net
// in peer` на его снятии смену не валит (11.A/11.8).
func TestUpdatePeer_TunnelIP_OldAbsentTolerated(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	sim := newSimRouter(t, fg, poster, `[]`)
	seedPeer(t, store)
	sim.seed("Wireguard1", "PEER1") // пир есть, старого /32 у него нет
	poster.respond = func(m map[string]interface{}) json.RawMessage {
		b, _ := json.Marshal(m)
		if strings.Contains(string(b), `"address":"10.66.66.2","mask":"255.255.255.255","no":true`) {
			return json.RawMessage(`{"interface":{"Wireguard1":{"wireguard":{"peer":[{"status":[{"status":"error","message":"\"Wireguard1\": no such net in peer \"PEER1\"."}]}]}}}}`)
		}
		return nil
	}
	if err := svc.UpdatePeer(context.Background(), "Wireguard1", "PEER1", UpdatePeerRequest{Description: "branch", TunnelIP: "10.66.66.3/32"}); err != nil {
		t.Fatal(err)
	}
	if sv, _ := store.GetManagedServerByID("Wireguard1"); sv.Peers[0].TunnelIP != "10.66.66.3/32" {
		t.Fatalf("tunnel IP не записан: %+v", sv.Peers[0])
	}
}

// Fix round 1 / IMPORTANT 2: ctx запроса отменён посреди — откат пира всё
// равно доходит до роутера (отвязанный ctx).
func TestAddPeer_CancelledCtx_PeerStillRemoved(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	_ = newSimRouter(t, fg, poster, `[]`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	poster.honorCtx = true
	poster.failOn = func(m map[string]interface{}) error {
		b, _ := json.Marshal(m)
		if strings.Contains(string(b), `"comment":"awgm-peer:`) {
			cancel()
			return errors.New("route refused")
		}
		return nil
	}
	if _, err := svc.AddPeer(ctx, "Wireguard1", AddPeerRequest{Description: "branch", TunnelIP: "10.66.66.2/32", RemoteSubnets: []string{"192.168.77.0/24"}}); err == nil {
		t.Fatal("ожидали отказ")
	}
	if posts := postsJSON(poster); indexOf(posts, `{"key":"pub-1","no":true}`) < 0 {
		t.Fatalf("пир не снят при отменённом ctx:\n%s", strings.Join(posts, "\n"))
	}
	if sv, _ := store.GetManagedServerByID("Wireguard1"); len(sv.Peers) != 0 {
		t.Fatal("пир записан")
	}
}

// Fix round 1 / MINOR 3: запись в хранилище отказала (гонка автовыдачи
// адреса) — сети и пир снимаются с роутера, сирот нет.
func TestAddPeer_StoreFailure_RemovesSubnetsAndPeer(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	sim := newSimRouter(t, fg, poster, `[]`)
	sim.after = func(m map[string]interface{}) {
		b, _ := json.Marshal(m)
		if !strings.Contains(string(b), `"comment":"awgm-peer:pub-1"`) {
			return
		}
		// Маршрут встал на роутере, а параллельный AddPeer занял тот же адрес.
		_ = store.UpdateManagedServer("Wireguard1", func(sv *storage.ManagedServer) error {
			sv.Peers = append(sv.Peers, storage.ManagedPeer{PublicKey: "RACE", TunnelIP: "10.66.66.2/32"})
			return nil
		})
	}
	if _, err := svc.AddPeer(context.Background(), "Wireguard1", AddPeerRequest{Description: "branch", TunnelIP: "10.66.66.2/32", RemoteSubnets: []string{"192.168.77.0/24"}}); err == nil {
		t.Fatal("ожидали отказ записи")
	}
	// Маршрут — по метке с роутера, до снятия пира; allow-ips уходят с пиром.
	posts := postsJSON(poster)
	iRouteOff, iPeerOff := indexOf(posts, route77Off), indexOf(posts, `{"key":"pub-1","no":true}`)
	if iRouteOff < 0 || iPeerOff < 0 || iRouteOff > iPeerOff {
		t.Fatalf("порядок отката: routeOff=%d peerOff=%d\n%s", iRouteOff, iPeerOff, strings.Join(posts, "\n"))
	}
	if allow, routes := sim.state("Wireguard1", "pub-1", "awgm-peer:pub-1"); len(allow)+len(routes) != 0 {
		t.Fatalf("сироты на роутере: allow=%v routes=%v", allow, routes)
	}
	if sv, _ := store.GetManagedServerByID("Wireguard1"); len(sv.Peers) != 1 || sv.Peers[0].PublicKey != "RACE" {
		t.Fatalf("store = %+v", sv.Peers)
	}
}

// Final review I2: ACL сервера с LANSegments пропускает только подсеть сервера —
// сети за клиентом отвергаются до RCI; снять уже стоящие — можно.
func TestPeer_RemoteSubnetsRejectedWithLANSegments(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, rcOurs77)
	sim := newSimRouter(t, fg, poster, rcOurs77)
	seedSimPeer(t, store, sim, "192.168.77.0/24")
	if err := store.UpdateManagedServer("Wireguard1", func(sv *storage.ManagedServer) error {
		sv.LANSegments = []string{"Home"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err := svc.AddPeer(ctx, "Wireguard1", AddPeerRequest{Description: "x", TunnelIP: "10.66.66.3/32", RemoteSubnets: []string{"192.168.78.0/24"}})
	if !errors.Is(err, peersubnet.ErrInvalidRemoteSubnets) || !strings.Contains(err.Error(), "LAN-сегмент") {
		t.Fatalf("add: err = %v", err)
	}
	err = svc.UpdatePeer(ctx, "Wireguard1", "PEER1", UpdatePeerRequest{Description: "branch", TunnelIP: "10.66.66.2/32", RemoteSubnets: &[]string{"192.168.78.0/24"}})
	if !errors.Is(err, peersubnet.ErrInvalidRemoteSubnets) {
		t.Fatalf("update: err = %v", err)
	}
	if posts := postsJSON(poster); len(posts) != 0 {
		t.Fatalf("RCI дёрнут при отказе:\n%s", strings.Join(posts, "\n"))
	}
	if err := svc.UpdatePeer(ctx, "Wireguard1", "PEER1", UpdatePeerRequest{Description: "branch", TunnelIP: "10.66.66.2/32", RemoteSubnets: &[]string{}}); err != nil {
		t.Fatalf("снятие сетей отвергнуто: %v", err)
	}
	if sv, _ := store.GetManagedServerByID("Wireguard1"); len(sv.Peers[0].RemoteSubnets) != 0 {
		t.Fatalf("store = %v", sv.Peers[0].RemoteSubnets)
	}
}

// Final review I4: сверка прошла, запись отказала — сети и /32 на роутере
// возвращаются к записанному (паритет с системным путём).
func TestUpdatePeer_StoreFailure_RevertsSubnetsAndTunnelIP(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	sim := newSimRouter(t, fg, poster, `[]`)
	seedSimPeer(t, store, sim)
	sim.after = func(m map[string]interface{}) {
		b, _ := json.Marshal(m)
		if !strings.Contains(string(b), `"comment":"awgm-peer:PEER1"`) {
			return
		}
		// Маршрут встал, а пира параллельно удалили из хранилища — мутатор откажет.
		_ = store.UpdateManagedServer("Wireguard1", func(sv *storage.ManagedServer) error {
			sv.Peers = nil
			return nil
		})
	}
	err := svc.UpdatePeer(context.Background(), "Wireguard1", "PEER1", UpdatePeerRequest{Description: "branch", TunnelIP: "10.66.66.3/32", RemoteSubnets: &[]string{"192.168.77.0/24"}})
	if err == nil || !strings.Contains(err.Error(), "save to storage") {
		t.Fatalf("err = %v", err)
	}
	posts := postsJSON(poster)
	const (
		old32    = `"allow-ips":[{"address":"10.66.66.2","mask":"255.255.255.255"}]`
		new32Off = `"allow-ips":[{"address":"10.66.66.3","mask":"255.255.255.255","no":true}]`
		routeOff = `"route":{"interface":"Wireguard1","mask":"255.255.255.0","network":"192.168.77.0","no":true}`
	)
	iRoute := indexOf(posts, `"comment":"awgm-peer:PEER1"`)
	after := func(sub string) int {
		for i := iRoute + 1; i < len(posts); i++ {
			if strings.Contains(posts[i], sub) {
				return i
			}
		}
		return -1
	}
	if iRoute < 0 || after(allow77Off) < 0 || after(routeOff) < 0 || after(new32Off) < 0 || after(old32) < 0 {
		t.Fatalf("роутер не возвращён к записанному:\n%s", strings.Join(posts, "\n"))
	}
	if allow, routes := sim.state("Wireguard1", "PEER1", "awgm-peer:PEER1"); !slices.Equal(allow, []string{"10.66.66.2/255.255.255.255"}) || len(routes) != 0 {
		t.Fatalf("роутер: allow=%v routes=%v", allow, routes)
	}
}

// driftServerWithSubnetPeer — managed-сервер Wireguard5 есть в хранилище, но не
// на роутере (дрейф); у пира — сеть за клиентом.
func driftServerWithSubnetPeer(t *testing.T, store *storage.SettingsStore) {
	t.Helper()
	if err := store.AddManagedServer(storage.ManagedServer{InterfaceName: "Wireguard5", Address: "10.77.0.1", Mask: "255.255.255.0", ListenPort: 51825,
		PrivateKey: validPrivateKey(9), Policy: "none",
		Peers: []storage.ManagedPeer{{PublicKey: "PEER5", TunnelIP: "10.77.0.2/32", Enabled: true, Description: "site", RemoteSubnets: []string{"192.168.90.0/24"}}}}); err != nil {
		t.Fatal(err)
	}
}

func restoreDrift(t *testing.T, svc *Service) {
	t.Helper()
	drift, err := svc.Drift(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := svc.RestoreDrift(context.Background(), drift, RestoreOptions{})
	if len(out) != 1 || out[0].Action != "created" {
		t.Fatalf("outcomes: %+v", out)
	}
}

// Final review I3: пир пересоздан из записи — его сети за клиентом ставятся
// на роутер (allow-ips и маршрут) и остаются в записи.
func TestRestoreDrift_ReappliesPeerRemoteSubnets(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	sim := newSimRouter(t, fg, poster, `[]`)
	driftServerWithSubnetPeer(t, store)
	restoreDrift(t, svc)
	posts := postsJSON(poster)
	iPeer := indexOf(posts, `"key":"PEER5"`)
	iAllow := indexOf(posts, `"allow-ips":[{"address":"192.168.90.0","mask":"255.255.255.0"}]`)
	iRoute := indexOf(posts, `"comment":"awgm-peer:PEER5"`)
	if iPeer < 0 || iAllow < 0 || iRoute < 0 || !(iPeer < iAllow && iAllow < iRoute) {
		t.Fatalf("сети не восстановлены: peer=%d allow=%d route=%d\n%s", iPeer, iAllow, iRoute, strings.Join(posts, "\n"))
	}
	sv, _ := store.GetManagedServerByID("Wireguard5")
	if len(sv.Peers) != 1 || len(sv.Peers[0].RemoteSubnets) != 1 {
		t.Fatalf("store = %+v", sv.Peers)
	}
	if allow, _ := sim.state("Wireguard5", "PEER5", ""); !slices.Equal(allow, []string{"10.77.0.2/255.255.255.255", "192.168.90.0/255.255.255.0"}) {
		t.Fatalf("allow = %v", allow)
	}
}

// Сети не встали — запись их не хранит (иначе неисцелимо через UI), пир и
// сервер восстановлены.
func TestRestoreDrift_SubnetFailureClearsStoredSubnets(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	newSimRouter(t, fg, poster, `[]`)
	driftServerWithSubnetPeer(t, store)
	poster.failOn = func(m map[string]interface{}) error {
		b, _ := json.Marshal(m)
		if strings.Contains(string(b), `"comment":"awgm-peer:`) {
			return errors.New("route refused")
		}
		return nil
	}
	restoreDrift(t, svc)
	sv, _ := store.GetManagedServerByID("Wireguard5")
	if len(sv.Peers) != 1 || len(sv.Peers[0].RemoteSubnets) != 0 {
		t.Fatalf("store = %+v", sv.Peers)
	}
}

// Merge-путь: роутер для сетей недоступен (Commands не подключены) — пир
// добавлен, сети из записи сняты.
func TestRestore_MergeClearsUnappliedRemoteSubnets(t *testing.T) {
	store := storage.NewSettingsStore(t.TempDir())
	_, _ = store.Load()
	priv := validPrivateKey(58)
	_ = store.AddManagedServer(storage.ManagedServer{InterfaceName: "Wireguard0", Address: "10.64.0.1", Mask: "255.255.255.0", ListenPort: 51854, PrivateKey: priv, Policy: "none"})
	getter := &restoreLiveGetter{live: map[string]restoreLiveEntry{"Wireguard0": {Present: true, Address: "10.64.0.1", Mask: "255.255.255.0", PublicKey: mustDerivePublicKey(t, priv)}}}
	ifaces := query.NewInterfaceStoreWithTTL(getter, query.NopLogger(), 0, 0)
	queries := &query.Queries{Interfaces: ifaces, WGServers: query.NewWGServerStore(getter, query.NopLogger(), ifaces)}
	s := &Service{settings: store, transport: &fakePoster{onPost: getter.applyPost}, queries: queries}
	in := []storage.ManagedPeer{{PublicKey: validPeerKey(59), TunnelIP: "10.64.0.2/32", Enabled: true, RemoteSubnets: []string{"192.168.91.0/24"}}}
	out := s.Restore(context.Background(), []ManagedServerExport{{InterfaceName: "Wireguard0", Address: "10.64.0.1", Mask: "255.255.255.0", ListenPort: 51854, PrivateKey: priv, Policy: "none", Peers: in}}, RestoreOptions{})
	if len(out) != 1 || out[0].Action != "merged" || out[0].AddedPeers != 1 {
		t.Fatalf("outcomes: %+v", out)
	}
	sv, _ := store.GetManagedServerByID("Wireguard0")
	if len(sv.Peers) != 1 || len(sv.Peers[0].RemoteSubnets) != 0 {
		t.Fatalf("store = %+v", sv.Peers)
	}
	if len(in[0].RemoteSubnets) != 1 {
		t.Fatal("входной бэкап изменён")
	}
}

// W2-P1 (решение владельца 28.09): в правке пира поле сетей отсутствует или
// null — значение пира не меняется ни на роутере, ни в хранилище.
// Запрос декодируется из JSON — различие absent/null/[] живёт только там.
func updatePeerJSON(t *testing.T, svc *Service, body string) error {
	t.Helper()
	var req UpdatePeerRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	return svc.UpdatePeer(context.Background(), "Wireguard1", "PEER1", req)
}

func seedPeerAllowed(t *testing.T, store *storage.SettingsStore, allowed string) {
	t.Helper()
	if err := store.UpdateManagedServer("Wireguard1", func(sv *storage.ManagedServer) error {
		sv.Peers[0].ClientAllowedIPs = allowed
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUpdatePeer_AbsentOrNullNetworksUntouched(t *testing.T) {
	for _, body := range []string{
		`{"description":"renamed","tunnelIP":"10.66.66.2/32"}`,
		`{"description":"renamed","tunnelIP":"10.66.66.2/32","clientAllowedIPs":null,"remoteSubnets":null}`,
	} {
		svc, store, poster, _ := newPeerSubnetTestService(t, rcOurs77)
		seedPeer(t, store, "192.168.77.0/24")
		seedPeerAllowed(t, store, "10.66.66.0/24")
		if err := updatePeerJSON(t, svc, body); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if posts := postsJSON(poster); indexOf(posts, `"allow-ips"`) >= 0 || indexOf(posts, `"route"`) >= 0 {
			t.Fatalf("%s: сети тронуты:\n%s", body, strings.Join(posts, "\n"))
		}
		p := func() storage.ManagedPeer { sv, _ := store.GetManagedServerByID("Wireguard1"); return sv.Peers[0] }()
		if p.Description != "renamed" || p.ClientAllowedIPs != "10.66.66.0/24" || len(p.RemoteSubnets) != 1 || p.RemoteSubnets[0] != "192.168.77.0/24" {
			t.Fatalf("%s: peer = %+v", body, p)
		}
	}
}

func TestUpdatePeer_EmptyClientAllowedIPsClears(t *testing.T) {
	svc, store, _, _ := newPeerSubnetTestService(t, `[]`)
	seedPeer(t, store)
	seedPeerAllowed(t, store, "10.66.66.0/24")
	if err := updatePeerJSON(t, svc, `{"description":"branch","tunnelIP":"10.66.66.2/32","clientAllowedIPs":""}`); err != nil {
		t.Fatal(err)
	}
	if sv, _ := store.GetManagedServerByID("Wireguard1"); sv.Peers[0].ClientAllowedIPs != "" {
		t.Fatalf("clientAllowedIPs = %q", sv.Peers[0].ClientAllowedIPs)
	}
}

// Отсутствие сетей не упирается в ограничение LAN-сегментов: править
// описание пира на таком сервере можно.
func TestUpdatePeer_AbsentRemoteSubnets_LANSegmentsPass(t *testing.T) {
	svc, store, _, _ := newPeerSubnetTestService(t, `[]`)
	seedPeer(t, store)
	if err := store.UpdateManagedServer("Wireguard1", func(sv *storage.ManagedServer) error {
		sv.LANSegments = []string{"Bridge0"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := updatePeerJSON(t, svc, `{"description":"x","tunnelIP":"10.66.66.2/32"}`); err != nil {
		t.Fatal(err)
	}
}

// F509: «было» читается с роутера. В записи [77], на роутере ещё сирота 78
// (allow-ip и наш маршрут — прошлый RollbackError): сохранение [77] снимает 78,
// по 77 — ни одного вызова.
func TestUpdatePeer_HealsRouterDrift(t *testing.T) {
	rc := `[{"network":"192.168.77.0","mask":"255.255.255.0","interface":"Wireguard1","comment":"awgm-peer:PEER1"},
		{"network":"192.168.78.0","mask":"255.255.255.0","interface":"Wireguard1","comment":"awgm-peer:PEER1"}]`
	svc, store, poster, fg := newPeerSubnetTestService(t, rc)
	sim := newSimRouter(t, fg, poster, rc)
	seedSimPeer(t, store, sim, "192.168.77.0/24")
	sim.seed("Wireguard1", "PEER1", "192.168.78.0/24")
	if err := svc.UpdatePeer(context.Background(), "Wireguard1", "PEER1", UpdatePeerRequest{Description: "branch", TunnelIP: "10.66.66.2/32", RemoteSubnets: &[]string{"192.168.77.0/24"}}); err != nil {
		t.Fatal(err)
	}
	posts := postsJSON(poster)
	if len(posts) != 2 || indexOf(posts, `"allow-ips":[{"address":"192.168.78.0","mask":"255.255.255.0","no":true}]`) != 0 || indexOf(posts, `"network":"192.168.78.0","no":true`) != 1 {
		t.Fatalf("posts:\n%s", strings.Join(posts, "\n"))
	}
	allow, routes := sim.state("Wireguard1", "PEER1", "awgm-peer:PEER1")
	if !slices.Equal(allow, []string{"10.66.66.2/255.255.255.255", "192.168.77.0/255.255.255.0"}) || !slices.Equal(routes, []string{"192.168.77.0"}) {
		t.Fatalf("роутер: allow=%v routes=%v", allow, routes)
	}
}

// Роутер потерял сеть (ни allow-ip, ни маршрута), запись [77] — сохранение
// того же списка её восстанавливает.
func TestUpdatePeer_RestoresLostSubnet(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	sim := newSimRouter(t, fg, poster, `[]`)
	seedPeer(t, store, "192.168.77.0/24")
	sim.seed("Wireguard1", "PEER1", "10.66.66.2/32")
	if err := svc.UpdatePeer(context.Background(), "Wireguard1", "PEER1", UpdatePeerRequest{Description: "branch", TunnelIP: "10.66.66.2/32", RemoteSubnets: &[]string{"192.168.77.0/24"}}); err != nil {
		t.Fatal(err)
	}
	allow, routes := sim.state("Wireguard1", "PEER1", "awgm-peer:PEER1")
	if !slices.Equal(allow, []string{"10.66.66.2/255.255.255.255", "192.168.77.0/255.255.255.0"}) || !slices.Equal(routes, []string{"192.168.77.0"}) {
		t.Fatalf("роутер: allow=%v routes=%v", allow, routes)
	}
}

// Смена адреса вместе со сверкой: новый /32 туннеля сетью за клиентом не
// считается и сверкой не снимается.
func TestUpdatePeer_TunnelIPChange_TunnelHostNotASubnet(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	sim := newSimRouter(t, fg, poster, `[]`)
	seedSimPeer(t, store, sim)
	if err := svc.UpdatePeer(context.Background(), "Wireguard1", "PEER1", UpdatePeerRequest{Description: "branch", TunnelIP: "10.66.66.3/32", RemoteSubnets: &[]string{}}); err != nil {
		t.Fatal(err)
	}
	if allow, _ := sim.state("Wireguard1", "PEER1", ""); !slices.Equal(allow, []string{"10.66.66.3/255.255.255.255"}) {
		t.Fatalf("allow = %v", allow)
	}
}

// Удаление пира снимает все маршруты с его меткой, найденные на роутере, —
// и сироту, которой в записи нет; чужие не трогает.
func TestDeletePeer_SweepsOrphanRoutes(t *testing.T) {
	rc := `[{"network":"192.168.78.0","mask":"255.255.255.0","interface":"Wireguard1","comment":"awgm-peer:PEER1"},
		{"host":"192.168.79.5","interface":"Wireguard1","comment":"awgm-peer:PEER1"},
		{"network":"192.168.80.0","mask":"255.255.255.0","interface":"Wireguard1","comment":"manual"}]`
	svc, store, poster, fg := newPeerSubnetTestService(t, rc)
	sim := newSimRouter(t, fg, poster, rc)
	seedSimPeer(t, store, sim)
	if err := svc.DeletePeer(context.Background(), "Wireguard1", "PEER1"); err != nil {
		t.Fatal(err)
	}
	if _, routes := sim.state("Wireguard1", "PEER1", "awgm-peer:PEER1"); len(routes) != 0 {
		t.Fatalf("сироты: %v", routes)
	}
	if _, routes := sim.state("Wireguard1", "PEER1", "manual"); len(routes) != 1 {
		t.Fatal("чужой маршрут снят")
	}
}

// Состояние роутера не читается — отказ до единой мутации, запись не тронута.
func TestUpdatePeer_RouterReadFailure_NoMutation(t *testing.T) {
	for _, path := range []string{"/show/rc/ip/route", "/show/rc/interface/Wireguard1"} {
		svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
		seedPeer(t, store, "192.168.77.0/24")
		// Пустой список: занятые не собираются, падает само чтение состояния.
		fg.SetError(path, errors.New("rci down"))
		err := svc.UpdatePeer(context.Background(), "Wireguard1", "PEER1", UpdatePeerRequest{Description: "branch", TunnelIP: "10.66.66.2/32", RemoteSubnets: &[]string{}})
		if err == nil || len(postsJSON(poster)) != 0 {
			t.Fatalf("%s: err=%v posts=%v", path, err, postsJSON(poster))
		}
		if sv, _ := store.GetManagedServerByID("Wireguard1"); len(sv.Peers[0].RemoteSubnets) != 1 {
			t.Fatalf("%s: запись тронута: %v", path, sv.Peers[0].RemoteSubnets)
		}
	}
}

// F508: две параллельные правки разных пиров с пересекающимися сетями — ровно
// одна проходит, вторая получает пересечение. Первая, дойдя до роутера, ждёт,
// пока вторая соберёт занятые (без блокировки та собрала бы их до записи
// первой и прошла бы тоже); с блокировкой вторая ждёт на ней, и ожидание
// кончается таймаутом.
func TestUpdatePeer_ConcurrentOverlap_OneWins(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	sim := newSimRouter(t, fg, poster, `[]`)
	seedSimPeer(t, store, sim)
	if err := store.UpdateManagedServer("Wireguard1", func(sv *storage.ManagedServer) error {
		sv.Peers = append(sv.Peers, storage.ManagedPeer{PublicKey: "PEER2", PrivateKey: "p", Description: "branch2", TunnelIP: "10.66.66.3/32", Enabled: true})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sim.seed("Wireguard1", "PEER2", "10.66.66.3/32")
	const occupiedScan = "/show/rc/interface/Wireguard0" // читает только OccupiedSubnets
	var once sync.Once
	sim.after = func(m map[string]interface{}) {
		once.Do(func() {
			deadline := time.Now().Add(300 * time.Millisecond)
			for fg.Calls(occupiedScan) < 2 && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
		})
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, c := range []struct{ key, desc, ip, net string }{
		{"PEER1", "branch", "10.66.66.2/32", "192.168.77.0/24"},
		{"PEER2", "branch2", "10.66.66.3/32", "192.168.77.0/25"},
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = svc.UpdatePeer(context.Background(), "Wireguard1", c.key, UpdatePeerRequest{Description: c.desc, TunnelIP: c.ip, RemoteSubnets: &[]string{c.net}})
		}()
	}
	wg.Wait()
	ok, overlap := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, peersubnet.ErrRemoteSubnetOverlap):
			overlap++
		default:
			t.Fatalf("err = %v", err)
		}
	}
	if ok != 1 || overlap != 1 {
		t.Fatalf("ok=%d overlap=%d errs=%v", ok, overlap, errs)
	}
}

// I1: сверка прошла, пира на роутере не стало (параллельное удаление мимо
// блокировки — веб-морда), запись отказала. Компенсация видит «пир не
// найден» и не шлёт ничего: allow-ips на отсутствующий ключ NDMS создал бы
// пира-призрака.
func TestUpdatePeer_PeerVanished_CompensationCreatesNoGhost(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	sim := newSimRouter(t, fg, poster, `[]`)
	seedSimPeer(t, store, sim)
	vanishedAt := -1
	sim.after = func(m map[string]interface{}) {
		b, _ := json.Marshal(m)
		if vanishedAt >= 0 || !strings.Contains(string(b), `"comment":"awgm-peer:PEER1"`) {
			return
		}
		sim.mu.Lock()
		delete(sim.peers["Wireguard1"], "PEER1")
		sim.renderLocked()
		sim.mu.Unlock()
		vanishedAt = len(poster.posts)
		_ = store.UpdateManagedServer("Wireguard1", func(sv *storage.ManagedServer) error {
			sv.Peers = nil
			return nil
		})
	}
	err := svc.UpdatePeer(context.Background(), "Wireguard1", "PEER1", UpdatePeerRequest{Description: "branch", TunnelIP: "10.66.66.2/32", RemoteSubnets: &[]string{"192.168.77.0/24"}})
	if err == nil || vanishedAt < 0 {
		t.Fatalf("err=%v vanishedAt=%d", err, vanishedAt)
	}
	if posts := postsJSON(poster); len(posts) != vanishedAt {
		t.Fatalf("после исчезновения пира ушли посты:\n%s", strings.Join(posts[vanishedAt:], "\n"))
	}
	sim.mu.Lock()
	_, ghost := sim.peers["Wireguard1"]["PEER1"]
	sim.mu.Unlock()
	if ghost {
		t.Fatal("пир-призрак создан")
	}
}

// waitsForLock — call не завершается, пока LockPeerSubnets держат снаружи, и
// завершается после отпускания.
func waitsForLock(t *testing.T, svc *Service, call func()) bool {
	t.Helper()
	unlock := svc.LockPeerSubnets()
	done := make(chan struct{})
	go func() { call(); close(done) }()
	select {
	case <-done:
		unlock()
		return false
	case <-time.After(150 * time.Millisecond):
	}
	unlock()
	<-done
	return true
}

// I1(а): удаление пира ждёт блокировку правок сетей.
func TestDeletePeer_TakesPeerSubnetsLock(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	sim := newSimRouter(t, fg, poster, `[]`)
	seedSimPeer(t, store, sim)
	if !waitsForLock(t, svc, func() { _ = svc.DeletePeer(context.Background(), "Wireguard1", "PEER1") }) {
		t.Fatal("удаление прошло мимо блокировки")
	}
}

// M2: [] у пира без сетей — ни блокировки, ни чтений роутера; правка
// с непустым списком её ждёт.
func TestUpdatePeer_EmptyToEmpty_NoLockNoReads(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	sim := newSimRouter(t, fg, poster, `[]`)
	seedSimPeer(t, store, sim)
	routeReads := fg.Calls("/show/rc/ip/route")
	var err error
	if waitsForLock(t, svc, func() {
		err = svc.UpdatePeer(context.Background(), "Wireguard1", "PEER1", UpdatePeerRequest{Description: "branch", TunnelIP: "10.66.66.2/32", RemoteSubnets: &[]string{}})
	}) || err != nil {
		t.Fatalf("правка без сетей ждала блокировку или отказала: %v", err)
	}
	if fg.Calls("/show/rc/ip/route") != routeReads || len(postsJSON(poster)) != 0 {
		t.Fatalf("чтения/посты при [] → []: reads=%d posts=%v", fg.Calls("/show/rc/ip/route")-routeReads, postsJSON(poster))
	}
	if !waitsForLock(t, svc, func() {
		_ = svc.UpdatePeer(context.Background(), "Wireguard1", "PEER1", UpdatePeerRequest{Description: "x", TunnelIP: "10.66.66.2/32", RemoteSubnets: &[]string{"192.168.77.0/24"}})
	}) {
		t.Fatal("правка сетей прошла мимо блокировки")
	}
}

// I3: сети из бэкапа проверяются по занятым: пересекающаяся с чужим пиром
// (172.16.5.0/24 у пира Wireguard0) не ставится и из записи убирается,
// свободная — ставится. Восстановление берёт блокировку правок сетей.
func TestRestoreDrift_OccupiedSubnetDropped(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	sim := newSimRouter(t, fg, poster, `[]`)
	if err := store.AddManagedServer(storage.ManagedServer{InterfaceName: "Wireguard5", Address: "10.77.0.1", Mask: "255.255.255.0", ListenPort: 51825,
		PrivateKey: validPrivateKey(9), Policy: "none",
		Peers: []storage.ManagedPeer{{PublicKey: "PEER5", TunnelIP: "10.77.0.2/32", Enabled: true, Description: "site", RemoteSubnets: []string{"172.16.5.0/24", "192.168.90.0/24"}}}}); err != nil {
		t.Fatal(err)
	}
	if !waitsForLock(t, svc, func() { restoreDrift(t, svc) }) {
		t.Fatal("восстановление прошло мимо блокировки")
	}
	sv, _ := store.GetManagedServerByID("Wireguard5")
	if len(sv.Peers) != 1 || !slices.Equal(sv.Peers[0].RemoteSubnets, []string{"192.168.90.0/24"}) {
		t.Fatalf("store = %+v", sv.Peers)
	}
	if allow, _ := sim.state("Wireguard5", "PEER5", ""); !slices.Equal(allow, []string{"10.77.0.2/255.255.255.255", "192.168.90.0/255.255.255.0"}) {
		t.Fatalf("allow = %v", allow)
	}
}

// Fix round 2: пир удалён посреди смены адреса (мимо панели), запись
// отказала — откат адреса видит «пира нет» и старый /32 не шлёт: allow-ips на
// отсутствующий ключ NDMS создал бы пира-призрака.
func TestUpdatePeer_TunnelRevert_PeerDeleted_NoGhost(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	sim := newSimRouter(t, fg, poster, `[]`)
	seedSimPeer(t, store, sim)
	vanishedAt := -1
	sim.after = func(m map[string]interface{}) {
		b, _ := json.Marshal(m)
		if vanishedAt >= 0 || !strings.Contains(string(b), `"allow-ips":[{"address":"10.66.66.3","mask":"255.255.255.255"}]`) {
			return
		}
		sim.mu.Lock()
		delete(sim.peers["Wireguard1"], "PEER1")
		sim.renderLocked()
		sim.mu.Unlock()
		vanishedAt = len(poster.posts)
		_ = store.UpdateManagedServer("Wireguard1", func(sv *storage.ManagedServer) error {
			sv.Peers = nil
			return nil
		})
	}
	err := svc.UpdatePeer(context.Background(), "Wireguard1", "PEER1", UpdatePeerRequest{Description: "branch", TunnelIP: "10.66.66.3/32"})
	if err == nil || vanishedAt < 0 {
		t.Fatalf("err=%v vanishedAt=%d", err, vanishedAt)
	}
	for _, p := range postsJSON(poster)[vanishedAt:] {
		if strings.Contains(p, `"allow-ips"`) {
			t.Fatalf("allow-ips отправлены удалённому пиру: %s", p)
		}
	}
	sim.mu.Lock()
	_, ghost := sim.peers["Wireguard1"]["PEER1"]
	sim.mu.Unlock()
	if ghost {
		t.Fatal("пир-призрак создан")
	}
}

// Fix round 2: смена адреса без сетей берёт блокировку правок сетей (её же
// берёт удаление пира).
func TestUpdatePeer_TunnelChange_TakesPeerSubnetsLock(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	sim := newSimRouter(t, fg, poster, `[]`)
	seedSimPeer(t, store, sim)
	if !waitsForLock(t, svc, func() {
		_ = svc.UpdatePeer(context.Background(), "Wireguard1", "PEER1", UpdatePeerRequest{Description: "branch", TunnelIP: "10.66.66.3/32"})
	}) {
		t.Fatal("смена адреса прошла мимо блокировки")
	}
}

// Fix round 2: наличие пира перед откатом адреса не прочиталось — старый /32
// не шлётся (не знаем, не создадим ли пира).
func TestUpdatePeer_TunnelRevert_PresenceReadFails_NoAdd(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	sim := newSimRouter(t, fg, poster, `[]`)
	seedSimPeer(t, store, sim)
	brokeAt := -1
	sim.after = func(m map[string]interface{}) {
		b, _ := json.Marshal(m)
		if brokeAt >= 0 || !strings.Contains(string(b), `"allow-ips":[{"address":"10.66.66.3","mask":"255.255.255.255"}]`) {
			return
		}
		fg.SetError("/show/rc/interface/Wireguard1", errors.New("rci down"))
		brokeAt = len(poster.posts)
		_ = store.UpdateManagedServer("Wireguard1", func(sv *storage.ManagedServer) error {
			sv.Peers = nil
			return nil
		})
	}
	if err := svc.UpdatePeer(context.Background(), "Wireguard1", "PEER1", UpdatePeerRequest{Description: "branch", TunnelIP: "10.66.66.3/32"}); err == nil || brokeAt < 0 {
		t.Fatalf("err=%v brokeAt=%d", err, brokeAt)
	}
	for _, p := range postsJSON(poster)[brokeAt:] {
		if strings.Contains(p, `"allow-ips":[{"address":"10.66.66.2","mask":"255.255.255.255"}]`) {
			t.Fatalf("старый /32 отправлен без проверки наличия пира: %s", p)
		}
	}
}

// Fix round 3: пир в записи есть, на роутере нет (снят в веб-морде) — смена
// адреса отказывает до единого поста: allow-ips на неизвестный ключ создали
// бы призрака.
func TestUpdatePeer_TunnelChange_PeerAbsentOnRouter_NoPosts(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	sim := newSimRouter(t, fg, poster, `[]`)
	seedPeer(t, store)
	err := svc.UpdatePeer(context.Background(), "Wireguard1", "PEER1", UpdatePeerRequest{Description: "branch", TunnelIP: "10.66.66.3/32"})
	if !errors.Is(err, peersubnet.ErrPeerNotFound) || len(postsJSON(poster)) != 0 {
		t.Fatalf("err=%v posts=%v", err, postsJSON(poster))
	}
	sim.mu.Lock()
	_, ghost := sim.peers["Wireguard1"]["PEER1"]
	sim.mu.Unlock()
	if ghost {
		t.Fatal("пир-призрак создан")
	}
}

// Fix round 4 (N2): решение о сверке — по снимку, но пока правка ждала
// блокировку, соседняя записала пиру сеть. Присланный [] обязан её снять:
// решение пересчитывается по записи, перечитанной под блокировкой.
func TestUpdatePeer_EmptyList_RecheckedUnderLock(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	sim := newSimRouter(t, fg, poster, rcOurs77)
	seedSimPeer(t, store, sim)
	unlock := svc.LockPeerSubnets()
	done := make(chan error, 1)
	go func() {
		// Смена адреса берёт блокировку; снимок — «сетей у пира нет».
		done <- svc.UpdatePeer(context.Background(), "Wireguard1", "PEER1", UpdatePeerRequest{Description: "branch", TunnelIP: "10.66.66.3/32", RemoteSubnets: &[]string{}})
	}()
	time.Sleep(150 * time.Millisecond) // снимок снят, правка ждёт блокировку
	// Конкурентная правка сетей завершилась: роутер и запись с 192.168.77.0/24.
	sim.seed("Wireguard1", "PEER1", "192.168.77.0/24")
	if err := store.UpdateManagedServer("Wireguard1", func(sv *storage.ManagedServer) error {
		sv.Peers[0].RemoteSubnets = []string{"192.168.77.0/24"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	unlock()
	if err := <-done; err != nil {
		t.Fatalf("UpdatePeer: %v", err)
	}
	if allow, routes := sim.state("Wireguard1", "PEER1", "awgm-peer:PEER1"); len(allow) != 1 || len(routes) != 0 {
		t.Fatalf("[] проигнорирован: allow=%v routes=%v", allow, routes)
	}
	if sv, _ := store.GetManagedServerByID("Wireguard1"); len(sv.Peers[0].RemoteSubnets) != 0 {
		t.Fatalf("запись: %v", sv.Peers[0].RemoteSubnets)
	}
}

// Fix round 4 (N3): два пира нового сервера в одном бэкапе с пересекающимися
// сетями. Сервера ещё нет в записи — OccupiedSubnets allow-ips его пиров не
// видит, поэтому второй пир сверяется с уже принятыми сетями первого:
// конфликтующая сеть отбрасывается, свободная — ставится.
func TestRestore_NewServerPeersSeeEachOther(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	sim := newSimRouter(t, fg, poster, `[]`)
	k1, k2 := validPeerKey(71), validPeerKey(72)
	out := svc.Restore(context.Background(), []ManagedServerExport{{InterfaceName: "Wireguard5", Address: "10.77.0.1", Mask: "255.255.255.0", ListenPort: 51825,
		PrivateKey: validPrivateKey(9), Policy: "none",
		Peers: []storage.ManagedPeer{
			{PublicKey: k1, TunnelIP: "10.77.0.2/32", Enabled: true, RemoteSubnets: []string{"192.168.90.0/24"}},
			{PublicKey: k2, TunnelIP: "10.77.0.3/32", Enabled: true, RemoteSubnets: []string{"192.168.90.0/25", "192.168.91.0/24"}},
		}}}, RestoreOptions{})
	if len(out) != 1 || out[0].Action != "created" {
		t.Fatalf("outcomes: %+v", out)
	}
	sv, _ := store.GetManagedServerByID("Wireguard5")
	if len(sv.Peers) != 2 || !slices.Equal(sv.Peers[0].RemoteSubnets, []string{"192.168.90.0/24"}) || !slices.Equal(sv.Peers[1].RemoteSubnets, []string{"192.168.91.0/24"}) {
		t.Fatalf("store = %+v", sv.Peers)
	}
	if allow, _ := sim.state("Wireguard5", k2, ""); !slices.Equal(allow, []string{"10.77.0.3/255.255.255.255", "192.168.91.0/255.255.255.0"}) {
		t.Fatalf("allow второго пира = %v", allow)
	}
}
