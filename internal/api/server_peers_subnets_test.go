package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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
	fg := query.NewFakeGetter()
	fg.SetJSON("/show/interface/", `{
		"Wireguard0":{"id":"Wireguard0","type":"Wireguard","description":"Wireguard VPN Server","state":"up","link":"up","address":"10.9.0.1","mask":"255.255.255.0","wireguard":{"peer":[{"public-key":"`+peerFixturePubKey+`","comment":"phone"}]}},
		"Bridge0":{"id":"Bridge0","type":"Bridge","description":"Home","address":"192.168.1.1","mask":"255.255.255.0"}}`)
	fg.SetJSON("/show/rc/interface/Wireguard0", `{"wireguard":{"peer":[{"key":"`+peerFixturePubKey+`","comment":"phone","allow-ips":`+peerAllowIPs+`}]}}`)
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
	return h, store, poster, p, spy
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
	h, store, poster, p, _ := newServersSubnetHarness(t, `[]`, `[]`)
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

// Свойство 2: tunnel IP и сети меняются вместе, Apply отказал — старый /32
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
	sec, _ := store.GetServerPeerSecret("Wireguard0", peerFixturePubKey)
	if sec.TunnelIP != "10.9.0.2/32" || len(sec.RemoteSubnets) != 0 {
		t.Fatalf("запись тронута: %+v", sec)
	}
}

// Отказ записи секрета после успеха на роутере: сети снимаются, /32
// возвращается — иначе маршрут без записи никто уже не снимет.
func TestServersHandler_UpdateServerPeer_SaveFails_UndoesRouter(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("под root chmod не запрещает запись")
	}
	// Фейк отдаёт снимок маршрутов статичным: запись с нашей меткой стоит с
	// начала, иначе снятие по свежему чтению её бы не увидело.
	h, store, poster, _, _ := newServersSubnetHarness(t, `[{"address":"10.9.0.2","mask":"255.255.255.255"}]`, sysRcOurs77)
	_ = store.SetServerPeerSecret("Wireguard0", peerFixturePubKey, storage.ServerPeerSecret{PrivateKey: "P", TunnelIP: "10.9.0.2/32"})
	dir := store.DataDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
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
	// Final review M5: Apply свои сети откатил — в оставленном секрете их нет.
	if len(sec.RemoteSubnets) != 0 {
		t.Fatalf("в секрете сети, которых нет на роутере: %v", sec.RemoteSubnets)
	}
}

// Откат Apply сам не завершился (allow-ips не снялись) — сети на роутере,
// секрет их держит, чтобы удаление пира сняло метки.
func TestServersHandler_AddServerPeer_RollbackRemoveFails_ApplyRollbackFails_KeepsSubnets(t *testing.T) {
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
	if sec, ok := store.GetServerPeerSecret("Wireguard0", peerFixturePubKey); !ok || len(sec.RemoteSubnets) != 1 {
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
