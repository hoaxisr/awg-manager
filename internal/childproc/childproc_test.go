package childproc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// fakeDownloader writes fixed content to destPath, emulating a successful
// non-atomic download (Install activates via chmod+rename).
type fakeDownloader struct {
	content []byte
}

func (f fakeDownloader) DownloadFile(_ context.Context, _ string, destPath string, _ int64) error {
	return os.WriteFile(destPath, f.content, 0644)
}

func TestInstall(t *testing.T) {
	content := []byte("hello-binary")
	sum := sha256.Sum256(content)
	goodSHA := hex.EncodeToString(sum[:])
	d := fakeDownloader{content: content}

	t.Run("correct SHA installs", func(t *testing.T) {
		dir := t.TempDir()
		bin := filepath.Join(dir, "child")
		if err := Install(context.Background(), d, bin, "http://x/child", goodSHA, int64(len(content))); err != nil {
			t.Fatalf("Install: unexpected error %v", err)
		}
		fi, err := os.Stat(bin)
		if err != nil {
			t.Fatalf("binary not installed: %v", err)
		}
		if fi.Mode().Perm() != 0755 {
			t.Fatalf("mode: want 0755, got %o", fi.Mode().Perm())
		}
		if _, err := os.Stat(bin + ".tmp"); !os.IsNotExist(err) {
			t.Fatalf("tmp file left behind: %v", err)
		}
	})

	t.Run("wrong SHA is ChecksumError, nothing installed", func(t *testing.T) {
		dir := t.TempDir()
		bin := filepath.Join(dir, "child")
		wrongSHA := hex.EncodeToString(make([]byte, 32))
		err := Install(context.Background(), d, bin, "http://x/child", wrongSHA, int64(len(content)))
		var ce *ChecksumError
		if !errors.As(err, &ce) {
			t.Fatalf("want *ChecksumError, got %v", err)
		}
		if ce.Got != goodSHA {
			t.Fatalf("ChecksumError.Got: want %s, got %s", goodSHA, ce.Got)
		}
		if _, err := os.Stat(bin); !os.IsNotExist(err) {
			t.Fatalf("binary must not exist on mismatch: %v", err)
		}
		if _, err := os.Stat(bin + ".tmp"); !os.IsNotExist(err) {
			t.Fatalf("tmp file left behind: %v", err)
		}
	})

	t.Run("empty SHA fails closed, nothing installed", func(t *testing.T) {
		dir := t.TempDir()
		bin := filepath.Join(dir, "child")
		err := Install(context.Background(), d, bin, "http://x/child", "", int64(len(content)))
		if err == nil {
			t.Fatal("empty SHA must fail (fail-closed), got nil")
		}
		var ce *ChecksumError
		if errors.As(err, &ce) {
			t.Fatalf("empty SHA must not be a ChecksumError, got %v", err)
		}
		if _, err := os.Stat(bin); !os.IsNotExist(err) {
			t.Fatalf("binary must not exist on empty SHA: %v", err)
		}
		if _, err := os.Stat(bin + ".tmp"); !os.IsNotExist(err) {
			t.Fatalf("tmp file left behind: %v", err)
		}
	})
}

func TestNewInstanceID(t *testing.T) {
	a := NewInstanceID()
	b := NewInstanceID()
	if len(a) != 24 {
		t.Fatalf("want 24 hex chars, got %d (%q)", len(a), a)
	}
	if a == b {
		t.Fatal("two ids collided")
	}
}

func TestRingBuffer_KeepsLastN(t *testing.T) {
	b := NewRingBuffer(3)
	for _, l := range []string{"1", "2", "3", "4", "5"} {
		b.WriteLine(l)
	}
	if got := b.String(); got != "3\n4\n5" {
		t.Fatalf("String: want last 3, got %q", got)
	}
	if got := b.LastLines(2); got != "4\n5" {
		t.Fatalf("LastLines(2): want %q, got %q", "4\n5", got)
	}
	if got := b.LastLines(10); got != "3\n4\n5" {
		t.Fatalf("LastLines(over): want whole buffer, got %q", got)
	}
	if got := b.LastLines(0); got != "" {
		t.Fatalf("LastLines(0): want empty, got %q", got)
	}
	b.Reset()
	if got := b.String(); got != "" {
		t.Fatalf("Reset: want empty, got %q", got)
	}
}

func TestIsAlive(t *testing.T) {
	if IsAlive(0) || IsAlive(-1) {
		t.Fatal("IsAlive must be false for non-positive pid")
	}
	// A live child is alive; once reaped it is not — this exercises the
	// Signal(0) probe on both the linux and non-linux implementations.
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn sleep: %v", err)
	}
	pid := cmd.Process.Pid
	if !IsAlive(pid) {
		t.Fatalf("running child pid %d reported not alive", pid)
	}
	_ = Kill(pid)
	_ = cmd.Wait()
	if IsAlive(pid) {
		t.Fatalf("reaped child pid %d reported alive", pid)
	}
}
