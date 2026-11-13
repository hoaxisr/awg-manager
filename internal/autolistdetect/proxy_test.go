package autolistdetect

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func proxyTestService(t *testing.T, sat *httptest.Server) *Service {
	t.Helper()
	s := NewService()
	s.statusAddr = strings.TrimPrefix(sat.URL, "http://")
	return s
}

// The whitelist is exactly the satellite's operational surface: no /probe,
// no /exclude, no diagnostics (TECHNICAL_SPEC sections 1 and 8).
func TestAllowedPathsExcludeProbeAndDiagnostics(t *testing.T) {
	for _, path := range []string{"/status", "/config", "/add", "/dismiss", "/forget", "/restore", "/remove", "/history/clear"} {
		if !allowedPaths[path] {
			t.Fatalf("%s must be allowed", path)
		}
	}
	for _, path := range []string{"/probe", "/exclude", "/connections", "/logs", "/inspect", "/rules", "/staging"} {
		if allowedPaths[path] {
			t.Fatalf("%s must not be allowed", path)
		}
	}
}

func TestProxyForwardsGetStatus(t *testing.T) {
	sat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" || r.Method != http.MethodGet {
			t.Errorf("satellite saw %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("credentials must not be forwarded to the loopback satellite")
		}
		_, _ = w.Write([]byte(`{"version":"0.9.0-1"}`))
	}))
	defer sat.Close()

	p := NewProxy(proxyTestService(t, sat))
	req := httptest.NewRequest(http.MethodGet, "/api/autolist/status", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"data":{"version":"0.9.0-1"},"success":true}` {
		t.Fatalf("envelope body = %q", got)
	}
}

// POST bodies pass through untouched; the satellite answers raw.
func TestProxyForwardsPostBody(t *testing.T) {
	var gotPath, gotBody, gotContentType string
	sat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"results":{"example.com":"added"}}`))
	}))
	defer sat.Close()

	p := NewProxy(proxyTestService(t, sat))
	req := httptest.NewRequest(http.MethodPost, "/api/autolist/add",
		bytes.NewReader([]byte(`{"id":"domain|example.com|tcp|443"}`)))
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/add" {
		t.Fatalf("satellite path = %q", gotPath)
	}
	if gotContentType != "application/json" {
		t.Fatalf("content type = %q", gotContentType)
	}
	if gotBody != `{"id":"domain|example.com|tcp|443"}` {
		t.Fatalf("forwarded body = %q", gotBody)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"data":{"results":{"example.com":"added"}},"success":true}` {
		t.Fatalf("missing AWGM envelope: %q", got)
	}
}

func TestProxyForwardsClearHistory(t *testing.T) {
	sat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/history/clear" || r.Method != http.MethodPost {
			t.Errorf("satellite saw %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"cleared":true}`))
	}))
	defer sat.Close()
	rec := httptest.NewRecorder()
	NewProxy(proxyTestService(t, sat)).ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/autolist/history/clear", bytes.NewReader([]byte(`{}`))))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"cleared":true`) {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// Unknown and deliberately removed paths are 404: no probe, no exclude.
func TestProxyRejectsUnknownPaths(t *testing.T) {
	called := false
	sat := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	defer sat.Close()

	p := NewProxy(proxyTestService(t, sat))
	for _, path := range []string{"/probe", "/exclude", "/diagnostics", "/../config", "/config/extra", "", "/"} {
		req := httptest.NewRequest(http.MethodPost, "/api/autolist"+path, bytes.NewReader([]byte(`{}`)))
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%q: code = %d, want 404", path, rec.Code)
		}
	}
	if called {
		t.Fatal("satellite was reached from a rejected path")
	}
}

// Only GET and POST reach the satellite.
func TestProxyRejectsOtherMethods(t *testing.T) {
	sat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("satellite must not be reached")
	}))
	defer sat.Close()

	p := NewProxy(proxyTestService(t, sat))
	for _, method := range []string{http.MethodDelete, http.MethodPut, http.MethodPatch} {
		req := httptest.NewRequest(method, "/api/autolist/config", nil)
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s: code = %d", method, rec.Code)
		}
	}
}

func TestProxyRejectsOversizedBody(t *testing.T) {
	sat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("satellite must not be reached with an oversized body")
	}))
	defer sat.Close()

	p := NewProxy(proxyTestService(t, sat))
	req := httptest.NewRequest(http.MethodPost, "/api/autolist/add", bytes.NewReader(make([]byte, maxRequestBody+1)))
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("code = %d, want 413", rec.Code)
	}
}

func TestProxyRejectsOversizedResponse(t *testing.T) {
	sat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, maxStatusBody+1))
	}))
	defer sat.Close()

	p := NewProxy(proxyTestService(t, sat))
	req := httptest.NewRequest(http.MethodGet, "/api/autolist/status", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502", rec.Code)
	}
}

// A silent satellite becomes 502 with the raw reason, not a fabricated JSON.
func TestProxySatelliteDownIs502(t *testing.T) {
	sat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	svc := NewService()
	svc.statusAddr = "127.0.0.1:1" // nothing listens here
	sat.Close()

	p := NewProxy(svc)
	req := httptest.NewRequest(http.MethodGet, "/api/autolist/status", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "satellite is not answering") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

// Upstream error statuses pass through with their bodies unchanged.
func TestProxyPassesThroughUpstreamErrors(t *testing.T) {
	sat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"unknown candidate"}`, http.StatusNotFound)
	}))
	defer sat.Close()

	p := NewProxy(proxyTestService(t, sat))
	req := httptest.NewRequest(http.MethodPost, "/api/autolist/dismiss", bytes.NewReader([]byte(`{"id":"missing"}`)))
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404 passthrough", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"unknown candidate"}` {
		t.Fatalf("body = %q", got)
	}
}

// /add and /remove run coverage and apply the router config upstream: they
// must not inherit the short status timeout.
func TestProxyCommandPathsUseCommandTimeout(t *testing.T) {
	sat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(20 * time.Millisecond)
		_, _ = w.Write([]byte(`{"result":"ok"}`))
	}))
	defer sat.Close()

	s := proxyTestService(t, sat)
	s.client = &http.Client{Timeout: time.Millisecond}

	for _, path := range []string{"/add", "/remove"} {
		req := httptest.NewRequest(http.MethodPost, "/api/autolist"+path, bytes.NewReader([]byte(`{}`)))
		rec := httptest.NewRecorder()
		NewProxy(s).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s inherited the short status timeout: %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

// /status still honors the short client timeout.
func TestProxyStatusUsesShortTimeout(t *testing.T) {
	sat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer sat.Close()

	s := proxyTestService(t, sat)
	s.client = &http.Client{Timeout: time.Millisecond}

	req := httptest.NewRequest(http.MethodGet, "/api/autolist/status", nil)
	rec := httptest.NewRecorder()
	NewProxy(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502 on timeout", rec.Code)
	}
}

// GET requests must not carry a body upstream.
func TestProxyGetHasNoBody(t *testing.T) {
	sat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if len(b) != 0 {
			t.Errorf("GET carried a body: %q", b)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer sat.Close()

	p := NewProxy(proxyTestService(t, sat))
	req := httptest.NewRequest(http.MethodGet, "/api/autolist/config", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
}
