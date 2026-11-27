package updater

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/downloader"
)

// Лог пользователя (aarch64, 2.20.6 → 2.21.0, F933) плюс итог нашего скрипта.
const userUpgradeLog = `wget: bad address 'bin.entware.net'
Collected errors:
 * opkg_download: Failed to download http://bin.entware.net/aarch64-k3.10/libc_2.27-12_aarch64-3.10.ipk, wget returned 1.
 * opkg_install_pkg: Failed to download libc. Perhaps you need to run 'opkg update'?
 * opkg_install_cmd: Cannot install package awg-manager.
Upgrading awg-manager on root from 2.20.6 to 2.21.0...
Installing libc (2.27-12) to root...
Downloading http://bin.entware.net/aarch64-k3.10/libc_2.27-12_aarch64-3.10.ipk
upgrade FAILED: opkg rc=255
`

func TestUpgradeFailureReason_UserLog(t *testing.T) {
	got := upgradeFailureReason(userUpgradeLog)
	want := strings.Join([]string{
		"wget: bad address 'bin.entware.net'",
		"opkg_download: Failed to download http://bin.entware.net/aarch64-k3.10/libc_2.27-12_aarch64-3.10.ipk, wget returned 1.",
		"opkg_install_pkg: Failed to download libc. Perhaps you need to run 'opkg update'?",
		"opkg_install_cmd: Cannot install package awg-manager.",
		"upgrade FAILED: opkg rc=255",
	}, "\n")
	if got != want {
		t.Fatalf("reason:\n%s\nwant:\n%s", got, want)
	}
	if h := upgradeFailureHint(got); h != "dns" {
		t.Fatalf("hint = %q, want dns", h)
	}
}

func TestUpgradeFailureHint(t *testing.T) {
	for reason, want := range map[string]string{
		"wget: unable to resolve host address 'bin.entware.net'":                    "dns",
		"opkg_install_pkg: write error: No space left on device":                    "space",
		"Could not lock /opt/var/lock/opkg.lock: Resource temporarily unavailable.": "lock",
		"opkg_install_cmd: Cannot install package awg-manager.":                     "",
	} {
		if got := upgradeFailureHint(reason); got != want {
			t.Errorf("hint(%q) = %q, want %q", reason, got, want)
		}
	}
}

// Отказ opkg при живом демоне: причина из лога доходит до LastUpgradeFailure,
// флаг «идёт обновление» снимается сразу, новая попытка причину сбрасывает.
func TestApplyUpgrade_FailureReachesLastUpgradeFailure(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "upgrade.log")
	if err := os.WriteFile(logPath, []byte(userUpgradeLog), 0o644); err != nil {
		t.Fatal(err)
	}
	oldLog := upgradeLogPath
	upgradeLogPath = logPath
	t.Cleanup(func() { upgradeLogPath = oldLog })

	exitErr := errors.New("exit status 1")
	oldStart := startDetachedUpgrade
	startDetachedUpgrade = func(_ string, done func(error)) error { done(exitErr); return nil }
	t.Cleanup(func() { startDetachedUpgrade = oldStart })

	s := New("2.20.6", nil, nil, t.TempDir(), nil)
	s.SetDownloader(&fakeDownloader{
		downloadFileFn: func(_ context.Context, req downloader.FileRequest) (downloader.FileResult, error) {
			return downloader.FileResult{Path: req.DestPath}, nil
		},
	})
	s.cached = &UpdateInfo{DownloadURL: "http://repo.example/aarch64-k3.10/awg-manager_2.21.0_aarch64-3.10-kn.ipk"}
	s.snapshot = nil

	if err := s.ApplyUpgrade(context.Background()); err != nil {
		t.Fatalf("ApplyUpgrade: %v", err)
	}
	f := s.LastUpgradeFailure()
	if f.Hint != "dns" || !strings.Contains(f.Reason, "bad address 'bin.entware.net'") || f.At.IsZero() {
		t.Fatalf("failure = %+v, want dns reason from log", f)
	}
	if s.isUpgrading() {
		t.Fatal("upgrading flag must be cleared once opkg failed")
	}

	startDetachedUpgrade = func(_ string, _ func(error)) error { return nil }
	if err := s.ApplyUpgrade(context.Background()); err != nil {
		t.Fatalf("second ApplyUpgrade: %v", err)
	}
	if f := s.LastUpgradeFailure(); f.Reason != "" {
		t.Fatalf("new attempt must clear the old failure, got %+v", f)
	}
}

// Настоящий запуск: итог скрипта доходит до done, вывод — в лог установки.
// installedBinPath на машине теста нет, поэтому проверка бинаря проваливается.
func TestStartDetachedUpgrade_ReportsScriptExit(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"opkg": "echo opkg-ran; exit 0", "logger": ":"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	oldLog := upgradeLogPath
	upgradeLogPath = filepath.Join(dir, "upgrade.log")
	t.Cleanup(func() { upgradeLogPath = oldLog })

	got := make(chan error, 1)
	if err := startDetachedUpgrade(filepath.Join(dir, "x.ipk"), func(err error) { got <- err }); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-got:
		if err == nil {
			t.Fatal("done(nil), want script failure")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("done was not called")
	}
	raw, _ := os.ReadFile(upgradeLogPath)
	if !strings.Contains(string(raw), "opkg-ran") || !strings.Contains(string(raw), "upgrade FAILED") {
		t.Fatalf("upgrade log = %q", raw)
	}
}
