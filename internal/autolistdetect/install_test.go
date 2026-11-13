package autolistdetect

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakePackageManager struct {
	updateErr  error
	installErr error
	calls      []string
}

func (f *fakePackageManager) Update() (string, error) {
	f.calls = append(f.calls, "update")
	return "update output", f.updateErr
}
func (f *fakePackageManager) Install([]string) (string, error) {
	f.calls = append(f.calls, "install")
	return "install output", f.installErr
}

type fakeKeys struct {
	key  string
	auth bool
}

func (f fakeKeys) GetApiKey() string   { return f.key }
func (f fakeKeys) IsAuthEnabled() bool { return f.auth }

func testInstaller(t *testing.T) (*Installer, *fakePackageManager, string) {
	t.Helper()
	dir := t.TempDir()
	pkg := &fakePackageManager{}
	installer := NewInstaller(pkg, fakeKeys{key: "secret", auth: true})
	installer.keyPath = filepath.Join(dir, "awgmautolist.key")
	installer.enabledPath = filepath.Join(dir, "awgmautolist.enabled")
	installer.initPath = filepath.Join(dir, "S99awgmautolist")
	installer.SetConfirm(func(context.Context) error { return nil })
	return installer, pkg, dir
}

func TestInstallRequiresAPIKey(t *testing.T) {
	installer := NewInstaller(&fakePackageManager{}, fakeKeys{auth: true})
	if err := installer.Install(context.Background()); !errors.Is(err, ErrNeedApiKey) {
		t.Fatalf("err=%v", err)
	}
}

func TestInstallWritesKeyAndConfirms(t *testing.T) {
	installer, pkg, _ := testInstaller(t)
	if err := os.WriteFile(installer.enabledPath, []byte("ENABLED=no\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installer.initPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	confirmed := false
	installer.SetConfirm(func(context.Context) error { confirmed = true; return nil })
	if err := installer.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !confirmed || strings.Join(pkg.calls, ",") != "update,install" {
		t.Fatalf("confirmed=%t calls=%v", confirmed, pkg.calls)
	}
	if _, err := os.Stat(installer.enabledPath); !os.IsNotExist(err) {
		t.Fatalf("old disabled intent survived: %v", err)
	}
	data, err := os.ReadFile(installer.keyPath)
	if err != nil || string(data) != "AWGM_API_KEY='secret'\n" {
		t.Fatalf("key=%q err=%v", data, err)
	}
	info, _ := os.Stat(installer.keyPath)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode=%o", info.Mode().Perm())
	}
}

func TestInstallReportsUpdateAndInstallErrors(t *testing.T) {
	installer, _, _ := testInstaller(t)
	installer.pkg.(*fakePackageManager).updateErr = errors.New("feed failed")
	installer.pkg.(*fakePackageManager).installErr = errors.New("package missing")
	if err := installer.Install(context.Background()); err == nil || !strings.Contains(err.Error(), "feed failed") {
		t.Fatalf("err=%v", err)
	}
}

func TestInstallerInitWaitDelayAllowsDaemonizedChild(t *testing.T) {
	installer, _, _ := testInstaller(t)
	script := "#!/bin/sh\nsleep 1 &\nexit 0\n"
	if err := os.WriteFile(installer.initPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := installerWaitDelay
	installerWaitDelay = 100 * time.Millisecond
	t.Cleanup(func() { installerWaitDelay = old })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := time.Now()
	if err := installer.runInitCommand(ctx, "restart"); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("init command waited for the daemonized child's pipes")
	}
}

func TestApiKeyChangedRestartsOnlyInstalled(t *testing.T) {
	installer, _, _ := testInstaller(t)
	called := false
	installer.runInit = func(context.Context, string) error { called = true; return nil }
	if err := installer.ApiKeyChanged(); err != nil || called {
		t.Fatalf("not installed: called=%t err=%v", called, err)
	}
	if err := os.WriteFile(installer.initPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := installer.ApiKeyChanged(); err != nil || !called {
		t.Fatalf("installed: called=%t err=%v", called, err)
	}
}
