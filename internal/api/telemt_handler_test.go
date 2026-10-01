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

	_ = svc.Close()
}

func TestTelemtHandler_NilService(t *testing.T) {
	handler := NewTelemtHandler(nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux, func(h http.HandlerFunc) http.HandlerFunc { return h })

	req := httptest.NewRequest(http.MethodGet, "/api/telemt/status", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for nil service, got %d", w.Code)
	}
}

// Compile check to ensure context is used
var _ = context.Background
