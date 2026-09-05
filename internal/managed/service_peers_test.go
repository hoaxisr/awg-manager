package managed

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// DeletePeer снимает пира из NDMS (`peer no key`) и из storage; иначе отозванный
// клиент продолжает ходить через сервер (доступ снят только на бумаге).
func TestDeletePeer_RemovesPeerFromNDMSAndStorage(t *testing.T) {
	const pub = "PEERKEY0123456789abcdef="
	svc, poster, store := newTestService(t, &storage.ManagedServer{
		InterfaceName: "Wireguard0",
		Peers:         []storage.ManagedPeer{{PublicKey: pub, Description: "phone"}, {PublicKey: "OTHERKEY0123456789=", Description: "laptop"}},
	}, nil, `{}`)

	if err := svc.DeletePeer(context.Background(), "Wireguard0", pub); err != nil {
		t.Fatalf("DeletePeer: %v", err)
	}

	if len(poster.posts) != 1 {
		t.Fatalf("RCI POST'ов %d, want 1: %v", len(poster.posts), poster.posts)
	}
	got, _ := json.Marshal(poster.posts[0])
	want := `{"interface":{"Wireguard0":{"wireguard":{"peer":[{"key":"PEERKEY0123456789abcdef=","no":true}]}}}}`
	if string(got) != want {
		t.Fatalf("RCI payload:\n got %s\nwant %s", got, want)
	}
	sv, ok := store.GetManagedServerByID("Wireguard0")
	if !ok || len(sv.Peers) != 1 || sv.Peers[0].PublicKey != "OTHERKEY0123456789=" {
		t.Fatalf("в storage должен остаться только второй пир: %+v", sv)
	}
}

func TestDeletePeer_UnknownKeyIsErrorWithoutRCI(t *testing.T) {
	svc, poster, _ := newTestService(t, &storage.ManagedServer{
		InterfaceName: "Wireguard0",
		Peers:         []storage.ManagedPeer{{PublicKey: "PEERKEY0123456789abcdef=", Description: "phone"}},
	}, nil, `{}`)
	if err := svc.DeletePeer(context.Background(), "Wireguard0", "NOPE0123456789="); err == nil {
		t.Fatal("неизвестный ключ обязан быть ошибкой")
	}
	if len(poster.posts) != 0 {
		t.Fatalf("RCI не ждём: %v", poster.posts)
	}
}

// Отказ RCI на снятии пира — отказ DeletePeer, и пир ОСТАЁТСЯ в storage: раньше запись
// исчезала с карточки, а пир продолжал ходить через сервер (fail-open).
func TestDeletePeer_RCIFailureKeepsPeerInStorage(t *testing.T) {
	const pub = "PEERKEY0123456789abcdef="
	svc, _, store := newTestService(t, &storage.ManagedServer{
		InterfaceName: "Wireguard0",
		Peers:         []storage.ManagedPeer{{PublicKey: pub, Description: "phone"}},
	}, errors.New("rci: connection refused"), `{}`)

	err := svc.DeletePeer(context.Background(), "Wireguard0", pub)
	if err == nil || !strings.Contains(err.Error(), "remove peer via RCI: ") {
		t.Fatalf("err = %v", err)
	}
	sv, ok := store.GetManagedServerByID("Wireguard0")
	if !ok || len(sv.Peers) != 1 || sv.Peers[0].PublicKey != pub {
		t.Fatalf("пир должен остаться в storage: %+v", sv)
	}
}
