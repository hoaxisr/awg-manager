package api

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestSyncLinkedAwgTunnelEndpoints(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	tun := &storage.AWGTunnel{
		ID:               "awgm1",
		Name:             "FT",
		FreeTurnClientID: "client-a",
		Peer:             storage.AWGPeer{Endpoint: "127.0.0.1:9000"},
	}
	if err := store.Save(tun); err != nil {
		t.Fatal(err)
	}

	updated, errs := syncLinkedAwgTunnelEndpoints(context.Background(), store, nil, nil, func(t storage.AWGTunnel) bool {
		return tunnelLinkedToFreeTurnClient(t, "client-a")
	}, "127.0.0.1:9001")
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(updated) != 1 || updated[0] != "awgm1" {
		t.Fatalf("updated = %v", updated)
	}
	got, err := store.Get("awgm1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Peer.Endpoint != "127.0.0.1:9001" {
		t.Fatalf("endpoint = %q", got.Peer.Endpoint)
	}

	updated, errs = syncLinkedAwgTunnelEndpoints(context.Background(), store, nil, nil, func(t storage.AWGTunnel) bool {
		return tunnelLinkedToFreeTurnClient(t, "client-a")
	}, "127.0.0.1:9001")
	if len(errs) != 0 || len(updated) != 0 {
		t.Fatalf("idempotent sync: updated=%v errs=%v", updated, errs)
	}
}

func TestSyncLinkedAwgTunnelNames(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	tun := &storage.AWGTunnel{
		ID:               "awgm1",
		Name:             "Old FT",
		FreeTurnClientID: "client-a",
	}
	if err := store.Save(tun); err != nil {
		t.Fatal(err)
	}

	renamed, errs := syncLinkedAwgTunnelNames(context.Background(), store, nil, func(t storage.AWGTunnel) bool {
		return tunnelLinkedToFreeTurnClient(t, "client-a")
	}, "New FT")
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(renamed) != 1 || renamed[0] != "awgm1" {
		t.Fatalf("renamed = %v", renamed)
	}
	got, err := store.Get("awgm1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "New FT" {
		t.Fatalf("name = %q", got.Name)
	}

	renamed, errs = syncLinkedAwgTunnelNames(context.Background(), store, nil, func(t storage.AWGTunnel) bool {
		return tunnelLinkedToFreeTurnClient(t, "client-a")
	}, "New FT")
	if len(errs) != 0 || len(renamed) != 0 {
		t.Fatalf("idempotent rename: renamed=%v errs=%v", renamed, errs)
	}
}
