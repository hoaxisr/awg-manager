package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/pagescan"
)

func TestPageScanHandler_Post(t *testing.T) {
	h := NewPageScanHandler(pagescan.NewService(pagescan.Options{}))

	t.Run("rejects non-POST with 405 JSON envelope", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/diagnostics/page-scan", nil)
		rec := httptest.NewRecorder()
		h.Post(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want 405", rec.Code)
		}
		var env map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("expected JSON envelope, got %q (%v)", rec.Body.String(), err)
		}
	})

	t.Run("rejects malformed JSON and oversized body with 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/diagnostics/page-scan", strings.NewReader("{bad"))
		rec := httptest.NewRecorder()
		h.Post(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}

		big := strings.Repeat("a", maxPageScanReqBytes+10)
		reqBig := httptest.NewRequest(http.MethodPost, "/api/diagnostics/page-scan", strings.NewReader(big))
		recBig := httptest.NewRecorder()
		h.Post(recBig, reqBig)
		if recBig.Code != http.StatusBadRequest {
			t.Fatalf("oversized body status = %d, want 400", recBig.Code)
		}
	})

	t.Run("rejects invalid URL, SSRF target, and invalid fetchMode with 400", func(t *testing.T) {
		payloads := []string{
			`{"url":""}`,
			`{"url":"ftp://example.com"}`,
			`{"url":"http://127.0.0.1:79/rci/"}`,
			`{"url":"http://localhost:2222"}`,
			`{"url":"https://example.com","fetchMode":"socks"}`,
		}
		for _, p := range payloads {
			req := httptest.NewRequest(http.MethodPost, "/api/diagnostics/page-scan", strings.NewReader(p))
			rec := httptest.NewRecorder()
			h.Post(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("payload %s status = %d, want 400", p, rec.Code)
			}
		}
	})

	t.Run("returns 503 when proxy mode requested without active proxy", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/diagnostics/page-scan", strings.NewReader(`{"url":"https://example.com","fetchMode":"proxy"}`))
		rec := httptest.NewRecorder()
		h.Post(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503 (%s)", rec.Code, rec.Body.String())
		}
		var env struct {
			Error bool   `json:"error"`
			Code  string `json:"code"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode error envelope: %v", err)
		}
		if !env.Error || env.Code != "PROXY_UNAVAILABLE" {
			t.Fatalf("unexpected envelope: %+v", env)
		}
	})

	t.Run("handles canceled upstream fetch with 502", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		req := httptest.NewRequest(http.MethodPost, "/api/diagnostics/page-scan", bytes.NewBufferString(`{"url":"https://example.com","fetchMode":"direct"}`)).WithContext(ctx)
		rec := httptest.NewRecorder()
		h.Post(rec, req)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502 (%s)", rec.Code, rec.Body.String())
		}
	})
}
