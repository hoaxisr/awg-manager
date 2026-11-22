package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/auth"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy"
)

const (
	statusLeakUserA  = "a1a1a1a1b2b2b2b2c3c3c3c3d4d4d4d4"
	statusLeakUserB  = "e5e5e5e5f6f6f6f6a7a7a7a7b8b8b8b8"
	statusLeakLegacy = "fedcba9876543210fedcba9876543210"
)

// setupTgWebProxyWithLinks boots a real tgwebproxy.Service from a persisted
// multi-user, multi-endpoint config, so status responses would contain
// connection links if they were ever built there.
func setupTgWebProxyWithLinks(t *testing.T) (*TgWebProxyHandler, *tgwebproxy.Service, string) {
	t.Helper()
	tempDir := t.TempDir()
	cfg := tgwebproxy.Config{
		SchemaVersion:   tgwebproxy.CurrentSchemaVersion,
		Enabled:         false,
		Scenario:        tgwebproxy.ScenarioDual,
		ListenPort:      tgwebproxy.DefaultListenPort,
		AdminPort:       tgwebproxy.DefaultAdminPort,
		PublicHostname:  "cdn.example.test",
		DirectHost:      "home.example.test",
		DirectPort:      8448,
		TlsDomain:       "home.example.test",
		Secret:          statusLeakUserA,
		LegacySecret:    statusLeakLegacy,
		LegacyExpiresAt: "2099-01-01T00:00:00Z",
		Backend:         tgwebproxy.DefaultBackend,
		CarrierMode:     tgwebproxy.DefaultCarrierMode,
		Endpoints: []tgwebproxy.WebProxyEndpoint{
			{ID: "ep-cdn", Hostname: "cdn.example.test", IsCDN: true, Enabled: true, ListenPort: 8085, AdminPort: 8086},
			{ID: "ep-direct", Hostname: "direct.example.test", Enabled: true, ListenPort: 8087, AdminPort: 8088},
		},
		Users: []tgwebproxy.TelegramUser{
			{ID: "u-a", Remark: "Alice", Secret: statusLeakUserA, Enabled: true},
			{ID: "u-b", Remark: "Bob", Secret: statusLeakUserB, Enabled: true},
		},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(tempDir, "tproxy", "tgwebproxy-settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	sessionStore := auth.NewSessionStore(nil)
	sessionToken, err := sessionStore.Create("root")
	if err != nil {
		t.Fatal(err)
	}
	svc := tgwebproxy.New(tempDir, nil)
	handler := NewTgWebProxyHandler(svc, sessionStore)
	t.Cleanup(func() {
		sessionStore.Stop()
		_ = svc.Close()
	})

	// Fixture sanity: the loaded config really produces secret links, so a
	// clean status below is meaningful.
	rev, err := svc.RevealUserSecret("u-a")
	if err != nil || len(rev.WebProxies) == 0 || !strings.Contains(rev.WebProxies[0].TgURL, statusLeakUserA) {
		t.Fatalf("fixture not loaded: reveal=%+v err=%v", rev.WebProxies, err)
	}
	return handler, svc, sessionToken
}

func assertNoTgSecretsInBody(t *testing.T, label string, body []byte) {
	t.Helper()
	for _, sec := range []string{statusLeakUserA, statusLeakUserB, statusLeakLegacy} {
		if bytes.Contains(body, []byte(sec)) || bytes.Contains(bytes.ToLower(body), []byte(sec)) {
			t.Errorf("%s: response contains a raw secret", label)
		}
		for _, host := range []string{"cdn.example.test", "direct.example.test"} {
			if tok := tgwebproxy.DeriveBridgeToken(host, sec); bytes.Contains(body, []byte(tok)) {
				t.Errorf("%s: response contains a bridge token for %s", label, host)
			}
		}
	}
	for _, marker := range []string{"tg://", "t.me/", "bridge=", `"tg_url"`, `"bridge_url"`} {
		if bytes.Contains(body, []byte(marker)) {
			t.Errorf("%s: response contains %q", label, marker)
		}
	}
	// Endpoint identity is still present for the UI.
	if !bytes.Contains(body, []byte("direct.example.test")) {
		t.Errorf("%s: endpoint hostnames missing from status: %s", label, body)
	}
}

func TestTgWebProxyHTTP_StatusHasNoSecretLinks(t *testing.T) {
	handler, _, _ := setupTgWebProxyWithLinks(t)

	req := httptest.NewRequest(http.MethodGet, "http://192.168.10.1:2222/api/servers/tgwebproxy/status", nil)
	w := httptest.NewRecorder()
	handler.GetStatus(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET status: %d %s", w.Code, w.Body.String())
	}
	assertNoTgSecretsInBody(t, "GET /status", w.Body.Bytes())
}

func TestTgWebProxyHTTP_ActionResponseHasNoSecretLinks(t *testing.T) {
	handler, _, sessionToken := setupTgWebProxyWithLinks(t)
	token := getValidCSRFToken(t, handler, sessionToken)

	req := httptest.NewRequest(http.MethodPost, "http://192.168.10.1:2222/api/servers/tgwebproxy/action",
		strings.NewReader(`{"action":"clear_scanner_cache"}`))
	req.Host = "192.168.10.1:2222"
	req.Header.Set("Origin", "http://192.168.10.1:2222")
	req.Header.Set("X-CSRF-Token", token)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessionToken})
	w := httptest.NewRecorder()
	handler.Action(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST action: %d %s", w.Code, w.Body.String())
	}
	assertNoTgSecretsInBody(t, "POST /action", w.Body.Bytes())
}
