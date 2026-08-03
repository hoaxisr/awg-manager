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
	// Кэш-ветка: прогреваем кэш, далее мутируем результат Load.
	t.Run("cache", func(t *testing.T) {
		store := NewStore(t.TempDir())
		if _, err := store.Load(); err != nil {
			t.Fatal(err)
		}
		assertLoadIsolated(t, store)
	})

	// Первый Load, disk-ветка file-missing (DefaultConfig): срез результата
	// не должен алиасить кэш, который выставил saveLocked.
	t.Run("first-load-missing", func(t *testing.T) {
		store := NewStore(t.TempDir())
		assertLoadIsolated(t, store)
	})

	// Первый Load, disk-ветка file-exists: читаем готовый файл, срез
	// результата не должен алиасить кэш s.cfg.
	t.Run("first-load-existing", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "wdtt.json"),
			[]byte(`{"version":1,"clients":[{"id":"default","name":"n"}]}`), 0644); err != nil {
			t.Fatal(err)
		}
		assertLoadIsolated(t, NewStore(dir))
	})
}

// assertLoadIsolated: результат первого зафиксированного Load мутируется, и
// эта мутация не должна быть видна в последующем Load (т.е. не протекла в кэш).
func assertLoadIsolated(t *testing.T, store *Store) {
	t.Helper()
	cfg, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Clients) == 0 {
		t.Fatal("want at least one client")
	}
	cfg.Clients[0].Config.Password = "leaked"

	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Clients[0].Config.Password == "leaked" {
		t.Fatal("мутация результата Load протекла в кэш")
	}
}

// Вложенный срез клиентов сервера тоже обязан быть изолирован: иначе правка
// списка мутирует кэш стора до Save и гоняется с читателями.
func TestStore_LoadIsolatesServerClients(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	cfg, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Servers[0].Config.Clients = []ServerClient{{Password: "p1", Comment: "Иван"}}
	if err := store.Save(cfg); err != nil {
		t.Fatal(err)
	}

	first, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	first.Servers[0].Config.Clients[0].Comment = "leaked"

	second, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if second.Servers[0].Config.Clients[0].Comment != "Иван" {
		t.Fatal("мутация клиентов из результата Load протекла в кэш")
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
