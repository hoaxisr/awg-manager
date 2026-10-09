package updater

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/backup"
	"github.com/hoaxisr/awg-manager/internal/downloader"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/appver"
)

type fakeDownloader struct {
	readAllFn      func(ctx context.Context, req downloader.Request) ([]byte, downloader.ResponseMeta, error)
	downloadFileFn func(ctx context.Context, req downloader.FileRequest) (downloader.FileResult, error)
}

type recordingAppLogger struct {
	entries []logging.LogEntry
}

func (r *recordingAppLogger) AppLog(level logging.Level, group, subgroup, action, target, message string) {
	r.entries = append(r.entries, logging.LogEntry{
		Level:    string(level),
		Group:    group,
		Subgroup: subgroup,
		Action:   action,
		Target:   target,
		Message:  message,
	})
}

func TestIPKFilenameFromURL_RejectsUnsafeShellCharacters(t *testing.T) {
	cases := []string{
		"http://repo.local/a/awg-manager_2.12.0_aarch64-3.10-kn.ipk%3Btouch",
		"http://repo.local/a/awg-manager_2.12.0_aarch64-3.10-kn.ipk%26x",
		"http://repo.local/a/awg-manager_2.12.0_aarch64-3.10-kn.ipk%7Cx",
		"http://repo.local/a/awg-manager_2.12.0_aarch64-3.10-kn.ipk%24%28x%29",
		"http://repo.local/a/awg-manager_2.12.0_aarch64-3.10-kn.ipk%60x%60",
		"http://repo.local/a/awg-manager%202.12.0.ipk",
		"http://repo.local/a/../awg-manager_2.12.0.ipk",
		"http://repo.local/a/other-package_1.0.0.ipk",
		"http://repo.local/a/awg-manager_2.12.0_aarch64-3.10-kn.txt",
	}
	for _, raw := range cases {
		name, err := ipkFilenameFromURL(raw)
		if err == nil {
			t.Fatalf("expected reject for %q, got name=%q", raw, name)
		}
	}
}

func (f *fakeDownloader) ReadAll(ctx context.Context, req downloader.Request) ([]byte, downloader.ResponseMeta, error) {
	if f.readAllFn != nil {
		return f.readAllFn(ctx, req)
	}
	return nil, downloader.ResponseMeta{}, errors.New("readall not implemented")
}

func (f *fakeDownloader) DownloadFile(ctx context.Context, req downloader.FileRequest) (downloader.FileResult, error) {
	if f.downloadFileFn != nil {
		return f.downloadFileFn(ctx, req)
	}
	return downloader.FileResult{}, errors.New("downloadfile not implemented")
}

func TestCheckWithDownloader_UsesDownloaderRequest(t *testing.T) {
	arch := archSuffix()
	ipkName := "awg-manager_9.9.9_" + arch + "-kn.ipk"
	packages := "Package: awg-manager\nVersion: 9.9.9\nFilename: " + ipkName + "\n"
	var seen downloader.Request
	dl := &fakeDownloader{
		readAllFn: func(_ context.Context, req downloader.Request) ([]byte, downloader.ResponseMeta, error) {
			seen = req
			return gzipBytes(t, packages), downloader.ResponseMeta{StatusCode: http.StatusOK}, nil
		},
	}

	info := checkWithDownloader(context.Background(), "2.0.0", channelStable, dl, nil)
	if !info.Available {
		t.Fatalf("expected update available, got %+v", info)
	}
	if seen.Purpose != "awgm-update-check" {
		t.Fatalf("purpose = %q, want awgm-update-check", seen.Purpose)
	}
	if seen.UserAgent != appver.UA() {
		t.Fatalf("user-agent = %q, want %q", seen.UserAgent, appver.UA())
	}
	if seen.Timeout != repoTimeout {
		t.Fatalf("timeout = %s, want %s", seen.Timeout, repoTimeout)
	}
	if seen.MaxBodyBytes != packagesMaxBytes {
		t.Fatalf("max body bytes = %d, want %d", seen.MaxBodyBytes, packagesMaxBytes)
	}
}

func TestLoggingDownloader_LogsUpdateCheckChangelogAndUpgradeURLs(t *testing.T) {
	rec := &recordingAppLogger{}
	scoped := logging.NewScopedLogger(rec, logging.GroupSystem, logging.SubUpdate)
	dl := newLoggingDownloader(&fakeDownloader{
		readAllFn: func(_ context.Context, req downloader.Request) ([]byte, downloader.ResponseMeta, error) {
			return []byte("ok"), downloader.ResponseMeta{
				StatusCode: http.StatusOK,
				Route: downloader.RouteInfo{
					Tag:  "RelayCH",
					Kind: "AWG",
				},
			}, nil
		},
		downloadFileFn: func(_ context.Context, req downloader.FileRequest) (downloader.FileResult, error) {
			return downloader.FileResult{
				Path: req.DestPath,
				Size: 123,
				Route: downloader.RouteInfo{
					Tag:  "RelayCH",
					Kind: "AWG",
				},
			}, nil
		},
	}, scoped)

	_, _, err := dl.ReadAll(context.Background(), downloader.Request{
		Purpose:      "awgm-update-check",
		URL:          "http://repo.local/VERSION",
		MaxBodyBytes: 10,
	})
	if err != nil {
		t.Fatalf("ReadAll update check: %v", err)
	}
	_, _, err = dl.ReadAll(context.Background(), downloader.Request{
		Purpose:      "awgm-changelog",
		URL:          "http://repo.local/CHANGELOG.md",
		MaxBodyBytes: 10,
	})
	if err != nil {
		t.Fatalf("ReadAll changelog: %v", err)
	}
	_, err = dl.DownloadFile(context.Background(), downloader.FileRequest{
		Request: downloader.Request{
			Purpose:      "awgm-update-ipk",
			URL:          "http://repo.local/awg-manager_2.12.0_aarch64-3.10-kn.ipk",
			MaxBodyBytes: 10,
		},
		DestPath:     filepath.Join(t.TempDir(), "pkg.ipk"),
		MaxFileBytes: 10,
	})
	if err != nil {
		t.Fatalf("DownloadFile upgrade: %v", err)
	}

	wantMessages := []string{
		"Проверка обновлений через RelayCH (AWG): http://repo.local/VERSION",
		"Загрузка changelog через RelayCH (AWG): http://repo.local/CHANGELOG.md",
		"Обновление AWGM через RelayCH (AWG): http://repo.local/awg-manager_2.12.0_aarch64-3.10-kn.ipk",
	}
	if len(rec.entries) != len(wantMessages) {
		t.Fatalf("log entries = %d, want %d: %+v", len(rec.entries), len(wantMessages), rec.entries)
	}
	for i, want := range wantMessages {
		if rec.entries[i].Message != want {
			t.Fatalf("log[%d] message = %q, want %q", i, rec.entries[i].Message, want)
		}
		if rec.entries[i].Group != logging.GroupSystem || rec.entries[i].Subgroup != logging.SubUpdate {
			t.Fatalf("log[%d] scope = %s/%s, want system/update", i, rec.entries[i].Group, rec.entries[i].Subgroup)
		}
	}
}

func TestLoggingDownloader_LogsErrorsWithURLs(t *testing.T) {
	rec := &recordingAppLogger{}
	scoped := logging.NewScopedLogger(rec, logging.GroupSystem, logging.SubUpdate)
	dl := newLoggingDownloader(&fakeDownloader{
		readAllFn: func(_ context.Context, req downloader.Request) ([]byte, downloader.ResponseMeta, error) {
			return nil, downloader.ResponseMeta{
				Route: downloader.RouteInfo{
					Tag:  "RelayCH",
					Kind: "AWG",
				},
			}, errors.New("network down")
		},
		downloadFileFn: func(_ context.Context, req downloader.FileRequest) (downloader.FileResult, error) {
			return downloader.FileResult{
				Route: downloader.RouteInfo{
					Tag:  "RelayCH",
					Kind: "AWG",
				},
			}, errors.New("disk full")
		},
	}, scoped)

	_, _, _ = dl.ReadAll(context.Background(), downloader.Request{
		Purpose:      "awgm-update-check",
		URL:          "http://repo.local/VERSION",
		MaxBodyBytes: 10,
	})
	_, _, _ = dl.ReadAll(context.Background(), downloader.Request{
		Purpose:      "awgm-changelog",
		URL:          "http://repo.local/CHANGELOG.md",
		MaxBodyBytes: 10,
	})
	_, _ = dl.DownloadFile(context.Background(), downloader.FileRequest{
		Request: downloader.Request{
			Purpose:      "awgm-update-ipk",
			URL:          "http://repo.local/awg-manager_2.12.0_aarch64-3.10-kn.ipk",
			MaxBodyBytes: 10,
		},
		DestPath:     filepath.Join(t.TempDir(), "pkg.ipk"),
		MaxFileBytes: 10,
	})

	wantMessages := []string{
		"Ошибка проверки обновлений через RelayCH (AWG): http://repo.local/VERSION: network down",
		"Ошибка загрузки changelog через RelayCH (AWG): http://repo.local/CHANGELOG.md: network down",
		"Ошибка обновления AWGM через RelayCH (AWG): http://repo.local/awg-manager_2.12.0_aarch64-3.10-kn.ipk: disk full",
	}
	if len(rec.entries) != len(wantMessages) {
		t.Fatalf("log entries = %d, want %d: %+v", len(rec.entries), len(wantMessages), rec.entries)
	}
	for i, want := range wantMessages {
		if rec.entries[i].Level != string(logging.LevelWarn) {
			t.Fatalf("log[%d] level = %q, want warn", i, rec.entries[i].Level)
		}
		if rec.entries[i].Message != want {
			t.Fatalf("log[%d] message = %q, want %q", i, rec.entries[i].Message, want)
		}
	}
}

func TestChangelogFetcher_UsesDownloaderRequest(t *testing.T) {
	const md = "## [1.0.0] - 2026-01-01\n\n### Added\n- item\n"
	var seen downloader.Request
	dl := &fakeDownloader{
		readAllFn: func(_ context.Context, req downloader.Request) ([]byte, downloader.ResponseMeta, error) {
			seen = req
			return []byte(md), downloader.ResponseMeta{StatusCode: http.StatusOK}, nil
		},
	}
	f := newChangelogFetcher("http://repo.local/CHANGELOG.md", repoTimeout, dl)
	entries, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch changelog: %v", err)
	}
	if entries["1.0.0"].Version != "1.0.0" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
	if seen.Purpose != "awgm-changelog" {
		t.Fatalf("purpose = %q, want awgm-changelog", seen.Purpose)
	}
	if seen.UserAgent != appver.UA() {
		t.Fatalf("user-agent = %q, want %q", seen.UserAgent, appver.UA())
	}
	if seen.MaxBodyBytes != changelogMaxBytes {
		t.Fatalf("max body bytes = %d, want %d", seen.MaxBodyBytes, changelogMaxBytes)
	}
}

func TestUpgradeWithDownloader_UsesFileRequest(t *testing.T) {
	// downloadDir is constant; verify destination suffix and purpose.
	var seen downloader.FileRequest
	dl := &fakeDownloader{
		downloadFileFn: func(_ context.Context, req downloader.FileRequest) (downloader.FileResult, error) {
			seen = req
			return downloader.FileResult{Path: req.DestPath, Size: 123}, nil
		},
	}

	oldStart := startDetachedUpgrade
	startDetachedUpgrade = func(_ string) error { return nil }
	t.Cleanup(func() { startDetachedUpgrade = oldStart })

	url := "http://repo.local/aarch64-k3.10/awg-manager_2.12.0_aarch64-3.10-kn.ipk?token=x"
	if err := upgradeWithDownloader(context.Background(), url, "", dl, nil); err != nil {
		t.Fatalf("upgradeWithDownloader: %v", err)
	}
	if seen.Request.Purpose != "awgm-update-ipk" {
		t.Fatalf("purpose = %q, want awgm-update-ipk", seen.Request.Purpose)
	}
	if seen.Request.UserAgent != appver.UA() {
		t.Fatalf("user-agent = %q, want %q", seen.Request.UserAgent, appver.UA())
	}
	if seen.Request.Timeout != downloadTimeout {
		t.Fatalf("timeout = %s, want %s", seen.Request.Timeout, downloadTimeout)
	}
	if seen.MaxFileBytes != ipkMaxBytes {
		t.Fatalf("max file bytes = %d, want %d", seen.MaxFileBytes, ipkMaxBytes)
	}
	if !strings.HasPrefix(filepath.Clean(seen.DestPath), filepath.Clean(downloadDir)+string(os.PathSeparator)) {
		t.Fatalf("dest path = %q, want inside %q", seen.DestPath, downloadDir)
	}
	if filepath.Base(seen.DestPath) != "awg-manager_2.12.0_aarch64-3.10-kn.ipk" {
		t.Fatalf("dest filename = %q, want awg-manager_2.12.0_aarch64-3.10-kn.ipk", filepath.Base(seen.DestPath))
	}
}

func TestApplyUpgrade_SnapshotsBeforeInstall(t *testing.T) {
	dl := &fakeDownloader{
		downloadFileFn: func(_ context.Context, req downloader.FileRequest) (downloader.FileResult, error) {
			return downloader.FileResult{Path: req.DestPath, Size: 123}, nil
		},
	}
	var calls []string
	oldStart := startDetachedUpgrade
	startDetachedUpgrade = func(_ string) error { calls = append(calls, "install"); return nil }
	t.Cleanup(func() { startDetachedUpgrade = oldStart })

	s := New("2.11.0", nil, nil, t.TempDir(), nil)
	s.SetDownloader(dl)
	// Снимок, который не удался, не останавливает обновление.
	s.snapshot = func(spare int64) (backup.Snapshot, error) {
		calls = append(calls, "snapshot")
		if spare < backup.MinSnapshotSpare {
			t.Errorf("spare = %d, want at least %d", spare, backup.MinSnapshotSpare)
		}
		return backup.Snapshot{}, errors.New("мало места")
	}
	s.cached = &UpdateInfo{DownloadURL: "http://repo.local/aarch64-k3.10/awg-manager_2.12.0_aarch64-3.10-kn.ipk"}
	if err := s.ApplyUpgrade(context.Background()); err != nil {
		t.Fatalf("ApplyUpgrade: %v", err)
	}
	if strings.Join(calls, ",") != "snapshot,install" {
		t.Fatalf("calls = %v, want snapshot before install", calls)
	}
}

func TestUpgradeWithDownloader_NoSnapshotWhenPackageRejected(t *testing.T) {
	dl := &fakeDownloader{
		downloadFileFn: func(_ context.Context, req downloader.FileRequest) (downloader.FileResult, error) {
			return downloader.FileResult{Path: req.DestPath, Size: 123}, nil
		},
	}
	oldStart := startDetachedUpgrade
	startDetachedUpgrade = func(_ string) error {
		t.Fatal("установка после отвергнутого пакета")
		return nil
	}
	t.Cleanup(func() { startDetachedUpgrade = oldStart })

	called := false
	url := "http://repo.local/aarch64-k3.10/awg-manager_2.12.0_aarch64-3.10-kn.ipk"
	// Файла нет, контрольная сумма задана — проверка пакета не проходит.
	if err := upgradeWithDownloader(context.Background(), url, strings.Repeat("0", 64), dl, func(string) { called = true }); err == nil {
		t.Fatal("ожидалась ошибка проверки пакета")
	}
	if called {
		t.Fatal("снимок снят, хотя обновление не дошло до установки")
	}
}

// Снимок, выключенный в настройках, не снимается, а установка идёт.
func TestApplyUpgrade_SnapshotDisabledInSettings(t *testing.T) {
	dl := &fakeDownloader{
		downloadFileFn: func(_ context.Context, req downloader.FileRequest) (downloader.FileResult, error) {
			return downloader.FileResult{Path: req.DestPath, Size: 123}, nil
		},
	}
	var calls []string
	oldStart := startDetachedUpgrade
	startDetachedUpgrade = func(_ string) error { calls = append(calls, "install"); return nil }
	t.Cleanup(func() { startDetachedUpgrade = oldStart })

	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)
	if err := store.Update(func(cur *storage.Settings) error { cur.Updates.SnapshotDisabled = true; return nil }); err != nil {
		t.Fatal(err)
	}
	s := New("2.11.0", store, nil, dir, nil)
	s.SetDownloader(dl)
	s.snapshot = func(int64) (backup.Snapshot, error) {
		calls = append(calls, "snapshot")
		return backup.Snapshot{}, nil
	}
	s.cached = &UpdateInfo{DownloadURL: "http://repo.local/aarch64-k3.10/awg-manager_2.12.0_aarch64-3.10-kn.ipk"}
	if err := s.ApplyUpgrade(context.Background()); err != nil {
		t.Fatalf("ApplyUpgrade: %v", err)
	}
	if strings.Join(calls, ",") != "install" {
		t.Fatalf("calls = %v, want install only", calls)
	}
}

// Запас под установку считается от размера пакета, но не меньше нижней границы.
func TestSnapshotSpare(t *testing.T) {
	dir := t.TempDir()
	small := filepath.Join(dir, "small.ipk")
	big := filepath.Join(dir, "big.ipk")
	if err := os.WriteFile(small, make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(big, make([]byte, 10<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := snapshotSpare(small); got != backup.MinSnapshotSpare {
		t.Errorf("small: %d, want floor %d", got, backup.MinSnapshotSpare)
	}
	if got := snapshotSpare(big); got != 30<<20 {
		t.Errorf("big: %d, want %d", got, 30<<20)
	}
	if got := snapshotSpare(filepath.Join(dir, "missing.ipk")); got != backup.MinSnapshotSpare {
		t.Errorf("missing: %d, want floor", got)
	}
}
