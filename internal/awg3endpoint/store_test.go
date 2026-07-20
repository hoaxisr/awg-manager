package awg3endpoint

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestStore_CRUD(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "awg3.json"))
	rec := Record{ID: "id1", Tag: "AMS", Endpoint: json.RawMessage(`{"type":"awg"}`)}
	if err := s.Add(rec); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.List(); len(list) != 1 || list[0].Tag != "AMS" {
		t.Fatalf("list = %v", list)
	}
	if !s.Tags()["AMS"] {
		t.Fatal("Tags missing AMS")
	}
	if err := s.Rename("id1", "AMS2"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get("id1"); got.Tag != "AMS2" {
		t.Fatalf("rename failed: %v", got)
	}
	if err := s.Delete("id1"); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.List(); len(list) != 0 {
		t.Fatalf("delete failed: %v", list)
	}
}

func TestStore_Persists(t *testing.T) {
	p := filepath.Join(t.TempDir(), "awg3.json")
	s := NewStore(p)
	_ = s.Add(Record{ID: "id1", Tag: "T", Endpoint: json.RawMessage(`{}`)})
	if list, _ := NewStore(p).List(); len(list) != 1 {
		t.Fatal("not persisted")
	}
}
