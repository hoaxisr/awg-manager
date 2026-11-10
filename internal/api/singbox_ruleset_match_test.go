package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/router"
)

func TestMatchRuleSets_ReturnsPerTagResults(t *testing.T) {
	svc := &mockRouterSvc{ruleSetMatchResults: []router.RuleSetMatchResult{
		{Tag: "known", Matched: true, Supported: true},
		{Tag: "unknown", Supported: false, Error: "rule-set is not defined"},
	}}
	h := newMockRouterHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/router/rulesets/match",
		strings.NewReader(`{"input":"example.com","tags":["known","unknown"]}`))
	rr := httptest.NewRecorder()
	h.MatchRuleSets(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	if svc.ruleSetMatchInput != "example.com" || !reflect.DeepEqual(svc.ruleSetMatchTags, []string{"known", "unknown"}) {
		t.Fatalf("service input = %q, tags = %#v", svc.ruleSetMatchInput, svc.ruleSetMatchTags)
	}
	var got SingboxRouterRuleSetMatchResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !got.Success || len(got.Data) != 2 {
		t.Fatalf("response = %+v, want success with two results", got)
	}
	if !got.Data[0].Matched || !got.Data[0].Supported || got.Data[0].Error != "" {
		t.Errorf("known result = %+v", got.Data[0])
	}
	if got.Data[1].Matched || got.Data[1].Supported || got.Data[1].Error == "" {
		t.Errorf("unknown result = %+v, want per-tag unsupported error", got.Data[1])
	}
}

func TestMatchRuleSets_Validation(t *testing.T) {
	tests := []struct {
		name   string
		method string
		body   string
		code   int
	}{
		{name: "method", method: http.MethodGet, code: http.StatusMethodNotAllowed},
		{name: "malformed JSON", method: http.MethodPost, body: `{`, code: http.StatusBadRequest},
		{name: "missing input", method: http.MethodPost, body: `{"tags":["known"]}`, code: http.StatusBadRequest},
		{name: "blank input", method: http.MethodPost, body: `{"input":" ","tags":["known"]}`, code: http.StatusBadRequest},
		{name: "missing tags", method: http.MethodPost, body: `{"input":"example.com"}`, code: http.StatusBadRequest},
		{name: "empty tags", method: http.MethodPost, body: `{"input":"example.com","tags":[]}`, code: http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newMockRouterHandler(&mockRouterSvc{})
			req := httptest.NewRequest(tc.method, "/api/singbox/router/rulesets/match", strings.NewReader(tc.body))
			rr := httptest.NewRecorder()
			h.MatchRuleSets(rr, req)
			if rr.Code != tc.code {
				t.Fatalf("status = %d, want %d (body: %s)", rr.Code, tc.code, rr.Body.String())
			}
		})
	}
}

func TestMatchRuleSets_ServiceErrorFailsRequest(t *testing.T) {
	h := newMockRouterHandler(&mockRouterSvc{ruleSetMatchErr: errors.New("load config")})
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/router/rulesets/match",
		strings.NewReader(`{"input":"example.com","tags":["known"]}`))
	rr := httptest.NewRecorder()
	h.MatchRuleSets(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body: %s)", rr.Code, rr.Body.String())
	}
}
