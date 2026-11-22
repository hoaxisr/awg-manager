package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/auth"
	"github.com/hoaxisr/awg-manager/internal/openflux"
)

type openfluxTestAuthChecker struct {
	enabled bool
	apiKey  string
}

func (c *openfluxTestAuthChecker) IsAuthEnabled() bool { return c.enabled }
func (c *openfluxTestAuthChecker) GetApiKey() string   { return c.apiKey }

func fetchOpenFluxCSRF(t *testing.T, mux http.Handler, sessionCookie *http.Cookie) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/openflux/csrf", nil)
	req.Host = "example.com"
	req.Header.Set("Origin", "http://example.com")
	if sessionCookie != nil {
		req.AddCookie(sessionCookie)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/openflux/csrf status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Success bool              `json:"success"`
		Data    map[string]string `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal csrf response: %v", err)
	}
	tok := resp.Data["token"]
	if tok == "" {
		t.Fatalf("empty csrf token: %s", w.Body.String())
	}
	return tok
}

func TestOpenFluxHandler_Routes(t *testing.T) {
	tmpDir := t.TempDir()
	svc := openflux.New(tmpDir, "aarch64", func(ctx context.Context) string {
		return "198.51.100.1"
	})
	sessions := auth.NewSessionStore(nil)
	t.Cleanup(sessions.Stop)
	sessToken, err := sessions.Create("root")
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: auth.SessionCookie, Value: sessToken}

	checker := &openfluxTestAuthChecker{enabled: true}
	mw := auth.NewMiddleware(sessions, checker, nil)

	handler := NewOpenFluxHandler(svc)
	handler.SetSessions(sessions)
	handler.SetAuthChecker(checker)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux, mw.RequireAuthFunc)

	csrfToken := fetchOpenFluxCSRF(t, mux, cookie)

	newPost := func(path string, body []byte) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "http://example.com"+path, bytes.NewReader(body))
		req.Host = "example.com"
		req.Header.Set("Origin", "http://example.com")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("X-CSRF-Token", csrfToken)
		req.AddCookie(cookie)
		return req
	}
	newGet := func(path string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "http://example.com"+path, nil)
		req.Host = "example.com"
		req.AddCookie(cookie)
		return req
	}

	// 1. GET /api/openflux/status
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, newGet("/api/openflux/status"))
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var statusResp struct {
		Success bool            `json:"success"`
		Data    openflux.Status `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &statusResp); err != nil {
		t.Fatalf("unmarshal status response: %v", err)
	}
	if !statusResp.Success || !statusResp.Data.ArchSupported {
		t.Fatalf("unexpected status response: %+v", statusResp)
	}

	// 2. POST /api/openflux/config
	cfg := openflux.Config{
		Enabled:       false,
		Mode:          "l4",
		TransportMode: "session",
		Transports:    []string{"direct"},
		DirectListen:  "0.0.0.0:9999",
		Secret:        "1234567890123456",
		Codec:         "batched",
		LowMemory:     true,
		OpenFirewall:  true,
		Debug:         0,
		Name:          "TestNode",
	}
	cfgBytes, _ := json.Marshal(cfg)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, newPost("/api/openflux/config", cfgBytes))
	if w.Code != http.StatusOK {
		t.Fatalf("expected config 200, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "1234567890123456") || strings.Contains(w.Body.String(), "openflux://v1/") {
		t.Fatalf("SECURITY LEAK: status response after POST /config leaked secret/link: %s", w.Body.String())
	}

	// 3. POST /api/openflux/link (explicit CSRF-protected reveal; GET must return 405)
	wGetLink := httptest.NewRecorder()
	mux.ServeHTTP(wGetLink, newGet("/api/openflux/link"))
	if wGetLink.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 on GET /api/openflux/link, got %d", wGetLink.Code)
	}

	w = httptest.NewRecorder()
	mux.ServeHTTP(w, newPost("/api/openflux/link", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected POST /link 200, got %d: %s", w.Code, w.Body.String())
	}
	var linkResp struct {
		Success bool              `json:"success"`
		Data    map[string]string `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &linkResp); err != nil {
		t.Fatalf("unmarshal link response: %v", err)
	}
	if !linkResp.Success || linkResp.Data["link"] == "" {
		t.Fatalf("expected non-empty link response: %+v", linkResp)
	}

	// 4. GET /api/openflux/logs
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, newGet("/api/openflux/logs?lines=50"))
	if w.Code != http.StatusOK {
		t.Fatalf("expected logs 200, got %d: %s", w.Code, w.Body.String())
	}

	// 5. POST /api/openflux/client/parse-link
	parseBody, _ := json.Marshal(map[string]string{"link": linkResp.Data["link"]})
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, newPost("/api/openflux/client/parse-link", parseBody))
	if w.Code != http.StatusOK {
		t.Fatalf("expected parse-link 200, got %d: %s", w.Code, w.Body.String())
	}

	var parseResp struct {
		Success bool `json:"success"`
		Data    struct {
			Config openflux.ClientConfig `json:"config"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &parseResp); err != nil {
		t.Fatalf("unmarshal parse-link response: %v", err)
	}
	if parseResp.Data.Config.Name != "TestNode" || parseResp.Data.Config.DirectDial != "198.51.100.1:9999" {
		t.Fatalf("unexpected parsed config: %+v", parseResp.Data.Config)
	}

	// 6. POST /api/openflux/client/config
	cliBytes, _ := json.Marshal(parseResp.Data.Config)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, newPost("/api/openflux/client/config", cliBytes))
	if w.Code != http.StatusOK {
		t.Fatalf("expected client/config 200, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "1234567890123456") {
		t.Fatalf("SECURITY LEAK: status response after POST /client/config leaked secret: %s", w.Body.String())
	}

	// 7. GET /api/openflux/client/status
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, newGet("/api/openflux/client/status"))
	if w.Code != http.StatusOK {
		t.Fatalf("expected client/status 200, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "1234567890123456") {
		t.Fatalf("SECURITY LEAK: GET /client/status leaked secret: %s", w.Body.String())
	}

	// 8. GET /api/openflux/client/logs
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, newGet("/api/openflux/client/logs?lines=50"))
	if w.Code != http.StatusOK {
		t.Fatalf("expected client/logs 200, got %d: %s", w.Code, w.Body.String())
	}

	// 9. Wrong HTTP method rejected cleanly with 405
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, newPost("/api/openflux/status", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 on POST /status, got %d", w.Code)
	}
}

func TestOpenFluxHandler_CSRF_AuthEnabledAndBearer(t *testing.T) {
	tmpDir := t.TempDir()
	svc := openflux.New(tmpDir, "aarch64", func(context.Context) string { return "198.51.100.1" })
	seedCfg := openflux.DefaultConfig()
	seedCfg.Transports = []string{"direct"}
	seedCfg.DirectListen = "0.0.0.0:9999"
	seedCfg.Secret = "1234567890123456"
	if err := svc.SaveConfig(context.Background(), seedCfg); err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewSessionStore(nil)
	t.Cleanup(sessions.Stop)
	sessA, _ := sessions.Create("root")
	sessB, _ := sessions.Create("root")

	checker := &openfluxTestAuthChecker{
		enabled: true,
		apiKey:  "test-api-key-123456",
	}
	mw := auth.NewMiddleware(sessions, checker, nil)

	handler := NewOpenFluxHandler(svc)
	handler.SetSessions(sessions)
	handler.SetAuthChecker(checker)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux, mw.RequireAuthFunc)

	csrfA := fetchOpenFluxCSRF(t, mux, &http.Cookie{Name: auth.SessionCookie, Value: sessA})
	csrfB := fetchOpenFluxCSRF(t, mux, &http.Cookie{Name: auth.SessionCookie, Value: sessB})

	mutRoutes := []string{
		"/api/openflux/config",
		"/api/openflux/install",
		"/api/openflux/start",
		"/api/openflux/stop",
		"/api/openflux/restart",
		"/api/openflux/link",
		"/api/openflux/client/config",
		"/api/openflux/client/start",
		"/api/openflux/client/stop",
		"/api/openflux/client/restart",
		"/api/openflux/client/delete",
		"/api/openflux/client/parse-link",
	}

	for _, path := range mutRoutes {
		// 1. Missing Origin and Referer -> 403
		reqNoOrig := httptest.NewRequest(http.MethodPost, "http://example.com"+path, nil)
		reqNoOrig.Host = "example.com"
		reqNoOrig.Header.Set("X-CSRF-Token", csrfA)
		reqNoOrig.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessA})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, reqNoOrig)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s missing Origin/Referer: status=%d, want 403", path, w.Code)
		}

		// 2. Cross-origin Origin -> 403
		reqEvil := httptest.NewRequest(http.MethodPost, "http://example.com"+path, nil)
		reqEvil.Host = "example.com"
		reqEvil.Header.Set("Origin", "http://attacker.invalid")
		reqEvil.Header.Set("X-CSRF-Token", csrfA)
		reqEvil.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessA})
		w = httptest.NewRecorder()
		mux.ServeHTTP(w, reqEvil)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s foreign Origin: status=%d, want 403", path, w.Code)
		}

		// 3. Cross-site Sec-Fetch-Site -> 403
		reqSFS := httptest.NewRequest(http.MethodPost, "http://example.com"+path, nil)
		reqSFS.Host = "example.com"
		reqSFS.Header.Set("Origin", "http://example.com")
		reqSFS.Header.Set("Sec-Fetch-Site", "cross-site")
		reqSFS.Header.Set("X-CSRF-Token", csrfA)
		reqSFS.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessA})
		w = httptest.NewRecorder()
		mux.ServeHTTP(w, reqSFS)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s cross-site Sec-Fetch-Site: status=%d, want 403", path, w.Code)
		}

		// 4. Token bound to another session -> 403
		reqWrongTok := httptest.NewRequest(http.MethodPost, "http://example.com"+path, nil)
		reqWrongTok.Host = "example.com"
		reqWrongTok.Header.Set("Origin", "http://example.com")
		reqWrongTok.Header.Set("X-CSRF-Token", csrfB)
		reqWrongTok.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessA})
		w = httptest.NewRecorder()
		mux.ServeHTTP(w, reqWrongTok)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s wrong session CSRF token: status=%d, want 403", path, w.Code)
		}
	}

	// Valid Bearer API key bypasses CSRF when auth is enabled.
	reqBearer := httptest.NewRequest(http.MethodPost, "http://example.com/api/openflux/link", nil)
	reqBearer.Host = "example.com"
	reqBearer.Header.Set("Authorization", "Bearer test-api-key-123456")
	wBearer := httptest.NewRecorder()
	mux.ServeHTTP(wBearer, reqBearer)
	if wBearer.Code != http.StatusOK {
		t.Fatalf("Bearer /api/openflux/link status=%d, want 200: %s", wBearer.Code, wBearer.Body.String())
	}

	// Cross-origin GET /csrf must fail with 403.
	reqCrossCSRF := httptest.NewRequest(http.MethodGet, "http://example.com/api/openflux/csrf", nil)
	reqCrossCSRF.Host = "example.com"
	reqCrossCSRF.Header.Set("Origin", "http://attacker.invalid")
	reqCrossCSRF.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessA})
	wCrossCSRF := httptest.NewRecorder()
	mux.ServeHTTP(wCrossCSRF, reqCrossCSRF)
	if wCrossCSRF.Code != http.StatusForbidden {
		t.Fatalf("cross-origin GET /csrf status=%d, want 403", wCrossCSRF.Code)
	}
}

func TestOpenFluxHandler_CSRF_AuthDisabled(t *testing.T) {
	tmpDir := t.TempDir()
	svc := openflux.New(tmpDir, "aarch64", func(context.Context) string { return "198.51.100.1" })
	seedCfg := openflux.DefaultConfig()
	seedCfg.Transports = []string{"direct"}
	seedCfg.DirectListen = "0.0.0.0:9999"
	seedCfg.Secret = "1234567890123456"
	if err := svc.SaveConfig(context.Background(), seedCfg); err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewSessionStore(nil)
	t.Cleanup(sessions.Stop)

	// Even with apiKey populated in storage, when IsAuthEnabled() == false Bearer bypass MUST stay off.
	checker := &openfluxTestAuthChecker{
		enabled: false,
		apiKey:  "test-api-key-123456",
	}
	mw := auth.NewMiddleware(sessions, checker, nil)

	handler := NewOpenFluxHandler(svc)
	handler.SetSessions(sessions)
	handler.SetAuthChecker(checker)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux, mw.RequireAuthFunc)

	// Token fetch succeeds without cookie when auth disabled.
	token := fetchOpenFluxCSRF(t, mux, nil)

	// Missing CSRF token or foreign Origin STILL blocked with 403 when auth is disabled.
	reqNoTok := httptest.NewRequest(http.MethodPost, "http://example.com/api/openflux/link", nil)
	reqNoTok.Host = "example.com"
	reqNoTok.Header.Set("Origin", "http://example.com")
	wNoTok := httptest.NewRecorder()
	mux.ServeHTTP(wNoTok, reqNoTok)
	if wNoTok.Code != http.StatusForbidden {
		t.Fatalf("auth-disabled missing CSRF token status=%d, want 403", wNoTok.Code)
	}

	reqForeign := httptest.NewRequest(http.MethodPost, "http://example.com/api/openflux/stop", nil)
	reqForeign.Host = "example.com"
	reqForeign.Header.Set("Origin", "http://attacker.invalid")
	reqForeign.Header.Set("X-CSRF-Token", token)
	wForeign := httptest.NewRecorder()
	mux.ServeHTTP(wForeign, reqForeign)
	if wForeign.Code != http.StatusForbidden {
		t.Fatalf("auth-disabled foreign Origin status=%d, want 403", wForeign.Code)
	}

	reqBearerOff := httptest.NewRequest(http.MethodPost, "http://example.com/api/openflux/link", nil)
	reqBearerOff.Host = "example.com"
	reqBearerOff.Header.Set("Authorization", "Bearer test-api-key-123456")
	wBearerOff := httptest.NewRecorder()
	mux.ServeHTTP(wBearerOff, reqBearerOff)
	if wBearerOff.Code != http.StatusForbidden {
		t.Fatalf("auth-disabled Bearer without CSRF must still fail with 403, got %d", wBearerOff.Code)
	}

	// Same-origin with valid CSRF token succeeds cleanly when auth disabled.
	reqOK := httptest.NewRequest(http.MethodPost, "http://example.com/api/openflux/link", nil)
	reqOK.Host = "example.com"
	reqOK.Header.Set("Origin", "http://example.com")
	reqOK.Header.Set("Sec-Fetch-Site", "same-origin")
	reqOK.Header.Set("X-CSRF-Token", token)
	wOK := httptest.NewRecorder()
	mux.ServeHTTP(wOK, reqOK)
	if wOK.Code != http.StatusOK {
		t.Fatalf("auth-disabled valid CSRF link reveal status=%d, want 200: %s", wOK.Code, wOK.Body.String())
	}
}

func TestOpenFluxHandler_NilService(t *testing.T) {
	handler := NewOpenFluxHandler(nil)
	mux := http.NewServeMux()
	guarded := func(h http.HandlerFunc) http.HandlerFunc { return h }
	handler.RegisterRoutes(mux, guarded)

	req := httptest.NewRequest(http.MethodGet, "/api/openflux/status", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for nil service, got %d", w.Code)
	}
}
