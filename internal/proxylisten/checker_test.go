package proxylisten

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/freeturn"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/wdtt"
)

func TestCrossChecker_IncludesWdttForFreeTurn(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wdtt.json"), []byte(`{"clients":[{"id":"wdtt-a","name":"WDTT","config":{"listen":"127.0.0.1:9000"}}],"servers":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	checker := &CrossChecker{
		AWGStore:           storage.NewAWGTunnelStore(dir),
		WDTT:               wdtt.NewService(dir, filepath.Join(dir, "run"), "wdtt-client", "wdtt-server"),
		IncludeWdttClients: true,
	}
	used, err := checker.OccupiedLocalListenPorts()
	if err != nil {
		t.Fatal(err)
	}
	if !used[9000] {
		t.Fatalf("expected wdtt port 9000 reserved, got %v", used)
	}
}

func TestCrossChecker_IncludesFreeTurnForWdtt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "freeturn.json"), []byte(`{"clients":[{"id":"ft-a","name":"FT","config":{"listen":"127.0.0.1:9000"}}],"servers":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	checker := &CrossChecker{
		AWGStore:               storage.NewAWGTunnelStore(dir),
		FreeTurn:               freeturn.NewService(dir, filepath.Join(dir, "run"), "freeturn-client", "freeturn-server"),
		IncludeFreeTurnClients: true,
	}
	used, err := checker.OccupiedLocalListenPorts()
	if err != nil {
		t.Fatal(err)
	}
	if !used[9000] {
		t.Fatalf("expected freeturn port 9000 reserved, got %v", used)
	}
}

func TestCrossChecker_IncludesAWGTunnelEndpoint(t *testing.T) {
	dir := t.TempDir()
	awgStore := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	if err := awgStore.Save(&storage.AWGTunnel{
		ID:   "awg10",
		Name: "linked",
		Peer: storage.AWGPeer{Endpoint: "127.0.0.1:9002"},
	}); err != nil {
		t.Fatal(err)
	}

	checker := &CrossChecker{AWGStore: awgStore}
	used, err := checker.OccupiedLocalListenPorts()
	if err != nil {
		t.Fatal(err)
	}
	if !used[9002] {
		t.Fatalf("expected awg endpoint port 9002 reserved, got %v", used)
	}
}
