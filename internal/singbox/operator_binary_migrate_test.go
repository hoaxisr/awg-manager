package singbox

import (
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func writeMigrateFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// Процесс под прежним именем гасится, бинарь и meta переезжают на новое имя.
func TestMigrateLegacyBinaryStopsAndRenames(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "sing-box")
	binary := filepath.Join(dir, "awgm-box")
	pidPath := filepath.Join(dir, "sing-box.pid")
	writeMigrateFile(t, legacy, "old engine")
	writeMigrateFile(t, legacy+".meta.json", `{"version":"old"}`)
	// Движок опознаётся по argv0, поэтому процесс — shell под его именем.
	// Копия системного sleep не годится: в Ubuntu 26.04 это мультивызовный
	// uutils, под чужим именем он выходит сразу, и тест зеленел или падал
	// смотря по тому, успевала ли проба застать умирающий процесс.
	cmd := &exec.Cmd{Path: "/bin/sh", Args: []string{legacy, "-c", "read line"}}
	if _, err := cmd.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-exited })
	writeMigrateFile(t, pidPath, strconv.Itoa(cmd.Process.Pid))
	// Сразу после Start процесс может ещё не опознаваться (ядро заполняет
	// cmdline уже после точки, где Start возвращается).
	probe := NewProcess(legacy, "", pidPath)
	for {
		if running, _ := probe.IsRunning(); running {
			break
		}
		select {
		case <-exited:
			t.Fatal("legacy process died before migration")
		case <-time.After(time.Millisecond):
		}
	}

	migrateLegacyBinary(binary, filepath.Join(dir, "config.d"), pidPath, slog.New(slog.NewTextHandler(io.Discard, nil)))

	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("legacy process still running")
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy binary left behind: %v", err)
	}
	if b, err := os.ReadFile(binary); err != nil || string(b) != "old engine" {
		t.Fatalf("binary not renamed: %q, %v", b, err)
	}
	if b, err := os.ReadFile(binary + ".meta.json"); err != nil || string(b) != `{"version":"old"}` {
		t.Fatalf("meta not renamed: %q %v", b, err)
	}
}

// Новый бинарь уже стоит: старый удаляется, новый не трогается.
func TestMigrateLegacyBinaryKeepsExistingTarget(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "sing-box")
	binary := filepath.Join(dir, "awgm-box")
	writeMigrateFile(t, legacy, "old")
	writeMigrateFile(t, legacy+".meta.json", "old-meta")
	writeMigrateFile(t, binary, "new")

	migrateLegacyBinary(binary, filepath.Join(dir, "config.d"), filepath.Join(dir, "sing-box.pid"), slog.New(slog.NewTextHandler(io.Discard, nil)))

	for _, p := range []string{legacy, legacy + ".meta.json"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s left behind: %v", p, err)
		}
	}
	if b, _ := os.ReadFile(binary); string(b) != "new" {
		t.Fatalf("existing binary overwritten: %q", b)
	}
}
