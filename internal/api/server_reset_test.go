package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/auth"
	"github.com/hoaxisr/awg-manager/internal/cdndispatcher"
	"github.com/hoaxisr/awg-manager/internal/serveringress"
	"github.com/hoaxisr/awg-manager/internal/vlessserver"
)

func TestVlessServerHandler_FactoryReset(t *testing.T) {
	tmpDir := t.TempDir()
	svc := vlessserver.New(tmpDir, nil)
	disp := cdndispatcher.NewWithDataDir(tmpDir, cdndispatcher.Config{})
	coord := serveringress.New(tmpDir, svc, disp, nil)
	handler := NewVlessServerHandler(svc, coord)

	// Method check: GET -> 405
	reqGet := httptest.NewRequest(http.MethodGet, "/api/servers/vless/reset", nil)
	recGet := httptest.NewRecorder()
	handler.FactoryReset(recGet, reqGet)
	if recGet.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for GET /reset, got %d", recGet.Code)
	}

	// Seed config on disk
	if err := svc.UpdateConfig(vlessserver.Config{
		Enabled:      false,
		PublicDomain: "vless.example.com",
		PublicPort:   443,
		Path:         "/cdn-bridge",
		Transport:    "xhttp",
	}); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/servers/vless/reset", bytes.NewReader([]byte(`{"delete_keendns":false}`)))
	rec := httptest.NewRecorder()
	handler.FactoryReset(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/servers/vless/reset code=%d body=%s", rec.Code, rec.Body.String())
	}

	var env struct {
		Success bool                      `json:"success"`
		Data    serveringress.ResetResult `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !env.Success || !env.Data.Reset || env.Data.Server != "vless" {
		t.Fatalf("unexpected reset response: %+v", env)
	}
	if svc.GetConfig().PublicDomain != "" {
		t.Fatalf("expected empty public_domain after reset, got %q", svc.GetConfig().PublicDomain)
	}
}

func TestTgWebProxyHandler_FactoryReset_CSRFAndSuccess(t *testing.T) {
	handler, svc, _, sessionToken, tempDir := setupTestTgWebProxyHandler(t)

	disp := cdndispatcher.NewWithDataDir(tempDir, cdndispatcher.Config{})
	coord := serveringress.New(tempDir, nil, disp, svc)
	handler.SetCoordinator(coord)

	// 1. Missing CSRF -> 403
	reqNoCSRF := httptest.NewRequest(http.MethodPost, "http://192.168.10.1:2222/api/servers/tgwebproxy/reset", bytes.NewReader([]byte(`{}`)))
	reqNoCSRF.Host = "192.168.10.1:2222"
	reqNoCSRF.Header.Set("Origin", "http://192.168.10.1:2222")
	reqNoCSRF.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessionToken})
	recNoCSRF := httptest.NewRecorder()
	handler.FactoryReset(recNoCSRF, reqNoCSRF)
	if recNoCSRF.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without CSRF token, got %d body=%s", recNoCSRF.Code, recNoCSRF.Body.String())
	}

	// 2. Valid CSRF -> 200
	csrfToken := getValidCSRFToken(t, handler, sessionToken)
	reqOK := httptest.NewRequest(http.MethodPost, "http://192.168.10.1:2222/api/servers/tgwebproxy/reset", bytes.NewReader([]byte(`{"delete_keendns":false}`)))
	reqOK.Host = "192.168.10.1:2222"
	reqOK.Header.Set("Origin", "http://192.168.10.1:2222")
	reqOK.Header.Set("X-CSRF-Token", csrfToken)
	reqOK.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessionToken})
	recOK := httptest.NewRecorder()
	handler.FactoryReset(recOK, reqOK)
	if recOK.Code != http.StatusOK {
		t.Fatalf("expected 200 with valid CSRF, got %d body=%s", recOK.Code, recOK.Body.String())
	}

	var env struct {
		Success bool                      `json:"success"`
		Data    serveringress.ResetResult `json:"data"`
	}
	if err := json.Unmarshal(recOK.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !env.Success || !env.Data.Reset || env.Data.Server != "tgwebproxy" {
		t.Fatalf("unexpected reset response: %+v", env)
	}
}
