package api

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestTunnelLinkedToFreeTurnClient(t *testing.T) {
	tun := storage.AWGTunnel{ID: "awgm1", FreeTurnClientID: "client-a"}
	if !tunnelLinkedToFreeTurnClient(tun, "client-a") {
		t.Fatal("tagged tunnel must match its client id")
	}
	if tunnelLinkedToFreeTurnClient(tun, "client-b") {
		t.Fatal("must not match another client id")
	}
	if tunnelLinkedToFreeTurnClient(storage.AWGTunnel{Peer: storage.AWGPeer{Endpoint: "127.0.0.1:9000"}}, "client-a") {
		t.Fatal("manual tunnel without tag must not match")
	}
}
