package managed

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// F511: NDMS отвечает HTTP 200 и на отказ, пряча его во вложенный status —
// транспорт такой ответ ошибкой не считает. rciPost обязан его увидеть, иначе
// отказ роутера считается успехом и хранилище пишется.

// nestedError — форма отказа wireguard-команды (стенд 5.02.A.11).
func nestedError(iface, msg string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"interface": map[string]any{iface: map[string]any{
		"wireguard": map[string]any{"peer": []any{map[string]any{"status": []any{map[string]any{
			"status": "error", "code": "7405600", "ident": "Network::Interface::Wireguard", "message": msg,
		}}}}},
	}}})
	return b
}

// ifaceError — отказ на уровне интерфейса: {"interface":{"X":{"status":[…]}}}.
func ifaceError(iface, msg string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"interface": map[string]any{iface: map[string]any{
		"status": []any{map[string]any{"status": "error", "message": msg}},
	}}})
	return b
}

// ifacePayload — interface.<name> из payload'а managed; nil — чужая форма.
func ifacePayload(m map[string]interface{}, iface string) map[string]interface{} {
	top, _ := m["interface"].(map[string]interface{})
	v, _ := top[iface].(map[string]interface{})
	return v
}

// peerPayload — первый элемент wireguard.peer[] у interface.<name>.
func peerPayload(m map[string]interface{}, iface string) map[string]interface{} {
	wg, _ := ifacePayload(m, iface)["wireguard"].(map[string]interface{})
	peers, _ := wg["peer"].([]map[string]interface{})
	if len(peers) == 0 {
		return nil
	}
	return peers[0]
}

func TestCreate_NestedErrorIsFailureAndNotStored(t *testing.T) {
	svc, store, _ := newCreateTestService(t)
	poster := svc.transport.(*recordingPoster)
	poster.respond = func(m map[string]interface{}) json.RawMessage {
		if iface := ifacePayload(m, "Wireguard0"); iface != nil && iface["security-level"] != nil {
			return ifaceError("Wireguard0", "invalid port")
		}
		return nil
	}
	_, err := svc.Create(context.Background(), CreateServerRequest{Address: "10.66.66.1", Mask: "255.255.255.0", ListenPort: 51820})
	if err == nil || !strings.Contains(err.Error(), "invalid port") {
		t.Fatalf("err = %v, want отказ роутера", err)
	}
	if got := store.GetManagedServers(); len(got) != 0 {
		t.Fatalf("сервер записан при отказе роутера: %+v", got)
	}
}

func TestAddPeer_NestedErrorIsFailureAndNotStored(t *testing.T) {
	svc, store, poster, _ := newPeerSubnetTestService(t, `[]`)
	poster.respond = func(m map[string]interface{}) json.RawMessage {
		if p := peerPayload(m, "Wireguard1"); p != nil && p["preshared-key"] != nil {
			return nestedError("Wireguard1", "subnet overlaps with the other peer")
		}
		return nil
	}
	_, err := svc.AddPeer(context.Background(), "Wireguard1", AddPeerRequest{Description: "phone"})
	if err == nil || !strings.Contains(err.Error(), "subnet overlaps") {
		t.Fatalf("err = %v, want отказ роутера", err)
	}
	sv, _ := store.GetManagedServerByID("Wireguard1")
	if len(sv.Peers) != 0 {
		t.Fatalf("пир записан при отказе роутера: %+v", sv.Peers)
	}
	// Ключ/PSK могли встать поэлементно — откат снимает свежий ключ.
	last := peerPayload(poster.posts[len(poster.posts)-1], "Wireguard1")
	if last == nil || last["no"] != true || last["key"] != "pub-1" {
		t.Fatalf("нет отката добавленного пира: %v", poster.posts)
	}
}

func TestUpdatePeer_AllowIPsNestedErrorIsFailureAndNotStored(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	// Пир есть на роутере: откат адреса проверяет его наличие свежим чтением.
	sim := newSimRouter(t, fg, poster, `[]`)
	seedSimPeer(t, store, sim)
	poster.respond = func(m map[string]interface{}) json.RawMessage {
		p := peerPayload(m, "Wireguard1")
		ips, _ := p["allow-ips"].([]map[string]interface{})
		if len(ips) == 1 && ips[0]["no"] == nil && ips[0]["address"] == "10.66.66.3" {
			return nestedError("Wireguard1", "subnet overlaps with the other peer")
		}
		return nil
	}
	err := svc.UpdatePeer(context.Background(), "Wireguard1", "PEER1", UpdatePeerRequest{Description: "branch", TunnelIP: "10.66.66.3/32"})
	if err == nil || !strings.Contains(err.Error(), "subnet overlaps") {
		t.Fatalf("err = %v, want отказ роутера", err)
	}
	sv, _ := store.GetManagedServerByID("Wireguard1")
	if sv.Peers[0].TunnelIP != "10.66.66.2/32" {
		t.Fatalf("tunnel IP записан при отказе роутера: %q", sv.Peers[0].TunnelIP)
	}
	// Старый /32 снят до отказа — обязан вернуться: иначе у пира нет адреса
	// из записи, а .conf его выдаёт.
	ips, _ := peerPayload(poster.posts[len(poster.posts)-1], "Wireguard1")["allow-ips"].([]map[string]interface{})
	if len(ips) != 1 || ips[0]["no"] != nil || ips[0]["address"] != "10.66.66.2" {
		t.Fatalf("старый /32 не восстановлен последним шагом: %v", poster.posts)
	}
}

func TestSetPeerComment_NestedErrorIsFailure(t *testing.T) {
	svc, _, poster, _ := newPeerSubnetTestService(t, `[]`)
	poster.respond = func(map[string]interface{}) json.RawMessage {
		return nestedError("Wireguard1", "no such peer")
	}
	if err := svc.rciSetPeerComment(context.Background(), "Wireguard1", "PEER1", "x"); err == nil {
		t.Fatal("отказ роутера во вложенном status принят за успех")
	}
}

func TestDeletePeer_NestedErrorKeepsPeerInStorage(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	seedPeer(t, store)
	// Пир на роутере есть — отказ настоящий.
	fg.SetJSON("/show/rc/interface/Wireguard1", `{"wireguard":{"peer":[{"key":"PEER1","allow-ips":[{"address":"10.66.66.2","mask":"255.255.255.255"}]}]}}`)
	poster.respond = func(m map[string]interface{}) json.RawMessage {
		if p := peerPayload(m, "Wireguard1"); p != nil && p["no"] == true {
			return nestedError("Wireguard1", "peer is busy")
		}
		return nil
	}
	if err := svc.DeletePeer(context.Background(), "Wireguard1", "PEER1"); err == nil {
		t.Fatal("отказ роутера во вложенном status принят за успех")
	}
	sv, _ := store.GetManagedServerByID("Wireguard1")
	if len(sv.Peers) != 1 {
		t.Fatalf("пир снят из хранилища при отказе роутера: %+v", sv.Peers)
	}
}

// Терпимые отказы: цель уже достигнута — операция не падает.

func deleteIfaceResponder(msg string) func(map[string]interface{}) json.RawMessage {
	return func(m map[string]interface{}) json.RawMessage {
		if iface := ifacePayload(m, "Wireguard1"); iface != nil && iface["no"] == true {
			return ifaceError("Wireguard1", msg)
		}
		return nil
	}
}

func TestDelete_MissingInterfaceTolerated(t *testing.T) {
	svc, store, poster, _ := newPeerSubnetTestService(t, `[]`)
	poster.respond = deleteIfaceResponder(`unable to find interface "Wireguard1"`)
	if err := svc.Delete(context.Background(), "Wireguard1"); err != nil {
		t.Fatalf("снос отсутствующего интерфейса — цель достигнута, а не отказ: %v", err)
	}
	if _, ok := store.GetManagedServerByID("Wireguard1"); ok {
		t.Fatal("запись сервера осталась — удаление застряло бы навсегда")
	}
}

func TestDelete_OtherNestedErrorKeepsServer(t *testing.T) {
	svc, store, poster, _ := newPeerSubnetTestService(t, `[]`)
	poster.respond = deleteIfaceResponder("interface is busy")
	if err := svc.Delete(context.Background(), "Wireguard1"); err == nil {
		t.Fatal("отказ сноса интерфейса принят за успех")
	}
	if _, ok := store.GetManagedServerByID("Wireguard1"); !ok {
		t.Fatal("запись сервера снята при отказе роутера — интерфейс осиротел")
	}
}

func TestRemoveStaticNAT_UnknownInterfaceTolerated(t *testing.T) {
	svc, _, poster, _ := newPeerSubnetTestService(t, `[]`)
	poster.respond = func(map[string]interface{}) json.RawMessage {
		return json.RawMessage(`{"ip":{"static":[{"status":[{"status":"error","message":"unknown interface \"PPPoE0\"."}]}]}}`)
	}
	if err := svc.rciSetStaticNAT(context.Background(), "Wireguard1", "PPPoE0", false); err != nil {
		t.Fatalf("снятие static NAT с исчезнувшего выхода: %v", err)
	}
	// Допуск только на снятие: постановка на отсутствующий выход — отказ.
	if err := svc.rciSetStaticNAT(context.Background(), "Wireguard1", "PPPoE0", true); err == nil {
		t.Fatal("постановка static NAT на неизвестный интерфейс принята за успех")
	}
}

func TestMigratePeerAllowIPs_NoSuchNetTolerated(t *testing.T) {
	for _, tc := range []struct {
		msg          string
		wantMigrated bool
	}{
		{`"Wireguard0": no such net in peer "PEER_A"`, true},
		{"peer is busy", false},
	} {
		store, _ := seedAllowIPsStore(t, []storage.ManagedPeer{{PublicKey: "PEER_A", TunnelIP: "10.0.0.2/32"}}, false)
		poster := &recordingPoster{respond: func(map[string]interface{}) json.RawMessage {
			return nestedError("Wireguard0", tc.msg)
		}}
		s := &Service{settings: store, transport: poster, queries: routerWithPeers(t, "PEER_A")}
		s.MigratePeerAllowIPs(context.Background())
		if got := store.IsManagedPeerAllowIPsMigrated(); got != tc.wantMigrated {
			t.Errorf("%q: флаг миграции %v, want %v", tc.msg, got, tc.wantMigrated)
		}
	}
}

// Пир снят мимо панели (веб-морда): NDMS отвечает на снятие `no input […]`
// (стенд 5.02.A.11). Фраза общая — решает свежее чтение rc: пира нет → успех.
func TestDeletePeer_AbsentOnRouterIsRemoved(t *testing.T) {
	svc, store, poster, _ := newPeerSubnetTestService(t, `[]`) // rc Wireguard1 = {} — пиров нет
	seedPeer(t, store)
	poster.respond = func(m map[string]interface{}) json.RawMessage {
		if p := peerPayload(m, "Wireguard1"); p != nil && p["no"] == true {
			return nestedError("Wireguard1", "no input [http/rci 127.0.0.1].")
		}
		return nil
	}
	if err := svc.DeletePeer(context.Background(), "Wireguard1", "PEER1"); err != nil {
		t.Fatalf("пир уже снят на роутере — удаление обязано пройти: %v", err)
	}
	sv, _ := store.GetManagedServerByID("Wireguard1")
	if len(sv.Peers) != 0 {
		t.Fatalf("запись пира осталась: %+v", sv.Peers)
	}
}

// Чтение rc упало — отсутствие пира не доказано, отказ остаётся отказом.
func TestDeletePeer_RereadFailureKeepsError(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	seedPeer(t, store)
	fg.SetError("/show/rc/interface/Wireguard1", errors.New("rci down"))
	poster.respond = func(m map[string]interface{}) json.RawMessage {
		if p := peerPayload(m, "Wireguard1"); p != nil && p["no"] == true {
			return nestedError("Wireguard1", "no input [http/rci 127.0.0.1].")
		}
		return nil
	}
	if err := svc.DeletePeer(context.Background(), "Wireguard1", "PEER1"); err == nil {
		t.Fatal("отказ принят за успех без доказательства отсутствия пира")
	}
	sv, _ := store.GetManagedServerByID("Wireguard1")
	if len(sv.Peers) != 1 {
		t.Fatalf("запись пира снята: %+v", sv.Peers)
	}
}

// На отказе роутера save и инвалидация всё равно идут: NDMS применяет payload
// поэлементно, отказ может приехать вместе с применённым.
func TestRciPost_FailureStillSavesAndInvalidates(t *testing.T) {
	svc, _, poster, fg := newPeerSubnetTestService(t, `[]`)
	ctx := context.Background()
	if _, err := svc.queries.WGServers.List(ctx); err != nil {
		t.Fatal(err)
	}
	before := fg.Calls("/show/rc/interface/Wireguard1")
	poster.respond = func(map[string]interface{}) json.RawMessage { return nestedError("Wireguard1", "boom") }
	if err := svc.rciSetPeerComment(ctx, "Wireguard1", "PEER1", "x"); err == nil {
		t.Fatal("отказ принят за успех")
	}
	if got := svc.saveCoord.Status().PendingCount; got != 1 {
		t.Errorf("save не запрошен на отказе: pending=%d", got)
	}
	if _, err := svc.queries.WGServers.List(ctx); err != nil {
		t.Fatal(err)
	}
	if fg.Calls("/show/rc/interface/Wireguard1") == before {
		t.Error("кэш списка WG-серверов не инвалидирован на отказе")
	}
}
