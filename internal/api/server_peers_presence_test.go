package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Операции по ключу пира на ключе, которого на интерфейсе нет (стенд
// 5.02.A.11, 28.09): снятие отвергается `no input`, любая другая — СОЗДАЁТ пира.

// I1: пир снят в веб-морде (в списке сервера ещё есть, в свежем rc — нет):
// удаление проходит и снимает секрет — иначе его сети навсегда заняты.
func TestServersHandler_DeleteServerPeer_RemovedOutsidePanel_SecretDeleted(t *testing.T) {
	h, store, poster, _, _, fg := newServersSubnetHarnessFG(t, `[]`, `[]`, "")
	sim := newSysSimRouter(t, fg.FakeGetter, poster, `[]`) // пира на «роутере» нет
	_ = store.SetServerPeerSecret("Wireguard0", peerFixturePubKey, storage.ServerPeerSecret{PrivateKey: "P", TunnelIP: "10.9.0.2/32", RemoteSubnets: []string{"192.168.77.0/24"}})
	rr := deleteServerPeer(t, h, peerFixturePubKey)
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if _, ok := store.GetServerPeerSecret("Wireguard0", peerFixturePubKey); ok {
		t.Fatal("секрет остался — его сети заняты навсегда")
	}
	if sim.has(peerFixturePubKey) {
		t.Fatal("пир-призрак создан")
	}
}

// I1: пира уже нет и в списке сервера, секрет остался — удаление по ключу
// обязано пройти, а не ответить NOT_FOUND.
func TestServersHandler_DeleteServerPeer_GoneFromList_SecretDeleted(t *testing.T) {
	h, store, poster, _, _ := newServersPeerHarness(t, false)
	_ = store.SetServerPeerSecret(harnessServerID, peerFixturePubKey, storage.ServerPeerSecret{PrivateKey: "P", RemoteSubnets: []string{"192.168.77.0/24"}})
	poster.setFailOn(func(payload string) error {
		if strings.Contains(payload, `"no":true`) {
			return errors.New("no input [http/rci 127.0.0.1].")
		}
		return nil
	})
	if rr := deleteServerPeer(t, h, peerFixturePubKey); rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if _, ok := store.GetServerPeerSecret(harnessServerID, peerFixturePubKey); ok {
		t.Fatal("секрет остался")
	}
}

// I1: снятие отказало, а перечитать пиров не удалось — отсутствие не
// доказано: отказ, секрет на месте.
func TestServersHandler_DeleteServerPeer_RereadFails_KeepsSecret(t *testing.T) {
	h, store, poster, _, _, fg := newServersSubnetHarnessFG(t, `[]`, `[]`, "")
	sim := newSysSimRouter(t, fg.FakeGetter, poster, `[]`)
	sim.seed(peerFixturePubKey, "10.9.0.2/32")
	_ = store.SetServerPeerSecret("Wireguard0", peerFixturePubKey, storage.ServerPeerSecret{PrivateKey: "P", TunnelIP: "10.9.0.2/32"})
	sim.fail = func(payload string) error {
		if strings.Contains(payload, `{"key":"`+peerFixturePubKey+`","no":true}`) {
			fg.SetError("/show/rc/interface/Wireguard0", errors.New("rci down"))
			return errors.New("no input [http/rci 127.0.0.1].")
		}
		return nil
	}
	rr := deleteServerPeer(t, h, peerFixturePubKey)
	if rr.Code != http.StatusBadRequest || decodeJSONBody(t, rr)["code"] != "DELETE_PEER_FAILED" {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if _, ok := store.GetServerPeerSecret("Wireguard0", peerFixturePubKey); !ok {
		t.Fatal("секрет удалён без доказательства отсутствия пира")
	}
}

// M2: переименование пира, снятого мимо панели, — NOT_FOUND до единого поста:
// comment на неизвестный ключ NDMS создал бы пира.
func TestServersHandler_UpdateServerPeer_Rename_PeerAbsent_NoGhost(t *testing.T) {
	h, store, poster, _, _, fg := newServersSubnetHarnessFG(t, `[]`, `[]`, "")
	sim := newSysSimRouter(t, fg.FakeGetter, poster, `[]`) // пира на «роутере» нет
	_ = store.SetServerPeerSecret("Wireguard0", peerFixturePubKey, storage.ServerPeerSecret{PrivateKey: "P", Description: "phone", TunnelIP: "10.9.0.2/32"})
	rr := putServerPeer(t, h, peerFixturePubKey, `{"description":"renamed","tunnelIP":"10.9.0.2/32"}`)
	if rr.Code != http.StatusBadRequest || decodeJSONBody(t, rr)["code"] != "NOT_FOUND" {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if posts := poster.snapshot(); len(posts) != 0 || sim.has(peerFixturePubKey) {
		t.Fatalf("posts=%v ghost=%v", posts, sim.has(peerFixturePubKey))
	}
	if sec, _ := store.GetServerPeerSecret("Wireguard0", peerFixturePubKey); sec.Description != "phone" {
		t.Fatalf("секрет переписан: %+v", sec)
	}
}

// M2: вкл/выкл пира, снятого мимо панели, — NOT_FOUND до единого поста.
func TestServersHandler_ToggleServerPeer_PeerAbsent_NoGhost(t *testing.T) {
	h, _, poster, _, _, fg := newServersSubnetHarnessFG(t, `[]`, `[]`, "")
	sim := newSysSimRouter(t, fg.FakeGetter, poster, `[]`) // пира на «роутере» нет
	rr := toggleServerPeer(t, h, peerFixturePubKey, false)
	if rr.Code != http.StatusBadRequest || decodeJSONBody(t, rr)["code"] != "NOT_FOUND" {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if posts := poster.snapshot(); len(posts) != 0 || sim.has(peerFixturePubKey) {
		t.Fatalf("posts=%v ghost=%v", posts, sim.has(peerFixturePubKey))
	}
}
