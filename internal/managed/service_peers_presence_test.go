package managed

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/peersubnet"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Операции по ключу пира на ключе, которого на интерфейсе нет (стенд
// 5.02.A.11, 28.09): любая, кроме снятия, СОЗДАЁТ пира. Пир в записи есть,
// на роутере снят мимо панели — операция отказывает до единого поста.

func storedPeer(t *testing.T, store *storage.SettingsStore) storage.ManagedPeer {
	t.Helper()
	sv, _ := store.GetManagedServerByID("Wireguard1")
	return sv.Peers[0]
}

// M2: переименование.
func TestUpdatePeer_Rename_PeerAbsentOnRouter_NoGhost(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	sim := newSimRouter(t, fg, poster, `[]`)
	seedPeer(t, store) // в записи есть, на «роутере» нет
	err := svc.UpdatePeer(context.Background(), "Wireguard1", "PEER1", UpdatePeerRequest{Description: "renamed", TunnelIP: "10.66.66.2/32"})
	if !errors.Is(err, peersubnet.ErrPeerNotFound) {
		t.Fatalf("err = %v, want ErrPeerNotFound", err)
	}
	if posts := postsJSON(poster); len(posts) != 0 {
		t.Fatalf("посты при отсутствующем пире:\n%s", strings.Join(posts, "\n"))
	}
	if _, ghost := sim.peers["Wireguard1"]["PEER1"]; ghost {
		t.Fatal("пир-призрак создан")
	}
	if p := storedPeer(t, store); p.Description != "branch" {
		t.Fatalf("запись переписана: %+v", p)
	}
}

// M2: вкл/выкл.
func TestTogglePeer_PeerAbsentOnRouter_NoGhost(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	sim := newSimRouter(t, fg, poster, `[]`)
	seedPeer(t, store)
	err := svc.TogglePeer(context.Background(), "Wireguard1", "PEER1", false)
	if !errors.Is(err, peersubnet.ErrPeerNotFound) {
		t.Fatalf("err = %v, want ErrPeerNotFound", err)
	}
	if posts := postsJSON(poster); len(posts) != 0 {
		t.Fatalf("посты при отсутствующем пире:\n%s", strings.Join(posts, "\n"))
	}
	if _, ghost := sim.peers["Wireguard1"]["PEER1"]; ghost {
		t.Fatal("пир-призрак создан")
	}
	if p := storedPeer(t, store); !p.Enabled {
		t.Fatalf("запись переписана: %+v", p)
	}
}

// M2: наличие не прочиталось — отказ без поста.
func TestTogglePeer_PresenceReadFails_NoPost(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	seedPeer(t, store)
	fg.SetError("/show/rc/interface/Wireguard1", errors.New("rci down"))
	if err := svc.TogglePeer(context.Background(), "Wireguard1", "PEER1", false); err == nil {
		t.Fatal("отказ чтения принят за наличие пира")
	}
	if posts := postsJSON(poster); len(posts) != 0 {
		t.Fatalf("посты без проверки наличия:\n%s", strings.Join(posts, "\n"))
	}
}

// M2: проверка наличия и пост — под блокировкой, которую берёт удаление.
func TestPeerKeyedEdits_TakePeerSubnetsLock(t *testing.T) {
	for name, call := range map[string]func(*Service){
		"toggle": func(svc *Service) { _ = svc.TogglePeer(context.Background(), "Wireguard1", "PEER1", false) },
		"rename": func(svc *Service) {
			_ = svc.UpdatePeer(context.Background(), "Wireguard1", "PEER1", UpdatePeerRequest{Description: "renamed", TunnelIP: "10.66.66.2/32"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
			sim := newSimRouter(t, fg, poster, `[]`)
			seedSimPeer(t, store, sim)
			if !waitsForLock(t, svc, func() { call(svc) }) {
				t.Fatal("правка по ключу прошла мимо блокировки")
			}
		})
	}
}

// lockProbeKeyGen — fakeKeyGen, который запоминает, была ли занята блокировка
// F508 в момент генерации ключей.
type lockProbeKeyGen struct {
	fakeKeyGen
	svc       *Service
	underLock bool
}

func (k *lockProbeKeyGen) GenerateKeyPair(ctx context.Context) (string, string, error) {
	if k.svc.peerSubnetsMu.TryLock() {
		k.svc.peerSubnetsMu.Unlock()
	} else {
		k.underLock = true
	}
	return k.fakeKeyGen.GenerateKeyPair(ctx)
}

// M1: генерация ключей (exec awg) — не под блокировкой F508.
func TestAddPeer_KeygenOutsideLock(t *testing.T) {
	svc, _, poster, fg := newPeerSubnetTestService(t, `[]`)
	newSimRouter(t, fg, poster, `[]`)
	kg := &lockProbeKeyGen{svc: svc}
	svc.keyGen = kg
	if _, err := svc.AddPeer(context.Background(), "Wireguard1", AddPeerRequest{Description: "branch", RemoteSubnets: []string{"192.168.77.0/24"}}); err != nil {
		t.Fatal(err)
	}
	if kg.underLock {
		t.Fatal("ключи генерируются под блокировкой F508")
	}
}
