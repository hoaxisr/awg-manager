package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestClashProxy_HTTPForwarding verifies that proxyHTTP correctly forwards the
// request path, copies response headers, status code, and body from the upstream.
// We call proxyHTTP directly to avoid the nil *singbox.Operator dependency.
func TestClashProxy_HTTPForwarding(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/proxies" {
			t.Errorf("upstream path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Custom", "passthrough")
		w.Write([]byte(`{"proxies":{}}`))
	}))
	defer upstream.Close()

	addr := strings.TrimPrefix(upstream.URL, "http://")

	p := &ClashProxy{} // proxyHTTP does not access op
	req := httptest.NewRequest(http.MethodGet, "/api/singbox/clash/proxies", nil)
	rec := httptest.NewRecorder()
	p.proxyHTTP(rec, req, addr, "/proxies")

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type not forwarded: %q", ct)
	}
	if cu := rec.Header().Get("X-Custom"); cu != "passthrough" {
		t.Errorf("X-Custom header lost: %q", cu)
	}
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), "proxies") {
		t.Errorf("unexpected body: %s", body)
	}
}

// TestClashProxy_WebSocketRequiresHijacker verifies that proxyWebSocket returns
// 500 when the ResponseWriter does not implement http.Hijacker (e.g. httptest.Recorder).
func TestClashProxy_WebSocketRequiresHijacker(t *testing.T) {
	p := &ClashProxy{}
	req := httptest.NewRequest(http.MethodGet, "/api/singbox/clash/traffic", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	rec := httptest.NewRecorder() // does NOT implement http.Hijacker
	p.proxyWebSocket(rec, req, "127.0.0.1:9999", "/traffic")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 for non-hijacker ResponseWriter, got %d", rec.Code)
	}
}
