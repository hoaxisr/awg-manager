package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/downloader"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/updater"
)

// autoInstallMarkerFile mirrors the unexported constant of the same name in
// internal/updater/autoinstall.go — duplicated here because the test writes
// the marker file directly (from outside the package) to set up a "prior
// attempt" fixture without exporting internals just for tests.
const autoInstallMarkerFile = "update-autoinstall.json"

// failingDownloader always errors, so CheckNow completes instantly without
// touching the network — the force branch only needs to exercise the
// response shape, not a real update check.
type failingDownloader struct{}

func (failingDownloader) ReadAll(ctx context.Context, req downloader.Request) ([]byte, downloader.ResponseMeta, error) {
	return nil, downloader.ResponseMeta{}, errors.New("no network in test")
}

func (failingDownloader) DownloadFile(ctx context.Context, req downloader.FileRequest) (downloader.FileResult, error) {
	return downloader.FileResult{}, errors.New("no network in test")
}

// newUpdateHandlerForTest wires an updater.Service with auto-install enabled,
// so NextAutoInstallAt() returns a non-zero "next" for the handler to
// surface. withMarker additionally stamps a prior auto-install attempt on
// disk, so NextAutoInstallAt() also returns a non-zero "last".
func newUpdateHandlerForTest(t *testing.T, withMarker bool) *UpdateHandler {
	t.Helper()
	dir := t.TempDir()

	settingsStore := storage.NewSettingsStore(dir)
	settings, err := settingsStore.Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	settings.Updates.AutoInstallEnabled = true
	settings.Updates.AutoInstallIntervalDays = 7
	settings.Updates.AutoInstallTime = "05:00"
	if err := settingsStore.Update(func(cur *storage.Settings) error { *cur = *settings; return nil }); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	if withMarker {
		marker := struct {
			LastAttemptAt time.Time `json:"lastAttemptAt"`
			FromVersion   string    `json:"fromVersion"`
			ToVersion     string    `json:"toVersion"`
		}{
			LastAttemptAt: time.Now().Add(-24 * time.Hour),
			FromVersion:   "2.4.0",
			ToVersion:     "2.5.0",
		}
		data, err := json.Marshal(marker)
		if err != nil {
			t.Fatalf("marshal marker: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, autoInstallMarkerFile), data, 0o600); err != nil {
			t.Fatalf("write marker: %v", err)
		}
	}

	svc := updater.New("2.5.0", settingsStore, nil, dir, nil)
	svc.SetDownloader(failingDownloader{})

	return NewUpdateHandler(svc, nil)
}

func decodeCheckResponse(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var body struct {
		Success bool                   `json:"success"`
		Data    map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, w.Body.String())
	}
	if !body.Success {
		t.Fatalf("expected success=true, body=%s", w.Body.String())
	}
	return body.Data
}

// TestUpdateHandler_Check_CachedBranch_IncludesScheduleFields covers task
// #559-4 (MAJOR-2): the cached branch (no ?force) must surface
// nextAutoInstallAt/lastAutoInstallAt even though they are never stored in
// the cached UpdateInfo struct itself.
func TestUpdateHandler_Check_CachedBranch_IncludesScheduleFields(t *testing.T) {
	h := newUpdateHandlerForTest(t, false)

	req := httptest.NewRequest(http.MethodGet, "/api/system/update/check", nil)
	w := httptest.NewRecorder()
	h.Check(w, req)

	data := decodeCheckResponse(t, w)
	if next, ok := data["nextAutoInstallAt"]; !ok || next == "" {
		t.Errorf("expected non-empty nextAutoInstallAt in cached response, got %+v", data)
	}
	if _, ok := data["lastAutoInstallAt"]; ok {
		t.Errorf("expected no lastAutoInstallAt yet (no attempt recorded), got %+v", data)
	}
}

// TestUpdateHandler_Check_ForceBranch_NotWipedByCheckNow is the regression
// the MAJOR-2 finding guards against: CheckNow replaces s.cached wholesale
// (internal/updater/service.go, doCheck/CheckNow), so if the schedule
// fields were ever stored on the cached UpdateInfo struct itself they would
// be wiped by every force refresh. A marker recorded before the call proves
// lastAutoInstallAt still comes through — because it is computed at the
// response layer from NextAutoInstallAt(), independent of s.cached.
func TestUpdateHandler_Check_ForceBranch_NotWipedByCheckNow(t *testing.T) {
	h := newUpdateHandlerForTest(t, true)

	req := httptest.NewRequest(http.MethodGet, "/api/system/update/check?force=true", nil)
	w := httptest.NewRecorder()
	h.Check(w, req)

	data := decodeCheckResponse(t, w)
	if next, ok := data["nextAutoInstallAt"]; !ok || next == "" {
		t.Errorf("expected non-empty nextAutoInstallAt after CheckNow, got %+v", data)
	}
	if last, ok := data["lastAutoInstallAt"]; !ok || last == "" {
		t.Errorf("expected non-empty lastAutoInstallAt after CheckNow (marker was pre-stamped), got %+v", data)
	}
}
