package storage

import (
	"os"
	"path/filepath"
	"testing"
)

// Занятость номеров OpkgTun обязана отличать «записей нет» от «не смогли
// посмотреть»: прощающее перечисление на битом файле уносит его в карантин и
// продолжает, то есть молча освобождает номер туннеля, который никуда не делся.
func TestListStrictFailsOnCorruptFile(t *testing.T) {
	store, dir := newTestAWGStore(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(dir, "awg10.json")
	if err := os.WriteFile(good, []byte(`{"id":"awg10","backend":"kernel"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "awg11.json")
	if err := os.WriteFile(bad, []byte(`{"id":"awg11",`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := store.ListStrict(); err == nil {
		t.Fatal("ListStrict на битом файле обязан вернуть ошибку")
	}
	if _, err := os.Stat(bad); err != nil {
		t.Errorf("ListStrict не должен трогать файлы, а awg11.json исчез: %v", err)
	}
}

func TestListStrictMissingDirIsEmpty(t *testing.T) {
	store, _ := newTestAWGStore(t)

	tunnels, err := store.ListStrict()
	if err != nil {
		t.Fatalf("отсутствующий каталог — законное «пусто», got: %v", err)
	}
	if len(tunnels) != 0 {
		t.Errorf("ожидалась пустая выдача, got %d", len(tunnels))
	}
}

func TestListStrictReadsAll(t *testing.T) {
	store, dir := newTestAWGStore(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"awg10", "awg13"} {
		p := filepath.Join(dir, id+".json")
		if err := os.WriteFile(p, []byte(`{"id":"`+id+`","backend":"kernel"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	tunnels, err := store.ListStrict()
	if err != nil {
		t.Fatalf("ListStrict: %v", err)
	}
	if len(tunnels) != 2 {
		t.Fatalf("ожидались обе записи, got %d", len(tunnels))
	}
	for _, tn := range tunnels {
		if tn.Type != "awg" {
			t.Errorf("%s: пустой Type должен доопределяться в awg, got %q", tn.ID, tn.Type)
		}
	}
}
