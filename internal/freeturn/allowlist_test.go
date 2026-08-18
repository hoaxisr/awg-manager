package freeturn

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAllowlistAddListRemove(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clients.json")

	if err := addAllowlistClient(path, "aabbccddeeff00112233445566778899", "Alice"); err != nil {
		t.Fatal(err)
	}
	if err := addAllowlistClient(path, "00112233445566778899aabbccddeeff", "Bob"); err != nil {
		t.Fatal(err)
	}

	st, err := loadAllowlistStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Enabled || len(st.Clients) != 2 {
		t.Fatalf("status=%+v", st)
	}

	if err := removeAllowlistClient(path, "aabbccddeeff00112233445566778899"); err != nil {
		t.Fatal(err)
	}
	st, err = loadAllowlistStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Clients) != 1 || st.Clients[0].Comment != "Bob" {
		t.Fatalf("after remove: %+v", st.Clients)
	}

	mode := os.FileMode(0)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	if mode&0077 != 0 {
		t.Fatalf("allowlist file should not be world-readable: %o", mode)
	}
}

func TestValidateAllowlistClientID(t *testing.T) {
	if err := validateAllowlistClientID(""); err == nil {
		t.Fatal("empty id")
	}
	if err := validateAllowlistClientID("not-hex"); err == nil {
		t.Fatal("non-hex")
	}
	if err := validateAllowlistClientID("aabbccddeeff00112233445566778899"); err != nil {
		t.Fatal(err)
	}
}

func TestAddServerAllowlistClient_EnablesOnFirstSave(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, "", "")
	res, err := s.AddServerAllowlistClient("default", "aabbccddeeff00112233445566778899", "test")
	if err != nil {
		t.Fatal(err)
	}
	if !res.NeedsRestart {
		t.Fatal("expected needsRestart on first enable")
	}
	cfg, err := s.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Servers[0].Config.ClientsFile == "" {
		t.Fatal("clientsFile not set")
	}
	st, err := s.ListServerAllowlist("default")
	if err != nil || len(st.Clients) != 1 {
		t.Fatalf("list: %+v err=%v", st, err)
	}
}

func TestDisableServerAllowlist_NeedsRestart(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, "", "")
	if _, err := s.AddServerAllowlistClient("default", "aabbccddeeff00112233445566778899", "test"); err != nil {
		t.Fatal(err)
	}

	needsRestart, err := s.DisableServerAllowlist("default")
	if err != nil {
		t.Fatal(err)
	}
	if !needsRestart {
		t.Fatal("expected needsRestart on disable: -clients-file is a start argument")
	}
	cfg, err := s.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Servers[0].Config.ClientsFile != "" {
		t.Fatal("clientsFile not cleared")
	}

	// Повторное выключение ничего не меняет — перезапускать нечего.
	needsRestart, err = s.DisableServerAllowlist("default")
	if err != nil {
		t.Fatal(err)
	}
	if needsRestart {
		t.Fatal("already disabled: needsRestart must be false")
	}
}
