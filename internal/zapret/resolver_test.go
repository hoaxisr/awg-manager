package zapret

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setupResolverInstalls(t *testing.T, names ...string) []string {
	t.Helper()
	root := t.TempDir()
	var bases []string
	for _, name := range names {
		base := filepath.Join(root, name)
		for _, dir := range []string{"nfq2", "ipset", filepath.Join("init.d", "sysv")} {
			if err := os.MkdirAll(filepath.Join(base, dir), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		config := "NFQWS2_OPT=\"--filter-tcp=443 --lua-desync=fake\"\n"
		if err := os.WriteFile(filepath.Join(base, "config"), []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
		binary := filepath.Join(base, "nfq2", "nfqws2")
		if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		init := filepath.Join(base, "init.d", "sysv", "zapret2")
		if err := os.WriteFile(init, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		bases = append(bases, base)
	}

	oldRoot, oldDirs, oldHook := optRoot, initDirs, nfqProcessesHook
	optRoot = root
	initDirs = nil
	nfqProcessesHook = func() []nfqProc { return nil }
	t.Cleanup(func() {
		optRoot, initDirs, nfqProcessesHook = oldRoot, oldDirs, oldHook
	})
	return bases
}

func TestResolveLayoutSelectsRunningBaseAndKeepsItWhenStopped(t *testing.T) {
	bases := setupResolverInstalls(t, "zapret-stale", "zapret-live")
	dataDir := t.TempDir()
	nfqProcessesHook = func() []nfqProc {
		return []nfqProc{{pid: 41, exe: filepath.Join(bases[0], "nfq2", "nfqws2")}}
	}
	svc := &Service{dataDir: dataDir}
	got, err := svc.resolveLayout()
	if err != nil {
		t.Fatal(err)
	}
	if got.base != bases[0] {
		t.Fatalf("selected %s, want running base %s", got.base, bases[0])
	}
	stored, err := os.ReadFile(filepath.Join(dataDir, "zapret-last-running"))
	if err != nil || strings.TrimSpace(string(stored)) != canonicalBase(bases[0]) {
		t.Fatalf("last-running cache = %q, err=%v", stored, err)
	}

	// Make the other copy look newer. Once stopped, the actual last-running
	// installation must win over config mtime.
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(bases[1], "config"), future, future); err != nil {
		t.Fatal(err)
	}
	nfqProcessesHook = func() []nfqProc { return nil }
	got, err = svc.resolveLayout()
	if err != nil {
		t.Fatal(err)
	}
	if got.base != bases[0] {
		t.Fatalf("stopped resolver selected %s, want cached base %s", got.base, bases[0])
	}
}

func TestResolveLayoutRejectsMultipleStoppedCandidatesWithoutCache(t *testing.T) {
	setupResolverInstalls(t, "zapret-one", "zapret-two")
	svc := &Service{dataDir: t.TempDir()}
	if _, err := svc.resolveLayout(); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestResolveLayoutDoesNotRedirectWhenRememberedInstallDisappears(t *testing.T) {
	bases := setupResolverInstalls(t, "zapret-remaining")
	dataDir := t.TempDir()
	missing := filepath.Join(t.TempDir(), "zapret-previous")
	if err := os.WriteFile(filepath.Join(dataDir, "zapret-last-running"), []byte(missing+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := &Service{dataDir: dataDir}
	if got, err := svc.resolveLayout(); !errors.Is(err, ErrConflict) || got.base != "" {
		t.Fatalf("stale cache should conflict without selecting remaining copy: layout=%+v err=%v", got, err)
	}

	// Once the surviving installation is actually observed running, it can
	// safely replace the stale cache and then remain selected after stopping.
	nfqProcessesHook = func() []nfqProc {
		return []nfqProc{{pid: 91, exe: filepath.Join(bases[0], "nfq2", "nfqws2")}}
	}
	if got, err := svc.resolveLayout(); err != nil || got.base != bases[0] {
		t.Fatalf("live owner should refresh stale cache: layout=%+v err=%v", got, err)
	}
	nfqProcessesHook = func() []nfqProc { return nil }
	if got, err := svc.resolveLayout(); err != nil || got.base != bases[0] {
		t.Fatalf("refreshed cache should resolve after stopping: layout=%+v err=%v", got, err)
	}
}

func TestResolveLayoutRejectsMultipleRunningBases(t *testing.T) {
	bases := setupResolverInstalls(t, "zapret-one", "zapret-two")
	nfqProcessesHook = func() []nfqProc {
		return []nfqProc{
			{pid: 11, exe: filepath.Join(bases[0], "nfq2", "nfqws2")},
			{pid: 12, exe: filepath.Join(bases[1], "nfq2", "nfqws2")},
		}
	}
	if _, err := (&Service{dataDir: t.TempDir()}).resolveLayout(); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestResolveLayoutRejectsUnknownProcessOwner(t *testing.T) {
	setupResolverInstalls(t, "zapret-one")
	nfqProcessesHook = func() []nfqProc { return []nfqProc{{pid: 77}} }
	status := (&Service{dataDir: t.TempDir()}).GetStatus()
	if !status.Conflict || !status.Running || status.Base != "" || len(status.PIDs) != 0 {
		t.Fatalf("unsafe status should report conflict without selecting a base or PIDs: %+v", status)
	}
}

func TestResolveLayoutSingleStoppedInstallDoesNotBecomeLastRunning(t *testing.T) {
	bases := setupResolverInstalls(t, "zapret-one")
	dataDir := t.TempDir()
	got, err := (&Service{dataDir: dataDir}).resolveLayout()
	if err != nil {
		t.Fatal(err)
	}
	if got.base != bases[0] {
		t.Fatalf("selected %s, want sole candidate %s", got.base, bases[0])
	}
	if _, err := os.Stat(filepath.Join(dataDir, "zapret-last-running")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stopped install was cached as last-running: stat err=%v", err)
	}
}

func TestRememberRunningBaseDoesNotRewriteUnchangedCache(t *testing.T) {
	dataDir := t.TempDir()
	svc := &Service{dataDir: dataDir}
	base := filepath.Join(t.TempDir(), "zapret-one")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	base = canonicalBase(base)
	if err := svc.rememberRunningBase(base); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(dataDir, "zapret-last-running")
	old := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(cache, old, old); err != nil {
		t.Fatal(err)
	}
	if err := svc.rememberRunningBase(base); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(cache)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(old) {
		t.Fatalf("unchanged cache was rewritten: mtime=%s want=%s", info.ModTime(), old)
	}
}

func TestReadLastRunningBaseRetriesAfterReadError(t *testing.T) {
	dataDir := t.TempDir()
	cache := filepath.Join(dataDir, "zapret-last-running")
	if err := os.Mkdir(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	svc := &Service{dataDir: dataDir}
	if _, err := svc.readLastRunningBase(); err == nil {
		t.Fatal("reading a directory as the cache should fail")
	}
	if err := os.Remove(cache); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dataDir, "zapret-one")
	if err := os.WriteFile(cache, []byte(want+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := svc.readLastRunningBase()
	if err != nil || got != want {
		t.Fatalf("cache after recovery = %q, err=%v; want %q", got, err, want)
	}
}

func TestResolveLayoutMatchesSymlinkAliasToCanonicalBase(t *testing.T) {
	root := t.TempDir()
	opt := filepath.Join(root, "opt")
	physical := filepath.Join(root, "vendor", "zapret-actual")
	for _, dir := range []string{"nfq2", "ipset", filepath.Join("init.d", "sysv")} {
		if err := os.MkdirAll(filepath.Join(physical, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(physical, "config"), []byte("NFQWS2_OPT=\"--x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(physical, "nfq2", "nfqws2"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(physical, "init.d", "sysv", "zapret2"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(opt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(physical, filepath.Join(opt, "zapret-alias")); err != nil {
		t.Fatal(err)
	}
	oldRoot, oldDirs, oldHook := optRoot, initDirs, nfqProcessesHook
	optRoot, initDirs = opt, nil
	nfqProcessesHook = func() []nfqProc {
		return []nfqProc{{pid: 88, exe: filepath.Join(physical, "nfq2", "nfqws2")}}
	}
	t.Cleanup(func() { optRoot, initDirs, nfqProcessesHook = oldRoot, oldDirs, oldHook })

	got, err := (&Service{dataDir: t.TempDir()}).resolveLayout()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(opt, "zapret-alias")
	if got.base != want {
		t.Fatalf("selected %s, want discovered alias %s", got.base, want)
	}
}

func TestFindInitScriptOnlyReturnsScriptOwnedBySelectedBase(t *testing.T) {
	bases := setupResolverInstalls(t, "zapret-one", "zapret-two")
	initDirs = []string{filepath.Join(bases[1], "init.d", "sysv")}

	got := findInitScript(bases[0])
	want := filepath.Join(bases[0], "init.d", "sysv", "zapret2")
	if got != want {
		t.Fatalf("findInitScript = %s, want %s", got, want)
	}
}
