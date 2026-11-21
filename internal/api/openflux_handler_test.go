package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/openflux"
)

func TestOpenFluxHandler_Routes(t *testing.T) {
	tmpDir := t.TempDir()
	svc := openflux.New(tmpDir, "aarch64", func(ctx context.Context) string {
		return "192.168.90.1"
	})
	handler := NewOpenFluxHandler(svc)

	mux := http.NewServeMux()
	guarded := func(h http.HandlerFunc) http.HandlerFunc { return h }
	handler.RegisterRoutes(mux, guarded)

	// 1. GET /api/openflux/status
	req := httptest.NewRequest(http.MethodGet, "/api/openflux/status", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

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
	req = httptest.NewRequest(http.MethodPost, "/api/openflux/config", bytes.NewReader(cfgBytes))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected config 200, got %d: %s", w.Code, w.Body.String())
	}

	// 3. GET /api/openflux/link
	req = httptest.NewRequest(http.MethodGet, "/api/openflux/link", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected link 200, got %d: %s", w.Code, w.Body.String())
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
	req = httptest.NewRequest(http.MethodGet, "/api/openflux/logs?lines=50", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected logs 200, got %d: %s", w.Code, w.Body.String())
	}

	// 5. POST /api/openflux/client/parse-link
	parseBody, _ := json.Marshal(map[string]string{"link": linkResp.Data["link"]})
	req = httptest.NewRequest(http.MethodPost, "/api/openflux/client/parse-link", bytes.NewReader(parseBody))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
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
	if parseResp.Data.Config.Name != "TestNode" || parseResp.Data.Config.DirectDial != "192.168.90.1:9999" {
		t.Fatalf("unexpected parsed config: %+v", parseResp.Data.Config)
	}

	// 6. POST /api/openflux/client/config
	cliBytes, _ := json.Marshal(parseResp.Data.Config)
	req = httptest.NewRequest(http.MethodPost, "/api/openflux/client/config", bytes.NewReader(cliBytes))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected client/config 200, got %d: %s", w.Code, w.Body.String())
	}

	// 7. GET /api/openflux/client/status
	req = httptest.NewRequest(http.MethodGet, "/api/openflux/client/status", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected client/status 200, got %d: %s", w.Code, w.Body.String())
	}

	// 8. GET /api/openflux/client/logs
	req = httptest.NewRequest(http.MethodGet, "/api/openflux/client/logs?lines=50", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected client/logs 200, got %d: %s", w.Code, w.Body.String())
	}

	// 9. Wrong HTTP method rejected cleanly with 405
	req = httptest.NewRequest(http.MethodPost, "/api/openflux/status", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 on POST /status, got %d", w.Code)
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
