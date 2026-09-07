package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/httpdownload"
)

type fakeDownloader struct {
	downloadFileFn func(ctx context.Context, req DownloadFileRequest) (DownloadFileResult, error)
	lastReq        DownloadFileRequest
	callCount      int
}

type testHTTPDownloader struct{}

func (d *testHTTPDownloader) DownloadFile(ctx context.Context, req DownloadFileRequest) (DownloadFileResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, req.URL, nil)
	if err != nil {
		return DownloadFileResult{}, err
	}
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return DownloadFileResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return DownloadFileResult{}, fmt.Errorf("status %d", resp.StatusCode)
	}
	out, err := os.OpenFile(req.DestPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return DownloadFileResult{}, err
	}
	defer out.Close()
	src := io.Reader(resp.Body)
	if req.Progress != nil {
		src = httpdownload.NewReader(resp.Body, resp.ContentLength, req.Progress)
	}
	n, err := io.Copy(out, src)
	if err != nil {
		return DownloadFileResult{}, err
	}
	return DownloadFileResult{Path: req.DestPath, Size: n}, nil
}

func (f *fakeDownloader) DownloadFile(ctx context.Context, req DownloadFileRequest) (DownloadFileResult, error) {
	f.callCount++
	f.lastReq = req
	if f.downloadFileFn != nil {
		return f.downloadFileFn(ctx, req)
	}
	return DownloadFileResult{Path: req.DestPath}, nil
}

func TestInstaller_Download_Success(t *testing.T) {
	body := []byte("fake sing-box binary contents")
	sum := sha256.Sum256(body)
	hexSum := hex.EncodeToString(sum[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "sing-box")
	inst := New(target, "test-arch", BinarySpec{Version: "1.13.11", URL: srv.URL, SHA256: hexSum}, nil)
	inst.SetDownloader(&testHTTPDownloader{})

	tmp, err := inst.Download(context.Background(), nil)
	if err != nil {
		t.Fatalf("Download err: %v", err)
	}
	if tmp != target+".tmp" {
		t.Errorf("tmp path = %q, want %s.tmp", tmp, target)
	}
	got, _ := os.ReadFile(tmp)
	if string(got) != string(body) {
		t.Errorf("content mismatch")
	}
}

func TestInstaller_Download_SHAMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("tampered content"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "sing-box")
	inst := New(target, "test-arch", BinarySpec{Version: "1.13.11", URL: srv.URL, SHA256: "0000"}, nil)
	inst.SetDownloader(&testHTTPDownloader{})

	if _, err := inst.Download(context.Background(), nil); err == nil {
		t.Fatal("expected SHA mismatch error, got nil")
	}
	if _, err := os.Stat(target + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("tmp file leaked after SHA mismatch: %v", err)
	}
}

func TestInstaller_Download_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "sing-box")
	inst := New(target, "test-arch", BinarySpec{Version: "1.13.11", URL: srv.URL, SHA256: "0000"}, nil)
	inst.SetDownloader(&testHTTPDownloader{})

	if _, err := inst.Download(context.Background(), nil); err == nil {
		t.Fatal("expected error on 404, got nil")
	}
	if _, err := os.Stat(target + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("tmp file leaked after HTTP error")
	}
}

func TestInstaller_Activate_AtomicMove(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sing-box")
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, []byte("binary"), 0644); err != nil {
		t.Fatal(err)
	}

	inst := New(target, "test-arch", BinarySpec{}, nil)
	if err := inst.Activate(tmp); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	st, err := os.Stat(target)
	if err != nil {
		t.Fatalf("target missing after Activate: %v", err)
	}
	if st.Mode().Perm()&0100 == 0 {
		t.Errorf("executable bit not set: %v", st.Mode())
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("tmp file still exists after Activate")
	}
}

func TestInstaller_CurrentVersion_NotInstalled(t *testing.T) {
	dir := t.TempDir()
	inst := New(filepath.Join(dir, "sing-box"), "x", BinarySpec{}, nil)
	if v := inst.CurrentVersion(context.Background()); v != "" {
		t.Errorf("CurrentVersion on missing binary = %q, want empty", v)
	}
}

func TestInstaller_CurrentVersion_AcceptsMixedCaseBanner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable script fixture is POSIX-only")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "sing-box")
	script := "#!/bin/sh\necho 'SingBox Version 1.13.11'\n"
	if err := os.WriteFile(target, []byte(script), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	inst := New(target, "x", BinarySpec{}, nil)
	if v := inst.CurrentVersion(context.Background()); v != "1.13.11" {
		t.Errorf("CurrentVersion() = %q, want 1.13.11", v)
	}
}

func TestInstaller_CurrentVersion_AllowsSlowBanner(t *testing.T) {
	// Entware/UPX builds can take several seconds before the version
	// banner appears. The probe timeout must be wide enough that a
	// 4-second-delayed banner still parses — narrower timeouts (e.g.
	// the previous 3s) caused empty version strings on slow targets
	// and made the UI fall back to "v—".
	if runtime.GOOS == "windows" {
		t.Skip("executable script fixture is POSIX-only")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "sing-box")
	script := "#!/bin/sh\nsleep 4\necho 'sing-box version 1.13.11'\n"
	if err := os.WriteFile(target, []byte(script), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	inst := New(target, "x", BinarySpec{}, nil)
	if v := inst.CurrentVersion(context.Background()); v != "1.13.11" {
		t.Errorf("CurrentVersion() = %q, want 1.13.11", v)
	}
}

func TestInstaller_BinaryPathAndRequiredVersion(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sing-box")
	inst := New(target, "test-arch", BinarySpec{Version: "1.2.3", URL: "u", SHA256: "s"}, nil)
	if got := inst.BinaryPath(); got != target {
		t.Errorf("BinaryPath() = %q, want %q", got, target)
	}
	if got := inst.RequiredVersion(); got != "1.2.3" {
		t.Errorf("RequiredVersion() = %q, want 1.2.3", got)
	}
	if got := inst.RequiredSHA256(); got != "s" {
		t.Errorf("RequiredSHA256() = %q, want s", got)
	}
}

func TestInstaller_CurrentSHA256(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sing-box")
	body := []byte("same-version rebuilt binary")
	sum := sha256.Sum256(body)
	want := hex.EncodeToString(sum[:])
	if err := os.WriteFile(target, body, 0755); err != nil {
		t.Fatal(err)
	}

	inst := New(target, "test-arch", BinarySpec{Version: "1.2.3", SHA256: want}, nil)
	got, err := inst.CurrentSHA256()
	if err != nil {
		t.Fatalf("CurrentSHA256() err: %v", err)
	}
	if got != want {
		t.Errorf("CurrentSHA256() = %q, want %q", got, want)
	}
}

func TestInstaller_CurrentSHA256_CachedWhileUnchanged(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sing-box")
	body := []byte("original binary contents AAAA")
	sum := sha256.Sum256(body)
	want := hex.EncodeToString(sum[:])
	if err := os.WriteFile(target, body, 0755); err != nil {
		t.Fatal(err)
	}

	inst := New(target, "test-arch", BinarySpec{Version: "1.2.3", SHA256: want}, nil)
	if got, err := inst.CurrentSHA256(); err != nil || got != want {
		t.Fatalf("CurrentSHA256() = %q, %v; want %q", got, err, want)
	}

	// Swap contents keeping the same size, then restore mtime: the stat
	// snapshot matches, so the cached value must be served (no rehash).
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	swapped := []byte("different binary contents BBB")
	if len(swapped) != len(body) {
		t.Fatalf("test bug: sizes differ (%d vs %d)", len(swapped), len(body))
	}
	if err := os.WriteFile(target, swapped, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(target, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	if got, err := inst.CurrentSHA256(); err != nil || got != want {
		t.Fatalf("CurrentSHA256() after same-stat swap = %q, %v; want cached %q", got, err, want)
	}
}

func TestInstaller_CurrentSHA256_RecomputesOnChange(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sing-box")
	if err := os.WriteFile(target, []byte("v1"), 0755); err != nil {
		t.Fatal(err)
	}

	inst := New(target, "test-arch", BinarySpec{}, nil)
	if _, err := inst.CurrentSHA256(); err != nil {
		t.Fatalf("CurrentSHA256() err: %v", err)
	}

	body2 := []byte("v2 — longer replacement")
	sum2 := sha256.Sum256(body2)
	want2 := hex.EncodeToString(sum2[:])
	if err := os.WriteFile(target, body2, 0755); err != nil {
		t.Fatal(err)
	}
	if got, err := inst.CurrentSHA256(); err != nil || got != want2 {
		t.Fatalf("CurrentSHA256() after change = %q, %v; want %q", got, err, want2)
	}
}

func TestInstaller_MatchesRequired_RequiresVersionAndSHA(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sing-box")
	body := []byte("#!/bin/sh\necho 'sing-box version 1.2.3'\n")
	sum := sha256.Sum256(body)
	hexSum := hex.EncodeToString(sum[:])
	if err := os.WriteFile(target, body, 0755); err != nil {
		t.Fatal(err)
	}

	inst := New(target, "test-arch", BinarySpec{Version: "1.2.3", SHA256: hexSum}, nil)
	if !inst.MatchesRequired(context.Background()) {
		t.Fatal("MatchesRequired() = false, want true for same version and SHA")
	}

	rebuilt := New(target, "test-arch", BinarySpec{Version: "1.2.3", SHA256: strings.Repeat("0", 64)}, nil)
	if rebuilt.MatchesRequired(context.Background()) {
		t.Fatal("MatchesRequired() = true, want false for same version but different SHA")
	}
}

func TestInstaller_Download_ContextCancellation(t *testing.T) {
	// Slow handler — writes one byte then blocks until the request context
	// is cancelled. Mimics a stalled download.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1024")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte("X"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "sing-box")
	inst := New(target, "test-arch", BinarySpec{Version: "1.13.11", URL: srv.URL, SHA256: "0000"}, nil)
	inst.SetDownloader(&testHTTPDownloader{})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	if _, err := inst.Download(ctx, nil); err == nil {
		t.Fatal("expected error on cancelled context, got nil")
	}
	if _, err := os.Stat(target + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("tmp file leaked after context cancellation: %v", err)
	}
}

func TestInstaller_Download_UsesDownloaderRequestFields(t *testing.T) {
	body := []byte("fake managed binary")
	sum := sha256.Sum256(body)
	hexSum := hex.EncodeToString(sum[:])

	dir := t.TempDir()
	target := filepath.Join(dir, "sing-box")
	fdl := &fakeDownloader{}
	fdl.downloadFileFn = func(_ context.Context, req DownloadFileRequest) (DownloadFileResult, error) {
		if err := os.WriteFile(req.DestPath, body, 0o644); err != nil {
			return DownloadFileResult{}, err
		}
		if req.Progress != nil {
			req.Progress(int64(len(body)), int64(len(body)))
		}
		return DownloadFileResult{
			Path: req.DestPath,
			Size: int64(len(body)),
		}, nil
	}

	inst := New(target, "test-arch", BinarySpec{Version: "1.13.11", URL: "https://example.org/sing-box", SHA256: hexSum}, nil)
	inst.SetDownloader(fdl)

	progressCalled := false
	tmp, err := inst.Download(context.Background(), func(downloaded, total int64) {
		progressCalled = downloaded == int64(len(body)) && total == int64(len(body))
	})
	if err != nil {
		t.Fatalf("Download err: %v", err)
	}
	if tmp != target+".tmp" {
		t.Fatalf("tmp path = %q, want %q", tmp, target+".tmp")
	}
	if fdl.callCount != 1 {
		t.Fatalf("download calls = %d, want 1", fdl.callCount)
	}
	if fdl.lastReq.URL != "https://example.org/sing-box" {
		t.Fatalf("url = %q", fdl.lastReq.URL)
	}
	if fdl.lastReq.Timeout != 5*time.Minute {
		t.Fatalf("timeout = %s, want 5m", fdl.lastReq.Timeout)
	}
	if fdl.lastReq.DestPath != target+".tmp" {
		t.Fatalf("dest path = %q", fdl.lastReq.DestPath)
	}
	if fdl.lastReq.MaxFileBytes <= 0 {
		t.Fatalf("max file bytes = %d, want > 0", fdl.lastReq.MaxFileBytes)
	}
	if fdl.lastReq.Progress == nil || !progressCalled {
		t.Fatalf("progress callback was not propagated")
	}
}

func TestInstaller_Download_SHAMismatchRemovesTmpWithDownloader(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sing-box")
	fdl := &fakeDownloader{}
	fdl.downloadFileFn = func(_ context.Context, req DownloadFileRequest) (DownloadFileResult, error) {
		if err := os.WriteFile(req.DestPath, []byte("tampered"), 0o644); err != nil {
			return DownloadFileResult{}, err
		}
		return DownloadFileResult{Path: req.DestPath, Size: 8}, nil
	}

	inst := New(target, "test-arch", BinarySpec{Version: "1.13.11", URL: "https://example.org/sing-box", SHA256: strings.Repeat("0", 64)}, nil)
	inst.SetDownloader(fdl)

	if _, err := inst.Download(context.Background(), nil); err == nil {
		t.Fatal("expected sha mismatch")
	}
	if _, err := os.Stat(target + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("tmp file leaked after SHA mismatch: %v", err)
	}
}

func TestInstaller_Download_DownloaderErrorRemovesTmp(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sing-box")
	fdl := &fakeDownloader{
		downloadFileFn: func(_ context.Context, _ DownloadFileRequest) (DownloadFileResult, error) {
			return DownloadFileResult{}, errors.New("download failed")
		},
	}
	inst := New(target, "test-arch", BinarySpec{Version: "1.13.11", URL: "https://example.org/sing-box", SHA256: strings.Repeat("0", 64)}, nil)
	inst.SetDownloader(fdl)

	if _, err := inst.Download(context.Background(), nil); err == nil {
		t.Fatal("expected download error")
	}
	if _, err := os.Stat(target + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("tmp file leaked after downloader error: %v", err)
	}
}

func TestInstaller_Download_FailsWhenDownloaderNotConfigured(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sing-box")
	inst := New(target, "test-arch", BinarySpec{
		Version: "1.13.11",
		URL:     "https://example.org/sing-box",
		SHA256:  strings.Repeat("0", 64),
	}, nil)
	inst.SetDownloader(nil)

	if _, err := inst.Download(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "downloader is not configured") {
		t.Fatalf("expected downloader is not configured error, got %v", err)
	}
}

// helper для write+sha управляемого бинарника
func writeBinary(t *testing.T, path string, body string) (string, int64) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:]), int64(len(body))
}

func TestEvaluateInstallState_CleanInstall_HasSpace(t *testing.T) {
	target := filepath.Join(t.TempDir(), "sing-box")
	inst := New(target, "test-arch", BinarySpec{Version: "1.2.3", URL: "u", SHA256: "s", Size: 10 << 20}, nil)
	inst.SetFreeDiskFn(func(string) (int64, bool) { return 200 << 20, true })
	if got := inst.EvaluateInstallState(""); got != InstallStateMissing {
		t.Fatalf("got %q, want %q", got, InstallStateMissing)
	}
}

func TestEvaluateInstallState_CleanInstall_NoSpace(t *testing.T) {
	target := filepath.Join(t.TempDir(), "sing-box")
	inst := New(target, "test-arch", BinarySpec{Version: "1.2.3", URL: "u", SHA256: "s", Size: 100 << 20}, nil)
	inst.SetFreeDiskFn(func(string) (int64, bool) { return 50 << 20, true })
	if got := inst.EvaluateInstallState(""); got != InstallStateMissingNoSpace {
		t.Fatalf("got %q, want %q", got, InstallStateMissingNoSpace)
	}
}

func TestEvaluateInstallState_Upgrade_HasSpace(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sing-box")
	_, _ = writeBinary(t, target, "old-content")
	inst := New(target, "test-arch", BinarySpec{Version: "1.2.3", URL: "u", SHA256: "different-sha", Size: 10 << 20}, nil)
	inst.SetFreeDiskFn(func(string) (int64, bool) { return 200 << 20, true })
	if got := inst.EvaluateInstallState(""); got != InstallStateMissing {
		t.Fatalf("got %q, want %q (upgrade allowed)", got, InstallStateMissing)
	}
}

func TestEvaluateInstallState_Upgrade_NoSpace(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sing-box")
	_, _ = writeBinary(t, target, "old-content")
	inst := New(target, "test-arch", BinarySpec{Version: "1.2.3", URL: "u", SHA256: "different-sha", Size: 100 << 20}, nil)
	inst.SetFreeDiskFn(func(string) (int64, bool) { return 50 << 20, true })
	if got := inst.EvaluateInstallState(""); got != InstallStateOutdatedNoSpace {
		t.Fatalf("got %q, want %q", got, InstallStateOutdatedNoSpace)
	}
}

func TestEvaluateInstallState_Installed_SameSHA(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sing-box")
	sha, _ := writeBinary(t, target, "pinned-content")
	inst := New(target, "test-arch", BinarySpec{Version: "1.2.3", URL: "u", SHA256: sha, Size: 10 << 20}, nil)
	inst.SetFreeDiskFn(func(string) (int64, bool) { return 200 << 20, true })
	if got := inst.EvaluateInstallState(""); got != InstallStateInstalled {
		t.Fatalf("got %q, want %q", got, InstallStateInstalled)
	}
}

func TestEvaluateInstallState_FreeDiskUnknown_SkipsGate(t *testing.T) {
	target := filepath.Join(t.TempDir(), "sing-box")
	inst := New(target, "test-arch", BinarySpec{Version: "1.2.3", URL: "u", SHA256: "s", Size: 100 << 20}, nil)
	inst.SetFreeDiskFn(func(string) (int64, bool) { return 0, false })
	if got := inst.EvaluateInstallState(""); got != InstallStateMissing {
		t.Fatalf("got %q, want %q", got, InstallStateMissing)
	}
}

func TestEvaluateInstallState_RequiredSizeZero_SkipsGate(t *testing.T) {
	target := filepath.Join(t.TempDir(), "sing-box")
	inst := New(target, "test-arch", BinarySpec{Version: "1.2.3", URL: "u", SHA256: "s", Size: 0}, nil)
	inst.SetFreeDiskFn(func(string) (int64, bool) { return 1 << 10, true })
	if got := inst.EvaluateInstallState(""); got != InstallStateMissing {
		t.Fatalf("got %q, want %q", got, InstallStateMissing)
	}
}

// Пользователи на маленьком флеше сжимают бинарь сами (upx): SHA такого
// файла никогда не совпадёт с pinned, но версия та же — обновлять нечего.
func TestEvaluateInstallState_UPXPacked_MatchesOnlySameVersion(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sing-box")
	_, _ = writeBinary(t, target, "\x7fELF\x01\x01\x01\x00padding UPX! packed body")
	inst := New(target, "test-arch", BinarySpec{Version: "1.2.3", URL: "u", SHA256: "different-sha", Size: 100 << 20}, nil)
	inst.SetFreeDiskFn(func(string) (int64, bool) { return 50 << 20, true })
	if got := inst.EvaluateInstallState("1.2.3"); got != InstallStateInstalled {
		t.Fatalf("same version: got %q, want %q", got, InstallStateInstalled)
	}
	if got := inst.EvaluateInstallState("1.0.0"); got != InstallStateOutdatedNoSpace {
		t.Fatalf("other version: got %q, want %q", got, InstallStateOutdatedNoSpace)
	}
	if got := inst.EvaluateInstallState(""); got != InstallStateOutdatedNoSpace {
		t.Fatalf("unknown version: got %q, want %q", got, InstallStateOutdatedNoSpace)
	}
}

func TestInstaller_MatchesRequired_UPXPackedSameVersion(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sing-box")
	body := []byte("#!/bin/sh\n# UPX! marker as in a packed ELF\necho 'sing-box version 1.2.3'\n")
	if err := os.WriteFile(target, body, 0755); err != nil {
		t.Fatal(err)
	}
	inst := New(target, "test-arch", BinarySpec{Version: "1.2.3", SHA256: strings.Repeat("0", 64)}, nil)
	if !inst.MatchesRequired(context.Background()) {
		t.Fatal("MatchesRequired() = false, want true for UPX-packed binary of the pinned version")
	}
	other := New(target, "test-arch", BinarySpec{Version: "1.2.4", SHA256: strings.Repeat("0", 64)}, nil)
	if other.MatchesRequired(context.Background()) {
		t.Fatal("MatchesRequired() = true, want false for UPX-packed binary of another version")
	}
}
