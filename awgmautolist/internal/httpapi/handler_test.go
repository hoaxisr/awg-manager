package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/config"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/engine"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/store"
)

type fakeEngine struct {
	status  engine.Status
	actions []string
}

func (f *fakeEngine) Status() engine.Status                          { return f.status }
func (f *fakeEngine) PatchConfig(data []byte) (config.Config, error) { return config.Default(), nil }
func (f *fakeEngine) Add(_ context.Context, id string) error {
	f.actions = append(f.actions, "add:"+id)
	return nil
}
func (f *fakeEngine) Dismiss(id string) error {
	f.actions = append(f.actions, "dismiss:"+id)
	return nil
}
func (f *fakeEngine) Delete(_ context.Context, id string) error {
	f.actions = append(f.actions, "remove:"+id)
	if id == "partial" {
		return errors.New("apply failed after removal")
	}
	return nil
}
func (f *fakeEngine) Forget(id string) error { f.actions = append(f.actions, "forget:"+id); return nil }
func (f *fakeEngine) Restore(id string) error {
	f.actions = append(f.actions, "restore:"+id)
	return nil
}
func (f *fakeEngine) ClearHistory() error {
	f.actions = append(f.actions, "history:clear")
	return nil
}

func testHandler(t *testing.T) (http.Handler, *fakeEngine) {
	t.Helper()
	eng := &fakeEngine{status: engine.Status{Running: true, CaptureRunning: true, Protocols: []string{"tcp/443"}}}
	cfg := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	state := store.New("")
	target, _ := model.NewTarget("example.com", "185.59.211.40", model.NetworkTCP, 443)
	_ = state.Update(false, func(s *store.State) {
		s.Candidates[target.CandidateKey()] = model.Candidate{ID: target.CandidateKey(), Target: target, State: model.CandidateWaiting}
		s.History = append(s.History, model.HistoryEntry{At: time.Now(), Action: "added"})
	})
	return New(eng, cfg, state, "0.10.0-1"), eng
}

func TestOperationalStatus(t *testing.T) {
	handler, _ := testHandler(t)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/status", nil))
	if recorder.Code != 200 {
		t.Fatalf("status=%d", recorder.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["version"] != "0.10.0-1" || body["captureActive"] != true {
		t.Fatalf("body=%s", recorder.Body.String())
	}
	if _, ok := body["candidates"]; !ok {
		t.Fatal("candidates missing")
	}
	if _, ok := body["history"]; !ok {
		t.Fatal("history missing")
	}
}

func TestCandidateActionsAndRawErrors(t *testing.T) {
	handler, eng := testHandler(t)
	for _, path := range []string{"/add", "/dismiss", "/forget", "/restore"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"id":"candidate"}`)))
		if recorder.Code != 200 {
			t.Fatalf("%s status=%d body=%s", path, recorder.Code, recorder.Body.String())
		}
	}
	if len(eng.actions) != 4 {
		t.Fatalf("actions=%v", eng.actions)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/remove", strings.NewReader(`{"id":"partial"}`)))
	var body struct {
		Results map[string]any `json:"results"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Results["partial"] != "apply failed after removal" {
		t.Fatalf("raw error lost: %s", recorder.Body.String())
	}
}

func TestClearHistoryAction(t *testing.T) {
	handler, eng := testHandler(t)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/history/clear", strings.NewReader(`{}`)))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"cleared":true`) {
		t.Fatalf("code=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(eng.actions) != 1 || eng.actions[0] != "history:clear" {
		t.Fatalf("actions=%v", eng.actions)
	}
}

func TestDiagnosticsAreAbsent(t *testing.T) {
	handler, _ := testHandler(t)
	for _, path := range []string{"/probe", "/exclude", "/debug"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d", path, recorder.Code)
		}
	}
}
