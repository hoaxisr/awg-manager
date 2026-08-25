package ftlink

import (
	"os"
	"path/filepath"
	"testing"
)

// Перенос файловых тестов allowlist.go старого пакета (allowlist_test.go:8-62).
// Сервисные тесты (включение списка, выключение) пересажены на новые швы и
// живут в allowlist_service_test.go.

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
