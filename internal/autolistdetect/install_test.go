package autolistdetect

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type fakePkg struct {
	installed []string
	onInstall func()
	updErr    error
	instErr   error
}

func (f *fakePkg) Update() (string, error) { return "", f.updErr }
func (f *fakePkg) Install(names []string) (string, error) {
	f.installed = append(f.installed, names...)
	if f.onInstall != nil {
		f.onInstall()
	}
	return "opkg out", f.instErr
}

type fakeKeys struct {
	key  string
	auth bool
}

func (k *fakeKeys) GetApiKey() string   { return k.key }
func (k *fakeKeys) IsAuthEnabled() bool { return k.auth }

func newInstallService(t *testing.T, keys *fakeKeys) (*Service, *fakePkg) {
	t.Helper()
	dir := t.TempDir()
	s := NewService()
	s.initPath = filepath.Join(dir, "S99awgmautolist")
	s.statePath = filepath.Join(dir, "awgmautolist.enabled")
	s.keyPath = filepath.Join(dir, "awgmautolist.key")
	s.confirm = func(context.Context, bool) error { return nil }
	pkg := &fakePkg{onInstall: func() {
		if err := os.WriteFile(s.initPath, []byte("#!/bin/sh\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}}
	s.SetInstaller(pkg, keys)
	return s, pkg
}

// Ключ должен прочитаться init-скриптом как есть, включая кавычки в нём.
func TestInstallWritesShellSafeKeyBeforeInstall(t *testing.T) {
	key := `k'ey $(id) "x"`
	s, pkg := newInstallService(t, &fakeKeys{key: key, auth: true})
	if err := os.WriteFile(s.statePath, []byte("ENABLED=no\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var keyAtInstall string
	inner := pkg.onInstall
	pkg.onInstall = func() {
		b, _ := os.ReadFile(s.keyPath)
		keyAtInstall = string(b)
		inner()
	}
	if err := s.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(pkg.installed, ",") != "awgmautolist" {
		t.Fatalf("installed = %v", pkg.installed)
	}
	if keyAtInstall == "" {
		t.Fatal("key file was not written before opkg install")
	}
	if _, err := os.Stat(s.statePath); !os.IsNotExist(err) {
		t.Fatalf("stale ENABLED=no survived install: %v", err)
	}
	if fi, err := os.Stat(s.keyPath); err != nil || fi.Mode().Perm() != 0600 {
		t.Fatalf("key file mode = %v, %v", fi.Mode().Perm(), err)
	}
	out, err := exec.Command("sh", "-c", `. "$0"; printf %s "$AWGM_API_KEY"`, s.keyPath).Output()
	if err != nil || string(out) != key {
		t.Fatalf("sourced key = %q, %v", out, err)
	}
}

func TestInstallRefusesWithoutKeyWhenAuthEnabled(t *testing.T) {
	s, pkg := newInstallService(t, &fakeKeys{auth: true})
	if err := s.Install(context.Background()); !errors.Is(err, ErrNeedApiKey) {
		t.Fatalf("err = %v", err)
	}
	if len(pkg.installed) != 0 {
		t.Fatal("opkg install called without key")
	}
}

// opkg может выйти с 0 на неизвестном пакете — ориентир наличие init-скрипта.
func TestInstallFailsWhenPackageDidNotLand(t *testing.T) {
	s, pkg := newInstallService(t, &fakeKeys{})
	pkg.onInstall = nil
	pkg.updErr = errors.New("feed down")
	err := s.Install(context.Background())
	if err == nil || !strings.Contains(err.Error(), "feed down") {
		t.Fatalf("err = %v", err)
	}
}

func TestApiKeyChangedRewritesKeyAndRestarts(t *testing.T) {
	keys := &fakeKeys{key: "old"}
	s, _ := newInstallService(t, keys)
	var actions []string
	s.command = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		actions = append(actions, args...)
		return nil, nil
	}
	// Не установлен — ни файла, ни init.
	if err := s.ApiKeyChanged(); err != nil || len(actions) != 0 {
		t.Fatalf("not installed: err=%v actions=%v", err, actions)
	}
	if _, err := os.Stat(s.keyPath); !os.IsNotExist(err) {
		t.Fatal("key file written for absent satellite")
	}
	if err := os.WriteFile(s.initPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	keys.key = "new"
	if err := s.ApiKeyChanged(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(s.keyPath); string(b) != "AWGM_API_KEY='new'\n" {
		t.Fatalf("key file = %q", b)
	}
	if strings.Join(actions, ",") != "restart" {
		t.Fatalf("actions = %v", actions)
	}
}
