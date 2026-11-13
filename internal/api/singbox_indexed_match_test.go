package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/router"
)

type mockIndexedCoverageSvc struct {
	*mockRouterSvc
	matchInput string
	matchTags  []string
	snapTags   []string
	err        error
}

func (m *mockIndexedCoverageSvc) MatchRuleSetsIndexed(_ context.Context, input string, tags []string) (router.IndexedRuleSetMatch, error) {
	m.matchInput, m.matchTags = input, tags
	return router.IndexedRuleSetMatch{Generation: 42, Results: []router.RuleSetMatchResult{{Tag: "geo", Matched: true, Supported: true}}}, m.err
}

func (m *mockIndexedCoverageSvc) CoverageIndexSnapshot(_ context.Context, tags []string) (router.CoverageSnapshot, error) {
	m.snapTags = tags
	return router.CoverageSnapshot{Generation: 42, Slot: "fakeip", Rules: []router.Rule{}}, m.err
}

func TestIndexedMatchResponseAndValidation(t *testing.T) {
	svc := &mockIndexedCoverageSvc{mockRouterSvc: &mockRouterSvc{}}
	h := NewSingboxRouterHandler(svc, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/router/rulesets/indexed-match", strings.NewReader(`{"input":"example.com","tags":["geo"]}`))
	rr := httptest.NewRecorder()
	h.MatchRuleSetsIndexed(rr, req)
	if rr.Code != http.StatusOK || svc.matchInput != "example.com" || len(svc.matchTags) != 1 {
		t.Fatalf("status=%d input=%q tags=%v body=%s", rr.Code, svc.matchInput, svc.matchTags, rr.Body)
	}
	var body struct {
		Success bool                       `json:"success"`
		Data    router.IndexedRuleSetMatch `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || !body.Success || body.Data.Generation != 42 || len(body.Data.Results) != 1 {
		t.Fatalf("body=%+v err=%v", body, err)
	}

	for _, body := range []string{
		`{"input":"example.com","tags":[]}`,
		`{"input":"example.com","tags":["geo","geo"]}`,
		`{"input":"example.com","tags":[""]}`,
		`{"input":" ","tags":["geo"]}`,
	} {
		req = httptest.NewRequest(http.MethodPost, "/api/singbox/router/rulesets/indexed-match", strings.NewReader(body))
		rr = httptest.NewRecorder()
		h.MatchRuleSetsIndexed(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status=%d response=%s", body, rr.Code, rr.Body)
		}
	}
}

func TestCoverageSnapshotAndErrors(t *testing.T) {
	svc := &mockIndexedCoverageSvc{mockRouterSvc: &mockRouterSvc{}}
	h := NewSingboxRouterHandler(svc, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/router/rulesets/indexed-snapshot", strings.NewReader(`{"tags":["geo"]}`))
	rr := httptest.NewRecorder()
	h.CoverageIndexSnapshot(rr, req)
	if rr.Code != http.StatusOK || len(svc.snapTags) != 1 {
		t.Fatalf("status=%d tags=%v body=%s", rr.Code, svc.snapTags, rr.Body)
	}

	svc.err = errors.New("generation failed")
	rr = httptest.NewRecorder()
	h.MatchRuleSetsIndexed(rr, httptest.NewRequest(http.MethodPost, "/api/singbox/router/rulesets/indexed-match", strings.NewReader(`{"input":"example.com","tags":["geo"]}`)))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("generation error status=%d body=%s", rr.Code, rr.Body)
	}
}
