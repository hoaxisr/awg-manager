package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
func (f *fakeEngine) Recheck(_ context.Context, id string) error {
	f.actions = append(f.actions, "recheck:"+id)
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
	for _, key := range []string{"candidates", "history"} {
		if _, ok := body[key]; ok {
			t.Fatalf("%s must not be part of the operational status: %s", key, recorder.Body.String())
		}
	}
}

func TestCandidateActionsAndRawErrors(t *testing.T) {
	handler, eng := testHandler(t)
	for _, path := range []string{"/add", "/dismiss", "/forget", "/restore", "/recheck"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"id":"candidate"}`)))
		if recorder.Code != 200 {
			t.Fatalf("%s status=%d body=%s", path, recorder.Code, recorder.Body.String())
		}
	}
	if len(eng.actions) != 5 {
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

// listTestHandler builds a handler over a store seeded with n candidates and
// n history entries with distinct timestamps, first-seen/append order
// matching the id order.
func listTestHandler(t *testing.T, n int) (http.Handler, *store.Store) {
	t.Helper()
	eng := &fakeEngine{status: engine.Status{Running: true}}
	state := store.New("")
	base := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	err := state.Update(false, func(s *store.State) {
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("id-%04d", i)
			s.Candidates[id] = model.Candidate{
				ID:       id,
				State:    model.CandidateWaiting,
				FirstAt:  base.Add(time.Duration(i) * time.Second),
				ClientIP: fmt.Sprintf("192.168.1.%d", i%256),
			}
			s.History = append(s.History, model.HistoryEntry{
				At:       base.Add(time.Duration(i) * time.Second),
				Domain:   fmt.Sprintf("host-%04d.example.com", i),
				ClientIP: fmt.Sprintf("192.168.2.%d", i%256),
				Action:   "added",
			})
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	return New(eng, config.NewStore(filepath.Join(t.TempDir(), "config.json")), state, "0.10.0-1"), state
}

func postList(t *testing.T, handler http.Handler, body string) (int, map[string]any) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/list", strings.NewReader(body)))
	var decoded map[string]any
	if len(recorder.Body.Bytes()) > 0 {
		if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("body=%s", recorder.Body.String())
		}
	}
	return recorder.Code, decoded
}

// The unified list endpoint pages both tables by 128 with totals and a
// keyset cursor; pages concatenate into the full seeded order.
func TestListEndpointPagination(t *testing.T) {
	handler, _ := listTestHandler(t, 300)

	code, page1 := postList(t, handler, `{"kind":"candidates"}`)
	if code != http.StatusOK {
		t.Fatalf("code=%d body=%v", code, page1)
	}
	items, _ := page1["items"].([]any)
	if len(items) != 128 || page1["total"] != float64(300) || page1["hasMore"] != true || page1["nextCursor"] == "" {
		t.Fatalf("page1=%v", page1)
	}
	code, page2 := postList(t, handler, `{"kind":"candidates","cursor":"`+page1["nextCursor"].(string)+`"}`)
	if code != http.StatusOK {
		t.Fatalf("code=%d body=%v", code, page2)
	}
	items2, _ := page2["items"].([]any)
	if len(items2) != 128 || page2["hasMore"] != true {
		t.Fatalf("page2=%v", page2)
	}
	code, page3 := postList(t, handler, `{"kind":"candidates","cursor":"`+page2["nextCursor"].(string)+`"}`)
	items3, _ := page3["items"].([]any)
	if len(items3) != 44 || page3["hasMore"] != false || page3["nextCursor"] != "" {
		t.Fatalf("page3=%v", page3)
	}
	first := items[0].(map[string]any)
	if first["id"] != "id-0000" || first["state"] != "waiting" {
		t.Fatalf("first candidate=%v", first)
	}

	code, hist := postList(t, handler, `{"kind":"history"}`)
	if code != http.StatusOK {
		t.Fatalf("code=%d", code)
	}
	hentries, _ := hist["items"].([]any)
	if len(hentries) != 128 || hist["total"] != float64(300) {
		t.Fatalf("history=%v", hist)
	}
	newest := hentries[0].(map[string]any)
	if newest["domain"] != "host-0299.example.com" {
		t.Fatalf("history must be newest first: %v", newest)
	}
}

// state and q filter on the backend: totals reflect the filters and q reaches
// rows beyond the first page.
func TestListEndpointFiltersAndSearch(t *testing.T) {
	handler, state := listTestHandler(t, 300)
	if err := state.Update(false, func(s *store.State) {
		for i := 0; i < 128; i++ {
			delete(s.Candidates, fmt.Sprintf("id-%04d", i))
		}
		candidate := s.Candidates["id-0200"]
		candidate.State = model.CandidateAdded
		s.Candidates[candidate.ID] = candidate
	}); err != nil {
		t.Fatal(err)
	}

	code, added := postList(t, handler, `{"kind":"candidates","state":"added"}`)
	if code != http.StatusOK {
		t.Fatalf("code=%d", code)
	}
	if added["total"] != float64(1) {
		t.Fatalf("added=%v", added)
	}
	code, active := postList(t, handler, `{"kind":"candidates","state":"active"}`)
	if active["total"] != float64(171) { // waiting 170 + none checking
		t.Fatalf("active=%v", active)
	}

	code, found := postList(t, handler, `{"kind":"candidates","q":"192.168.1.200"}`)
	if code != http.StatusOK || found["total"] != float64(1) {
		t.Fatalf("q by clientIP=%v", found)
	}
	code, hist := postList(t, handler, `{"kind":"history","q":"HOST-0299.example.com"}`)
	if code != http.StatusOK || hist["total"] != float64(1) {
		t.Fatalf("history q=%v", hist)
	}
}

func TestListEndpointValidation(t *testing.T) {
	handler, _ := listTestHandler(t, 10)
	for _, body := range []string{
		`{"kind":"unknown"}`,
		`{"kind":"candidates","state":"bogus"}`,
		`{"kind":"candidates","cursor":"!!!garbage"}`,
		`{"kind":"history","cursor":"!!!garbage"}`,
		`{"broken json`,
	} {
		code, body := postList(t, handler, body)
		if code != http.StatusBadRequest {
			t.Fatalf("%s: code=%d want 400", body, code)
		}
	}
}

// limit defaults to and caps at 128 per page.
func TestListEndpointLimitClamp(t *testing.T) {
	handler, _ := listTestHandler(t, 300)
	for _, body := range []string{`{"kind":"candidates","limit":0}`, `{"kind":"candidates","limit":100000}`} {
		_, page := postList(t, handler, body)
		items, _ := page["items"].([]any)
		if len(items) != 128 {
			t.Fatalf("%s: items=%d want 128", body, len(items))
		}
	}
	_, small := postList(t, handler, `{"kind":"candidates","limit":7}`)
	items, _ := small["items"].([]any)
	if len(items) != 7 || small["hasMore"] != true {
		t.Fatalf("small=%v", small)
	}
}

// Candidates added between page fetches never duplicate rows the client
// already received.
func TestListEndpointAppendBetweenPages(t *testing.T) {
	handler, state := listTestHandler(t, 300)
	_, page1 := postList(t, handler, `{"kind":"candidates"}`)
	if err := state.Update(false, func(s *store.State) {
		s.Candidates["id-late"] = model.Candidate{
			ID:      "id-late",
			State:   model.CandidateWaiting,
			FirstAt: time.Date(2026, 11, 27, 14, 0, 0, 0, time.UTC),
		}
	}); err != nil {
		t.Fatal(err)
	}
	_, page2 := postList(t, handler, `{"kind":"candidates","cursor":"`+page1["nextCursor"].(string)+`"}`)
	seen := map[string]bool{}
	for _, page := range []map[string]any{page1, page2} {
		for _, item := range page["items"].([]any) {
			id := item.(map[string]any)["id"].(string)
			if seen[id] {
				t.Fatalf("duplicate id across pages: %s", id)
			}
			seen[id] = true
		}
	}
}

// TestRecheckReturnsUpdatedCandidate verifies the /recheck contract: the
// engine call happens per id, and the response carries the refreshed
// candidate so the UI does not need a follow-up /status read.
func TestRecheckReturnsUpdatedCandidate(t *testing.T) {
	handler, _ := testHandler(t)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/recheck",
		strings.NewReader(`{"id":"domain|example.com|tcp|443"}`)))
	if recorder.Code != 200 {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Results map[string]json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	raw, ok := body.Results["domain|example.com|tcp|443"]
	if !ok {
		t.Fatalf("candidate result missing: %s", recorder.Body.String())
	}
	var cand model.Candidate
	if err := json.Unmarshal(raw, &cand); err != nil {
		t.Fatalf("recheck must return the updated candidate: %v body=%s", err, recorder.Body.String())
	}
	if cand.ID != "domain|example.com|tcp|443" {
		t.Fatalf("candidate id=%q", cand.ID)
	}
}
