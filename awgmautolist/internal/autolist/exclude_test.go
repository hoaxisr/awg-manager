package autolist

import (
	"path/filepath"
	"testing"
	"time"
)

func TestExcludeDomainsPersistsAndForgets(t *testing.T) {
	eng := NewEngine("http://127.0.0.1:1", "")
	path := filepath.Join(t.TempDir(), "config.json")
	if err := eng.LoadConfig(path); err != nil {
		t.Fatal(err)
	}
	eng.table.Note(Host{Name: "www.example.com"}, WhyReset, "dial", time.Now())
	eng.table.Note(Host{Name: "other.example"}, WhyReset, "dial", time.Now())
	if err := eng.ExcludeDomains([]string{"Example.COM.", "example.com"}); err != nil {
		t.Fatal(err)
	}
	if len(eng.Config().ExcludeDomains) != 1 || eng.Config().ExcludeDomains[0] != "example.com" {
		t.Fatalf("excludeDomains = %v", eng.Config().ExcludeDomains)
	}
	rows := eng.table.List()
	if len(rows) != 1 || rows[0].Domain != "other.example" {
		t.Fatalf("кандидат не удалён: %+v", rows)
	}
	eng2 := NewEngine("http://127.0.0.1:1", "")
	if err := eng2.LoadConfig(path); err != nil {
		t.Fatal(err)
	}
	if len(eng2.Config().ExcludeDomains) != 1 {
		t.Fatal("исключение не пережило перезапуск")
	}
}
