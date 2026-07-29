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
	used, err := checker.OccupiedLocalListenPorts("", "")
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
	used, err := checker.OccupiedLocalListenPorts("", "")
	if err != nil {
		t.Fatal(err)
	}
	if !used[9000] {
		t.Fatalf("expected freeturn port 9000 reserved, got %v", used)
	}
}

func TestCrossChecker_IncludesWdttServerPortsForFreeTurn(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wdtt.json"), []byte(`{"clients":[],"servers":[{"id":"wdtt-s","name":"WDTT","config":{"listen":"0.0.0.0:56002","wgPort":56001,"password":"x"}}]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	checker := &CrossChecker{
		WDTT: wdtt.NewService(dir, filepath.Join(dir, "run"), "wdtt-client", "wdtt-server"),
	}
	used, err := checker.OccupiedLocalListenPorts("", "")
	if err != nil {
		t.Fatal(err)
	}
	if !used[56001] || !used[56002] {
		t.Fatalf("expected wdtt server ports reserved, got %v", used)
	}
}

func TestCrossChecker_IncludesFreeTurnServerPortsForWdtt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "freeturn.json"), []byte(`{"clients":[],"servers":[{"id":"ft-s","name":"FT","config":{"listen":"0.0.0.0:56003","connect":"127.0.0.1:51820"}}]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	checker := &CrossChecker{
		FreeTurn: freeturn.NewService(dir, filepath.Join(dir, "run"), "freeturn-client", "freeturn-server"),
	}
	used, err := checker.OccupiedLocalListenPorts("", "")
	if err != nil {
		t.Fatal(err)
	}
	if !used[56003] {
		t.Fatalf("expected freeturn server port 56003 reserved, got %v", used)
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
	used, err := checker.OccupiedLocalListenPorts("", "")
	if err != nil {
		t.Fatal(err)
	}
	if !used[9002] {
		t.Fatalf("expected awg endpoint port 9002 reserved, got %v", used)
	}
}

// Туннель, привязанный к самому клиенту, не должен считаться занятым: иначе
// клиент на каждом старте видит свой же порт занятым и переезжает, а endpoint
// туннеля остаётся на старом — связка разваливается без шанса на самолечение.
func TestCrossChecker_SkipsOwnClientTunnel(t *testing.T) {
	dir := t.TempDir()
	awgStore := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	if err := awgStore.Save(&storage.AWGTunnel{
		ID:           "awg11",
		Name:         "linked-wdtt",
		WdttClientID: "client-a",
		Peer:         storage.AWGPeer{Endpoint: "127.0.0.1:9000"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := awgStore.Save(&storage.AWGTunnel{
		ID:               "awg12",
		Name:             "linked-freeturn",
		FreeTurnClientID: "client-b",
		Peer:             storage.AWGPeer{Endpoint: "127.0.0.1:9001"},
	}); err != nil {
		t.Fatal(err)
	}

	checker := &CrossChecker{AWGStore: awgStore}

	used, err := checker.OccupiedLocalListenPorts("client-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if used[9000] {
		t.Fatalf("собственный туннель клиента не должен резервировать 9000: %v", used)
	}
	if !used[9001] {
		t.Fatalf("чужой туннель обязан резервировать 9001: %v", used)
	}

	used, err = checker.OccupiedLocalListenPorts("", "client-b")
	if err != nil {
		t.Fatal(err)
	}
	if used[9001] {
		t.Fatalf("собственный туннель freeturn-клиента не должен резервировать 9001: %v", used)
	}
	if !used[9000] {
		t.Fatalf("чужой туннель обязан резервировать 9000: %v", used)
	}
}
