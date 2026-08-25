package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

const sampleConf = `[Interface]
PrivateKey = CA9lE1yLCcziI8Oq0dXDYr3QtdzFCEsKYw8sxAQ132o=
Address = 10.8.0.2/32

[Peer]
PublicKey = hOPHc7ZBk0dGrLLDFrCG7WHYzZ8SS5xBWMzOJ9CkNFo=
Endpoint = 192.0.2.1:51820
AllowedIPs = 0.0.0.0/0
`

func serviceWithStore(t *testing.T) (*ServiceImpl, string) {
	t.Helper()
	dir := t.TempDir()
	confs := filepath.Join(dir, "confs")
	old := tunnel.ConfDir
	tunnel.ConfDir = confs
	t.Cleanup(func() { tunnel.ConfDir = old })

	store := storage.NewAWGTunnelStoreWithLockDir(filepath.Join(dir, "tunnels"), filepath.Join(dir, "locks"))
	return &ServiceImpl{store: store, state: NewMockStateManager()}, confs
}

// Файл .conf нужен только kernel-пути: его читает `awg setconf`. У NativeWG
// конфигурация уезжает в NDMS байтами через RCI (nwg-оператор генерирует её из
// записи), а экспорт пользователю — тоже регенерация. То есть для nativewg файл
// на диске не читает никто, и писать его незачем.
func TestReplaceConfigSkipsConfForNativeWG(t *testing.T) {
	s, confs := serviceWithStore(t)
	stored := &storage.AWGTunnel{ID: "awg20", Name: "nwg", Backend: "nativewg"}
	if err := s.store.Save(stored); err != nil {
		t.Fatal(err)
	}

	if err := s.ReplaceConfig(context.Background(), "awg20", sampleConf, ""); err != nil {
		t.Fatalf("ReplaceConfig: %v", err)
	}

	if _, err := os.Stat(filepath.Join(confs, "awg20.conf")); !os.IsNotExist(err) {
		t.Errorf("для nativewg .conf писать не нужно, got %v", err)
	}
}

func TestReplaceConfigWritesConfForKernel(t *testing.T) {
	s, confs := serviceWithStore(t)
	stored := &storage.AWGTunnel{ID: "awg10", Name: "k", Backend: "kernel"}
	if err := s.store.Save(stored); err != nil {
		t.Fatal(err)
	}

	if err := s.ReplaceConfig(context.Background(), "awg10", sampleConf, ""); err != nil {
		t.Fatalf("ReplaceConfig: %v", err)
	}

	if _, err := os.Stat(filepath.Join(confs, "awg10.conf")); err != nil {
		t.Errorf("kernel-путь читает этот файл через awg setconf, он обязан быть: %v", err)
	}
}
