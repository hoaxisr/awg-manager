package pingcheck

import (
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func newTunnelStore(t *testing.T) *storage.AWGTunnelStore {
	t.Helper()
	dir := t.TempDir()
	return storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
}

// Требование 8: health бьёт по живому интерфейсу зеркальной записи. Id
// raw-записи цифр не несёт, поэтому общий резолв даёт для неё opkgtun0 —
// чужой существующий интерфейс, а не безобидный промах.
func TestResolveIfaceName_WdttRawUsesLiveIface(t *testing.T) {
	store := newTunnelStore(t)
	if err := store.Save(&storage.AWGTunnel{
		ID:             "wdttraw-de",
		Name:           "Германия",
		Backend:        "wdtt-raw",
		RawKernelIface: "opkgtun18",
	}); err != nil {
		t.Fatal(err)
	}
	s := &Service{tunnels: store}

	if got := s.resolveIfaceName("wdttraw-de"); got != "opkgtun18" {
		t.Fatalf("resolveIfaceName = %q, want opkgtun18", got)
	}
}

// Запись без живого имени интерфейса (инстанс ещё не поднялся) остаётся на
// общем резолве — ветка не должна возвращать пустую строку.
func TestResolveIfaceName_WdttRawWithoutLiveIface(t *testing.T) {
	store := newTunnelStore(t)
	if err := store.Save(&storage.AWGTunnel{
		ID:      "wdttraw-de",
		Name:    "Германия",
		Backend: "wdtt-raw",
	}); err != nil {
		t.Fatal(err)
	}
	s := &Service{tunnels: store}

	if got := s.resolveIfaceName("wdttraw-de"); got != "opkgtun0" {
		t.Fatalf("resolveIfaceName = %q, want opkgtun0", got)
	}
}

// Kernel-запись общий резолв не трогает.
func TestResolveIfaceName_KernelUnchanged(t *testing.T) {
	store := newTunnelStore(t)
	if err := store.Save(&storage.AWGTunnel{
		ID:      "awg3",
		Name:    "Kernel",
		Backend: "kernel",
		// Поле заполнено нарочно: kernel-ветка обязана его игнорировать.
		RawKernelIface: "opkgtun18",
	}); err != nil {
		t.Fatal(err)
	}
	s := &Service{tunnels: store}

	if got := s.resolveIfaceName("awg3"); got != "opkgtun3" {
		t.Fatalf("resolveIfaceName = %q, want opkgtun3", got)
	}
}
