package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

func newMcpServer(t *testing.T, mcpEnabled bool) (*Server, *storage.McpKeyStore) {
	t.Helper()
	dir := t.TempDir()
	settings := storage.NewSettingsStore(dir)
	if _, err := settings.Load(); err != nil {
		t.Fatal(err)
	}
	if err := settings.Update(func(s *storage.Settings) error {
		s.McpEnabled = mcpEnabled
		// AuthEnabled must NOT influence /mcp either way: the endpoint is
		// reachable remotely through KeenDNS, so it always demands a key.
		s.AuthEnabled = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	keys := storage.NewMcpKeyStore(dir)
	if err := keys.Load(); err != nil {
		t.Fatal(err)
	}
	return &Server{settings: settings, mcpKeys: keys}, keys
}

func mcpRouteHandlers() *routeHandlers {
	return &routeHandlers{
		guarded:       func(f http.HandlerFunc) http.HandlerFunc { return f },
		serverHandler: &api.ServersHandler{},
		systemHandler: &api.SystemHandler{},
	}
}

func TestRegisterMcpRoutes_SkippedWithoutKeyStore(t *testing.T) {
	mux := http.NewServeMux()
	s := &Server{}
	s.registerMcpRoutes(mux, mcpRouteHandlers())

	for _, path := range []string{"/mcp", "/api/mcp/keys", "/api/mcp/keys/create", "/api/mcp/keys/revoke"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if _, pattern := mux.Handler(req); pattern != "" {
			t.Errorf("%s registered without a key store", path)
		}
	}
}

func TestRegisterMcpRoutes_RegistersKeyManagement(t *testing.T) {
	mux := http.NewServeMux()
	s, _ := newMcpServer(t, true)
	s.registerMcpRoutes(mux, mcpRouteHandlers())

	for _, path := range []string{"/mcp", "/api/mcp/keys", "/api/mcp/keys/create", "/api/mcp/keys/revoke"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if _, pattern := mux.Handler(req); pattern == "" {
			t.Errorf("%s not registered", path)
		}
	}
}

// /mcp is invisible while the feature is off — 404, with no auth hint that
// would tell a scanner the endpoint exists.
func TestMcpEndpoint_NotFoundWhenDisabled(t *testing.T) {
	mux := http.NewServeMux()
	s, keys := newMcpServer(t, false)
	s.registerMcpRoutes(mux, mcpRouteHandlers())

	_, plaintext, err := keys.Create("laptop")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+plaintext)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
	if h := rec.Header().Get("WWW-Authenticate"); h != "" {
		t.Fatalf("disabled endpoint leaked an auth hint: %q", h)
	}
}

// With MCP on but AuthEnabled off, a missing or wrong key is still a 401 —
// the daemon's session auth never applies to /mcp.
func TestMcpEndpoint_UnauthorizedWithoutValidKey(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header string
	}{
		{"no header", ""},
		{"garbage token", "Bearer nope"},
		{"wrong scheme", "Basic " + storage.McpKeyPrefix + "x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			s, _ := newMcpServer(t, true)
			s.registerMcpRoutes(mux, mcpRouteHandlers())

			req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{}"))
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("code = %d, want 401", rec.Code)
			}
		})
	}
}

func TestSkipSlowRequestLogCoversMcp(t *testing.T) {
	if !skipSlowRequestLog("/mcp") {
		t.Fatal("/mcp must be exempt from the slow-request log")
	}
}
