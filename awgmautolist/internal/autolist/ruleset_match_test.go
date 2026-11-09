package autolist

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestRuleClientMatchRuleSets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/singbox/router/rulesets/match" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}
		var req struct {
			Input string   `json:"input"`
			Tags  []string `json:"tags"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.Input != "example.com" || !reflect.DeepEqual(req.Tags, []string{"known", "unsupported"}) {
			t.Errorf("request body = %+v", req)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":[{"tag":"known","matched":true,"supported":true},{"tag":"unsupported","matched":false,"supported":false,"error":"no sing-box binary"}]}`))
	}))
	defer srv.Close()

	client := NewRuleClient(srv.URL, "test-key")
	got, err := client.MatchRuleSets(context.Background(), "example.com", []string{"known", "unsupported"})
	if err != nil {
		t.Fatalf("MatchRuleSets: %v", err)
	}
	want := []RuleSetMatchResult{
		{Tag: "known", Matched: true, Supported: true},
		{Tag: "unsupported", Supported: false, Error: "no sing-box binary"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MatchRuleSets() = %#v, want %#v", got, want)
	}
}

func TestRuleClientMatchRuleSetsRejectsFailureEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":false,"data":[]}`))
	}))
	defer srv.Close()

	client := NewRuleClient(srv.URL, "")
	if _, err := client.MatchRuleSets(context.Background(), "example.com", []string{"known"}); err == nil {
		t.Fatal("MatchRuleSets() error = nil, want API failure")
	}
}
