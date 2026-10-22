package zapret

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGetStatusCachesNegativeDetectionUntilRootChangesOrTTLExpires(t *testing.T) {
	root := t.TempDir()
	oldRoot, oldDirs, oldProcesses := optRoot, initDirs, nfqProcessesHook
	optRoot, initDirs = root, nil
	processScans := 0
	nfqProcessesHook = func() []nfqProc {
		processScans++
		return nil
	}
	t.Cleanup(func() { optRoot, initDirs, nfqProcessesHook = oldRoot, oldDirs, oldProcesses })

	now := time.Now()
	svc := NewService(t.TempDir(), nil)
	svc.now = func() time.Time { return now }
	if got := svc.GetStatus(); got.Installed || got.ProcessState != StateNotInstalled {
		t.Fatalf("initial status = %+v, want absent", got)
	}
	if got := svc.GetStatus(); got.ProcessState != StateNotInstalled {
		t.Fatalf("cached status = %+v, want absent", got)
	}
	if processScans != 1 {
		t.Fatalf("process scans for repeated absent status = %d, want 1", processScans)
	}

	// Adding an installation entry changes the root directory mtime and must
	// invalidate the negative result immediately.
	base := filepath.Join(root, "zapret-dynamic")
	for _, dir := range []string{"nfq2", "ipset", filepath.Join("init.d", "sysv")} {
		if err := os.MkdirAll(filepath.Join(base, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, "config"), []byte("NFQWS2_OPT=\"--filter-tcp=443\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "nfq2", "nfqws2"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "init.d", "sysv", "zapret2"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(root, future, future); err != nil {
		t.Fatal(err)
	}
	if got := svc.GetStatus(); !got.Installed {
		t.Fatalf("status after installation appeared = %+v, want installed", got)
	}
	if processScans != 2 {
		t.Fatalf("process scans after root changed = %d, want 2", processScans)
	}
}

func TestGetStatusExpiresNegativeDetectionByTTL(t *testing.T) {
	root := t.TempDir()
	oldRoot, oldDirs, oldProcesses := optRoot, initDirs, nfqProcessesHook
	optRoot, initDirs = root, nil
	processScans := 0
	nfqProcessesHook = func() []nfqProc {
		processScans++
		return nil
	}
	t.Cleanup(func() { optRoot, initDirs, nfqProcessesHook = oldRoot, oldDirs, oldProcesses })

	now := time.Now()
	svc := NewService(t.TempDir(), nil)
	svc.now = func() time.Time { return now }
	svc.GetStatus()
	svc.GetStatus()
	if processScans != 1 {
		t.Fatalf("process scans before expiry = %d, want 1", processScans)
	}
	now = now.Add(time.Hour)
	svc.GetStatus()
	if processScans != 2 {
		t.Fatalf("process scans after expiry = %d, want 2", processScans)
	}
}
