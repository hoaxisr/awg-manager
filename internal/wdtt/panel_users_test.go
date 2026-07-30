//go:build !mips && !mipsle

package wdtt

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPanelUsersCRUD(t *testing.T) {
	dir := t.TempDir()
	mainPass := "abc123def4567890abcdef1234567890"

	// До первого старта сервера panel.db ещё нет: чтение не создаёт файл.
	st, err := loadPanelUsers(dir, mainPass)
	if err != nil {
		t.Fatal(err)
	}
	if st.Available {
		t.Fatal("expected unavailable before panel.db exists")
	}
	if len(st.Users) != 0 {
		t.Fatalf("expected empty users, got %d", len(st.Users))
	}

	st, err = addPanelUser(dir, mainPass, "", "Alice", "vkhash1")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Users) != 1 {
		t.Fatalf("expected 1 user, got %d", len(st.Users))
	}
	if st.Users[0].Comment != "Alice" {
		t.Fatalf("comment: %q", st.Users[0].Comment)
	}
	clientPass := st.Users[0].Password

	st, err = loadPanelUsers(dir, mainPass)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Users) != 1 {
		t.Fatalf("reload: expected 1 user, got %d", len(st.Users))
	}

	st, err = removePanelUser(dir, mainPass, clientPass)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Users) != 0 {
		t.Fatalf("after delete: expected 0 users, got %d", len(st.Users))
	}

	if _, err := removePanelUser(dir, mainPass, mainPass); err == nil {
		t.Fatal("expected error deleting main password")
	}

	if _, err := os.Stat(filepath.Join(dir, "panel.db")); err != nil {
		t.Fatalf("panel.db missing: %v", err)
	}
}
