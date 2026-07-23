package wdtt

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStore_DeleteAllClientsRestoresDefault(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	cfg, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Clients) != 1 {
		t.Fatalf("want 1 default client, got %d", len(cfg.Clients))
	}

	cfg.Clients = nil
	if err := store.Save(cfg); err != nil {
		t.Fatal(err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Clients) != 1 {
		t.Fatalf("after save with empty clients want 1, got %d", len(got.Clients))
	}
	if got.Clients[0].ID != DefaultInstanceID {
		t.Fatalf("want default id %q, got %q", DefaultInstanceID, got.Clients[0].ID)
	}
}

func TestStore_LoadReturnsIsolatedCopy(t *testing.T) {
	store := NewStore(t.TempDir())

	// Прогреваем кэш (как при старте сервиса), далее хендлеры читают из него.
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	cfg, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Clients) == 0 {
		t.Fatal("want at least one client")
	}
	// Мутируем результат Load — не должно затрагивать кэш.
	cfg.Clients[0].Config.Password = "leaked"

	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Clients[0].Config.Password == "leaked" {
		t.Fatal("мутация результата Load протекла в кэш")
	}
}

func TestStore_LoadEmptyFileRestoresDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wdtt.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"clients":[]}`), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := NewStore(dir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Clients) != 1 {
		t.Fatalf("want 1 client from empty file, got %d", len(got.Clients))
	}
}
