package api

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

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
