package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

func withConfDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "confs")
	old := tunnel.ConfDir
	tunnel.ConfDir = dir
	t.Cleanup(func() { tunnel.ConfDir = old })
	return dir
}

func sampleTunnel() *storage.AWGTunnel {
	return &storage.AWGTunnel{
		ID:   "awg10",
		Name: "t",
		Interface: storage.AWGInterface{
			PrivateKey: "CA9lE1yLCcziI8Oq0dXDYr3QtdzFCEsKYw8sxAQ132o=",
			Address:    "10.8.0.2/32",
			MTU:        1280,
		},
		Peer: storage.AWGPeer{
			PublicKey:  "hOPHc7ZBk0dGrLLDFrCG7WHYzZ8SS5xBWMzOJ9CkNFo=",
			Endpoint:   "192.0.2.1:51820",
			AllowedIPs: []string{"0.0.0.0/0"},
		},
	}
}

func TestWriteFileCreatesDirAndFile(t *testing.T) {
	dir := withConfDir(t)

	if err := WriteFile(sampleTunnel()); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	path := filepath.Join(dir, "awg10.conf")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("файл не создан: %v", err)
	}
	if !strings.Contains(string(data), "[Interface]") {
		t.Errorf("содержимое не похоже на конфиг: %q", string(data))
	}
}

// Путь записи обязан совпадать с тем, по которому файл потом применяют и
// удаляют, — иначе `awg setconf` возьмёт не тот файл.
func TestWriteFileUsesNamesPath(t *testing.T) {
	withConfDir(t)
	stored := sampleTunnel()

	if err := WriteFile(stored); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := os.Stat(tunnel.NewNames(stored.ID).ConfPath); err != nil {
		t.Errorf("файл не там, где его будут искать: %v", err)
	}
}

func TestWriteFilePermissions(t *testing.T) {
	dir := withConfDir(t)

	if err := WriteFile(sampleTunnel()); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, "awg10.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("права = %o, want 600 — в файле приватный ключ", perm)
	}
}

func TestRemoveFile(t *testing.T) {
	dir := withConfDir(t)
	if err := WriteFile(sampleTunnel()); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	RemoveFile("awg10")

	if _, err := os.Stat(filepath.Join(dir, "awg10.conf")); !os.IsNotExist(err) {
		t.Errorf("файл должен быть удалён, got %v", err)
	}
}

func TestRemoveFileMissingIsNoError(t *testing.T) {
	withConfDir(t)
	RemoveFile("awg99") // не паникует и не падает
}
