package storage

import (
	"os"
	"path/filepath"
	"testing"
)

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
