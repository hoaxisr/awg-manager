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

	t.Run("rejects invalid URL, SSRF target, invalid fetchMode, invalid interface, and invalid proxy with 400", func(t *testing.T) {
		payloads := []string{
			`{"url":""}`,
			`{"url":"ftp://example.com"}`,
			`{"url":"http://127.0.0.1:79/rci/"}`,
			`{"url":"http://localhost:2222"}`,
			`{"url":"https://example.com","fetchMode":"socks"}`,
			`{"url":"https://example.com","fetchMode":"interface","interface":"bad iface!"}`,
			`{"url":"https://example.com","interface":"nonexistent_tun9"}`,
			`{"url":"https://example.com","fetchMode":"proxy","proxyUrl":"ftp://127.0.0.1:1080"}`,
			`{"url":"https://example.com","proxyUrl":"http://192.168.1.1:1099"}`,
			`{"url":"https://example.com","proxyUrl":"http://127.0.0.1:79"}`,
			`{"url":"https://example.com","fetchMode":"auto","proxyUrl":"http://8.8.8.8:3128"}`,
			`{"url":"https://example.com","fetchMode":"direct","interface":"opkgtun11"}`,
		}
		for _, p := range payloads {
			req := httptest.NewRequest(http.MethodPost, "/api/diagnostics/page-scan", strings.NewReader(p))
			rec := httptest.NewRecorder()
			h.Post(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("payload %s status = %d, want 400 (%s)", p, rec.Code, rec.Body.String())
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

func TestPageScanHandler_GetOptions(t *testing.T) {
	h := NewPageScanHandler(pagescan.NewService(pagescan.Options{
		ProxyURLProvider: func(context.Context) string { return "http://127.0.0.1:1099" },
		ProxyLister: func(context.Context) []pagescan.ProxyOption {
			return []pagescan.ProxyOption{
				{URL: "http://127.0.0.1:1099", Label: "FIRC inbound (127.0.0.1:1099)"},
				{URL: "socks5://user:pass@127.0.0.1:1080", Label: "SOCKS5 proxy (:1080)"},
			}
		},
		InterfaceLister: func(context.Context) []pagescan.InterfaceOption {
			return []pagescan.InterfaceOption{{Name: "opkgtun11", Label: "VPN DE (opkgtun11)"}}
		},
	}))

	t.Run("rejects POST with 405", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/diagnostics/page-scan/options", nil)
		rec := httptest.NewRecorder()
		h.GetOptions(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want 405", rec.Code)
		}
	})

	t.Run("returns discovered options envelope", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/diagnostics/page-scan/options", nil)
		rec := httptest.NewRecorder()
		h.GetOptions(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
		}
		var env struct {
			Success bool                       `json:"success"`
			Data    pagescan.ScanOptionsResult `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode envelope: %v", err)
		}
		if !env.Success || env.Data.DefaultProxy != "http://127.0.0.1:1099" || env.Data.DefaultProxyLabel != "FIRC inbound (127.0.0.1:1099)" {
			t.Fatalf("unexpected default proxy in options: %+v", env)
		}
		if len(env.Data.Proxies) != 2 || env.Data.Proxies[1].URL != "socks5://127.0.0.1:1080" {
			t.Fatalf("unexpected proxies: %+v", env.Data.Proxies)
		}
		if len(env.Data.Interfaces) != 1 || env.Data.Interfaces[0].Name != "opkgtun11" {
			t.Fatalf("unexpected interfaces: %+v", env.Data.Interfaces)
		}
	})
}
