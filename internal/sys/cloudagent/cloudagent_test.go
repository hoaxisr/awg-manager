package cloudagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

func writeFakeProc(t *testing.T, root, pid, comm, cmdline string) {
	t.Helper()
	dir := filepath.Join(root, pid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(comm), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(cmdline), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResetWith_MatchesOnlyCoalagentAndHandlesErrors(t *testing.T) {
	root := t.TempDir()
	// Non-numeric / self dirs and unrelated daemons must be ignored.
	if err := os.MkdirAll(filepath.Join(root, "net"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeProc(t, root, strconv.Itoa(os.Getpid()), "coalagent\n", "/bin/coalagent\x00-f\x00")
	writeFakeProc(t, root, "268", "ndm\n", "ndm\x00")
	writeFakeProc(t, root, "401", "coalagent\n", "/usr/bin/fake\x00")
	writeFakeProc(t, root, "402", "other\n", "/bin/coalagent\x00")
	writeFakeProc(t, root, "563", "coalagent\n", "/bin/coalagent\x00-c\x00uuid\x00")

	var killed []int
	n, err := resetWith(context.Background(), root, func(pid int) error {
		killed = append(killed, pid)
		return nil
	})
	if err != nil {
		t.Fatalf("resetWith failed: %v", err)
	}
	if n != 1 || !slices.Equal(killed, []int{563}) {
		t.Fatalf("expected only pid 563 signaled, got n=%d killed=%v", n, killed)
	}

	// Kill error must be surfaced.
	wantErr := errors.New("eperm")
	nErr, err := resetWith(context.Background(), root, func(int) error {
		return wantErr
	})
	if !errors.Is(err, wantErr) || nErr != 0 {
		t.Fatalf("expected error %v with n=0, got n=%d err=%v", wantErr, nErr, err)
	}
}

func TestResetWith_EmptyProcAndCanceledContext(t *testing.T) {
	root := t.TempDir()
	n, err := resetWith(context.Background(), root, func(int) error {
		t.Fatal("unexpected kill call")
		return nil
	})
	if err != nil || n != 0 {
		t.Fatalf("expected (0, nil), got (%d, %v)", n, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := resetWith(ctx, root, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
