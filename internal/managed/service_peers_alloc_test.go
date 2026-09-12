package managed

import (
	"context"
	"errors"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/managed/peerip"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// TestAddPeer_AllocatesTunnelIPWhenEmpty — пустой TunnelIP означает «выдать
// первый свободный адрес в подсети сервера». Раньше это делал адаптер MCP
// своей копией правила; теперь правило одно (peerip) и применяется здесь.
func TestAddPeer_AllocatesTunnelIPWhenEmpty(t *testing.T) {
	svc, store, _ := newCreateTestService(t)
	if err := store.AddManagedServer(storage.ManagedServer{
		InterfaceName: "Wireguard1", Address: "10.0.0.1", Mask: "255.255.255.0", ListenPort: 51820, Policy: "none",
		Peers: []storage.ManagedPeer{{PublicKey: testPeerPubKey, TunnelIP: "10.0.0.2/32", Description: "laptop", Enabled: true}},
	}); err != nil {
		t.Fatal(err)
	}
	peer, err := svc.AddPeer(context.Background(), "Wireguard1", AddPeerRequest{Description: "phone"})
	if err != nil {
		t.Fatal(err)
	}
	if peer.TunnelIP != "10.0.0.3/32" {
		t.Fatalf("allocated %q, want the first free address after the existing peer", peer.TunnelIP)
	}
	stored, _ := store.GetManagedServerByID("Wireguard1")
	if len(stored.Peers) != 2 || stored.Peers[1].TunnelIP != "10.0.0.3/32" {
		t.Fatalf("persisted peers = %+v", stored.Peers)
	}

	// An explicit address is still validated, not replaced.
	if _, err := svc.AddPeer(context.Background(), "Wireguard1", AddPeerRequest{Description: "x", TunnelIP: "not-a-cidr"}); err == nil {
		t.Fatal("an invalid explicit address must still be refused")
	}

	// A server whose address the allocator cannot use yields a typed error
	// the caller can turn into "ask the user", not a peer with no address.
	if err := store.AddManagedServer(storage.ManagedServer{InterfaceName: "Wireguard6", Address: "fd00::1", Mask: "255.255.255.0", ListenPort: 51821, Policy: "none"}); err != nil {
		t.Fatal(err)
	}
	_, err = svc.AddPeer(context.Background(), "Wireguard6", AddPeerRequest{Description: "phone"})
	if !errors.Is(err, peerip.ErrNoFree) {
		t.Fatalf("err = %v, want peerip.ErrNoFree", err)
	}
}
