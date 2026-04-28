// internal/api/awg_outbounds_test.go
package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/awgoutbounds"
)

type mockAWGSvc struct {
	tags []awgoutbounds.TagInfo
	err  error
}

func (m *mockAWGSvc) ListTags(ctx context.Context) ([]awgoutbounds.TagInfo, error) {
	return m.tags, m.err
}

func TestAWGOutboundsTags_Success(t *testing.T) {
	svc := &mockAWGSvc{tags: []awgoutbounds.TagInfo{
		{Tag: "awg-x", Label: "X", Kind: "managed", Iface: "t2s0"},
	}}
	h := NewAWGOutboundsHandler(svc)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/singbox/awg-outbounds/tags", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	var body []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("body: %v", err)
	}
	if len(body) != 1 || body[0]["tag"] != "awg-x" {
		t.Errorf("body wrong: %v", body)
	}
}

func TestAWGOutboundsTags_Empty(t *testing.T) {
	svc := &mockAWGSvc{tags: nil}
	h := NewAWGOutboundsHandler(svc)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/singbox/awg-outbounds/tags", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	body := strings.TrimSpace(rr.Body.String())
	if body != "[]" {
		t.Errorf("expected empty array, got %q", body)
	}
}

func TestAWGOutboundsTags_MethodNotAllowed(t *testing.T) {
	svc := &mockAWGSvc{}
	h := NewAWGOutboundsHandler(svc)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/singbox/awg-outbounds/tags", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != 405 {
		t.Errorf("want 405, got %d", rr.Code)
	}
}
