package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/vlessserver"
)

func TestLegacyXrayRoutesRemovedAndVlessRoutesRegistered(t *testing.T) {
	tmpDir := t.TempDir()
	vlessSvc := vlessserver.New(tmpDir, nil)

	s := &Server{
		vlessServerService: vlessSvc,
	}
	h := &routeHandlers{
		vlessServerHandler:       api.NewVlessServerHandler(vlessSvc, nil),
		ingressDispatcherHandler: api.NewIngressDispatcherHandler(nil, nil, vlessSvc),
		serverWizardHandler:      api.NewServerWizardHandler(nil, nil),
		terminalHandler:          api.NewTerminalHandler(nil, nil),
		signatureHandler:         api.NewSignatureHandler(),
		guarded:                  func(f http.HandlerFunc) http.HandlerFunc { return f },
	}

	mux := http.NewServeMux()
	s.registerServerRoutes(mux, h)

	// Legacy Xray installer/binary/profile/capabilities routes MUST be gone (404).
	removed := []string{
		"/api/xray/status",
		"/api/xray/install",
		"/api/xray/uninstall",
		"/api/servers/xray/profiles",
		"/api/servers/xray/profiles/default",
		"/api/servers/xray/capabilities",
	}
	for _, path := range removed {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		hh, pattern := mux.Handler(req)
		rec := httptest.NewRecorder()
		hh.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("expected 404 for removed route %s (pattern=%q), got %d", path, pattern, rec.Code)
		}
	}

	// Primary /api/servers/vless/* and narrow compatibility aliases /api/servers/xray/* MUST be registered.
	expected := []string{
		"/api/servers/vless",
		"/api/servers/vless/status",
		"/api/servers/vless/action",
		"/api/servers/vless/clients",
		"/api/servers/vless/clients/abc/link",
		"/api/servers/vless/reality",
		"/api/servers/vless/reality/check-dest",
		"/api/servers/vless/reality/quick-setup",
		"/api/servers/vless/recovery/resolve",
		"/api/servers/vless/tuning",
		"/api/servers/vless/wizard/capabilities",
		"/api/servers/xray",
		"/api/servers/xray/status",
		"/api/servers/xray/action",
		"/api/servers/xray/clients",
		"/api/servers/xray/clients/abc/link",
		"/api/servers/xray/reality",
		"/api/servers/xray/reality/check-dest",
		"/api/servers/xray/reality/quick-setup",
		"/api/servers/xray/recovery/resolve",
		"/api/servers/xray/tuning",
		"/api/servers/xray/wizard/capabilities",
	}
	for _, path := range expected {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if _, pattern := mux.Handler(req); pattern == "" {
			t.Errorf("expected route %s to be registered", path)
		}
	}
}
