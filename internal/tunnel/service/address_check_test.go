package service

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Проверка конфликта по СТРОКЕ адреса нужна до создания записи: wdttlink
// отказывает импорту конфига, чей адрес уже занят другим туннелем (#869).
func TestStoredAddressConflicts_ByAddress(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(filepath.Join(dir, "tunnels"), filepath.Join(dir, "locks"))
	if err := store.Create(&storage.AWGTunnel{ID: "awg17", Name: "Сервер A", Backend: "kernel",
		Interface: storage.AWGInterface{Address: "10.66.0.4/32"}}); err != nil {
		t.Fatal(err)
	}

	got := StoredAddressConflicts(store, "10.66.0.4/32", "")
	if len(got) != 1 || !strings.Contains(got[0], "Сервер A") || !strings.Contains(got[0], "10.66.0.4") {
		t.Fatalf("конфликт не найден или без имени: %v", got)
	}
	if got := StoredAddressConflicts(store, "10.66.0.5/32", ""); len(got) != 0 {
		t.Fatalf("ложный конфликт: %v", got)
	}
	if got := StoredAddressConflicts(store, "10.66.0.4/32", "awg17"); len(got) != 0 {
		t.Fatalf("исключённый id всё равно конфликтует: %v", got)
	}
}
