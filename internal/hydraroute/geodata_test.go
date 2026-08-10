package hydraroute

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
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

func newTestGeoStore(t *testing.T) *GeoDataStore {
	t.Helper()
	tmp := t.TempDir()
	return NewGeoDataStore(tmp)
}

func TestGeoDataStore_LogsDownloadAndUpdateRouteURLs(t *testing.T) {
	store := newTestGeoStore(t)
	rec := &recordingAppLogger{}
	store.SetAppLogger(rec)

	dat := buildGeoDAT([][]byte{buildGeoEntry(1, "GOOGLE", 2, 1)})
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewReader(dat)),
				Request:    r,
			}, nil
		}),
	}

	entry, err := store.DownloadWithClientVia(context.Background(), "geosite", "https://example.com/geosite.dat", client, "RelayCH (AWG)")
	if err != nil {
		t.Fatalf("DownloadWithClientVia: %v", err)
	}
	if _, err := store.UpdateWithClientVia(context.Background(), entry.Path, client, "RelayCH (AWG)"); err != nil {
		t.Fatalf("UpdateWithClientVia: %v", err)
	}

	wantMessages := []string{
		"Загрузка geo-data через RelayCH (AWG): https://example.com/geosite.dat",
		"Обновление geo-data через RelayCH (AWG): https://example.com/geosite.dat",
	}
	var got []string
	for _, e := range rec.entries {
		if e.Action != "download-url" && e.Action != "update-url" {
			continue
		}
		if e.Group != logging.GroupRouting || e.Subgroup != logging.SubHrNeo {
			t.Fatalf("scope = %s/%s, want routing/hrneo", e.Group, e.Subgroup)
		}
		got = append(got, e.Message)
	}
	if len(got) != len(wantMessages) {
		t.Fatalf("route log messages = %d, want %d: %+v", len(got), len(wantMessages), got)
	}
	for i, want := range wantMessages {
		if got[i] != want {
			t.Fatalf("route log[%d] = %q, want %q", i, got[i], want)
		}
	}
}

func TestGeoDataStore_LogsDownloadAndUpdateErrors(t *testing.T) {
	store := newTestGeoStore(t)
	rec := &recordingAppLogger{}
	store.SetAppLogger(rec)

	_, err := store.DownloadWithClientVia(context.Background(), "geosite", "https://example.com/geosite.dat", &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return nil, io.ErrUnexpectedEOF
		}),
	}, "RelayCH (AWG)")
	if err == nil {
		t.Fatal("DownloadWithClientVia error = nil")
	}

	path := filepath.Join(store.geoDir, "managed.dat")
	if err := os.WriteFile(path, []byte("old-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.entries = []GeoFileEntry{
		{Type: "geosite", Path: path, URL: "https://example.com/geosite.dat"},
	}
	store.mu.Unlock()

	_, err = store.UpdateWithClientVia(context.Background(), path, &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return nil, io.ErrUnexpectedEOF
		}),
	}, "RelayCH (AWG)")
	if err == nil {
		t.Fatal("UpdateWithClientVia error = nil")
	}

	wantMessages := []string{
		"Ошибка загрузки geo-data через RelayCH (AWG): https://example.com/geosite.dat:",
		"Ошибка обновления geo-data через RelayCH (AWG): https://example.com/geosite.dat:",
	}
	var got []string
	for _, e := range rec.entries {
		if e.Action != "download-url" && e.Action != "update-url" {
			continue
		}
		if e.Level != string(logging.LevelWarn) {
			t.Fatalf("level = %q, want warn", e.Level)
		}
		got = append(got, e.Message)
	}
	if len(got) != len(wantMessages) {
		t.Fatalf("route error log messages = %d, want %d: %+v", len(got), len(wantMessages), got)
	}
	for i, wantPrefix := range wantMessages {
		if !strings.HasPrefix(got[i], wantPrefix) {
			t.Fatalf("route error log[%d] = %q, want prefix %q", i, got[i], wantPrefix)
		}
	}
}

func TestAdoptExternalFiles_AddsUnknownFiles(t *testing.T) {
	store := newTestGeoStore(t)

	geositePath := filepath.Join(store.geoDir, "geosite.dat")
	geoipPath := filepath.Join(store.geoDir, "geoip.dat")
	if err := os.WriteFile(geositePath, []byte("fake-content"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(geoipPath, []byte("fake-content"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{
		GeoSiteFiles: []string{geositePath},
		GeoIPFiles:   []string{geoipPath},
	}

	n, err := store.AdoptExternalFiles(cfg)
	if err != nil {
		t.Fatalf("AdoptExternalFiles: %v", err)
	}
	if n != 2 {
		t.Fatalf("adopted count = %d, want 2", n)
	}

	entries := store.List()
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	for _, e := range entries {
		if e.External {
			t.Errorf("entry %q: External=true for awg-manager/geo path", e.Path)
		}
		want := ""
		switch e.Type {
		case "geoip":
			want = GroundZerroGeoIPURL
		case "geosite":
			want = GroundZerroGeoSiteURL
		}
		if e.URL != want {
			t.Errorf("entry %q (type=%s): URL=%q, want %q", e.Path, e.Type, e.URL, want)
		}
	}
}

func TestAdoptExternalFiles_SkipsAlreadyTracked(t *testing.T) {
	store := newTestGeoStore(t)
	existingPath := filepath.Join(store.geoDir, "existing.dat")
	if err := os.WriteFile(existingPath, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	store.mu.Lock()
	store.entries = []GeoFileEntry{
		{Type: "geosite", Path: existingPath, URL: "https://example.com/f.dat"},
	}
	store.mu.Unlock()

	cfg := &Config{
		GeoSiteFiles: []string{existingPath},
	}

	n, err := store.AdoptExternalFiles(cfg)
	if err != nil {
		t.Fatalf("AdoptExternalFiles: %v", err)
	}
	if n != 0 {
		t.Fatalf("adopted = %d, want 0 (path already tracked)", n)
	}
	if len(store.List()) != 1 {
		t.Fatalf("entries = %d, want 1 (no duplicate)", len(store.List()))
	}
}

func TestAdoptExternalFiles_SkipsMissingFiles(t *testing.T) {
	store := newTestGeoStore(t)
	cfg := &Config{
		GeoSiteFiles: []string{filepath.Join(store.geoDir, "does-not-exist.dat")},
	}

	n, err := store.AdoptExternalFiles(cfg)
	if err != nil {
		t.Fatalf("AdoptExternalFiles: %v", err)
	}
	if n != 0 {
		t.Fatalf("adopted = %d, want 0 (file missing)", n)
	}
	if len(store.List()) != 0 {
		t.Fatalf("entries = %d, want 0", len(store.List()))
	}
}

func TestAdoptExternalFiles_NilConfig(t *testing.T) {
	store := newTestGeoStore(t)
	n, err := store.AdoptExternalFiles(nil)
	if err != nil {
		t.Fatalf("AdoptExternalFiles(nil): %v", err)
	}
	if n != 0 {
		t.Fatalf("adopted = %d, want 0", n)
	}
}

func TestAdoptExternalFiles_ResolvesRelativeHRPaths(t *testing.T) {
	tmp := t.TempDir()
	origHR := hrDir
	hrDir = filepath.Join(tmp, "HydraRoute")
	geoSub := filepath.Join(hrDir, "geo")
	if err := os.MkdirAll(geoSub, 0o755); err != nil {
		t.Fatal(err)
	}
	defer func() { hrDir = origHR }()

	relPath := "geo/geosite_GA.dat"
	absPath := filepath.Join(hrDir, relPath)
	if err := os.WriteFile(absPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	store := newTestGeoStore(t)
	cfg := &Config{GeoSiteFiles: []string{relPath}}
	n, err := store.AdoptExternalFiles(cfg)
	if err != nil {
		t.Fatalf("AdoptExternalFiles: %v", err)
	}
	if n != 1 {
		t.Fatalf("adopted = %d, want 1", n)
	}
	if store.entries[0].Path != absPath {
		t.Fatalf("path = %q, want %q", store.entries[0].Path, absPath)
	}
	if !store.entries[0].External {
		t.Fatal("expected External=true")
	}
}

func TestAdoptExternalFiles_SkipsUnmanagedPaths(t *testing.T) {
	tmp := t.TempDir()
	store := NewGeoDataStore(tmp)

	outsidePath := filepath.Join(tmp, "outside.dat")
	if err := os.WriteFile(outsidePath, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	insidePath := filepath.Join(store.geoDir, "inside.dat")
	if err := os.WriteFile(insidePath, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{
		GeoSiteFiles: []string{outsidePath, insidePath},
	}

	n, err := store.AdoptExternalFiles(cfg)
	if err != nil {
		t.Fatalf("AdoptExternalFiles: %v", err)
	}
	if n != 1 {
		t.Fatalf("adopted = %d, want 1 (only path under geoDir)", n)
	}
	entries := store.List()
	if len(entries) != 1 || entries[0].Path != insidePath {
		t.Fatalf("entries = %+v, want only %q", entries, insidePath)
	}
}

func TestUpdate_RejectsExternalEntry(t *testing.T) {
	store := newTestGeoStore(t)
	path := filepath.Join(store.geoDir, "adopted.dat")
	if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.entries = []GeoFileEntry{
		{Type: "geosite", Path: path, URL: GroundZerroGeoSiteURL, External: true},
	}
	store.mu.Unlock()

	_, err := store.Update(path)
	if err == nil {
		t.Fatal("Update returned nil, expected error for external entry")
	}
	if !strings.Contains(err.Error(), "external") {
		t.Fatalf("err = %q, want external rejection", err)
	}
}

func TestNewGeoDataStore_UsesGeoSubdir(t *testing.T) {
	tmp := t.TempDir()
	store := NewGeoDataStore(tmp)
	want := filepath.Join(tmp, "geo")
	if store.geoDir != want {
		t.Fatalf("geoDir = %q, want %q", store.geoDir, want)
	}
	if st, err := os.Stat(want); err != nil || !st.IsDir() {
		t.Fatalf("geo dir not created: %v", err)
	}
}

func TestDownloadFileWithClient_UsesProvidedClient(t *testing.T) {
	destDir := t.TempDir()
	dest := filepath.Join(destDir, "sample.dat")
	called := false

	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			called = true
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("test-data")),
				Request:    r,
			}, nil
		}),
	}

	size, err := downloadFileWithClient(context.Background(), client, "https://example.com/file.dat", dest, nil)
	if err != nil {
		t.Fatalf("downloadFileWithClient: %v", err)
	}
	if !called {
		t.Fatal("custom client was not used")
	}
	if size != int64(len("test-data")) {
		t.Fatalf("size = %d, want %d", size, len("test-data"))
	}
	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(raw) != "test-data" {
		t.Fatalf("dest content = %q", string(raw))
	}
}

func TestDownloadWithClient_SaveFailure_DoesNotEmitDone(t *testing.T) {
	store := newTestGeoStore(t)
	store.storagePath = store.geoDir // force saveUnlocked failure (path is a directory)

	var (
		mu     sync.Mutex
		phases []string
	)
	store.SetProgressReporter(func(rawURL, fileType, phase string, downloaded, total int64, errMsg string) {
		mu.Lock()
		phases = append(phases, phase)
		mu.Unlock()
	})

	dat := buildGeoDAT([][]byte{buildGeoEntry(1, "GOOGLE", 2, 1)})
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewReader(dat)),
				Request:    r,
			}, nil
		}),
	}

	_, err := store.DownloadWithClient(context.Background(), "geosite", "https://example.com/geosite.dat", client)
	if err == nil || !strings.Contains(err.Error(), "save metadata") {
		t.Fatalf("expected save metadata error, got: %v", err)
	}

	mu.Lock()
	gotPhases := append([]string(nil), phases...)
	mu.Unlock()
	for _, p := range gotPhases {
		if p == "done" {
			t.Fatalf("unexpected done phase on save failure: %v", gotPhases)
		}
	}
	hasError := false
	for _, p := range gotPhases {
		if p == "error" {
			hasError = true
			break
		}
	}
	if !hasError {
		t.Fatalf("expected error phase, got %v", gotPhases)
	}

	if entries := store.List(); len(entries) != 0 {
		t.Fatalf("entries = %d, want 0 after failed save", len(entries))
	}
	dest := filepath.Join(store.geoDir, "geosite.dat")
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("downloaded file still exists after failed save: %v", statErr)
	}
}

func TestUpdateWithClient_SaveFailure_DoesNotEmitDone(t *testing.T) {
	store := newTestGeoStore(t)

	path := filepath.Join(store.geoDir, "managed.dat")
	if err := os.WriteFile(path, []byte("old-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.entries = []GeoFileEntry{
		{Type: "geosite", Path: path, URL: "https://example.com/geosite.dat"},
	}
	store.mu.Unlock()
	store.storagePath = store.geoDir // force saveUnlocked failure (path is a directory)

	var (
		mu     sync.Mutex
		phases []string
	)
	store.SetProgressReporter(func(rawURL, fileType, phase string, downloaded, total int64, errMsg string) {
		mu.Lock()
		phases = append(phases, phase)
		mu.Unlock()
	})

	dat := buildGeoDAT([][]byte{buildGeoEntry(1, "GOOGLE", 2, 2)})
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewReader(dat)),
				Request:    r,
			}, nil
		}),
	}

	_, err := store.UpdateWithClient(context.Background(), path, client)
	if err == nil || !strings.Contains(err.Error(), "save metadata") {
		t.Fatalf("expected save metadata error, got: %v", err)
	}

	mu.Lock()
	gotPhases := append([]string(nil), phases...)
	mu.Unlock()
	for _, p := range gotPhases {
		if p == "done" {
			t.Fatalf("unexpected done phase on save failure: %v", gotPhases)
		}
	}
	hasError := false
	for _, p := range gotPhases {
		if p == "error" {
			hasError = true
			break
		}
	}
	if !hasError {
		t.Fatalf("expected error phase, got %v", gotPhases)
	}
}

func TestUpdateWithClient_SaveFailure_RollsBackFileAndMetadata(t *testing.T) {
	store := newTestGeoStore(t)

	path := filepath.Join(store.geoDir, "managed-roll-back.dat")
	if err := os.WriteFile(path, []byte("old-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.entries = []GeoFileEntry{
		{
			Type:     "geosite",
			Path:     path,
			URL:      "https://example.com/geosite.dat",
			Size:     111,
			TagCount: 1,
			Updated:  "old",
		},
	}
	store.mu.Unlock()
	store.storagePath = store.geoDir // force saveUnlocked failure

	var (
		mu     sync.Mutex
		phases []string
	)
	store.SetProgressReporter(func(rawURL, fileType, phase string, downloaded, total int64, errMsg string) {
		mu.Lock()
		phases = append(phases, phase)
		mu.Unlock()
	})

	dat := buildGeoDAT([][]byte{buildGeoEntry(1, "GOOGLE", 2, 2)})
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewReader(dat)),
				Request:    r,
			}, nil
		}),
	}

	_, err := store.UpdateWithClient(context.Background(), path, client)
	if err == nil || !strings.Contains(err.Error(), "save metadata") {
		t.Fatalf("expected save metadata error, got: %v", err)
	}

	mu.Lock()
	gotPhases := append([]string(nil), phases...)
	mu.Unlock()
	for _, p := range gotPhases {
		if p == "done" {
			t.Fatalf("unexpected done phase on save failure: %v", gotPhases)
		}
	}
	hasError := false
	for _, p := range gotPhases {
		if p == "error" {
			hasError = true
			break
		}
	}
	if !hasError {
		t.Fatalf("expected error phase, got %v", gotPhases)
	}

	entries := store.List()
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].Size != 111 {
		t.Fatalf("size = %d, want 111", entries[0].Size)
	}
	if entries[0].TagCount != 1 {
		t.Fatalf("tagCount = %d, want 1", entries[0].TagCount)
	}
	if entries[0].Updated != "old" {
		t.Fatalf("updated = %q, want old", entries[0].Updated)
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read updated path: %v", readErr)
	}
	if string(raw) != "old-data" {
		t.Fatalf("file content = %q, want old-data", string(raw))
	}
	candidates, globErr := filepath.Glob(path + ".update.*")
	if globErr != nil {
		t.Fatalf("glob update files: %v", globErr)
	}
	if len(candidates) > 0 {
		t.Fatalf("unexpected candidate files left: %v", candidates)
	}
	backups, globErr := filepath.Glob(path + ".backup.*")
	if globErr != nil {
		t.Fatalf("glob backup files: %v", globErr)
	}
	if len(backups) > 0 {
		t.Fatalf("unexpected backup files left: %v", backups)
	}
}

func TestUpdateWithClient_EntryMissingAfterSwap_RollsBackFile(t *testing.T) {
	store := newTestGeoStore(t)

	path := filepath.Join(store.geoDir, "managed-entry-missing.dat")
	if err := os.WriteFile(path, []byte("old-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.entries = []GeoFileEntry{
		{
			Type:     "geosite",
			Path:     path,
			URL:      "https://example.com/geosite.dat",
			Size:     111,
			TagCount: 1,
			Updated:  "old",
		},
	}
	store.mu.Unlock()

	var (
		mu     sync.Mutex
		phases []string
	)
	store.SetProgressReporter(func(rawURL, fileType, phase string, downloaded, total int64, errMsg string) {
		mu.Lock()
		phases = append(phases, phase)
		mu.Unlock()
		if phase == "validate" {
			store.mu.Lock()
			store.entries = nil
			store.mu.Unlock()
		}
	})

	dat := buildGeoDAT([][]byte{buildGeoEntry(1, "GOOGLE", 2, 2)})
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewReader(dat)),
				Request:    r,
			}, nil
		}),
	}

	_, err := store.UpdateWithClient(context.Background(), path, client)
	if err == nil || !strings.Contains(err.Error(), "geo file not found after update") {
		t.Fatalf("expected geo file not found after update error, got: %v", err)
	}

	mu.Lock()
	gotPhases := append([]string(nil), phases...)
	mu.Unlock()
	for _, p := range gotPhases {
		if p == "done" {
			t.Fatalf("unexpected done phase on idx-missing rollback path: %v", gotPhases)
		}
	}
	hasError := false
	for _, p := range gotPhases {
		if p == "error" {
			hasError = true
			break
		}
	}
	if !hasError {
		t.Fatalf("expected error phase, got %v", gotPhases)
	}

	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read path after rollback: %v", readErr)
	}
	if string(raw) != "old-data" {
		t.Fatalf("file content = %q, want old-data", string(raw))
	}
	candidates, globErr := filepath.Glob(path + ".update.*")
	if globErr != nil {
		t.Fatalf("glob update files: %v", globErr)
	}
	if len(candidates) > 0 {
		t.Fatalf("unexpected candidate files left: %v", candidates)
	}
	backups, globErr := filepath.Glob(path + ".backup.*")
	if globErr != nil {
		t.Fatalf("glob backup files: %v", globErr)
	}
	if len(backups) > 0 {
		t.Fatalf("unexpected backup files left: %v", backups)
	}
}

func TestGeoDataStore_Recovery_RemovesStaleUpdateArtifacts(t *testing.T) {
	tmp := t.TempDir()
	geoDir := filepath.Join(tmp, geoSubdir)
	if err := os.MkdirAll(geoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	updatePath := filepath.Join(geoDir, "geosite.dat.update.123.456")
	if err := os.WriteFile(updatePath, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}

	_ = NewGeoDataStore(tmp)

	if _, err := os.Stat(updatePath); !os.IsNotExist(err) {
		t.Fatalf("stale update artifact still exists: %v", err)
	}
}

func TestGeoDataStore_Recovery_RestoresBackupWhenOriginalMissing(t *testing.T) {
	tmp := t.TempDir()
	geoDir := filepath.Join(tmp, geoSubdir)
	if err := os.MkdirAll(geoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(geoDir, "geosite.dat.backup.123.456")
	origPath := filepath.Join(geoDir, "geosite.dat")
	if err := os.WriteFile(backupPath, []byte("old-data"), 0o644); err != nil {
		t.Fatal(err)
	}

	_ = NewGeoDataStore(tmp)

	raw, err := os.ReadFile(origPath)
	if err != nil {
		t.Fatalf("read restored original: %v", err)
	}
	if string(raw) != "old-data" {
		t.Fatalf("restored original content = %q, want old-data", string(raw))
	}
	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Fatalf("backup artifact still exists: %v", err)
	}
}

func TestGeoDataStore_Recovery_RemovesBackupWhenOriginalExists(t *testing.T) {
	tmp := t.TempDir()
	geoDir := filepath.Join(tmp, geoSubdir)
	if err := os.MkdirAll(geoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	origPath := filepath.Join(geoDir, "geosite.dat")
	backupPath := filepath.Join(geoDir, "geosite.dat.backup.123.456")
	if err := os.WriteFile(origPath, []byte("current-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backupPath, []byte("old-data"), 0o644); err != nil {
		t.Fatal(err)
	}

	_ = NewGeoDataStore(tmp)

	raw, err := os.ReadFile(origPath)
	if err != nil {
		t.Fatalf("read original: %v", err)
	}
	if string(raw) != "current-data" {
		t.Fatalf("original content changed: %q", string(raw))
	}
	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Fatalf("backup artifact still exists: %v", err)
	}
}

// TestGeoIPTagCounts_SumsAcrossFiles pins the budget-validation source: counts
// are summed per tag across every tracked geoip file, keyed lower-case, with
// geosite files ignored and unreadable files skipped.
func TestGeoIPTagCounts_SumsAcrossFiles(t *testing.T) {
	store := newTestGeoStore(t)

	first := filepath.Join(store.geoDir, "geoip-1.dat")
	if err := os.WriteFile(first, buildGeoDAT([][]byte{buildGeoEntry(1, "RU", 2, 2)}), 0o644); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(store.geoDir, "geoip-2.dat")
	if err := os.WriteFile(second, buildGeoDAT([][]byte{
		buildGeoEntry(1, "ru", 2, 3),
		buildGeoEntry(1, "US", 2, 1),
	}), 0o644); err != nil {
		t.Fatal(err)
	}
	site := filepath.Join(store.geoDir, "geosite.dat")
	if err := os.WriteFile(site, buildGeoDAT([][]byte{buildGeoEntry(1, "RU", 2, 7)}), 0o644); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(store.geoDir, "geoip-broken.dat")
	if err := os.WriteFile(broken, []byte("not-a-dat"), 0o644); err != nil {
		t.Fatal(err)
	}

	store.entries = []GeoFileEntry{
		{Type: "geoip", Path: first},
		{Type: "geoip", Path: second},
		{Type: "geosite", Path: site},
		{Type: "geoip", Path: broken},
	}

	counts := store.GeoIPTagCounts()
	if counts["ru"] != 5 {
		t.Fatalf("want ru=5, got %v", counts)
	}
	if counts["us"] != 1 {
		t.Fatalf("want us=1, got %v", counts)
	}
	if len(counts) != 2 {
		t.Fatalf("unexpected tags: %v", counts)
	}
}
