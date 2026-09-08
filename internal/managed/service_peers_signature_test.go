package managed

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/signature"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// testPeerPubKey — ключ реальной длины: UpdatePeer логирует pubkey[:8]
// и на коротком литерале паникует (находка, см. отчёт задачи).
const testPeerPubKey = "TESTPEERPUBKEYAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

func TestAddPeer_GeneratesDefaultSignature(t *testing.T) {
	svc, store, _ := newCreateTestService(t)
	if err := store.AddManagedServer(storage.ManagedServer{InterfaceName: "Wireguard1", Address: "10.0.0.1", Mask: "255.255.255.0", ListenPort: 51820, Policy: "none"}); err != nil {
		t.Fatal(err)
	}
	peer, err := svc.AddPeer(context.Background(), "Wireguard1", AddPeerRequest{Description: "phone", TunnelIP: "10.0.0.2/32"})
	if err != nil {
		t.Fatal(err)
	}
	if peer.SignatureProfile != signature.DefaultProfile || !strings.HasPrefix(peer.I1, "<b 0xc") || peer.I2 != "" {
		t.Fatalf("peer signature: profile=%q I1=%.20s I2=%q", peer.SignatureProfile, peer.I1, peer.I2)
	}
	stored, _ := store.GetManagedServerByID("Wireguard1")
	if stored.Peers[0].I1 != peer.I1 {
		t.Fatal("signature must be persisted")
	}
}

func TestUpdatePeer_SignatureOptionalAndValidated(t *testing.T) {
	svc, store, _ := newCreateTestService(t)
	_ = store.AddManagedServer(storage.ManagedServer{InterfaceName: "Wireguard1", Address: "10.0.0.1", Mask: "255.255.255.0", ListenPort: 51820, Policy: "none",
		Peers: []storage.ManagedPeer{{PublicKey: testPeerPubKey, TunnelIP: "10.0.0.2/32", Description: "d", I1: "<b 0x01>", SignatureProfile: "dns", Enabled: true}}})
	// nil Signature — не трогаем
	if err := svc.UpdatePeer(context.Background(), "Wireguard1", testPeerPubKey, UpdatePeerRequest{Description: "d2", TunnelIP: "10.0.0.2/32"}); err != nil {
		t.Fatal(err)
	}
	sv, _ := store.GetManagedServerByID("Wireguard1")
	if sv.Peers[0].I1 != "<b 0x01>" || sv.Peers[0].Description != "d2" {
		t.Fatalf("%+v", sv.Peers[0])
	}
	// явная сигнатура — заменяет все пять полей и профиль
	err := svc.UpdatePeer(context.Background(), "Wireguard1", testPeerPubKey, UpdatePeerRequest{Description: "d2", TunnelIP: "10.0.0.2/32",
		Signature: &PeerSignature{Profile: "sip", I1: "<b 0x02>", I2: "<rc 3>"}})
	if err != nil {
		t.Fatal(err)
	}
	sv, _ = store.GetManagedServerByID("Wireguard1")
	if sv.Peers[0].I1 != "<b 0x02>" || sv.Peers[0].I2 != "<rc 3>" || sv.Peers[0].SignatureProfile != "sip" {
		t.Fatalf("%+v", sv.Peers[0])
	}
	// профиль нормализуется к каноническому ключу
	err = svc.UpdatePeer(context.Background(), "Wireguard1", testPeerPubKey, UpdatePeerRequest{Description: "d2", TunnelIP: "10.0.0.2/32",
		Signature: &PeerSignature{Profile: " SIP ", I1: "<b 0x03>"}})
	if err != nil {
		t.Fatal(err)
	}
	sv, _ = store.GetManagedServerByID("Wireguard1")
	if sv.Peers[0].SignatureProfile != "sip" {
		t.Fatalf("profile must be canonical, got %q", sv.Peers[0].SignatureProfile)
	}
	// превышение лимита
	err = svc.UpdatePeer(context.Background(), "Wireguard1", testPeerPubKey, UpdatePeerRequest{Description: "d2", TunnelIP: "10.0.0.2/32",
		Signature: &PeerSignature{I1: "<r 1000><r 1000><r 1000><r 1000><r 97>"}})
	if !errors.Is(err, ErrSignatureTooLarge) {
		t.Fatalf("err = %v", err)
	}
	// неизвестный профиль
	err = svc.UpdatePeer(context.Background(), "Wireguard1", testPeerPubKey, UpdatePeerRequest{Description: "d2", TunnelIP: "10.0.0.2/32",
		Signature: &PeerSignature{Profile: "tls", I1: "<b 0x02>"}})
	if err == nil {
		t.Fatal("unknown profile must be rejected")
	}
}

// У сервера сигнатуры нет: что бы ни отдал NDMS (в форме 5.1 ключи i1..i5
// присутствуют всегда), значения обязаны быть пустыми — сливать туда нечего.
func TestGetASCParams_NoSignatureFieldsAnymore(t *testing.T) {
	svc, store, getter := newCreateTestService(t)
	_ = store.AddManagedServer(storage.ManagedServer{InterfaceName: "Wireguard1", Address: "10.0.0.1", Mask: "255.255.255.0", ListenPort: 51820, Policy: "none"})
	seedASC(t, getter, "Wireguard1")
	raw, err := svc.GetASCParams(context.Background(), "Wireguard1")
	if err != nil {
		t.Fatal(err)
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatalf("unmarshal ASC: %v (%s)", err, raw)
	}
	for _, key := range []string{"i1", "i2", "i3", "i4", "i5"} {
		v, ok := params[key]
		if !ok {
			continue
		}
		if s := strings.TrimSpace(string(v)); s != `""` && s != "null" {
			t.Fatalf("server ASC must not carry %s: %s", key, raw)
		}
	}
}
