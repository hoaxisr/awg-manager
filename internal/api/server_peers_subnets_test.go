package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/managed"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/netif"
)

const (
	sysAllow77    = `"allow-ips":[{"address":"192.168.77.0","mask":"255.255.255.0"}]`
	sysAllow77Off = `"allow-ips":[{"address":"192.168.77.0","mask":"255.255.255.0","no":true}]`
	sysAllow78    = `"allow-ips":[{"address":"192.168.78.0","mask":"255.255.255.0"}]`
	// peerFixturePubKey начинается с "AB/CD+EF" — это и есть метка.
	sysRoute77    = `"route":{"auto":true,"comment":"awgm-peer:AB/CD+EF","interface":"Wireguard0","mask":"255.255.255.0","network":"192.168.77.0"}`
	sysRoute77Off = `"route":{"interface":"Wireguard0","mask":"255.255.255.0","network":"192.168.77.0","no":true}`
	sysRcOurs77   = `[{"network":"192.168.77.0","mask":"255.255.255.0","interface":"Wireguard0","auto":true,"comment":"awgm-peer:AB/CD+EF"}]`
)

// newServersSubnetHarness — как newServersPeerHarness, плюс: LAN-бридж Home
// 192.168.1.0/24, allow-ips пира peerFixturePubKey на роутере (peerAllowIPs),
// записи /show/rc/ip/route и подключённый managed.Service (сбор занятых сетей,
// пресеты). Пир всегда в списке роутера.
func newServersSubnetHarness(t *testing.T, peerAllowIPs, rcRoutes string) (*ServersHandler, *storage.SettingsStore, *natPoster, *busProbe, *appLogSpy) {
	t.Helper()
	return newServersSubnetHarnessWithOther(t, peerAllowIPs, rcRoutes, "")
}

// otherPeerPubKey — второй пир Wireguard0 в newServersSubnetHarnessWithOther.
const otherPeerPubKey = "OTHERaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa="

// newServersSubnetHarnessWithOther — newServersSubnetHarness плюс второй пир
// otherPeerPubKey с allow-ips otherAllowIPs (пусто — второго пира нет).
func newServersSubnetHarnessWithOther(t *testing.T, peerAllowIPs, rcRoutes, otherAllowIPs string) (*ServersHandler, *storage.SettingsStore, *natPoster, *busProbe, *appLogSpy) {
	t.Helper()
	h, store, poster, p, spy, _ := newServersSubnetHarnessFG(t, peerAllowIPs, rcRoutes, otherAllowIPs)
	return h, store, poster, p, spy
}

// hookGetter — FakeGetter с хуком на каждое чтение (GET): видно, что читает
// хендлер и когда.
type hookGetter struct {
	*query.FakeGetter
	mu   sync.Mutex
	hook func(path string)
}

func (g *hookGetter) setHook(f func(path string)) { g.mu.Lock(); g.hook = f; g.mu.Unlock() }

func (g *hookGetter) fire(path string) {
	g.mu.Lock()
	f := g.hook
	g.mu.Unlock()
	if f != nil {
		f(path)
	}
}

func (g *hookGetter) Get(ctx context.Context, path string, dst any) error {
	g.fire(path)
	return g.FakeGetter.Get(ctx, path, dst)
}

func (g *hookGetter) GetRaw(ctx context.Context, path string) ([]byte, error) {
	g.fire(path)
	return g.FakeGetter.GetRaw(ctx, path)
}

// newServersSubnetHarnessFG — newServersSubnetHarnessWithOther, отдающий и
// FakeGetter (для sysSimRouter) с хуком чтений.
func newServersSubnetHarnessFG(t *testing.T, peerAllowIPs, rcRoutes, otherAllowIPs string) (*ServersHandler, *storage.SettingsStore, *natPoster, *busProbe, *appLogSpy, *hookGetter) {
	t.Helper()
	listPeers := `{"public-key":"` + peerFixturePubKey + `","comment":"phone"}`
	rcPeers := `{"key":"` + peerFixturePubKey + `","comment":"phone","allow-ips":` + peerAllowIPs + `}`
	if otherAllowIPs != "" {
		listPeers += `,{"public-key":"` + otherPeerPubKey + `","comment":"tablet"}`
		rcPeers += `,{"key":"` + otherPeerPubKey + `","comment":"tablet","allow-ips":` + otherAllowIPs + `}`
	}
	fg := &hookGetter{FakeGetter: query.NewFakeGetter()}
	fg.SetJSON("/show/interface/", `{
		"Wireguard0":{"id":"Wireguard0","type":"Wireguard","description":"Wireguard VPN Server","state":"up","link":"up","address":"10.9.0.1","mask":"255.255.255.0","wireguard":{"peer":[`+listPeers+`]}},
		"Bridge0":{"id":"Bridge0","type":"Bridge","description":"Home","address":"192.168.1.1","mask":"255.255.255.0"}}`)
	fg.SetJSON("/show/rc/interface/Wireguard0", `{"wireguard":{"peer":[`+rcPeers+`]}}`)
	fg.SetJSON("/show/rc/ip/route", rcRoutes)
	fg.SetJSON("/show/running-config", `{"message":["interface PPPoE0","    ip global 32767","!"]}`)
	queries := query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger()})
	poster := &natPoster{}
	store := storage.NewSettingsStore(t.TempDir())
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	spy := &appLogSpy{}
	h := NewServersHandler(queries, store, nil, spy)
	cmds := ndmscommand.NewCommands(ndmscommand.Deps{
		Poster:  poster,
		Queries: queries,
		Save:    ndmscommand.NewSaveCoordinator(poster, nil, time.Hour, time.Hour, 0, nil),
	})
	h.SetCommands(cmds)
	h.SetManagedService(managed.New(poster, nil, queries, cmds, store, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	p := newBusProbe(t)
	h.SetEventBus(p.bus())
	return h, store, poster, p, spy, fg
}

// sysSimRouter — роутер в памяти для сверки (#713): применяет посты allow-ips,
// снятия пира и маршрутов и перерисовывает /show/rc/interface/Wireguard0 и
// /show/rc/ip/route — Reconcile читает сделанное. Встаёт в natPoster.failOn;
// fail — отказ NDMS теста: отвергнутый пост состояние не меняет.
type sysSimRouter struct {
	mu     sync.Mutex
	fg     *query.FakeGetter
	peers  map[string][]string // key → "address/mask"
	routes []map[string]any
	fail   func(payload string) error
	// ignore — пост «принят», но состояние не меняется (роутер не выполнил).
	ignore func(payload string) bool
}

func newSysSimRouter(t *testing.T, fg *query.FakeGetter, poster *natPoster, rcRoutes string) *sysSimRouter {
	t.Helper()
	r := &sysSimRouter{fg: fg, peers: map[string][]string{}}
	if err := json.Unmarshal([]byte(rcRoutes), &r.routes); err != nil {
		t.Fatal(err)
	}
	poster.setFailOn(r.post)
	r.render()
	return r
}

func (r *sysSimRouter) seed(key string, cidrs ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.peers[key] = r.peers[key][:0:0]
	for _, c := range cidrs {
		_, n, _ := net.ParseCIDR(c)
		r.peers[key] = append(r.peers[key], n.IP.String()+"/"+net.IP(n.Mask).String())
	}
	r.render()
}

func (r *sysSimRouter) render() {
	keys := make([]string, 0, len(r.peers))
	for k := range r.peers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var list []map[string]any
	for _, k := range keys {
		var allow []map[string]string
		for _, a := range r.peers[k] {
			am := strings.SplitN(a, "/", 2)
			allow = append(allow, map[string]string{"address": am[0], "mask": am[1]})
		}
		list = append(list, map[string]any{"key": k, "allow-ips": allow})
	}
	b, _ := json.Marshal(map[string]any{"wireguard": map[string]any{"peer": list}})
	r.fg.SetJSON("/show/rc/interface/Wireguard0", string(b))
	b, _ = json.Marshal(r.routes)
	if r.routes == nil {
		b = []byte(`[]`)
	}
	r.fg.SetJSON("/show/rc/ip/route", string(b))
}

func (r *sysSimRouter) post(payload string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		if err := r.fail(payload); err != nil {
			return err
		}
	}
	if r.ignore != nil && r.ignore(payload) {
		return nil
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(payload), &m)
	if ifs, ok := m["interface"].(map[string]any); ok {
		wg, _ := ifs["Wireguard0"].(map[string]any)["wireguard"].(map[string]any)
		peerList, _ := wg["peer"].([]any)
		for _, pv := range peerList {
			p := pv.(map[string]any)
			key, _ := p["key"].(string)
			allow, hasAllow := p["allow-ips"].([]any)
			if p["no"] == true && !hasAllow {
				delete(r.peers, key)
				continue
			}
			// Как настоящий NDMS (стенд 5.02.A.11, 28.09): ЛЮБАЯ операция
			// allow-ips на неизвестном ключе создаёт пира — и снятие тоже.
			cur := r.peers[key]
			for _, av := range allow {
				a := av.(map[string]any)
				e := a["address"].(string) + "/" + a["mask"].(string)
				cur = slices.DeleteFunc(cur, func(x string) bool { return x == e })
				if a["no"] != true {
					cur = append(cur, e)
				}
			}
			r.peers[key] = cur
		}
	}
	if ip, ok := m["ip"].(map[string]any); ok {
		if rt, ok := ip["route"].(map[string]any); ok {
			same := func(x map[string]any) bool {
				return x["interface"] == rt["interface"] && x["network"] == rt["network"] && x["mask"] == rt["mask"] && x["host"] == rt["host"]
			}
			r.routes = slices.DeleteFunc(r.routes, same)
			if rt["no"] != true {
				r.routes = append(r.routes, rt)
			}
		}
	}
	r.render()
	return nil
}

// has — есть ли пир с ключом на симуляторе (в т.ч. призрак без адресов).
func (r *sysSimRouter) has(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.peers[key]
	return ok
}

// state — allow-ips пира и сети маршрутов с меткой comment.
func (r *sysSimRouter) state(key, comment string) (allow, routes []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	allow = append(allow, r.peers[key]...)
	for _, rt := range r.routes {
		if rt["comment"] == comment {
			n, _ := rt["network"].(string)
			routes = append(routes, n)
		}
	}
	sort.Strings(allow)
	sort.Strings(routes)
	return allow, routes
}

func idx(posts []string, sub string) int {
	for i, p := range posts {
		if strings.Contains(p, sub) {
			return i
		}
	}
	return -1
}

func TestServersHandler_AddServerPeer_RemoteSubnets_OrderAndSecret(t *testing.T) {
	h, store, poster, p, _, fg := newServersSubnetHarnessFG(t, `[]`, `[]`, "")
	sim := newSysSimRouter(t, fg.FakeGetter, poster, `[]`)
	stubPeerKeygen(t)
	rr := postServerPeer(t, h, `{"description":"Phone","tunnelIP":"10.9.0.7/32","clientAllowedIPs":"10.9.0.0/24, 192.168.1.0/24","remoteSubnets":["192.168.77.5/24"]}`)
	if rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	posts := poster.snapshot()
	iPeer, iAllow, iRoute := idx(posts, `"preshared-key"`), idx(posts, sysAllow77), idx(posts, sysRoute77)
	if iPeer < 0 || iAllow < 0 || iRoute < 0 || !(iPeer < iAllow && iAllow < iRoute) {
		t.Fatalf("порядок peer=%d allow=%d route=%d:\n%s", iPeer, iAllow, iRoute, strings.Join(posts, "\n"))
	}
	sec, _ := store.GetServerPeerSecret("Wireguard0", peerFixturePubKey)
	if sec.ClientAllowedIPs != "10.9.0.0/24, 192.168.1.0/24" || len(sec.RemoteSubnets) != 1 || sec.RemoteSubnets[0] != "192.168.77.0/24" {
		t.Fatalf("secret = %+v", sec)
	}
	if got := p.invalidated(); len(got) != 1 || got[0] != "servers/server-peer-added" {
		t.Fatalf("публикации = %v", got)
	}
	// /32 нового пира сверка сетью не считает и не снимает.
	if allow, _ := sim.state(peerFixturePubKey, ""); !slices.Equal(allow, []string{"10.9.0.7/255.255.255.255", "192.168.77.0/255.255.255.0"}) {
		t.Fatalf("allow = %v", allow)
	}
}

func TestServersHandler_AddServerPeer_OverlapWithLAN_NoRCI(t *testing.T) {
	h, store, poster, p, _ := newServersSubnetHarness(t, `[]`, `[]`)
	stubPeerKeygen(t)
	rr := postServerPeer(t, h, `{"description":"Phone","tunnelIP":"10.9.0.7/32","remoteSubnets":["192.168.1.128/25"]}`)
	body := decodeJSONBody(t, rr)
	if rr.Code != http.StatusBadRequest || body["code"] != "REMOTE_SUBNET_OVERLAP" || !strings.Contains(body["message"].(string), "Home") {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(poster.snapshot()) != 0 || len(p.invalidated()) != 0 {
		t.Fatal("RCI/публикация при отказе валидации")
	}
	if _, ok := store.GetServerPeerSecret("Wireguard0", peerFixturePubKey); ok {
		t.Fatal("секрет записан")
	}
}

func TestServersHandler_AddServerPeer_RouteFailure_RollsBackAll(t *testing.T) {
	h, store, poster, _, _ := newServersSubnetHarness(t, `[]`, `[]`)
	stubPeerKeygen(t)
	poster.setFailOn(func(payload string) error {
		if strings.Contains(payload, `"comment":"awgm-peer:`) {
			return errors.New("route refused")
		}
		return nil
	})
	rr := postServerPeer(t, h, `{"description":"Phone","tunnelIP":"10.9.0.7/32","remoteSubnets":["192.168.77.0/24"]}`)
	if rr.Code != http.StatusBadRequest || decodeJSONBody(t, rr)["code"] != "ADD_PEER_FAILED" {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	posts := poster.snapshot()
	if idx(posts, sysAllow77Off) < 0 || idx(posts, `{"key":"`+peerFixturePubKey+`","no":true}`) < 0 {
		t.Fatalf("нет отката allow-ips/пира:\n%s", strings.Join(posts, "\n"))
	}
	if _, ok := store.GetServerPeerSecret("Wireguard0", peerFixturePubKey); ok {
		t.Fatal("секрет пережил отказ")
	}
}

func TestServersHandler_UpdateServerPeer_DiffAndSecret(t *testing.T) {
	h, store, poster, _, _ := newServersSubnetHarness(t, `[{"address":"10.9.0.2","mask":"255.255.255.255"},{"address":"192.168.77.0","mask":"255.255.255.0"}]`, sysRcOurs77)
	_ = store.SetServerPeerSecret("Wireguard0", peerFixturePubKey, storage.ServerPeerSecret{PrivateKey: "P", TunnelIP: "10.9.0.2/32", RemoteSubnets: []string{"192.168.77.0/24"}})
	rr := putServerPeer(t, h, peerFixturePubKey, `{"description":"phone","tunnelIP":"10.9.0.2/32","remoteSubnets":["192.168.78.0/24"],"clientAllowedIPs":"0.0.0.0/1"}`)
	if rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	posts := poster.snapshot()
	for _, want := range []string{sysAllow78, sysAllow77Off, sysRoute77Off, `"network":"192.168.78.0"`} {
		if idx(posts, want) < 0 {
			t.Fatalf("нет %s:\n%s", want, strings.Join(posts, "\n"))
		}
	}
	sec, _ := store.GetServerPeerSecret("Wireguard0", peerFixturePubKey)
	if sec.ClientAllowedIPs != "0.0.0.0/1" || len(sec.RemoteSubnets) != 1 || sec.RemoteSubnets[0] != "192.168.78.0/24" {
		t.Fatalf("secret = %+v", sec)
	}
}

// Review Focus 2: старый /32 берётся из записи, не «первый /32 в allow-ips»;
// сети за клиентом смену адреса переживают.
func TestServersHandler_UpdateServerPeer_UpdateTunnelIP_OldIPFromSecretKeepsSubnets(t *testing.T) {
	h, _, poster, _, _ := newServersSubnetHarness(t, `[{"address":"192.168.77.1","mask":"255.255.255.255"},{"address":"10.9.0.2","mask":"255.255.255.255"}]`, `[]`)
	_ = h.settings.SetServerPeerSecret("Wireguard0", peerFixturePubKey, storage.ServerPeerSecret{PrivateKey: "P", TunnelIP: "10.9.0.2/32", RemoteSubnets: []string{"192.168.77.1/32"}})
	rr := putServerPeer(t, h, peerFixturePubKey, `{"description":"phone","tunnelIP":"10.9.0.9/32","remoteSubnets":["192.168.77.1/32"]}`)
	if rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	joined := strings.Join(poster.snapshot(), "\n")
	if !strings.Contains(joined, `{"address":"10.9.0.2","mask":"255.255.255.255","no":true}`) {
		t.Fatalf("снят не тот /32:\n%s", joined)
	}
	if strings.Contains(joined, `{"address":"192.168.77.1","mask":"255.255.255.255","no":true}`) {
		t.Fatalf("сеть за клиентом снята при смене адреса:\n%s", joined)
	}
}

func TestServersHandler_UpdateServerPeer_NoSecretGate(t *testing.T) {
	h, _, poster, _, _ := newServersSubnetHarness(t, `[{"address":"10.9.0.2","mask":"255.255.255.255"}]`, `[]`)
	for _, body := range []string{`{"description":"phone","remoteSubnets":["192.168.77.0/24"]}`, `{"description":"phone","clientAllowedIPs":"0.0.0.0/1"}`} {
		rr := putServerPeer(t, h, peerFixturePubKey, body)
		if rr.Code != http.StatusBadRequest || decodeJSONBody(t, rr)["code"] != "NO_PEER_SECRET" {
			t.Fatalf("%s: code=%d body=%s", body, rr.Code, rr.Body.String())
		}
	}
	if len(poster.snapshot()) != 0 {
		t.Fatal("RCI при NO_PEER_SECRET")
	}
}

// Review Focus 4: фронт всегда шлёт оба поля пустыми — чужой пир правится.
func TestServersHandler_UpdateServerPeer_ForeignPeer_EmptyNetworksPass(t *testing.T) {
	h, _, poster, _, _ := newServersSubnetHarness(t, `[{"address":"10.9.0.2","mask":"255.255.255.255"}]`, `[]`)
	rr := putServerPeer(t, h, peerFixturePubKey, `{"description":"laptop","tunnelIP":"10.9.0.2/32","dns":"","clientAllowedIPs":"","remoteSubnets":[]}`)
	if rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if joined := strings.Join(poster.snapshot(), "\n"); !strings.Contains(joined, `"comment":"laptop"`) || strings.Contains(joined, "allow-ips") {
		t.Fatalf("posts:\n%s", joined)
	}
}

func TestServersHandler_DeleteServerPeer_RoutesBeforePeer_FailClosed(t *testing.T) {
	h, store, poster, _, _ := newServersSubnetHarness(t, `[]`, sysRcOurs77)
	_ = store.SetServerPeerSecret("Wireguard0", peerFixturePubKey, storage.ServerPeerSecret{PrivateKey: "P", RemoteSubnets: []string{"192.168.77.0/24"}})
	rr := deleteServerPeer(t, h, peerFixturePubKey)
	if rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	posts := poster.snapshot()
	// payload RemovePeer — {"key":…,"no":true} (ключи JSON по алфавиту).
	if iR, iP := idx(posts, sysRoute77Off), idx(posts, `{"key":"`+peerFixturePubKey+`","no":true}`); iR < 0 || iP < 0 || iR > iP {
		t.Fatalf("порядок route=%d peer=%d:\n%s", iR, iP, strings.Join(posts, "\n"))
	}

	h, store, poster, _, _ = newServersSubnetHarness(t, `[]`, sysRcOurs77)
	_ = store.SetServerPeerSecret("Wireguard0", peerFixturePubKey, storage.ServerPeerSecret{PrivateKey: "P", RemoteSubnets: []string{"192.168.77.0/24"}})
	poster.setFailOn(func(payload string) error {
		if strings.Contains(payload, `"route"`) {
			return errors.New("route stuck")
		}
		return nil
	})
	rr = deleteServerPeer(t, h, peerFixturePubKey)
	if rr.Code != http.StatusBadRequest || decodeJSONBody(t, rr)["code"] != "DELETE_PEER_FAILED" {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if idx(poster.snapshot(), `{"key":"`+peerFixturePubKey+`","no":true}`) >= 0 {
		t.Fatal("пир снят при неснятом маршруте")
	}
	if _, ok := store.GetServerPeerSecret("Wireguard0", peerFixturePubKey); !ok {
		t.Fatal("секрет удалён при отказе")
	}
}

func TestServersHandler_DeleteServerPeer_ForeignRouteUntouched(t *testing.T) {
	h, store, poster, _, _ := newServersSubnetHarness(t, `[]`, `[{"network":"192.168.77.0","mask":"255.255.255.0","interface":"Wireguard0","auto":true,"comment":"manual"}]`)
	_ = store.SetServerPeerSecret("Wireguard0", peerFixturePubKey, storage.ServerPeerSecret{PrivateKey: "P", RemoteSubnets: []string{"192.168.77.0/24"}})
	if rr := deleteServerPeer(t, h, peerFixturePubKey); rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if idx(poster.snapshot(), `"route"`) >= 0 {
		t.Fatalf("чужой маршрут тронут:\n%s", strings.Join(poster.snapshot(), "\n"))
	}
}

func TestServersHandler_ServerPeerPresets(t *testing.T) {
	h, _, _, _, _ := newServersSubnetHarness(t, `[]`, `[]`)
	old := netif.RouterLANIP
	netif.RouterLANIP = func(string) string { return "192.168.1.1" }
	t.Cleanup(func() { netif.RouterLANIP = old })
	get := func(q string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		h.Subtree(rr, httptest.NewRequest(http.MethodGet, "/api/servers/Wireguard0/peers/presets"+q, nil))
		return rr
	}
	rr := get("")
	body := rr.Body.String()
	if rr.Code != 200 || !strings.Contains(body, `"routerOnly":"10.9.0.0/24, 192.168.1.0/24"`) || !strings.Contains(body, "192.168.1.1/32") || !strings.Contains(body, `::/0"`) {
		t.Fatalf("code=%d body=%s", rr.Code, body)
	}
	if body := get("?dns=8.8.8.8").Body.String(); strings.Contains(body, "/32") {
		t.Fatalf("резолвер вне сетей роутера получил /32: %s", body)
	}
	if body := get("?dns=zzz").Body.String(); !strings.Contains(body, "INVALID_PEER_DNS") {
		t.Fatalf("body=%s", body)
	}
}

func TestServersHandler_EnrichServerDTO_CarriesSubnetsAndTunnelIP(t *testing.T) {
	h, store, _, _, _ := newServersSubnetHarness(t, `[]`, `[]`)
	_ = store.SetServerPeerSecret("Wireguard0", peerFixturePubKey, storage.ServerPeerSecret{PrivateKey: "P", TunnelIP: "10.9.0.2/32", ClientAllowedIPs: "0.0.0.0/1", RemoteSubnets: []string{"192.168.77.0/24"}})
	srv := ndms.WireguardServer{ID: "Wireguard0", Peers: []ndms.WireguardServerPeer{{PublicKey: peerFixturePubKey}}}
	dto := h.enrichServerDTO(context.Background(), srv)
	p := dto.Peers[0]
	if p.TunnelIP != "10.9.0.2/32" || p.ClientAllowedIPs != "0.0.0.0/1" || len(p.RemoteSubnets) != 1 {
		t.Fatalf("peer dto = %+v", p)
	}
}

func TestServersHandler_GenerateServerPeerConf_AllowedIPs(t *testing.T) {
	h := newServerConfHarness(t, `{"jc":"0"}`)
	server := &ndms.WireguardServer{ID: harnessServerID, ListenPort: 51820, MTU: 1420}
	conf, err := h.generateServerPeerConf(context.Background(), server, peerFixturePubKey, storage.ServerPeerSecret{PrivateKey: "PRIV", TunnelIP: "10.9.0.2/32"}, "1.2.3.4")
	if err != nil || !strings.Contains(conf, "\nAllowedIPs = 0.0.0.0/0, ::/0\n") {
		t.Fatalf("default: %v\n%s", err, conf)
	}
	conf, err = h.generateServerPeerConf(context.Background(), server, peerFixturePubKey, storage.ServerPeerSecret{PrivateKey: "PRIV", TunnelIP: "10.9.0.2/32", ClientAllowedIPs: "10.9.0.0/24, 192.168.1.0/24"}, "1.2.3.4")
	if err != nil || !strings.Contains(conf, "\nAllowedIPs = 10.9.0.0/24, 192.168.1.0/24\n") || strings.Contains(conf, "0.0.0.0/0") {
		t.Fatalf("custom: %v\n%s", err, conf)
	}
}

// Свойство 2: tunnel IP и сети меняются вместе, сверка отказала — старый /32
// возвращается на роутер, запись не тронута.
func TestServersHandler_UpdateServerPeer_TunnelIPAndSubnets_ApplyFails_RestoresIP(t *testing.T) {
	h, store, poster, _, _ := newServersSubnetHarness(t, `[{"address":"10.9.0.2","mask":"255.255.255.255"}]`, `[]`)
	_ = store.SetServerPeerSecret("Wireguard0", peerFixturePubKey, storage.ServerPeerSecret{PrivateKey: "P", TunnelIP: "10.9.0.2/32"})
	poster.setFailOn(func(payload string) error {
		if strings.Contains(payload, `"comment":"awgm-peer:`) {
			return errors.New("route refused")
		}
		return nil
	})
	rr := putServerPeer(t, h, peerFixturePubKey, `{"description":"phone","tunnelIP":"10.9.0.9/32","remoteSubnets":["192.168.77.0/24"]}`)
	if rr.Code != http.StatusBadRequest || decodeJSONBody(t, rr)["code"] != "UPDATE_PEER_FAILED" {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	posts := poster.snapshot()
	iRoute := idx(posts, `"comment":"awgm-peer:`)
	restore := -1
	for i := iRoute + 1; i < len(posts); i++ {
		if strings.Contains(posts[i], `{"address":"10.9.0.2","mask":"255.255.255.255"}`) {
			restore = i
		}
	}
	if iRoute < 0 || restore < 0 {
		t.Fatalf("старый /32 не возвращён:\n%s", strings.Join(posts, "\n"))
	}
	if i := idx(posts[iRoute+1:], new9Off); i < 0 {
		t.Fatalf("новый /32 не снят при откате:\n%s", strings.Join(posts, "\n"))
	}
	sec, _ := store.GetServerPeerSecret("Wireguard0", peerFixturePubKey)
	if sec.TunnelIP != "10.9.0.2/32" || len(sec.RemoteSubnets) != 0 {
		t.Fatalf("запись тронута: %+v", sec)
	}
}

// breakSettingsSave — следующая запись настроек отказывает, в т.ч. под root
// (CI): chmod root не останавливает, а rename файла поверх непустого каталога
// на месте settings.json отказывает любому пользователю. Кэш стора загружен.
func breakSettingsSave(t *testing.T, store *storage.SettingsStore) {
	t.Helper()
	path := filepath.Join(store.DataDir(), "settings.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "busy"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// Отказ записи секрета после успеха на роутере: сети снимаются, /32
// возвращается — иначе маршрут без записи никто уже не снимет.
func TestServersHandler_UpdateServerPeer_SaveFails_UndoesRouter(t *testing.T) {
	h, store, poster, _, _, fg := newServersSubnetHarnessFG(t, `[]`, `[]`, "")
	sim := newSysSimRouter(t, fg.FakeGetter, poster, `[]`)
	sim.seed(peerFixturePubKey, "10.9.0.2/32")
	_ = store.SetServerPeerSecret("Wireguard0", peerFixturePubKey, storage.ServerPeerSecret{PrivateKey: "P", TunnelIP: "10.9.0.2/32"})
	breakSettingsSave(t, store)
	rr := putServerPeer(t, h, peerFixturePubKey, `{"description":"phone","tunnelIP":"10.9.0.9/32","remoteSubnets":["192.168.77.0/24"]}`)
	if rr.Code != http.StatusBadRequest || decodeJSONBody(t, rr)["code"] != "SAVE_FAILED" {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	posts := poster.snapshot()
	iAllow, iAllowOff, iRouteOff := idx(posts, sysAllow77), idx(posts, sysAllow77Off), idx(posts, sysRoute77Off)
	if iAllow < 0 || iAllowOff < iAllow || iRouteOff < iAllow {
		t.Fatalf("сети не сняты после отказа записи:\n%s", strings.Join(posts, "\n"))
	}
	if !strings.Contains(posts[len(posts)-1], `{"address":"10.9.0.2","mask":"255.255.255.255"}`) {
		t.Fatalf("старый /32 не возвращён последним:\n%s", strings.Join(posts, "\n"))
	}
	if allow, routes := sim.state(peerFixturePubKey, "awgm-peer:AB/CD+EF"); !slices.Equal(allow, []string{"10.9.0.2/255.255.255.255"}) || len(routes) != 0 {
		t.Fatalf("роутер не возвращён к записи: allow=%v routes=%v", allow, routes)
	}
}

// Отказ сетей при добавлении, и пир с роутера не снялся — секрет остаётся:
// без него пир на роутере — сирота с потерянным ключом.
func TestServersHandler_AddServerPeer_RollbackRemoveFails_KeepsSecret(t *testing.T) {
	h, store, poster, _, _ := newServersSubnetHarness(t, `[]`, `[]`)
	stubPeerKeygen(t)
	poster.setFailOn(func(payload string) error {
		if strings.Contains(payload, `"comment":"awgm-peer:`) || strings.Contains(payload, `{"key":"`+peerFixturePubKey+`","no":true}`) {
			return errors.New("refused")
		}
		return nil
	})
	rr := postServerPeer(t, h, `{"description":"Phone","tunnelIP":"10.9.0.7/32","remoteSubnets":["192.168.77.0/24"]}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	sec, ok := store.GetServerPeerSecret("Wireguard0", peerFixturePubKey)
	if !ok {
		t.Fatal("секрет удалён, хотя пир остался на роутере")
	}
	// Final review M5: сверка свои сети откатила — в оставленном секрете их нет.
	if len(sec.RemoteSubnets) != 0 {
		t.Fatalf("в секрете сети, которых нет на роутере: %v", sec.RemoteSubnets)
	}
}

// Откат Reconcile сам не завершился (allow-ips не снялись) и пир не снялся:
// секрет остаётся, но сетей не держит — остаток на роутере увидит сверка
// следующего сохранения, маршруты снимет удаление пира (оба читают роутер).
func TestServersHandler_AddServerPeer_RollbackRemoveFails_ReconcileRollbackFails(t *testing.T) {
	h, store, poster, _, _ := newServersSubnetHarness(t, `[]`, `[]`)
	stubPeerKeygen(t)
	poster.setFailOn(func(payload string) error {
		if strings.Contains(payload, `"comment":"awgm-peer:`) || strings.Contains(payload, sysAllow77Off) ||
			strings.Contains(payload, `{"key":"`+peerFixturePubKey+`","no":true}`) {
			return errors.New("refused")
		}
		return nil
	})
	rr := postServerPeer(t, h, `{"description":"Phone","tunnelIP":"10.9.0.7/32","remoteSubnets":["192.168.77.0/24"]}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if sec, ok := store.GetServerPeerSecret("Wireguard0", peerFixturePubKey); !ok || len(sec.RemoteSubnets) != 0 {
		t.Fatalf("secret = %+v ok=%v", sec, ok)
	}
}

// Final review M6: Commands без Routes — чистый отказ до RCI, не nil-паника в Apply.
func TestServersHandler_AddServerPeer_NoRoutesCommands_Refused(t *testing.T) {
	h, _, poster, _, _ := newServersSubnetHarness(t, `[]`, `[]`)
	stubPeerKeygen(t)
	h.SetCommands(&ndmscommand.Commands{Wireguard: h.commands.Wireguard})
	rr := postServerPeer(t, h, `{"description":"Phone","tunnelIP":"10.9.0.7/32","remoteSubnets":["192.168.77.0/24"]}`)
	if rr.Code == http.StatusOK || !strings.Contains(rr.Body.String(), "INTERNAL_ERROR") {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if n := len(poster.snapshot()); n != 0 {
		t.Fatalf("RCI дёрнут: %d", n)
	}
}

// Занятость адреса при добавлении: у пира с записью адрес берётся из неё, а не
// из «первого /32» в allow-ips (там может стоять сеть за клиентом).
func TestServersHandler_AddServerPeer_TunnelIPInUse_FromSecret(t *testing.T) {
	h, store, poster, _, _ := newServersSubnetHarness(t, `[{"address":"192.168.77.1","mask":"255.255.255.255"},{"address":"10.9.0.2","mask":"255.255.255.255"}]`, `[]`)
	_ = store.SetServerPeerSecret("Wireguard0", peerFixturePubKey, storage.ServerPeerSecret{PrivateKey: "P", TunnelIP: "10.9.0.2/32", RemoteSubnets: []string{"192.168.77.1/32"}})
	stubPeerKeygen(t)
	rr := postServerPeer(t, h, `{"description":"Tablet","tunnelIP":"10.9.0.2/32"}`)
	if rr.Code != http.StatusBadRequest || decodeJSONBody(t, rr)["code"] != "TUNNEL_IP_IN_USE" {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(poster.snapshot()) != 0 {
		t.Fatal("RCI при занятом адресе")
	}
}

// W2-P1 (решение владельца 28.09): поля сетей отсутствуют или null — сети
// пира не трогаются ни на роутере, ни в записи.
func TestServersHandler_UpdateServerPeer_AbsentOrNullNetworksUntouched(t *testing.T) {
	for _, body := range []string{
		`{"description":"renamed","tunnelIP":"10.9.0.2/32"}`,
		`{"description":"renamed","tunnelIP":"10.9.0.2/32","clientAllowedIPs":null,"remoteSubnets":null}`,
	} {
		h, store, poster, _, _ := newServersSubnetHarness(t, `[{"address":"10.9.0.2","mask":"255.255.255.255"},{"address":"192.168.77.0","mask":"255.255.255.0"}]`, sysRcOurs77)
		_ = store.SetServerPeerSecret("Wireguard0", peerFixturePubKey, storage.ServerPeerSecret{PrivateKey: "P", TunnelIP: "10.9.0.2/32", ClientAllowedIPs: "0.0.0.0/1", RemoteSubnets: []string{"192.168.77.0/24"}})
		if rr := putServerPeer(t, h, peerFixturePubKey, body); rr.Code != 200 {
			t.Fatalf("%s: code=%d body=%s", body, rr.Code, rr.Body.String())
		}
		if joined := strings.Join(poster.snapshot(), "\n"); strings.Contains(joined, "allow-ips") || strings.Contains(joined, `"route"`) {
			t.Fatalf("%s: сети тронуты:\n%s", body, joined)
		}
		sec, _ := store.GetServerPeerSecret("Wireguard0", peerFixturePubKey)
		if sec.Description != "renamed" || sec.ClientAllowedIPs != "0.0.0.0/1" || len(sec.RemoteSubnets) != 1 || sec.RemoteSubnets[0] != "192.168.77.0/24" {
			t.Fatalf("%s: secret = %+v", body, sec)
		}
	}
}

// Явные "" и [] — очистить: сети снимаются, запись пустеет.
func TestServersHandler_UpdateServerPeer_EmptyNetworksClear(t *testing.T) {
	h, store, poster, _, _ := newServersSubnetHarness(t, `[{"address":"10.9.0.2","mask":"255.255.255.255"},{"address":"192.168.77.0","mask":"255.255.255.0"}]`, sysRcOurs77)
	_ = store.SetServerPeerSecret("Wireguard0", peerFixturePubKey, storage.ServerPeerSecret{PrivateKey: "P", TunnelIP: "10.9.0.2/32", ClientAllowedIPs: "0.0.0.0/1", RemoteSubnets: []string{"192.168.77.0/24"}})
	rr := putServerPeer(t, h, peerFixturePubKey, `{"description":"phone","tunnelIP":"10.9.0.2/32","clientAllowedIPs":"","remoteSubnets":[]}`)
	if rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	posts := poster.snapshot()
	if idx(posts, sysAllow77Off) < 0 || idx(posts, sysRoute77Off) < 0 {
		t.Fatalf("сети не сняты:\n%s", strings.Join(posts, "\n"))
	}
	sec, _ := store.GetServerPeerSecret("Wireguard0", peerFixturePubKey)
	if sec.ClientAllowedIPs != "" || len(sec.RemoteSubnets) != 0 {
		t.Fatalf("secret = %+v", sec)
	}
}

// Чужой пир (без записи): правка без полей сетей проходит.
func TestServersHandler_UpdateServerPeer_ForeignPeer_AbsentNetworksPass(t *testing.T) {
	h, _, _, _, _ := newServersSubnetHarness(t, `[{"address":"10.9.0.2","mask":"255.255.255.255"}]`, `[]`)
	if rr := putServerPeer(t, h, peerFixturePubKey, `{"description":"laptop","tunnelIP":"10.9.0.2/32"}`); rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
}

const (
	old2On  = `{"address":"10.9.0.2","mask":"255.255.255.255"}`
	new9On  = `{"address":"10.9.0.9","mask":"255.255.255.255"}`
	new9Off = `{"address":"10.9.0.9","mask":"255.255.255.255","no":true}`
)

// T9: старый /32 снят, новый роутер не принял — старый возвращается, запись
// не тронута. Иначе пир без адреса, а повтор со старым IP — пустой diff.
func TestServersHandler_UpdateServerPeer_NewIPAddFails_RestoresOld(t *testing.T) {
	h, store, poster, p, _ := newServersSubnetHarness(t, `[{"address":"10.9.0.2","mask":"255.255.255.255"}]`, `[]`)
	_ = store.SetServerPeerSecret("Wireguard0", peerFixturePubKey, storage.ServerPeerSecret{PrivateKey: "P", TunnelIP: "10.9.0.2/32"})
	poster.setFailOn(func(payload string) error {
		if strings.Contains(payload, new9On) {
			return errors.New("add refused")
		}
		return nil
	})
	rr := putServerPeer(t, h, peerFixturePubKey, `{"description":"phone","tunnelIP":"10.9.0.9/32"}`)
	if rr.Code != http.StatusBadRequest || decodeJSONBody(t, rr)["code"] != "UPDATE_PEER_FAILED" {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	posts := poster.snapshot()
	iAdd := idx(posts, new9On)
	if iAdd < 0 || idx(posts[iAdd+1:], new9Off) < 0 || !strings.Contains(posts[len(posts)-1], old2On) {
		t.Fatalf("после отказа нужны снятие нового и возврат старого последним:\n%s", strings.Join(posts, "\n"))
	}
	sec, _ := store.GetServerPeerSecret("Wireguard0", peerFixturePubKey)
	if sec.TunnelIP != "10.9.0.2/32" {
		t.Fatalf("запись тронута: %+v", sec)
	}
	if len(p.invalidated()) != 0 {
		t.Fatal("публикация при отказе")
	}
}

// T9: у пира не было /32 (чужая форма allow-ips), следующий шаг отказал —
// откат снимает новый /32, возвращать нечего.
func TestServersHandler_UpdateServerPeer_NoOldIP_LaterFails_RemovesNew(t *testing.T) {
	h, _, poster, _, _ := newServersSubnetHarness(t, `[]`, `[]`)
	poster.setFailOn(func(payload string) error {
		if strings.Contains(payload, `"comment":"renamed"`) {
			return errors.New("comment refused")
		}
		return nil
	})
	rr := putServerPeer(t, h, peerFixturePubKey, `{"description":"renamed","tunnelIP":"10.9.0.9/32"}`)
	if rr.Code != http.StatusBadRequest || decodeJSONBody(t, rr)["code"] != "UPDATE_PEER_FAILED" {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	posts := poster.snapshot()
	iComment := idx(posts, `"comment":"renamed"`)
	if iComment < 0 || idx(posts[iComment+1:], new9Off) < 0 {
		t.Fatalf("новый /32 не снят после отказа:\n%s", strings.Join(posts, "\n"))
	}
}

// F512: новый tunnel IP занят другим пиром — по записи или по эвристике
// «первый /32» у пира без записи. Отказ как в Add, ни одного RCI.
func TestServersHandler_UpdateServerPeer_TunnelIPInUse(t *testing.T) {
	for _, tc := range []struct {
		name, otherAllow, otherSecretIP, body string
		wantCode                              string
	}{
		{"занят по записи", `[{"address":"192.168.77.0","mask":"255.255.255.0"}]`, "10.9.0.5/32", `{"description":"phone","tunnelIP":"10.9.0.5/32"}`, "TUNNEL_IP_IN_USE"},
		{"занят по эвристике", `[{"address":"10.9.0.6","mask":"255.255.255.255"}]`, "", `{"description":"phone","tunnelIP":"10.9.0.6/32"}`, "TUNNEL_IP_IN_USE"},
		{"свой же адрес", `[{"address":"10.9.0.6","mask":"255.255.255.255"}]`, "", `{"description":"phone","tunnelIP":"10.9.0.2/32"}`, ""},
		{"свободный", `[{"address":"10.9.0.6","mask":"255.255.255.255"}]`, "10.9.0.5/32", `{"description":"phone","tunnelIP":"10.9.0.9/32"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, store, poster, _, _ := newServersSubnetHarnessWithOther(t, `[{"address":"10.9.0.2","mask":"255.255.255.255"}]`, `[]`, tc.otherAllow)
			_ = store.SetServerPeerSecret("Wireguard0", peerFixturePubKey, storage.ServerPeerSecret{PrivateKey: "P", TunnelIP: "10.9.0.2/32"})
			if tc.otherSecretIP != "" {
				_ = store.SetServerPeerSecret("Wireguard0", otherPeerPubKey, storage.ServerPeerSecret{PrivateKey: "Q", TunnelIP: tc.otherSecretIP})
			}
			rr := putServerPeer(t, h, peerFixturePubKey, tc.body)
			if tc.wantCode == "" {
				if rr.Code != 200 {
					t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
				}
				return
			}
			if rr.Code != http.StatusBadRequest || decodeJSONBody(t, rr)["code"] != tc.wantCode {
				t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
			}
			if n := len(poster.snapshot()); n != 0 {
				t.Fatalf("RCI при занятом адресе: %d", n)
			}
			if sec, _ := store.GetServerPeerSecret("Wireguard0", peerFixturePubKey); sec.TunnelIP != "10.9.0.2/32" {
				t.Fatalf("запись тронута: %+v", sec)
			}
		})
	}
}

// F509: «было» читается с роутера. В записи [77], на роутере ещё сирота 78
// (allow-ip и наш маршрут): сохранение [77] снимает 78, по 77 — ни одного вызова.
func TestServersHandler_UpdateServerPeer_HealsRouterDrift(t *testing.T) {
	rc := `[{"network":"192.168.77.0","mask":"255.255.255.0","interface":"Wireguard0","comment":"awgm-peer:AB/CD+EF"},
		{"network":"192.168.78.0","mask":"255.255.255.0","interface":"Wireguard0","comment":"awgm-peer:AB/CD+EF"}]`
	h, store, poster, _, _, fg := newServersSubnetHarnessFG(t, `[]`, rc, "")
	sim := newSysSimRouter(t, fg.FakeGetter, poster, rc)
	sim.seed(peerFixturePubKey, "10.9.0.2/32", "192.168.77.0/24", "192.168.78.0/24")
	_ = store.SetServerPeerSecret("Wireguard0", peerFixturePubKey, storage.ServerPeerSecret{PrivateKey: "P", Description: "phone", TunnelIP: "10.9.0.2/32", RemoteSubnets: []string{"192.168.77.0/24"}})
	rr := putServerPeer(t, h, peerFixturePubKey, `{"description":"phone","tunnelIP":"10.9.0.2/32","remoteSubnets":["192.168.77.0/24"]}`)
	if rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	posts := poster.snapshot()
	if len(posts) != 2 || idx(posts, `{"address":"192.168.78.0","mask":"255.255.255.0","no":true}`) != 0 || idx(posts, `"network":"192.168.78.0","no":true`) != 1 {
		t.Fatalf("posts:\n%s", strings.Join(posts, "\n"))
	}
	allow, routes := sim.state(peerFixturePubKey, "awgm-peer:AB/CD+EF")
	if !slices.Equal(allow, []string{"10.9.0.2/255.255.255.255", "192.168.77.0/255.255.255.0"}) || !slices.Equal(routes, []string{"192.168.77.0"}) {
		t.Fatalf("роутер: allow=%v routes=%v", allow, routes)
	}
}

// Пир без записи (заведён в веб-морде): фронт шлёт remoteSubnets:[] всегда —
// сверки нет, его собственные allow-ips не «лишние».
func TestServersHandler_UpdateServerPeer_ForeignPeer_AllowIPsNotReconciled(t *testing.T) {
	h, _, poster, _, _ := newServersSubnetHarness(t, `[{"address":"10.9.0.2","mask":"255.255.255.255"},{"address":"192.168.50.0","mask":"255.255.255.0"}]`, `[]`)
	rr := putServerPeer(t, h, peerFixturePubKey, `{"description":"phone","tunnelIP":"10.9.0.2/32","remoteSubnets":[]}`)
	if rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if joined := strings.Join(poster.snapshot(), "\n"); strings.Contains(joined, "allow-ips") {
		t.Fatalf("allow-ips чужого пира тронуты:\n%s", joined)
	}
}

// Удаление пира снимает все маршруты с его меткой, найденные на роутере, —
// и сироту, которой в записи нет.
func TestServersHandler_DeleteServerPeer_SweepsOrphanRoutes(t *testing.T) {
	rc := `[{"network":"192.168.78.0","mask":"255.255.255.0","interface":"Wireguard0","comment":"awgm-peer:AB/CD+EF"}]`
	h, store, poster, _, _, fg := newServersSubnetHarnessFG(t, `[]`, rc, "")
	sim := newSysSimRouter(t, fg.FakeGetter, poster, rc)
	sim.seed(peerFixturePubKey, "10.9.0.2/32")
	_ = store.SetServerPeerSecret("Wireguard0", peerFixturePubKey, storage.ServerPeerSecret{PrivateKey: "P", TunnelIP: "10.9.0.2/32"})
	if rr := deleteServerPeer(t, h, peerFixturePubKey); rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if _, routes := sim.state(peerFixturePubKey, "awgm-peer:AB/CD+EF"); len(routes) != 0 {
		t.Fatalf("сирота осталась: %v", routes)
	}
}

// Откат добавления: сверка отказала, и её собственный откат маршрута не
// прошёл (RollbackError) — маршрут-сироту снимает откат пира по метке с роутера.
func TestServersHandler_AddServerPeer_RollbackSweepsOrphanRoute(t *testing.T) {
	h, store, poster, _, _, fg := newServersSubnetHarnessFG(t, `[]`, `[]`, "")
	stubPeerKeygen(t)
	sim := newSysSimRouter(t, fg.FakeGetter, poster, `[]`)
	failedOnce := false
	sim.fail = func(payload string) error {
		switch {
		case strings.Contains(payload, `"comment":"awgm-peer:`) && strings.Contains(payload, `"network":"192.168.78.0"`):
			return errors.New("route refused")
		case strings.Contains(payload, `"network":"192.168.77.0","no":true`) && !failedOnce:
			failedOnce = true
			return errors.New("route stuck")
		}
		return nil
	}
	rr := postServerPeer(t, h, `{"description":"Phone","tunnelIP":"10.9.0.7/32","remoteSubnets":["192.168.77.0/24","192.168.78.0/24"]}`)
	if rr.Code != http.StatusBadRequest || !failedOnce {
		t.Fatalf("code=%d body=%s failedOnce=%v", rr.Code, rr.Body.String(), failedOnce)
	}
	if _, routes := sim.state(peerFixturePubKey, "awgm-peer:AB/CD+EF"); len(routes) != 0 {
		t.Fatalf("маршрут-сирота: %v", routes)
	}
	if _, ok := store.GetServerPeerSecret("Wireguard0", peerFixturePubKey); ok {
		t.Fatal("секрет пережил откат")
	}
}

// F508: блокировка общая с managed-путём — пока она взята, правка сетей
// системного пира (создание и изменение) не начинается; правка без сетей её
// не ждёт.
func TestServersHandler_PeerSubnets_SharedLockWithManaged(t *testing.T) {
	cases := []struct {
		name  string
		call  func(h *ServersHandler) *httptest.ResponseRecorder
		waits bool
	}{
		{"add", func(h *ServersHandler) *httptest.ResponseRecorder {
			return postServerPeer(t, h, `{"description":"Phone","tunnelIP":"10.9.0.7/32","remoteSubnets":["192.168.77.0/24"]}`)
		}, true},
		{"update", func(h *ServersHandler) *httptest.ResponseRecorder {
			return putServerPeer(t, h, peerFixturePubKey, `{"description":"phone","tunnelIP":"10.9.0.2/32","remoteSubnets":["192.168.77.0/24"]}`)
		}, true},
		{"update without subnets", func(h *ServersHandler) *httptest.ResponseRecorder {
			return putServerPeer(t, h, peerFixturePubKey, `{"description":"renamed","tunnelIP":"10.9.0.2/32"}`)
		}, false},
		// M2: [] у пира без сетей — не сверка, блокировка не нужна.
		{"update empty to empty", func(h *ServersHandler) *httptest.ResponseRecorder {
			return putServerPeer(t, h, peerFixturePubKey, `{"description":"renamed","tunnelIP":"10.9.0.2/32","remoteSubnets":[]}`)
		}, false},
		// Fix round 2: смена адреса без сетей — под той же блокировкой.
		{"update tunnel ip", func(h *ServersHandler) *httptest.ResponseRecorder {
			return putServerPeer(t, h, peerFixturePubKey, `{"description":"phone","tunnelIP":"10.9.0.9/32"}`)
		}, true},
		// I1: удаление — под той же блокировкой.
		{"delete", func(h *ServersHandler) *httptest.ResponseRecorder { return deleteServerPeer(t, h, peerFixturePubKey) }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, store, _, _, _ := newServersSubnetHarness(t, `[{"address":"10.9.0.2","mask":"255.255.255.255"}]`, `[]`)
			stubPeerKeygen(t)
			_ = store.SetServerPeerSecret("Wireguard0", peerFixturePubKey, storage.ServerPeerSecret{PrivateKey: "P", TunnelIP: "10.9.0.2/32"})
			unlock := h.managedSvc.LockPeerSubnets()
			done := make(chan int)
			go func() { done <- tc.call(h).Code }()
			select {
			case code := <-done:
				unlock()
				if tc.waits {
					t.Fatalf("правка прошла мимо общей блокировки: code=%d", code)
				}
				return
			case <-time.After(150 * time.Millisecond):
			}
			unlock()
			code := <-done
			if !tc.waits {
				t.Fatal("правка без сетей ждала блокировку")
			}
			if code != 200 {
				t.Fatalf("code=%d", code)
			}
		})
	}
}

// F508: две параллельные правки разных пиров с пересекающимися сетями —
// ровно одна проходит, вторая получает REMOTE_SUBNET_OVERLAP. Первая, дойдя
// до роутера, медлит: без блокировки вторая за это время собрала бы занятые
// до записи первой и прошла бы тоже.
func TestServersHandler_UpdateServerPeer_ConcurrentOverlap_OneWins(t *testing.T) {
	h, store, poster, _, _, fg := newServersSubnetHarnessFG(t, `[]`, `[]`, `[]`)
	sim := newSysSimRouter(t, fg.FakeGetter, poster, `[]`)
	sim.seed(peerFixturePubKey, "10.9.0.2/32")
	sim.seed(otherPeerPubKey, "10.9.0.3/32")
	_ = store.SetServerPeerSecret("Wireguard0", peerFixturePubKey, storage.ServerPeerSecret{PrivateKey: "P", Description: "phone", TunnelIP: "10.9.0.2/32"})
	_ = store.SetServerPeerSecret("Wireguard0", otherPeerPubKey, storage.ServerPeerSecret{PrivateKey: "P", Description: "tablet", TunnelIP: "10.9.0.3/32"})
	var once sync.Once
	sim.fail = func(payload string) error {
		if strings.Contains(payload, "allow-ips") {
			once.Do(func() { time.Sleep(200 * time.Millisecond) })
		}
		return nil
	}
	var wg sync.WaitGroup
	codes := make([]string, 2)
	for i, c := range []struct{ key, desc, ip, net string }{
		{peerFixturePubKey, "phone", "10.9.0.2/32", "192.168.77.0/24"},
		{otherPeerPubKey, "tablet", "10.9.0.3/32", "192.168.77.0/25"},
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rr := putServerPeer(t, h, c.key, `{"description":"`+c.desc+`","tunnelIP":"`+c.ip+`","remoteSubnets":["`+c.net+`"]}`)
			if rr.Code == 200 {
				codes[i] = "ok"
				return
			}
			var body map[string]any
			_ = json.Unmarshal(rr.Body.Bytes(), &body)
			codes[i], _ = body["code"].(string)
		}()
	}
	wg.Wait()
	slices.Sort(codes)
	if !slices.Equal(codes, []string{"REMOTE_SUBNET_OVERLAP", "ok"}) {
		t.Fatalf("codes = %v", codes)
	}
}

// lockStateOfReads — хук на чтения роутера после первого поста мутации:
// для каждого чтения — была ли блокировка занята (ждём её 100 мс).
func lockStateOfReads(h *ServersHandler, fg *hookGetter, sim *sysSimRouter) func() []bool {
	var mu sync.Mutex
	var armed bool
	var held []bool
	sim.fail = func(string) error { mu.Lock(); armed = true; mu.Unlock(); return nil }
	fg.setHook(func(string) {
		mu.Lock()
		on := armed
		mu.Unlock()
		if !on {
			return
		}
		free := make(chan struct{})
		go func() { h.managedSvc.LockPeerSubnets()(); close(free) }()
		busy := false
		select {
		case <-free:
		case <-time.After(100 * time.Millisecond):
			busy = true
		}
		mu.Lock()
		held = append(held, busy)
		mu.Unlock()
	})
	return func() []bool { mu.Lock(); defer mu.Unlock(); return slices.Clone(held) }
}

// I2: блокировка отпускается до публикации и writeAll — ответ собирается
// (чтения RCI) уже без неё. Для всех трёх операций, которые её берут.
func TestServersHandler_PeerSubnets_LockReleasedBeforeResponse(t *testing.T) {
	cases := []struct {
		name string
		call func(h *ServersHandler) *httptest.ResponseRecorder
	}{
		{"add", func(h *ServersHandler) *httptest.ResponseRecorder {
			return postServerPeer(t, h, `{"description":"Phone","tunnelIP":"10.9.0.7/32","remoteSubnets":["192.168.77.0/24"]}`)
		}},
		{"update", func(h *ServersHandler) *httptest.ResponseRecorder {
			return putServerPeer(t, h, peerFixturePubKey, `{"description":"phone","tunnelIP":"10.9.0.2/32","remoteSubnets":["192.168.77.0/24"]}`)
		}},
		{"delete", func(h *ServersHandler) *httptest.ResponseRecorder { return deleteServerPeer(t, h, peerFixturePubKey) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, store, poster, _, _, fg := newServersSubnetHarnessFG(t, `[]`, `[]`, "")
			stubPeerKeygen(t)
			sim := newSysSimRouter(t, fg.FakeGetter, poster, `[]`)
			sim.seed(peerFixturePubKey, "10.9.0.2/32")
			_ = store.SetServerPeerSecret("Wireguard0", peerFixturePubKey, storage.ServerPeerSecret{PrivateKey: "P", Description: "phone", TunnelIP: "10.9.0.2/32"})
			reads := lockStateOfReads(h, fg, sim)
			if rr := tc.call(h); rr.Code != 200 {
				t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
			}
			// Сверка читает под блокировкой; последнее чтение — сборка ответа
			// (writeAll) — обязано идти уже без неё.
			if got := reads(); len(got) == 0 || got[len(got)-1] {
				t.Fatalf("ответ собирался под блокировкой: %v", got)
			}
		})
	}
}

// M5: смена адреса и сети одним запросом — ни старый, ни новый /32 сетью не
// считаются. Старый роутер «не снял» (остался в allow-ips) — сверка его тоже
// не трогает.
func TestServersHandler_UpdateServerPeer_TunnelIPChange_BothHostsNotSubnets(t *testing.T) {
	h, store, poster, _, _, fg := newServersSubnetHarnessFG(t, `[]`, `[]`, "")
	sim := newSysSimRouter(t, fg.FakeGetter, poster, `[]`)
	sim.seed(peerFixturePubKey, "10.9.0.2/32")
	ignored := false // только снятие при смене адреса; снятие сверкой — настоящее
	sim.ignore = func(payload string) bool {
		if !ignored && strings.Contains(payload, `{"address":"10.9.0.2","mask":"255.255.255.255","no":true}`) {
			ignored = true
			return true
		}
		return false
	}
	_ = store.SetServerPeerSecret("Wireguard0", peerFixturePubKey, storage.ServerPeerSecret{PrivateKey: "P", Description: "phone", TunnelIP: "10.9.0.2/32"})
	rr := putServerPeer(t, h, peerFixturePubKey, `{"description":"phone","tunnelIP":"10.9.0.9/32","remoteSubnets":["192.168.77.0/24"]}`)
	if rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	allow, _ := sim.state(peerFixturePubKey, "")
	if want := []string{"10.9.0.2/255.255.255.255", "10.9.0.9/255.255.255.255", "192.168.77.0/255.255.255.0"}; !slices.Equal(allow, want) {
		t.Fatalf("allow = %v", allow)
	}
}

// M2: [] у пира без сетей — ни чтений роутера, ни постов allow-ips/маршрутов.
func TestServersHandler_UpdateServerPeer_EmptyToEmpty_NoReads(t *testing.T) {
	h, store, poster, _, _, fg := newServersSubnetHarnessFG(t, `[{"address":"10.9.0.2","mask":"255.255.255.255"}]`, `[]`, "")
	_ = store.SetServerPeerSecret("Wireguard0", peerFixturePubKey, storage.ServerPeerSecret{PrivateKey: "P", Description: "phone", TunnelIP: "10.9.0.2/32"})
	if rr := putServerPeer(t, h, peerFixturePubKey, `{"description":"phone","tunnelIP":"10.9.0.2/32","remoteSubnets":[]}`); rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if n := fg.Calls("/show/rc/ip/route"); n != 0 || len(poster.snapshot()) != 0 {
		t.Fatalf("route reads=%d posts=%v", n, poster.snapshot())
	}
}

// I1: сверка прошла, пира на роутере не стало, запись секрета отказала —
// компенсация видит «пир не найден» и не создаёт пира-призрака.
func TestServersHandler_UpdateServerPeer_PeerVanished_NoGhost(t *testing.T) {
	h, store, poster, _, _, fg := newServersSubnetHarnessFG(t, `[]`, `[]`, "")
	sim := newSysSimRouter(t, fg.FakeGetter, poster, `[]`)
	sim.seed(peerFixturePubKey, "10.9.0.2/32")
	_ = store.SetServerPeerSecret("Wireguard0", peerFixturePubKey, storage.ServerPeerSecret{PrivateKey: "P", Description: "phone", TunnelIP: "10.9.0.2/32"})
	breakSettingsSave(t, store)
	vanishedAt := -1
	sim.fail = func(payload string) error {
		if vanishedAt < 0 && strings.Contains(payload, `"comment":"awgm-peer:`) {
			delete(sim.peers, peerFixturePubKey) // под sim.mu: post держит его
			sim.render()
			vanishedAt = len(poster.snapshot())
		}
		return nil
	}
	rr := putServerPeer(t, h, peerFixturePubKey, `{"description":"phone","tunnelIP":"10.9.0.2/32","remoteSubnets":["192.168.77.0/24"]}`)
	if rr.Code != http.StatusBadRequest || decodeJSONBody(t, rr)["code"] != "SAVE_FAILED" || vanishedAt < 0 {
		t.Fatalf("code=%d body=%s vanishedAt=%d", rr.Code, rr.Body.String(), vanishedAt)
	}
	if posts := poster.snapshot(); len(posts) != vanishedAt {
		t.Fatalf("после исчезновения пира ушли посты:\n%s", strings.Join(posts[vanishedAt:], "\n"))
	}
	if sim.has(peerFixturePubKey) {
		t.Fatal("пир-призрак создан")
	}
}

// Fix round 2: пир удалён посреди смены адреса (мимо панели), следующий шаг
// отказал — откат адреса видит «пира нет» и старый /32 не шлёт: allow-ips на
// отсутствующий ключ NDMS создал бы пира-призрака.
func TestServersHandler_UpdateServerPeer_TunnelRevert_PeerDeleted_NoGhost(t *testing.T) {
	h, store, poster, _, _, fg := newServersSubnetHarnessFG(t, `[]`, `[]`, "")
	sim := newSysSimRouter(t, fg.FakeGetter, poster, `[]`)
	sim.seed(peerFixturePubKey, "10.9.0.2/32")
	_ = store.SetServerPeerSecret("Wireguard0", peerFixturePubKey, storage.ServerPeerSecret{PrivateKey: "P", Description: "phone", TunnelIP: "10.9.0.2/32"})
	vanishedAt := -1
	sim.fail = func(payload string) error {
		if vanishedAt < 0 && strings.Contains(payload, `"comment":"renamed"`) {
			delete(sim.peers, peerFixturePubKey) // под sim.mu: post держит его
			sim.render()
			vanishedAt = len(poster.snapshot())
			return errors.New("no such peer")
		}
		return nil
	}
	rr := putServerPeer(t, h, peerFixturePubKey, `{"description":"renamed","tunnelIP":"10.9.0.9/32"}`)
	if rr.Code != http.StatusBadRequest || vanishedAt < 0 {
		t.Fatalf("code=%d body=%s vanishedAt=%d", rr.Code, rr.Body.String(), vanishedAt)
	}
	for _, p := range poster.snapshot()[vanishedAt:] {
		if strings.Contains(p, `"allow-ips":[{"address":"10.9.0.2","mask":"255.255.255.255"}]`) {
			t.Fatalf("старый /32 отправлен удалённому пиру: %s", p)
		}
	}
	if sim.has(peerFixturePubKey) {
		t.Fatal("пир-призрак создан")
	}
}

// Fix round 2: наличие пира перед откатом адреса не прочиталось — старый /32
// не шлётся.
func TestServersHandler_UpdateServerPeer_TunnelRevert_PresenceReadFails_NoAdd(t *testing.T) {
	h, store, poster, _, _, fg := newServersSubnetHarnessFG(t, `[]`, `[]`, "")
	sim := newSysSimRouter(t, fg.FakeGetter, poster, `[]`)
	sim.seed(peerFixturePubKey, "10.9.0.2/32")
	_ = store.SetServerPeerSecret("Wireguard0", peerFixturePubKey, storage.ServerPeerSecret{PrivateKey: "P", Description: "phone", TunnelIP: "10.9.0.2/32"})
	brokeAt := -1
	sim.fail = func(payload string) error {
		if brokeAt < 0 && strings.Contains(payload, `"comment":"renamed"`) {
			fg.SetError("/show/rc/interface/Wireguard0", errors.New("rci down"))
			brokeAt = len(poster.snapshot())
			return errors.New("comment refused")
		}
		return nil
	}
	if rr := putServerPeer(t, h, peerFixturePubKey, `{"description":"renamed","tunnelIP":"10.9.0.9/32"}`); rr.Code != http.StatusBadRequest || brokeAt < 0 {
		t.Fatalf("code=%d brokeAt=%d", rr.Code, brokeAt)
	}
	for _, p := range poster.snapshot()[brokeAt:] {
		if strings.Contains(p, `"allow-ips":[{"address":"10.9.0.2","mask":"255.255.255.255"}]`) {
			t.Fatalf("старый /32 отправлен без проверки наличия пира: %s", p)
		}
	}
}

// Fix round 3: пир в списке сервера (снимок) есть, в свежем rc нет — смена
// адреса отказывает до единого поста: allow-ips на неизвестный ключ создали
// бы призрака.
func TestServersHandler_UpdateServerPeer_TunnelChange_PeerAbsentOnRouter_NoPosts(t *testing.T) {
	h, store, poster, _, _, fg := newServersSubnetHarnessFG(t, `[]`, `[]`, "")
	sim := newSysSimRouter(t, fg.FakeGetter, poster, `[]`) // пира на «роутере» нет
	_ = store.SetServerPeerSecret("Wireguard0", peerFixturePubKey, storage.ServerPeerSecret{PrivateKey: "P", Description: "phone", TunnelIP: "10.9.0.2/32"})
	rr := putServerPeer(t, h, peerFixturePubKey, `{"description":"phone","tunnelIP":"10.9.0.9/32"}`)
	if rr.Code != http.StatusBadRequest || decodeJSONBody(t, rr)["code"] != "NOT_FOUND" {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if posts := poster.snapshot(); len(posts) != 0 || sim.has(peerFixturePubKey) {
		t.Fatalf("posts=%v ghost=%v", posts, sim.has(peerFixturePubKey))
	}
}

// Fix round 4: пир в списке сервера есть, на роутере нет (снят в веб-морде) —
// сверка сетей отказывает до мутаций, ответ — NOT_FOUND, а не «сбой правки».
func TestServersHandler_UpdateServerPeer_Reconcile_PeerAbsent_NotFound(t *testing.T) {
	h, store, poster, _, _, fg := newServersSubnetHarnessFG(t, `[]`, `[]`, "")
	sim := newSysSimRouter(t, fg.FakeGetter, poster, `[]`) // пира на «роутере» нет
	_ = store.SetServerPeerSecret("Wireguard0", peerFixturePubKey, storage.ServerPeerSecret{PrivateKey: "P", Description: "phone", TunnelIP: "10.9.0.2/32"})
	rr := putServerPeer(t, h, peerFixturePubKey, `{"description":"phone","remoteSubnets":["192.168.77.0/24"]}`)
	if rr.Code != http.StatusBadRequest || decodeJSONBody(t, rr)["code"] != "NOT_FOUND" {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if posts := poster.snapshot(); len(posts) != 0 || sim.has(peerFixturePubKey) {
		t.Fatalf("posts=%v ghost=%v", posts, sim.has(peerFixturePubKey))
	}
}
