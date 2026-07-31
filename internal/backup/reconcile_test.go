package backup

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestReconcileLinkedEndpoints_SyncsFreeTurnAndWdtt(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "awg-manager")
	tunnelDir := filepath.Join(dataDir, "tunnels")
	if err := os.MkdirAll(tunnelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "freeturn.json"), []byte(`{
		"clients":[{"id":"ft1","name":"FT","config":{"listen":"127.0.0.1:9000"}}],
		"servers":[]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "wdtt.json"), []byte(`{
		"clients":[{"id":"wd1","name":"DE","config":{"listen":"127.0.0.1:9001"}}],
		"servers":[]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	store := storage.NewAWGTunnelStoreWithLockDir(tunnelDir, filepath.Join(root, "locks"))
	for _, tun := range []storage.AWGTunnel{
		{ID: "awg-ft", Name: "FT", FreeTurnClientID: "ft1", Peer: storage.AWGPeer{Endpoint: "127.0.0.1:9003"}},
		{ID: "awg-wd", Name: "WD", WdttClientID: "wd1", Peer: storage.AWGPeer{Endpoint: "127.0.0.1:9005"}},
	} {
		if err := store.Save(&tun); err != nil {
			t.Fatal(err)
		}
	}

	n, err := ReconcileLinkedEndpoints(dataDir, store)
	if err != nil {
		t.Fatalf("ReconcileLinkedEndpoints: %v", err)
	}
	if n != 2 {
		t.Fatalf("updated = %d, want 2", n)
	}
	ft, err := store.Get("awg-ft")
	if err != nil {
		t.Fatal(err)
	}
	if ft.Peer.Endpoint != "127.0.0.1:9000" {
		t.Fatalf("freeturn endpoint = %q", ft.Peer.Endpoint)
	}
	wd, err := store.Get("awg-wd")
	if err != nil {
		t.Fatal(err)
	}
	if wd.Peer.Endpoint != "127.0.0.1:9001" {
		t.Fatalf("wdtt endpoint = %q", wd.Peer.Endpoint)
	}
}

func TestRestoreWritesPostRestoreMarker(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "awg-manager")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "settings.json"), []byte(`{"version":32}`), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := Export(dataDir, "2.16.0", &buf); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "live")
	if err := Restore(target, bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatal(err)
	}
	if !HasPostRestoreMarker(target) {
		t.Fatal("expected post-restore marker after Restore")
	}
	if !ConsumePostRestoreMarker(target) {
		t.Fatal("ConsumePostRestoreMarker should find marker")
	}
	if HasPostRestoreMarker(target) {
		t.Fatal("marker should be removed after consume")
	}
}
