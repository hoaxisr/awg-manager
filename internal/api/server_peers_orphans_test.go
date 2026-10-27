package api

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

const orphanPeerPubKey = "ORPHANaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa="

// F245: адрес занят пиром, которого нет в снимке списка (вторая вкладка или
// MCP добавили его после снимка), — перепроверка по свежему rc под
// блокировкой отказывает, пир на роутер не уходит.
func TestServersHandler_AddServerPeer_RechecksTunnelIPOnFreshRC(t *testing.T) {
	h, _, poster, _, _, fg := newServersSubnetHarnessFG(t, `[]`, `[]`, "")
	stubPeerKeygen(t)
	// Снимок списка закэширован ДО появления OTHER: так его видит запрос,
	// проверивший адрес раньше, чем параллельный пир добавился.
	if _, err := h.listServers(context.Background()); err != nil {
		t.Fatal(err)
	}
	fg.SetRC("Wireguard0", `{"wireguard":{"peer":[{"key":"`+peerFixturePubKey+`","allow-ips":[]},{"key":"`+otherPeerPubKey+`","allow-ips":[{"address":"10.9.0.7","mask":"255.255.255.255"}]}]}}`)
	rr := postServerPeer(t, h, `{"description":"Phone","tunnelIP":"10.9.0.7/32"}`)
	if rr.Code != http.StatusBadRequest || decodeJSONBody(t, rr)["code"] != "TUNNEL_IP_IN_USE" {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if posts := poster.snapshot(); len(posts) != 0 {
		t.Fatalf("пир ушёл на роутер при занятом адресе: %v", posts)
	}
}

// F528: секрет пира, снятого в веб-морде, удаление другого пира убирает;
// секрет живого пира остаётся.
func TestServersHandler_DeleteServerPeer_PrunesOrphanSecrets(t *testing.T) {
	h, store, _, _, _ := newServersSubnetHarnessWithOther(t, `[]`, `[]`, `[]`)
	for _, key := range []string{peerFixturePubKey, otherPeerPubKey, orphanPeerPubKey} {
		if err := store.SetServerPeerSecret("Wireguard0", key, storage.ServerPeerSecret{PrivateKey: "P"}); err != nil {
			t.Fatal(err)
		}
	}
	if rr := deleteServerPeer(t, h, peerFixturePubKey); rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if _, ok := store.GetServerPeerSecret("Wireguard0", orphanPeerPubKey); ok {
		t.Fatal("секрет пира, которого нет на роутере, остался")
	}
	if _, ok := store.GetServerPeerSecret("Wireguard0", otherPeerPubKey); !ok {
		t.Fatal("секрет живого пира удалён")
	}
}

// Свежий rc не прочитан — ни добавления, ни уборки: отсутствие пиров не
// доказано.
func TestServersHandler_AddServerPeer_RCReadFailureKeepsSecrets(t *testing.T) {
	h, store, poster, _, _, fg := newServersSubnetHarnessFG(t, `[]`, `[]`, "")
	stubPeerKeygen(t)
	if err := store.SetServerPeerSecret("Wireguard0", orphanPeerPubKey, storage.ServerPeerSecret{PrivateKey: "P"}); err != nil {
		t.Fatal(err)
	}
	// Снимок — из кэша, отказывает только свежее чтение rc под блокировкой.
	if _, err := h.listServers(context.Background()); err != nil {
		t.Fatal(err)
	}
	fg.SetError("/show/rc/interface/", errors.New("rci down"))
	rr := postServerPeer(t, h, `{"description":"Phone","tunnelIP":"10.9.0.7/32"}`)
	if rr.Code == http.StatusOK {
		t.Fatalf("добавление прошло без свежего rc: %s", rr.Body.String())
	}
	if _, ok := store.GetServerPeerSecret("Wireguard0", orphanPeerPubKey); !ok {
		t.Fatal("секрет удалён без прочитанного rc")
	}
	if posts := poster.snapshot(); len(posts) != 0 {
		t.Fatalf("пир ушёл на роутер без проверки адреса: %v", posts)
	}
}

// rc без блока wireguard (сбой формы) разбирается в пустой список — уборка не
// снимает секреты пиров, которых видно в списке сервера.
func TestServersHandler_AddServerPeer_EmptyRCKeepsListedSecrets(t *testing.T) {
	h, store, _, _, _, fg := newServersSubnetHarnessFG(t, `[]`, `[]`, `[]`)
	stubPeerKeygen(t)
	if err := store.SetServerPeerSecret("Wireguard0", otherPeerPubKey, storage.ServerPeerSecret{PrivateKey: "P"}); err != nil {
		t.Fatal(err)
	}
	fg.SetRC("Wireguard0", `{}`)
	_ = postServerPeer(t, h, `{"description":"Phone","tunnelIP":"10.9.0.7/32"}`)
	if _, ok := store.GetServerPeerSecret("Wireguard0", otherPeerPubKey); !ok {
		t.Fatal("секрет пира из списка снят по пустому rc")
	}
}

// F245: добавление без сетей за клиентом тоже ждёт общую блокировку — иначе
// перепроверка адреса и добавление двух запросов перемежаются.
func TestServersHandler_AddServerPeer_AlwaysTakesLock(t *testing.T) {
	h, _, _, _, _ := newServersSubnetHarness(t, `[]`, `[]`)
	stubPeerKeygen(t)
	unlock := h.managedSvc.LockPeerSubnets()
	done := make(chan struct{})
	go func() {
		postServerPeer(t, h, `{"description":"Phone","tunnelIP":"10.9.0.7/32"}`)
		close(done)
	}()
	select {
	case <-done:
		unlock()
		t.Fatal("добавление прошло, пока блокировка занята")
	case <-time.After(300 * time.Millisecond):
	}
	unlock()
	<-done
}
