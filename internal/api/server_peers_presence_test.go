package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

// M1: генерация ключей (exec awg) — не под блокировкой F508.
func TestServersHandler_AddServerPeer_KeygenOutsideLock(t *testing.T) {
	h, _, poster, _, _, fg := newServersSubnetHarnessFG(t, `[]`, `[]`, "")
	newSysSimRouter(t, fg.FakeGetter, poster, `[]`)
	stubPeerKeygen(t)
	var underLock atomic.Bool
	genKeyPair = func(context.Context) (string, string, error) {
		acquired := make(chan func(), 1)
		go func() { acquired <- h.managedSvc.LockPeerSubnets() }()
		select {
		case unlock := <-acquired:
			unlock()
		case <-time.After(100 * time.Millisecond):
			underLock.Store(true)
			go func() { (<-acquired)() }()
		}
		return "PRIV-fixture", peerFixturePubKey, nil
	}
	rr := postServerPeer(t, h, `{"description":"Phone","tunnelIP":"10.9.0.7/32","remoteSubnets":["192.168.77.0/24"]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if underLock.Load() {
		t.Fatal("ключи генерируются под блокировкой F508")
	}
}

// M3: сеть за клиентом поверх статического маршрута через другой интерфейс —
// отказ с его названием, а не молча второй маршрут.
func TestServersHandler_AddServerPeer_OverlapWithOtherIfaceRoute(t *testing.T) {
	h, _, poster, _, _ := newServersSubnetHarness(t, `[]`, `[{"network":"192.168.77.0","mask":"255.255.255.0","interface":"PPPoE0","auto":true}]`)
	stubPeerKeygen(t)
	rr := postServerPeer(t, h, `{"description":"Phone","tunnelIP":"10.9.0.7/32","remoteSubnets":["192.168.77.0/24"]}`)
	body := decodeJSONBody(t, rr)
	if rr.Code != http.StatusBadRequest || body["code"] != "REMOTE_SUBNET_OVERLAP" || !strings.Contains(body["message"].(string), "маршрут через PPPoE0") {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if posts := poster.snapshot(); len(posts) != 0 {
		t.Fatalf("posts=%v", posts)
	}
}

// Секрет пира, снятого в веб-морде, сети не занимает: новый пир берёт её.
// Пир на роутере есть — сеть занята. Секрет не удаляется.
func TestServersHandler_AddServerPeer_SubnetOfPeerGoneFromRouterIsFree(t *testing.T) {
	for _, onRouter := range []bool{false, true} {
		other := ""
		if onRouter {
			other = `[]`
		}
		h, store, _, _, _ := newServersSubnetHarnessWithOther(t, `[]`, `[]`, other)
		stubPeerKeygen(t)
		_ = store.SetServerPeerSecret("Wireguard0", otherPeerPubKey, storage.ServerPeerSecret{PrivateKey: "P", Description: "tablet", RemoteSubnets: []string{"192.168.77.0/24"}})
		rr := postServerPeer(t, h, `{"description":"Phone","tunnelIP":"10.9.0.7/32","remoteSubnets":["192.168.77.0/24"]}`)
		if onRouter {
			if rr.Code != http.StatusBadRequest || decodeJSONBody(t, rr)["code"] != "REMOTE_SUBNET_OVERLAP" {
				t.Fatalf("пир на роутере: code=%d body=%s", rr.Code, rr.Body.String())
			}
		} else if rr.Code != http.StatusOK {
			t.Fatalf("пир снят: code=%d body=%s", rr.Code, rr.Body.String())
		}
		if _, ok := store.GetServerPeerSecret("Wireguard0", otherPeerPubKey); !ok {
			t.Fatal("секрет удалён")
		}
	}
}
