package zapret

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestConcurrentStrategyEditsRejectStaleRevision(t *testing.T) {
	setupInstall(t)
	s := NewService(t.TempDir(), nil)
	before, err := s.GetStrategies()
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.ReorderStrategies(0, 1, before.Revision)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflicts=%d", success, conflicts)
	}
}

func TestHostlistRejectsExternalChangeAndMissingRevision(t *testing.T) {
	base := setupInstall(t)
	s := NewService(t.TempDir(), nil)
	_, path, original, err := s.ReadHostlist(HostlistUser)
	if err != nil {
		t.Fatal(err)
	}
	token := Revision(path, original)
	target := filepath.Join(base, "ipset", "zapret-hosts-user.txt")
	if err := os.WriteFile(target, []byte("external.example\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, revision := range []string{token, ""} {
		if _, _, err := s.WriteHostlist(HostlistUser, "overwrite.example", revision); !errors.Is(err, ErrConflict) {
			t.Fatalf("expected conflict, got %v", err)
		}
	}
	got, _ := os.ReadFile(target)
	if string(got) != "external.example\n" {
		t.Fatalf("overwrote external edit: %q", got)
	}
}

func TestHostlistWriteRefusesExternalSymlink(t *testing.T) {
	base := setupInstall(t)
	s := NewService(t.TempDir(), nil)
	target := filepath.Join(base, "ipset", "zapret-hosts-user.txt")
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("keep\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, target); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.WriteHostlist(HostlistUser, "replace\n", Revision(target, "keep\n")); err == nil {
		t.Fatal("accepted symlink write")
	}
	got, _ := os.ReadFile(outside)
	if string(got) != "keep\n" {
		t.Fatal("changed external file")
	}
	if _, err := os.Readlink(target); err != nil {
		t.Fatal("replaced symlink")
	}
}

func TestWritesWithoutRevisionAreConflicts(t *testing.T) {
	base := setupInstall(t)
	s := NewService(t.TempDir(), nil)
	if _, err := s.ReorderStrategies(0, 1, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("reorder without revision: %v, want ErrConflict", err)
	}
	if _, _, err := s.WriteHostlist(HostlistUser, "x.example\n", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("hostlist write without revision: %v, want ErrConflict", err)
	}
	if err := s.Control("restart", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("control without base: %v, want ErrConflict", err)
	}
	if err := s.Control("restart", base+"-other"); !errors.Is(err, ErrConflict) {
		t.Fatalf("control with foreign base: %v, want ErrConflict", err)
	}
}
