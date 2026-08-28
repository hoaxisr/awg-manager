package wdttusers

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeStatsLogMode(t *testing.T) {
	if got := normalizeStatsLogMode(""); got != StatsLogModeRAM {
		t.Fatalf("empty=%q", got)
	}
	if got := normalizeStatsLogMode("OFF"); got != StatsLogModeOff {
		t.Fatalf("off=%q", got)
	}
}

func TestRedirectServerStatsLog_RAM(t *testing.T) {
	dir := t.TempDir()
	if err := redirectServerStatsLog(dir, "default", StatsLogModeRAM); err != nil {
		t.Fatal(err)
	}
	link, err := os.Readlink(filepath.Join(dir, "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	if link != "/tmp/awg-wdtt-server-default.log" {
		t.Fatalf("link=%q", link)
	}
}

func TestRedirectServerStatsLog_Off(t *testing.T) {
	dir := t.TempDir()
	if err := redirectServerStatsLog(dir, "srv1", StatsLogModeOff); err != nil {
		t.Fatal(err)
	}
	link, err := os.Readlink(filepath.Join(dir, "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	if link != "/dev/null" {
		t.Fatalf("link=%q", link)
	}
}
