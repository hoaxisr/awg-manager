package managed

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

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
	svc, store, poster, _ := newPeerSubnetTestService(t, `[]`)
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
	svc, store, poster, _ := newPeerSubnetTestService(t, `[]`)
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

func TestUpdatePeer_DiffAddsAndRemoves(t *testing.T) {
	svc, store, poster, _ := newPeerSubnetTestService(t, rcOurs77)
	seedPeer(t, store, "192.168.77.0/24")
	err := svc.UpdatePeer(context.Background(), "Wireguard1", "PEER1", UpdatePeerRequest{Description: "branch", TunnelIP: "10.66.66.2/32", RemoteSubnets: []string{"192.168.78.0/24"}})
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
	svc, store, poster, _ := newPeerSubnetTestService(t, rcOurs77)
	seedPeer(t, store, "192.168.77.0/24")
	if err := svc.UpdatePeer(context.Background(), "Wireguard1", "PEER1", UpdatePeerRequest{Description: "branch", TunnelIP: "10.66.66.2/32"}); err != nil {
		t.Fatal(err)
	}
	posts := postsJSON(poster)
	if indexOf(posts, allow77Off) < 0 || indexOf(posts, route77Off) < 0 {
		t.Fatalf("сети не сняты:\n%s", strings.Join(posts, "\n"))
	}
	if sv, _ := store.GetManagedServerByID("Wireguard1"); len(sv.Peers[0].RemoteSubnets) != 0 {
		t.Fatalf("store = %v", sv.Peers[0].RemoteSubnets)
	}
}

// Правило 2: чужая запись на (N, I) — не добавляем и потом не снимаем.
func TestUpdatePeer_ForeignRouteNeverTouched(t *testing.T) {
	rc := `[{"network":"192.168.78.0","mask":"255.255.255.0","interface":"Wireguard1","auto":true,"comment":"manual"}]`
	svc, store, poster, _ := newPeerSubnetTestService(t, rc)
	seedPeer(t, store)
	if err := svc.UpdatePeer(context.Background(), "Wireguard1", "PEER1", UpdatePeerRequest{Description: "branch", TunnelIP: "10.66.66.2/32", RemoteSubnets: []string{"192.168.78.0/24"}}); err != nil {
		t.Fatal(err)
	}
	if err := svc.UpdatePeer(context.Background(), "Wireguard1", "PEER1", UpdatePeerRequest{Description: "branch", TunnelIP: "10.66.66.2/32"}); err != nil {
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
	svc, store, poster, _ := newPeerSubnetTestService(t, `[]`)
	seedPeer(t, store)
	poster.failOn = func(m map[string]interface{}) error {
		b, _ := json.Marshal(m)
		if strings.Contains(string(b), `"comment":"awgm-peer:`) {
			return errors.New("route refused")
		}
		return nil
	}
	err := svc.UpdatePeer(context.Background(), "Wireguard1", "PEER1", UpdatePeerRequest{Description: "branch", TunnelIP: "10.66.66.3/32", RemoteSubnets: []string{"192.168.77.0/24"}})
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
	svc, store, poster, _ := newPeerSubnetTestService(t, `[]`)
	seedPeer(t, store)
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
	svc, store, poster, _ := newPeerSubnetTestService(t, `[]`)
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
	poster.onPost = func(m map[string]interface{}) {
		b, _ := json.Marshal(m)
		if !strings.Contains(string(b), `"comment":"awgm-peer:pub-1"`) {
			return
		}
		// Маршрут встал на роутере, а параллельный AddPeer занял тот же адрес.
		fg.SetJSON("/show/rc/ip/route", `[{"network":"192.168.77.0","mask":"255.255.255.0","interface":"Wireguard1","auto":true,"comment":"awgm-peer:pub-1"}]`)
		_ = store.UpdateManagedServer("Wireguard1", func(sv *storage.ManagedServer) error {
			sv.Peers = append(sv.Peers, storage.ManagedPeer{PublicKey: "RACE", TunnelIP: "10.66.66.2/32"})
			return nil
		})
	}
	if _, err := svc.AddPeer(context.Background(), "Wireguard1", AddPeerRequest{Description: "branch", TunnelIP: "10.66.66.2/32", RemoteSubnets: []string{"192.168.77.0/24"}}); err == nil {
		t.Fatal("ожидали отказ записи")
	}
	posts := postsJSON(poster)
	iAllowOff, iRouteOff, iPeerOff := indexOf(posts, allow77Off), indexOf(posts, route77Off), indexOf(posts, `{"key":"pub-1","no":true}`)
	if iAllowOff < 0 || iRouteOff < 0 || iPeerOff < 0 || iRouteOff > iPeerOff {
		t.Fatalf("сироты на роутере: allowOff=%d routeOff=%d peerOff=%d\n%s", iAllowOff, iRouteOff, iPeerOff, strings.Join(posts, "\n"))
	}
	if sv, _ := store.GetManagedServerByID("Wireguard1"); len(sv.Peers) != 1 || sv.Peers[0].PublicKey != "RACE" {
		t.Fatalf("store = %+v", sv.Peers)
	}
}
