package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAvitoCookies(t *testing.T) {
	dir := t.TempDir()

	netscapePath := filepath.Join(dir, "avito_netscape.txt")
	netscapeContent := "# Netscape HTTP Cookie File\n" +
		".avito.ru\tTRUE\t/\tTRUE\t0\tsessid\tabc123\r\n" +
		"#HttpOnly_.avito.ru\tTRUE\t/\tTRUE\t0\tft\txyz789\n"
	if err := os.WriteFile(netscapePath, []byte(netscapeContent), 0600); err != nil {
		t.Fatal(err)
	}
	if got := loadAvitoCookies(netscapePath); got != "sessid=abc123; ft=xyz789" {
		t.Fatalf("loadAvitoCookies(netscape) = %q, want %q", got, "sessid=abc123; ft=xyz789")
	}

	rawPath := filepath.Join(dir, "avito_raw.txt")
	if err := os.WriteFile(rawPath, []byte("  sessid=raw1; ft=raw2 \n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := loadAvitoCookies(rawPath); got != "sessid=raw1; ft=raw2" {
		t.Fatalf("loadAvitoCookies(raw file) = %q, want %q", got, "sessid=raw1; ft=raw2")
	}

	emptyPath := filepath.Join(dir, "avito_empty.txt")
	if err := os.WriteFile(emptyPath, []byte("# comment only\n\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := loadAvitoCookies(emptyPath); got != "" {
		t.Fatalf("loadAvitoCookies(empty file) = %q, want empty", got)
	}

	inline := "sessid=inline.val; ft=1"
	if got := loadAvitoCookies(inline); got != inline {
		t.Fatalf("loadAvitoCookies(inline) = %q, want %q", got, inline)
	}
}
