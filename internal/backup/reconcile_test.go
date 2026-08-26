package backup

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// newRecords кладёт записи ЧЕРЕЗ хранилище: нормализация и инварианты те же,
// что у прода, и фикстура не может застыть в форме, которой store уже не
// отдаёт.
func newRecords(t *testing.T, dataDir string, recs ...instancestore.Record) *instancestore.Store {
	t.Helper()
	store := instancestore.New(dataDir)
	if _, err := store.Replace(func(st *instancestore.State) error {
		st.Records = recs
		return nil
	}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	return store
}

func TestReconcileLinkedEndpoints_SyncsFreeTurnAndWdtt(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "awg-manager")
	tunnelDir := filepath.Join(dataDir, "tunnels")
	if err := os.MkdirAll(tunnelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	records := newRecords(t, dataDir,
		instancestore.Record{ID: "ft1", Kind: instancestore.KindFreeTurnClient,
			// Порт НЕ 9000: это дефолт ListenPortFromAddr("") — на нём «пропатчено
			// дефолтом» неотличимо от «не тронуто», и freeturn-половина reconcile
			// осталась бы незастрахованной.
			FreeTurnClient: &roles.FreeTurnClientConfig{Listen: "127.0.0.1:9007"}},
		instancestore.Record{ID: "wd1", Kind: instancestore.KindWdttClient,
			WdttClient: &roles.WdttClientConfig{Mode: "wg", Listen: "127.0.0.1:9001"}},
	)

	store := storage.NewAWGTunnelStoreWithLockDir(tunnelDir, filepath.Join(root, "locks"))
	for _, tun := range []storage.AWGTunnel{
		{ID: "awg-ft", Name: "FT", FreeTurnClientID: "ft1", Peer: storage.AWGPeer{Endpoint: "127.0.0.1:9003"}},
		{ID: "awg-wd", Name: "WD", WdttClientID: "wd1", Peer: storage.AWGPeer{Endpoint: "127.0.0.1:9005"}},
	} {
		if err := store.Save(&tun); err != nil {
			t.Fatal(err)
		}
	}

	n, err := ReconcileLinkedEndpoints(records, store)
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
	if ft.Peer.Endpoint != "127.0.0.1:9007" {
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

// Туннель, чей клиент удалён, трогать нечем: адрес взять неоткуда, а дефолт
// 9000 увёл бы рабочий endpoint на порт чужого клиента.
func TestReconcileLinkedEndpoints_KeepsEndpointWhenClientGone(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "awg-manager")
	tunnelDir := filepath.Join(dataDir, "tunnels")
	if err := os.MkdirAll(tunnelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	records := newRecords(t, dataDir,
		instancestore.Record{ID: "wd1", Kind: instancestore.KindWdttClient,
			WdttClient: &roles.WdttClientConfig{Mode: "wg", Listen: "127.0.0.1:9000"}},
	)

	store := storage.NewAWGTunnelStoreWithLockDir(tunnelDir, filepath.Join(root, "locks"))
	orphan := storage.AWGTunnel{ID: "awg-gone", Name: "GONE", WdttClientID: "удалён",
		Peer: storage.AWGPeer{Endpoint: "127.0.0.1:9007"}}
	if err := store.Save(&orphan); err != nil {
		t.Fatal(err)
	}

	n, err := ReconcileLinkedEndpoints(records, store)
	if err != nil {
		t.Fatalf("ReconcileLinkedEndpoints: %v", err)
	}
	if n != 0 {
		t.Fatalf("updated = %d, want 0", n)
	}
	got, err := store.Get("awg-gone")
	if err != nil {
		t.Fatal(err)
	}
	if got.Peer.Endpoint != "127.0.0.1:9007" {
		t.Fatalf("endpoint осиротевшего туннеля = %q, ждали 127.0.0.1:9007", got.Peer.Endpoint)
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
