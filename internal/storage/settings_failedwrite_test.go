package storage

import (
	"os"
	"testing"
)

// failWrites делает каталог настроек read-only: AtomicWrite падает на создании
// temp-файла. Образец root-скипа — awg_store_strict_test.go.
func failWrites(t *testing.T, dir string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("под root chmod не запрещает запись")
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

// F3: saveUnlocked публиковал s.settings ДО AtomicWrite — при провале записи
// кэш нёс незаписанное.
func TestUpdate_FailedWriteKeepsCache(t *testing.T) {
	dir := t.TempDir()
	store := NewSettingsStore(dir)
	if _, err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := store.Update(func(s *Settings) error {
		s.DisableMemorySaving = true
		return nil
	}); err != nil {
		t.Fatalf("seed Update: %v", err)
	}
	failWrites(t, dir)
	if err := store.Update(func(s *Settings) error {
		s.DisableMemorySaving = false
		return nil
	}); err == nil {
		t.Fatal("ожидался отказ записи")
	}
	got, err := store.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.DisableMemorySaving {
		t.Fatal("кэш несёт незаписанное: провал AtomicWrite опубликовал мутацию")
	}
}
