package storage

import (
	"slices"
	"testing"
)

func TestForeignInterfaces_MarkUnmarkPersist(t *testing.T) {
	dir := t.TempDir()
	s := NewSettingsStore(dir)
	if _, err := s.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, n := range []string{"opkgtun7", "csqtt0", "opkgtun7"} {
		if err := s.MarkForeignInterface(n); err != nil {
			t.Fatalf("mark %s: %v", n, err)
		}
	}
	if got := s.GetForeignInterfaces(); !slices.Equal(got, []string{"opkgtun7", "csqtt0"}) {
		t.Fatalf("после отметки = %v", got)
	}
	if err := s.UnmarkForeignInterface("opkgtun7"); err != nil {
		t.Fatalf("unmark: %v", err)
	}
	if err := s.UnmarkForeignInterface("absent0"); err != nil {
		t.Fatalf("unmark отсутствующего: %v", err)
	}
	s2 := NewSettingsStore(dir)
	if _, err := s2.Load(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := s2.GetForeignInterfaces(); !slices.Equal(got, []string{"csqtt0"}) {
		t.Fatalf("после перезагрузки = %v", got)
	}
}

func TestForeignInterfaces_GetReturnsCopy(t *testing.T) {
	s := NewSettingsStore(t.TempDir())
	if _, err := s.Load(); err != nil {
		t.Fatal(err)
	}
	_ = s.MarkForeignInterface("csqtt0")
	got := s.GetForeignInterfaces()
	got[0] = "mutated"
	if s.GetForeignInterfaces()[0] != "csqtt0" {
		t.Fatal("GetForeignInterfaces отдал внутренний срез")
	}
}
