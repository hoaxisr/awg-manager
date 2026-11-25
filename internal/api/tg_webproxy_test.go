package api

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/auth"
	"github.com/hoaxisr/awg-manager/internal/serveringress"
	"github.com/hoaxisr/awg-manager/internal/testutil"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy"
)

func setupTestTgWebProxyHandler(t *testing.T) (*TgWebProxyHandler, *tgwebproxy.Service, *auth.SessionStore, string, string) {
	t.Helper()
	tempDir := t.TempDir()
	sessionStore := auth.NewSessionStore(nil)
	sessionToken, err := sessionStore.Create("root")
	if err != nil {
		t.Fatalf("create session failed: %v", err)
	}

	svc := tgwebproxy.New(tempDir, nil)
	svc.SetRuntimeDCProbeForTest(func(string) bool { return true })
	handler := NewTgWebProxyHandler(svc, sessionStore)
	t.Cleanup(func() {
		sessionStore.Stop()
		_ = svc.Close()
	})
	return handler, svc, sessionStore, sessionToken, tempDir
}

func getValidCSRFToken(t *testing.T, handler *TgWebProxyHandler, sessionToken string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://192.168.10.1:2222/api/servers/tgwebproxy/csrf", nil)
	req.Host = "192.168.10.1:2222"
	req.Header.Set("Origin", "http://192.168.10.1:2222")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessionToken})

	w := httptest.NewRecorder()
	handler.GetCSRFToken(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("failed to get CSRF token, code %d: %s", w.Code, w.Body.String())
	}

	var res struct {
		Success bool `json:"success"`
		Data    struct {
			CSRFToken string `json:"csrf_token"`
		} `json:"data"`
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal csrf token response: %v", err)
	}
	token := res.CSRFToken
	if token == "" {
		token = res.Data.CSRFToken
	}
	if token == "" {
		t.Fatalf("empty csrf token returned: %s", w.Body.String())
	}
	return token
}

func TestTgWebProxy_CSRF_GetToken(t *testing.T) {
	handler, _, sessionStore, sessionToken, _ := setupTestTgWebProxyHandler(t)

	// 1. POST method to /csrf should be rejected with 405 Method Not Allowed
	reqPost := httptest.NewRequest(http.MethodPost, "http://192.168.10.1:2222/api/servers/tgwebproxy/csrf", nil)
	wPost := httptest.NewRecorder()
	handler.GetCSRFToken(wPost, reqPost)
	if wPost.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 Method Not Allowed for POST /csrf, got %d", wPost.Code)
	}

	// 2. GET without session cookie should be rejected with 401 Unauthorized
	reqNoCookie := httptest.NewRequest(http.MethodGet, "http://192.168.10.1:2222/api/servers/tgwebproxy/csrf", nil)
	reqNoCookie.Host = "192.168.10.1:2222"
	reqNoCookie.Header.Set("Origin", "http://192.168.10.1:2222")
	wNoCookie := httptest.NewRecorder()
	handler.GetCSRFToken(wNoCookie, reqNoCookie)
	if wNoCookie.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized without session cookie, got %d", wNoCookie.Code)
	}

	// 3. GET with invalid/expired session cookie should be rejected with 401
	reqBadCookie := httptest.NewRequest(http.MethodGet, "http://192.168.10.1:2222/api/servers/tgwebproxy/csrf", nil)
	reqBadCookie.Host = "192.168.10.1:2222"
	reqBadCookie.Header.Set("Origin", "http://192.168.10.1:2222")
	reqBadCookie.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: "expired_or_invalid_session"})
	wBadCookie := httptest.NewRecorder()
	handler.GetCSRFToken(wBadCookie, reqBadCookie)
	if wBadCookie.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized for bad session, got %d", wBadCookie.Code)
	}

	// 4. GET with foreign Origin should be rejected with 403 Forbidden
	reqBadOrigin := httptest.NewRequest(http.MethodGet, "http://192.168.10.1:2222/api/servers/tgwebproxy/csrf", nil)
	reqBadOrigin.Host = "192.168.10.1:2222"
	reqBadOrigin.Header.Set("Origin", "http://evil-attacker.com")
	reqBadOrigin.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessionToken})
	wBadOrigin := httptest.NewRecorder()
	handler.GetCSRFToken(wBadOrigin, reqBadOrigin)
	if wBadOrigin.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for foreign Origin, got %d", wBadOrigin.Code)
	}

	// 5. GET with valid session returns 200, cache headers, and token
	reqValid := httptest.NewRequest(http.MethodGet, "http://192.168.10.1:2222/api/servers/tgwebproxy/csrf", nil)
	reqValid.Host = "192.168.10.1:2222"
	reqValid.Header.Set("Origin", "http://192.168.10.1:2222")
	reqValid.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessionToken})
	wValid := httptest.NewRecorder()
	handler.GetCSRFToken(wValid, reqValid)

	if wValid.Code != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", wValid.Code)
	}
	if cc := wValid.Header().Get("Cache-Control"); cc != "no-store, private" {
		t.Errorf("expected Cache-Control 'no-store, private', got %q", cc)
	}
	if pr := wValid.Header().Get("Pragma"); pr != "no-cache" {
		t.Errorf("expected Pragma 'no-cache', got %q", pr)
	}

	var res struct {
		Success bool `json:"success"`
		Data    struct {
			CSRFToken string `json:"csrf_token"`
		} `json:"data"`
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(wValid.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	token := res.CSRFToken
	if token == "" {
		token = res.Data.CSRFToken
	}
	if len(token) != 64 {
		t.Errorf("expected 64-char hex CSRF token, got %q", token)
	}

	_ = sessionStore
}

func TestTgWebProxy_CSRF_StrictRejectionOfStaticToken(t *testing.T) {
	handler, _, _, sessionToken, _ := setupTestTgWebProxyHandler(t)

	// Sending legacy static token "awgm-tgwebproxy" MUST BE STRICTLY REJECTED WITH 403
	reqBody, _ := json.Marshal(map[string]interface{}{"public_hostname": "test.com"})
	req := httptest.NewRequest(http.MethodPut, "http://192.168.10.1:2222/api/servers/tgwebproxy", bytes.NewReader(reqBody))
	req.Host = "192.168.10.1:2222"
	req.Header.Set("Origin", "http://192.168.10.1:2222")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessionToken})
	req.Header.Set("X-CSRF-Token", "awgm-tgwebproxy")

	w := httptest.NewRecorder()
	handler.UpdateConfig(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for static 'awgm-tgwebproxy' token, got %d", w.Code)
	}
}

func TestTgWebProxy_CSRF_CrossSessionRejection(t *testing.T) {
	handler, _, sessionStore, sessionTokenA, _ := setupTestTgWebProxyHandler(t)

	sessionTokenB, err := sessionStore.Create("admin2")
	if err != nil {
		t.Fatalf("create second session: %v", err)
	}

	tokenA := getValidCSRFToken(t, handler, sessionTokenA)

	// Sending token derived for session A together with session B cookie MUST BE REJECTED
	reqBody, _ := json.Marshal(map[string]interface{}{"public_hostname": "test.com"})
	req := httptest.NewRequest(http.MethodPut, "http://192.168.10.1:2222/api/servers/tgwebproxy", bytes.NewReader(reqBody))
	req.Host = "192.168.10.1:2222"
	req.Header.Set("Origin", "http://192.168.10.1:2222")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessionTokenB})
	req.Header.Set("X-CSRF-Token", tokenA)

	w := httptest.NewRecorder()
	handler.UpdateConfig(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden when using token from another session, got %d", w.Code)
	}
}

func TestTgWebProxy_CSRF_MissingOriginAndReferer(t *testing.T) {
	handler, _, _, sessionToken, _ := setupTestTgWebProxyHandler(t)
	token := getValidCSRFToken(t, handler, sessionToken)

	reqBody, _ := json.Marshal(map[string]interface{}{"public_hostname": "test.com"})
	req := httptest.NewRequest(http.MethodPut, "http://192.168.10.1:2222/api/servers/tgwebproxy", bytes.NewReader(reqBody))
	req.Host = "192.168.10.1:2222"
	// Neither Origin nor Referer provided
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessionToken})
	req.Header.Set("X-CSRF-Token", token)

	w := httptest.NewRecorder()
	handler.UpdateConfig(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden when neither Origin nor Referer is provided, got %d", w.Code)
	}
}

func TestTgWebProxy_MethodEnforcement(t *testing.T) {
	handler, _, _, sessionToken, _ := setupTestTgWebProxyHandler(t)
	token := getValidCSRFToken(t, handler, sessionToken)

	// 1. UpdateConfig requires PUT (rejects POST and GET)
	reqPost := httptest.NewRequest(http.MethodPost, "http://192.168.10.1:2222/api/servers/tgwebproxy", nil)
	reqPost.Host = "192.168.10.1:2222"
	reqPost.Header.Set("Origin", "http://192.168.10.1:2222")
	reqPost.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessionToken})
	reqPost.Header.Set("X-CSRF-Token", token)
	wPost := httptest.NewRecorder()
	handler.UpdateConfig(wPost, reqPost)
	if wPost.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 Method Not Allowed for POST to UpdateConfig, got %d", wPost.Code)
	}

	// 2. RevealSecret requires POST (rejects GET)
	reqRevealGet := httptest.NewRequest(http.MethodGet, "http://192.168.10.1:2222/api/servers/tgwebproxy/reveal", nil)
	reqRevealGet.Host = "192.168.10.1:2222"
	reqRevealGet.Header.Set("Origin", "http://192.168.10.1:2222")
	reqRevealGet.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessionToken})
	reqRevealGet.Header.Set("X-CSRF-Token", token)
	wRevealGet := httptest.NewRecorder()
	handler.RevealSecret(wRevealGet, reqRevealGet)
	if wRevealGet.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 Method Not Allowed for GET to RevealSecret, got %d", wRevealGet.Code)
	}

	// 3. Action requires POST (rejects GET)
	reqActionGet := httptest.NewRequest(http.MethodGet, "http://192.168.10.1:2222/api/servers/tgwebproxy/action", nil)
	reqActionGet.Host = "192.168.10.1:2222"
	reqActionGet.Header.Set("Origin", "http://192.168.10.1:2222")
	reqActionGet.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessionToken})
	reqActionGet.Header.Set("X-CSRF-Token", token)
	wActionGet := httptest.NewRecorder()
	handler.Action(wActionGet, reqActionGet)
	if wActionGet.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 Method Not Allowed for GET to Action, got %d", wActionGet.Code)
	}

	// 4. GetStatus requires GET (rejects POST)
	reqStatusPost := httptest.NewRequest(http.MethodPost, "http://192.168.10.1:2222/api/servers/tgwebproxy/status", nil)
	wStatusPost := httptest.NewRecorder()
	handler.GetStatus(wStatusPost, reqStatusPost)
	if wStatusPost.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 Method Not Allowed for POST to GetStatus, got %d", wStatusPost.Code)
	}
}

func TestTgWebProxy_RateLimit_SessionKeyed(t *testing.T) {
	handler, _, _, sessionToken, _ := setupTestTgWebProxyHandler(t)
	token := getValidCSRFToken(t, handler, sessionToken)

	req := httptest.NewRequest(http.MethodPost, "http://192.168.10.1:2222/api/servers/tgwebproxy/reveal", nil)
	req.Host = "192.168.10.1:2222"
	req.Header.Set("Origin", "http://192.168.10.1:2222")
	req.Header.Set("X-CSRF-Token", token)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessionToken})

	// First reveal request
	w1 := httptest.NewRecorder()
	handler.RevealSecret(w1, req)
	if w1.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on first reveal, got %d: %s", w1.Code, w1.Body.String())
	}

	// Second immediate reveal request with the same session should be rate-limited (HTTP 429)
	w2 := httptest.NewRecorder()
	handler.RevealSecret(w2, req)

	if w2.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429 Too Many Requests on immediate second reveal, got %d", w2.Code)
	}

	// Advance time past rate limit (5 seconds)
	handler.revealMu.Lock()
	for k := range handler.revealLimits {
		handler.revealLimits[k] = time.Now().Add(-6 * time.Second)
	}
	handler.revealMu.Unlock()

	w3 := httptest.NewRecorder()
	handler.RevealSecret(w3, req)
	if w3.Code == http.StatusTooManyRequests {
		t.Errorf("expected rate limit to expire after delay, got 429")
	}
}

func newRevealRequest(t *testing.T, handler *TgWebProxyHandler, sessionToken, userID string) *http.Request {
	t.Helper()
	token := getValidCSRFToken(t, handler, sessionToken)
	target := "http://192.168.10.1:2222/api/servers/tgwebproxy/reveal"
	if userID != "" {
		target += "?user_id=" + userID
	}
	req := httptest.NewRequest(http.MethodPost, target, nil)
	req.Host = "192.168.10.1:2222"
	req.Header.Set("Origin", "http://192.168.10.1:2222")
	req.Header.Set("X-CSRF-Token", token)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessionToken})
	return req
}

// An explicit user_id used to skip rate limiting entirely; it now shares the
// bucket of the user it resolves to, including the empty (default) reveal.
func TestTgWebProxy_RevealRateLimit_AppliesToExplicitUserID(t *testing.T) {
	handler, _, _, sessionToken, _ := setupTestTgWebProxyHandler(t)

	w1 := httptest.NewRecorder()
	handler.RevealSecret(w1, newRevealRequest(t, handler, sessionToken, "default"))
	if w1.Code != http.StatusOK {
		t.Fatalf("first reveal: got %d: %s", w1.Code, w1.Body.String())
	}
	if cc := w1.Header().Get("Cache-Control"); cc == "" || !bytes.Contains([]byte(cc), []byte("no-store")) {
		t.Errorf("reveal Cache-Control = %q, want no-store", cc)
	}

	w2 := httptest.NewRecorder()
	handler.RevealSecret(w2, newRevealRequest(t, handler, sessionToken, "default"))
	if w2.Code != http.StatusTooManyRequests {
		t.Errorf("immediate repeat with explicit user_id: got %d, want 429", w2.Code)
	}
	if bytes.Contains(w2.Body.Bytes(), []byte(`"secret"`)) || bytes.Contains(w2.Body.Bytes(), []byte("tg://")) {
		t.Errorf("rate-limited response carries secret material: %s", w2.Body.String())
	}

	w3 := httptest.NewRecorder()
	handler.RevealSecret(w3, newRevealRequest(t, handler, sessionToken, ""))
	if w3.Code != http.StatusTooManyRequests {
		t.Errorf("empty user_id resolving to same user: got %d, want 429", w3.Code)
	}
}

func TestTgWebProxy_RevealUnknownUser_NotFoundWithoutSecret(t *testing.T) {
	handler, svc, _, sessionToken, _ := setupTestTgWebProxyHandler(t)
	cfg := svc.GetConfig()
	if len(cfg.Users) == 0 {
		t.Fatalf("expected default user in fresh config")
	}

	w := httptest.NewRecorder()
	handler.RevealSecret(w, newRevealRequest(t, handler, sessionToken, "no-such-user"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown user: got %d, want 404: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, marker := range []string{"tg://", "t.me/", "bridge=", `"secret"`} {
		if bytes.Contains([]byte(body), []byte(marker)) {
			t.Errorf("404 body contains %q: %s", marker, body)
		}
	}

	// A failed lookup does not consume the real user's reveal budget.
	wOK := httptest.NewRecorder()
	handler.RevealSecret(wOK, newRevealRequest(t, handler, sessionToken, cfg.Users[0].ID))
	if wOK.Code != http.StatusOK {
		t.Errorf("reveal after 404: got %d, want 200", wOK.Code)
	}
}

func TestTgWebProxy_CSRF_NilSessionStore_FailClosed(t *testing.T) {
	tempDir := t.TempDir()
	svc := tgwebproxy.New(tempDir, nil)
	svc.SetRuntimeDCProbeForTest(func(string) bool { return true })
	t.Cleanup(func() { _ = svc.Close() })
	// Handler constructed with nil sessionStore
	handler := NewTgWebProxyHandler(svc, nil)

	// 1. GetCSRFToken should fail-closed with 503 Service Unavailable
	req := httptest.NewRequest(http.MethodGet, "http://192.168.10.1:2222/api/servers/tgwebproxy/csrf", nil)
	req.Host = "192.168.10.1:2222"
	req.Header.Set("Origin", "http://192.168.10.1:2222")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: "some_session"})

	w := httptest.NewRecorder()
	handler.GetCSRFToken(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 Service Unavailable when sessionStore is nil, got %d", w.Code)
	}

	// 2. checkCSRF should fail-closed and reject mutating endpoint
	mutReq := httptest.NewRequest(http.MethodPost, "http://192.168.10.1:2222/api/servers/tgwebproxy/reveal", nil)
	mutReq.Host = "192.168.10.1:2222"
	mutReq.Header.Set("Origin", "http://192.168.10.1:2222")
	mutReq.Header.Set("X-CSRF-Token", "some_token")
	mutReq.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: "some_session"})

	wMut := httptest.NewRecorder()
	handler.RevealSecret(wMut, mutReq)
	if wMut.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden on mutating action when sessionStore is nil, got %d", wMut.Code)
	}
}

func TestTgWebProxy_GetCSRF_MissingOriginAndReferer(t *testing.T) {
	handler, _, _, sessionToken, _ := setupTestTgWebProxyHandler(t)

	// Request with valid session cookie but neither Origin nor Referer
	req := httptest.NewRequest(http.MethodGet, "http://192.168.10.1:2222/api/servers/tgwebproxy/csrf", nil)
	req.Host = "192.168.10.1:2222"
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessionToken})

	w := httptest.NewRecorder()
	handler.GetCSRFToken(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden when Origin and Referer are both missing on /csrf, got %d", w.Code)
	}
}

func TestTgWebProxy_GetCSRF_MismatchedPort(t *testing.T) {
	handler, _, _, sessionToken, _ := setupTestTgWebProxyHandler(t)

	// Origin specifies port 8080 while Host is 2222
	req := httptest.NewRequest(http.MethodGet, "http://192.168.10.1:2222/api/servers/tgwebproxy/csrf", nil)
	req.Host = "192.168.10.1:2222"
	req.Header.Set("Origin", "http://192.168.10.1:8080")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessionToken})

	w := httptest.NewRecorder()
	handler.GetCSRFToken(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden when Origin port does not match Host port, got %d", w.Code)
	}
}

func TestTgWebProxy_CheckCSRF_MutatingMissingOriginAndReferer(t *testing.T) {
	handler, _, _, sessionToken, _ := setupTestTgWebProxyHandler(t)
	token := getValidCSRFToken(t, handler, sessionToken)

	// Valid token and session, but missing Origin and Referer
	req := httptest.NewRequest(http.MethodPost, "http://192.168.10.1:2222/api/servers/tgwebproxy/reveal", nil)
	req.Host = "192.168.10.1:2222"
	req.Header.Set("X-CSRF-Token", token)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessionToken})

	w := httptest.NewRecorder()
	handler.RevealSecret(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden on mutating endpoint when Origin and Referer are missing, got %d", w.Code)
	}
}

func TestTgWebProxy_RapidStopStartKeepsEnabled(t *testing.T) {
	handler, svc, _, sessionToken, tempDir := setupTestTgWebProxyHandler(t)
	coord := serveringress.New(tempDir, nil, nil, svc)
	handler.SetCoordinator(coord)

	directPort := testutil.FreePort(t)
	listenPort := testutil.FreePort(t)
	epListenPort := testutil.FreePort(t)
	epAdminPort := testutil.FreePort(t)
	rawPort := testutil.FreePort(t)

	initialCfg := svc.GetInternalConfig()
	initialCfg.Enabled = true
	initialCfg.Scenario = tgwebproxy.ScenarioDirectFakeTLS
	initialCfg.DirectHost = "127.0.0.1"
	initialCfg.DirectPort = directPort
	initialCfg.ListenPort = listenPort
	initialCfg.AdminPort = epAdminPort
	initialCfg.Backend = (&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: rawPort}).String()
	initialCfg.Endpoints = []tgwebproxy.WebProxyEndpoint{{
		ID:          "ep-1",
		Hostname:    "tg.example.com",
		ListenPort:  epListenPort,
		AdminPort:   epAdminPort,
		IsCDN:       true,
		CarrierMode: tgwebproxy.DefaultCarrierMode,
	}}
	if err := coord.ApplyTelegramConfig(context.Background(), initialCfg); err != nil {
		t.Fatalf("initial ApplyTelegramConfig: %v", err)
	}

	t.Cleanup(func() {
		_ = coord.SetTelegramEnabled(context.Background(), false)
	})

	token := getValidCSRFToken(t, handler, sessionToken)
	doAction := func(act string) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"action": act})
		req := httptest.NewRequest(http.MethodPost, "http://192.168.10.1:2222/api/servers/tgwebproxy/action", bytes.NewReader(body))
		req.Host = "192.168.10.1:2222"
		req.Header.Set("Origin", "http://192.168.10.1:2222")
		req.Header.Set("X-CSRF-Token", token)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessionToken})
		w := httptest.NewRecorder()
		handler.Action(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("action %q failed (%d): %s", act, w.Code, w.Body.String())
		}
	}

	delays := []time.Duration{0, 20 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond}
	for _, d := range delays {
		doAction("stop")
		if d > 0 {
			time.Sleep(d)
		}
		doAction("start")
		if cfg := svc.GetInternalConfig(); !cfg.Enabled {
			t.Fatalf("enabled=false after rapid stop->start (delay=%v)", d)
		}
		if st := svc.GetStatus(); !st.Running || !st.DirectOnline {
			t.Fatalf("expected running and direct_online after rapid stop->start (delay=%v), got %+v", d, st)
		}
	}
}

func TestTgWebProxy_CSRF_AuthDisabled(t *testing.T) {
	handler, _, _, sessionToken, _ := setupTestTgWebProxyHandler(t)
	checker := &fakeCSRFAuthChecker{enabled: false}
	handler.SetAuthChecker(checker)

	decodeCSRF := func(t *testing.T, body []byte) string {
		t.Helper()
		var res struct {
			Success bool `json:"success"`
			Data    struct {
				CSRFToken string `json:"csrf_token"`
			} `json:"data"`
			CSRFToken string `json:"csrf_token"`
		}
		if err := json.Unmarshal(body, &res); err != nil {
			t.Fatalf("unmarshal csrf response: %v (%s)", err, string(body))
		}
		tok := res.CSRFToken
		if tok == "" {
			tok = res.Data.CSRFToken
		}
		return tok
	}

	// 1. GetCSRFToken with valid Origin and NO cookie returns 200 and 64-char hex token.
	reqCSRF := httptest.NewRequest(http.MethodGet, "http://192.168.10.1:2222/api/servers/tgwebproxy/csrf", nil)
	reqCSRF.Host = "192.168.10.1:2222"
	reqCSRF.Header.Set("Origin", "http://192.168.10.1:2222")
	wCSRF := httptest.NewRecorder()
	handler.GetCSRFToken(wCSRF, reqCSRF)
	if wCSRF.Code != http.StatusOK {
		t.Fatalf("expected 200 on GetCSRFToken without cookie when auth disabled, got %d: %s", wCSRF.Code, wCSRF.Body.String())
	}
	token := decodeCSRF(t, wCSRF.Body.Bytes())
	if len(token) != 64 {
		t.Fatalf("expected 64-char hex token, got len=%d (%q)", len(token), token)
	}
	if _, err := hex.DecodeString(token); err != nil {
		t.Fatalf("expected valid hex token, decode error: %v", err)
	}

	// 2. GetCSRFToken with valid Origin and a stale/invalid cookie also returns 200.
	reqStale := httptest.NewRequest(http.MethodGet, "http://192.168.10.1:2222/api/servers/tgwebproxy/csrf", nil)
	reqStale.Host = "192.168.10.1:2222"
	reqStale.Header.Set("Origin", "http://192.168.10.1:2222")
	reqStale.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: "stale-expired-session"})
	wStale := httptest.NewRecorder()
	handler.GetCSRFToken(wStale, reqStale)
	if wStale.Code != http.StatusOK {
		t.Fatalf("expected 200 on GetCSRFToken with stale cookie when auth disabled, got %d: %s", wStale.Code, wStale.Body.String())
	}

	// 3. GetCSRFToken with missing Origin/Referer returns 403; foreign Origin returns 403.
	reqNoOrigin := httptest.NewRequest(http.MethodGet, "http://192.168.10.1:2222/api/servers/tgwebproxy/csrf", nil)
	reqNoOrigin.Host = "192.168.10.1:2222"
	wNoOrigin := httptest.NewRecorder()
	handler.GetCSRFToken(wNoOrigin, reqNoOrigin)
	if wNoOrigin.Code != http.StatusForbidden {
		t.Errorf("expected 403 on GetCSRFToken without Origin/Referer, got %d", wNoOrigin.Code)
	}

	reqForeign := httptest.NewRequest(http.MethodGet, "http://192.168.10.1:2222/api/servers/tgwebproxy/csrf", nil)
	reqForeign.Host = "192.168.10.1:2222"
	reqForeign.Header.Set("Origin", "http://evil-attacker.com")
	wForeign := httptest.NewRecorder()
	handler.GetCSRFToken(wForeign, reqForeign)
	if wForeign.Code != http.StatusForbidden {
		t.Errorf("expected 403 on GetCSRFToken with foreign Origin, got %d", wForeign.Code)
	}

	// 4. Negative CSRF checks on RevealSecret reject with 403 when auth is disabled.
	reqMutNoToken := httptest.NewRequest(http.MethodPost, "http://192.168.10.1:2222/api/servers/tgwebproxy/reveal", nil)
	reqMutNoToken.Host = "192.168.10.1:2222"
	reqMutNoToken.Header.Set("Origin", "http://192.168.10.1:2222")
	wMutNoToken := httptest.NewRecorder()
	handler.RevealSecret(wMutNoToken, reqMutNoToken)
	if wMutNoToken.Code != http.StatusForbidden {
		t.Errorf("expected 403 on RevealSecret without X-CSRF-Token, got %d", wMutNoToken.Code)
	}

	reqMutWrongToken := httptest.NewRequest(http.MethodPost, "http://192.168.10.1:2222/api/servers/tgwebproxy/reveal", nil)
	reqMutWrongToken.Host = "192.168.10.1:2222"
	reqMutWrongToken.Header.Set("Origin", "http://192.168.10.1:2222")
	reqMutWrongToken.Header.Set("X-CSRF-Token", "deadbeef")
	wMutWrongToken := httptest.NewRecorder()
	handler.RevealSecret(wMutWrongToken, reqMutWrongToken)
	if wMutWrongToken.Code != http.StatusForbidden {
		t.Errorf("expected 403 on RevealSecret with wrong X-CSRF-Token, got %d", wMutWrongToken.Code)
	}

	reqMutForeign := httptest.NewRequest(http.MethodPost, "http://192.168.10.1:2222/api/servers/tgwebproxy/reveal", nil)
	reqMutForeign.Host = "192.168.10.1:2222"
	reqMutForeign.Header.Set("Origin", "http://evil-attacker.com")
	reqMutForeign.Header.Set("X-CSRF-Token", token)
	wMutForeign := httptest.NewRecorder()
	handler.RevealSecret(wMutForeign, reqMutForeign)
	if wMutForeign.Code != http.StatusForbidden {
		t.Errorf("expected 403 on RevealSecret with foreign Origin, got %d", wMutForeign.Code)
	}

	// 5. RevealSecret with valid Origin + returned X-CSRF-Token and NO cookie returns 200.
	reqMutOK := httptest.NewRequest(http.MethodPost, "http://192.168.10.1:2222/api/servers/tgwebproxy/reveal", nil)
	reqMutOK.Host = "192.168.10.1:2222"
	reqMutOK.Header.Set("Origin", "http://192.168.10.1:2222")
	reqMutOK.Header.Set("X-CSRF-Token", token)
	wMutOK := httptest.NewRecorder()
	handler.RevealSecret(wMutOK, reqMutOK)
	if wMutOK.Code != http.StatusOK {
		t.Fatalf("expected 200 on RevealSecret without cookie when auth disabled, got %d: %s", wMutOK.Code, wMutOK.Body.String())
	}

	// 6. Flip auth enabled back to true: require session cookie and reject auth-disabled token.
	checker.enabled = true

	reqNoCookieAuthOn := httptest.NewRequest(http.MethodGet, "http://192.168.10.1:2222/api/servers/tgwebproxy/csrf", nil)
	reqNoCookieAuthOn.Host = "192.168.10.1:2222"
	reqNoCookieAuthOn.Header.Set("Origin", "http://192.168.10.1:2222")
	wNoCookieAuthOn := httptest.NewRecorder()
	handler.GetCSRFToken(wNoCookieAuthOn, reqNoCookieAuthOn)
	if wNoCookieAuthOn.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 on GetCSRFToken without cookie when auth enabled, got %d", wNoCookieAuthOn.Code)
	}

	reqOldTokAuthOn := httptest.NewRequest(http.MethodPost, "http://192.168.10.1:2222/api/servers/tgwebproxy/reveal", nil)
	reqOldTokAuthOn.Host = "192.168.10.1:2222"
	reqOldTokAuthOn.Header.Set("Origin", "http://192.168.10.1:2222")
	reqOldTokAuthOn.Header.Set("X-CSRF-Token", token)
	reqOldTokAuthOn.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessionToken})
	wOldTokAuthOn := httptest.NewRecorder()
	handler.RevealSecret(wOldTokAuthOn, reqOldTokAuthOn)
	if wOldTokAuthOn.Code != http.StatusForbidden {
		t.Errorf("expected 403 on RevealSecret with auth-disabled token when auth enabled, got %d", wOldTokAuthOn.Code)
	}
}

func postTgAction(t *testing.T, handler *TgWebProxyHandler, sessionToken, body string) *httptest.ResponseRecorder {
	t.Helper()
	token := getValidCSRFToken(t, handler, sessionToken)
	req := httptest.NewRequest(http.MethodPost, "http://192.168.10.1:2222/api/servers/tgwebproxy/action", bytes.NewBufferString(body))
	req.Host = "192.168.10.1:2222"
	req.Header.Set("Origin", "http://192.168.10.1:2222")
	req.Header.Set("X-CSRF-Token", token)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessionToken})
	w := httptest.NewRecorder()
	handler.Action(w, req)
	return w
}

func revealSecretOf(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Data tgwebproxy.RevealData `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode reveal: %v: %s", err, w.Body.String())
	}
	return env.Data.Secret
}

// The reveal modal (#1254) edits/regenerates a user's secret and immediately
// re-reveals it. The limiter must count disclosures of a given secret, so a
// changed secret is revealable at once while a repeat of the same one is not.
func TestTgWebProxy_RevealAfterSecretChangeNotRateLimited(t *testing.T) {
	handler, svc, _, sessionToken, _ := setupTestTgWebProxyHandler(t)
	userID := svc.GetConfig().Users[0].ID

	w1 := httptest.NewRecorder()
	handler.RevealSecret(w1, newRevealRequest(t, handler, sessionToken, userID))
	if w1.Code != http.StatusOK {
		t.Fatalf("initial reveal: %d %s", w1.Code, w1.Body.String())
	}
	before := revealSecretOf(t, w1)

	for _, body := range []string{
		`{"action":"update_user","user_id":"` + userID + `","regenerate_secret":true}`,
		`{"action":"update_user","user_id":"` + userID + `","secret":"0123456789abcdef0123456789abcdef"}`,
	} {
		if w := postTgAction(t, handler, sessionToken, body); w.Code != http.StatusOK {
			t.Fatalf("action %s: %d %s", body, w.Code, w.Body.String())
		}
		w := httptest.NewRecorder()
		handler.RevealSecret(w, newRevealRequest(t, handler, sessionToken, userID))
		if w.Code != http.StatusOK {
			t.Fatalf("reveal right after %s: got %d, want 200: %s", body, w.Code, w.Body.String())
		}
		after := revealSecretOf(t, w)
		if after == before {
			t.Fatalf("reveal after %s returned the old secret", body)
		}
		before = after
	}

	// Same secret again within the window is still limited.
	wSame := httptest.NewRecorder()
	handler.RevealSecret(wSame, newRevealRequest(t, handler, sessionToken, userID))
	if wSame.Code != http.StatusTooManyRequests {
		t.Errorf("repeat reveal of unchanged secret: got %d, want 429", wSame.Code)
	}
}

// With no users left there is nothing to disclose: repeated empty reveals
// must not be rate limited into an error for the UI.
func TestTgWebProxy_RevealWithNoUsersNotRateLimited(t *testing.T) {
	handler, svc, _, sessionToken, _ := setupTestTgWebProxyHandler(t)
	userID := svc.GetConfig().Users[0].ID
	if w := postTgAction(t, handler, sessionToken, `{"action":"delete_user","user_id":"`+userID+`"}`); w.Code != http.StatusOK {
		t.Fatalf("delete last user: %d %s", w.Code, w.Body.String())
	}
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		handler.RevealSecret(w, newRevealRequest(t, handler, sessionToken, ""))
		if w.Code != http.StatusOK || revealSecretOf(t, w) != "" {
			t.Fatalf("empty reveal #%d: %d %s", i+1, w.Code, w.Body.String())
		}
	}
}
