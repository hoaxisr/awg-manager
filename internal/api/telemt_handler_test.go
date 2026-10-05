package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/telemt"
)

func TestTelemtHandler_Routes(t *testing.T) {
	tmpDir := t.TempDir()
	svc := telemt.New(tmpDir, "aarch64")
	handler := NewTelemtHandler(svc)

	mux := http.NewServeMux()
	guarded := func(h http.HandlerFunc) http.HandlerFunc { return h }
	handler.RegisterRoutes(mux, guarded)

	// 1. GET /api/telemt/status
	req := httptest.NewRequest(http.MethodGet, "/api/telemt/status", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var statusResp struct {
		Success bool          `json:"success"`
		Data    telemt.Status `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &statusResp); err != nil {
		t.Fatalf("unmarshal status response: %v", err)
	}
	if !statusResp.Success {
		t.Fatalf("expected success true, got false")
	}
	if !statusResp.Data.ArchSupported {
		t.Fatalf("expected archSupported true for aarch64")
	}

	// 2. GET /api/telemt/config
	req = httptest.NewRequest(http.MethodGet, "/api/telemt/config", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected config 200, got %d: %s", w.Code, w.Body.String())
	}

	// 3. POST /api/telemt/config
	newCfg := telemt.Config{
		Enabled:   false,
		Port:      9443,
		ListenIP:  "0.0.0.0",
		Secret:    "0123456789abcdef0123456789abcdef",
		TLSDomain: "cloudflare.com",
	}
	cfgBody, _ := json.Marshal(newCfg)
	req = httptest.NewRequest(http.MethodPost, "/api/telemt/config", strings.NewReader(string(cfgBody)))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected save config 200, got %d: %s", w.Code, w.Body.String())
	}

	updated := svc.GetConfig()
	if updated.Port != 9443 {
		t.Errorf("expected port 9443, got %d", updated.Port)
	}
	if updated.TLSDomain != "cloudflare.com" {
		t.Errorf("expected tlsDomain cloudflare.com, got %s", updated.TLSDomain)
	}

	// 4. POST /api/telemt/config with invalid JSON -> 400
	req = httptest.NewRequest(http.MethodPost, "/api/telemt/config", strings.NewReader("invalid-json"))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON body, got %d", w.Code)
	}

	// 5. POST /api/telemt/stop -> 200
	req = httptest.NewRequest(http.MethodPost, "/api/telemt/stop", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected stop 200, got %d: %s", w.Code, w.Body.String())
	}
	if svc.GetConfig().Enabled {
		t.Fatalf("expected service to be disabled after stop")
	}

	// 6. POST /api/telemt/start (not installed -> 500)
	req = httptest.NewRequest(http.MethodPost, "/api/telemt/start", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected start 500 when uninstalled, got %d: %s", w.Code, w.Body.String())
	}

	// 7. POST /api/telemt/restart (not installed -> 500)
	req = httptest.NewRequest(http.MethodPost, "/api/telemt/restart", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected restart 500 when uninstalled, got %d: %s", w.Code, w.Body.String())
	}

	// 8. POST /api/telemt/uninstall -> 200
	req = httptest.NewRequest(http.MethodPost, "/api/telemt/uninstall", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected uninstall 200, got %d: %s", w.Code, w.Body.String())
	}

	_ = svc.Close()
}

func TestTelemtHandler_NilService(t *testing.T) {
	handler := NewTelemtHandler(nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux, func(h http.HandlerFunc) http.HandlerFunc { return h })

	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/telemt/status"},
		{http.MethodGet, "/api/telemt/config"},
		{http.MethodPost, "/api/telemt/config"},
		{http.MethodPost, "/api/telemt/install"},
		{http.MethodPost, "/api/telemt/update"},
		{http.MethodPost, "/api/telemt/start"},
		{http.MethodPost, "/api/telemt/stop"},
		{http.MethodPost, "/api/telemt/restart"},
		{http.MethodPost, "/api/telemt/uninstall"},
	} {
		req := httptest.NewRequest(route.method, route.path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)

		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 for %s %s with nil service, got %d", route.method, route.path, w.Code)
		}
	}
}

// Compile check to ensure context is used
var _ = context.Background
