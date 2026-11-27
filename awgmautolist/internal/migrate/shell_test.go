// One-time migration of the shell-sourced legacy files (BACKLOG section 8):
// the API key and the enabled intent move into the working directory only
// while the new file is absent; a fresh file always wins and the legacy one
// is never read again, permissions survive the move and a failed move keeps
// the source in place.
package migrate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMoveShellFilesMovesKeyAndEnabled(t *testing.T) {
	dir := t.TempDir()
	legacy := ShellFiles{
		Key:     filepath.Join(dir, "awgmautolist.key"),
		Enabled: filepath.Join(dir, "awgmautolist.enabled"),
		Conf:    filepath.Join(dir, "awgmautolist.conf"),
	}
	writeFile(t, legacy.Key, "AWGM_API_KEY=secret", 0o600)
	writeFile(t, legacy.Enabled, "ENABLED=yes", 0o644)
	writeFile(t, legacy.Conf, "AWGM_LISTEN=127.0.0.1:2226", 0o600)
	out := filepath.Join(dir, "out")

	moved, err := MoveShellFiles(out, legacy, nil)
	if err != nil || moved != 3 {
		t.Fatalf("moved=%d err=%v", moved, err)
	}
	mustContain(t, filepath.Join(out, "key"), "AWGM_API_KEY=secret")
	mustContain(t, filepath.Join(out, "enabled"), "ENABLED=yes")
	mustContain(t, filepath.Join(out, "awgmautolist.conf"), "AWGM_LISTEN=127.0.0.1:2226")
	info, err := os.Stat(filepath.Join(out, "key"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key perm = %v, %v", info, err)
	}
	for _, old := range []string{legacy.Key, legacy.Enabled, legacy.Conf} {
		if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s survived the move: %v", old, err)
		}
	}

	// Second run: nothing left to move.
	moved, err = MoveShellFiles(out, legacy, nil)
	if err != nil || moved != 0 {
		t.Fatalf("second run: moved=%d err=%v", moved, err)
	}
}

// A fresh key/enabled in the working directory has priority: the legacy file
// is not moved, not read and must not overwrite the new one.
func TestMoveShellFilesNeverOverwritesFreshFiles(t *testing.T) {
	dir := t.TempDir()
	legacy := ShellFiles{
		Key:     filepath.Join(dir, "awgmautolist.key"),
		Enabled: filepath.Join(dir, "awgmautolist.enabled"),
	}
	writeFile(t, legacy.Key, "AWGM_API_KEY=old", 0o600)
	writeFile(t, legacy.Enabled, "ENABLED=no", 0o644)
	out := filepath.Join(dir, "out")
	writeFile(t, filepath.Join(out, "key"), "AWGM_API_KEY=fresh", 0o600)

	moved, err := MoveShellFiles(out, legacy, nil)
	if err != nil || moved != 1 {
		t.Fatalf("moved=%d err=%v", moved, err)
	}
	mustContain(t, filepath.Join(out, "key"), "AWGM_API_KEY=fresh")
	// The legacy key stays on disk as an unread leftover; the fresh enabled
	// was absent, so only that one moved.
	if _, err := os.Stat(legacy.Key); err != nil {
		t.Fatalf("legacy key removed despite the fresh file: %v", err)
	}
	mustContain(t, filepath.Join(out, "enabled"), "ENABLED=no")
	if _, err := os.Stat(legacy.Enabled); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy enabled survived the move: %v", err)
	}
}

func TestMoveShellFilesSkipsMissing(t *testing.T) {
	dir := t.TempDir()
	legacy := ShellFiles{Key: filepath.Join(dir, "awgmautolist.key")}
	moved, err := MoveShellFiles(filepath.Join(dir, "out"), legacy, nil)
	if err != nil || moved != 0 {
		t.Fatalf("moved=%d err=%v", moved, err)
	}
}

// A failed move reports the error and keeps the source in place, so the
// migration is retried on the next start instead of losing the key.
func TestMoveShellFilesKeepsSourceOnFailure(t *testing.T) {
	dir := t.TempDir()
	legacy := ShellFiles{Key: filepath.Join(dir, "awgmautolist.key")}
	writeFile(t, legacy.Key, "AWGM_API_KEY=secret", 0o600)
	out := filepath.Join(dir, "out")
	if err := os.WriteFile(out, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	moved, err := MoveShellFiles(out, legacy, nil)
	if err == nil || moved != 0 {
		t.Fatalf("moved=%d err=%v, want a reported failure", moved, err)
	}
	mustContain(t, legacy.Key, "AWGM_API_KEY=secret")
	if _, statErr := os.Stat(filepath.Join(out, "key")); statErr == nil {
		t.Fatal("partial target appeared")
	}
}

func TestLegacyShellPaths(t *testing.T) {
	got := LegacyShell()
	if got.Key != "/opt/etc/awgmautolist.key" || got.Enabled != "/opt/etc/awgmautolist.enabled" || got.Conf != "/opt/etc/awgmautolist.conf" {
		t.Fatalf("legacy shell = %+v", got)
	}
}
