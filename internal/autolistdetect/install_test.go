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
	onInstall  func()
}

func (f *fakePackageManager) Update() (string, error) {
	f.calls = append(f.calls, "update")
	return "update output", f.updateErr
}
func (f *fakePackageManager) Install([]string) (string, error) {
	f.calls = append(f.calls, "install")
	if f.onInstall != nil {
		f.onInstall()
	}
	return "install output", f.installErr
}

type fakeKeys struct {
	key  string
	auth bool
}

func (f fakeKeys) GetApiKey() string   { return f.key }
func (f fakeKeys) IsAuthEnabled() bool { return f.auth }

// countingKeys: auth on, no key; counts the installer's precondition checks.
type countingKeys struct{ checks int }

func (c *countingKeys) GetApiKey() string   { return "" }
func (c *countingKeys) IsAuthEnabled() bool { c.checks++; return true }

func testInstaller(t *testing.T) (*Installer, *fakePackageManager, string) {
	t.Helper()
	dir := t.TempDir()
	pkg := &fakePackageManager{}
	installer := NewInstaller(pkg, fakeKeys{key: "secret", auth: true})
	installer.keyPath = filepath.Join(dir, "awgmautolist.key")
	installer.enabledPath = filepath.Join(dir, "awgmautolist.enabled")
	installer.initPath = filepath.Join(dir, "S99awgmautolist")
	installer.SetConfirm(func(context.Context, bool) error { return nil })
	pkg.onInstall = func() {
		if err := os.WriteFile(installer.initPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
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
	confirmed := false
	installer.SetConfirm(func(_ context.Context, running bool) error { confirmed = running; return nil })
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

// An installed package is upgraded in place: the disabled intent survives and
// the confirmation expects a stopped satellite.
func TestInstallUpgradeKeepsDisabledIntent(t *testing.T) {
	installer, _, _ := testInstaller(t)
	if err := os.WriteFile(installer.enabledPath, []byte("ENABLED=no\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installer.initPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	wantRunning := true
	installer.SetConfirm(func(_ context.Context, running bool) error { wantRunning = running; return nil })
	if err := installer.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if wantRunning {
		t.Fatal("upgrade of a disabled satellite confirmed a running state")
	}
	if b, err := os.ReadFile(installer.enabledPath); err != nil || string(b) != "ENABLED=no\n" {
		t.Fatalf("disabled intent lost: %q %v", b, err)
	}
}

func TestPackageStateOutdated(t *testing.T) {
	dir := t.TempDir()
	s := NewService()
	s.controlPath = filepath.Join(dir, "awgmautolist.control")
	if v, outdated := s.packageState(); v != "" || outdated {
		t.Fatalf("missing package: %q %t", v, outdated)
	}
	cases := []struct {
		installed, panel string
		want             bool
	}{
		{"2.20.1", "2.20.3", true},
		{"0.10.0-1", "2.20.3", true},
		{"2.20.3", "2.20.3+r5", false},
		{"2.20.3", "2.20.2", false},
		{"2.20.3", "dev", false},
	}
	for _, tc := range cases {
		control := "Package: awgmautolist\nVersion: " + tc.installed + "\nDepends: awg-manager\n"
		if err := os.WriteFile(s.controlPath, []byte(control), 0o644); err != nil {
			t.Fatal(err)
		}
		s.panelVersion = tc.panel
		if v, outdated := s.packageState(); v != tc.installed || outdated != tc.want {
			t.Errorf("%s vs %s: got %q %t, want %t", tc.installed, tc.panel, v, outdated, tc.want)
		}
	}
}

func TestAutoUpgrade(t *testing.T) {
	old := autoUpgradeDelay
	autoUpgradeDelay = time.Millisecond
	t.Cleanup(func() { autoUpgradeDelay = old })

	setup := func(t *testing.T) (*Service, *fakePackageManager, func(string)) {
		installer, pkg, dir := testInstaller(t)
		s := NewService()
		s.installer = installer
		s.panelVersion = "2.20.3"
		s.controlPath = filepath.Join(dir, "awgmautolist.control")
		write := func(v string) {
			if err := os.WriteFile(s.controlPath, []byte("Version: "+v+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		write("2.20.1")
		if err := os.WriteFile(installer.initPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return s, pkg, write
	}

	t.Run("upgrades once when opkg brings the panel version", func(t *testing.T) {
		s, pkg, write := setup(t)
		pkg.onInstall = func() { write("2.20.3") }
		if from, err := s.AutoUpgrade(context.Background()); from != "2.20.1" || err != nil {
			t.Fatalf("from=%q err=%v", from, err)
		}
		if strings.Join(pkg.calls, ",") != "update,install" {
			t.Fatalf("calls=%v", pkg.calls)
		}
	})
	t.Run("a failed confirmation after a real upgrade is not retried", func(t *testing.T) {
		s, pkg, write := setup(t)
		pkg.onInstall = func() { write("2.20.3") }
		s.installer.SetConfirm(func(context.Context, bool) error { return errors.New("not confirmed") })
		if _, err := s.AutoUpgrade(context.Background()); err == nil {
			t.Fatal("confirmation error lost")
		}
		if len(pkg.calls) != 2 {
			t.Fatalf("retried after the upgrade: %v", pkg.calls)
		}
	})
	t.Run("a feed without the version stops after one attempt", func(t *testing.T) {
		s, pkg, _ := setup(t)
		if _, err := s.AutoUpgrade(context.Background()); err == nil || !strings.Contains(err.Error(), "feed has no") {
			t.Fatalf("err=%v", err)
		}
		if len(pkg.calls) != 2 {
			t.Fatalf("calls=%v", pkg.calls)
		}
	})
	t.Run("a locked opkg is retried until the attempts run out", func(t *testing.T) {
		s, pkg, _ := setup(t)
		pkg.installErr = errors.New("could not lock")
		if _, err := s.AutoUpgrade(context.Background()); err == nil {
			t.Fatal("error lost")
		}
		if len(pkg.calls) != 2*autoUpgradeAttempts {
			t.Fatalf("calls=%v", pkg.calls)
		}
	})
	t.Run("a missing API key is not retried", func(t *testing.T) {
		s, pkg, _ := setup(t)
		keys := &countingKeys{}
		s.installer.keys = keys
		if _, err := s.AutoUpgrade(context.Background()); !errors.Is(err, ErrNeedApiKey) {
			t.Fatalf("err=%v", err)
		}
		if keys.checks != 1 || len(pkg.calls) != 0 {
			t.Fatalf("checks=%d calls=%v", keys.checks, pkg.calls)
		}
	})
	t.Run("a current package does nothing", func(t *testing.T) {
		s, pkg, write := setup(t)
		write("2.20.3")
		if from, err := s.AutoUpgrade(context.Background()); from != "" || err != nil || len(pkg.calls) != 0 {
			t.Fatalf("from=%q err=%v calls=%v", from, err, pkg.calls)
		}
	})
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
