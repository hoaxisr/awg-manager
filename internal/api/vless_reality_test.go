package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/serveringress"
	"github.com/hoaxisr/awg-manager/internal/vlessserver"
	"github.com/hoaxisr/awg-manager/internal/xhttpserver"
	"github.com/xtls/reality"
)

func freeAPITestPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("alloc free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func setupRealityAPITestHandler(t *testing.T) (*VlessServerHandler, *vlessserver.Service) {
	t.Helper()
	dir := t.TempDir()
	svc := vlessserver.New(dir, nil)
	svc.SetTestRuntimeHook(func(xhttpserver.Config) error { return nil })
	svc.SetRealityNetworkInfoProvider(func(ctx context.Context) vlessserver.RealityNetworkInfo {
		return vlessserver.RealityNetworkInfo{
			PublicHost: "203.0.113.10",
			ExternalIP: "203.0.113.10",
			WANAddress: "203.0.113.10",
			WhiteIP:    true,
		}
	})

	cfg := svc.GetConfig()
	cfg.Enabled = false
	cfg.ListenPort = freeAPITestPort(t)
	if err := svc.UpdateConfig(cfg); err != nil {
		t.Fatalf("init UpdateConfig: %v", err)
	}

	coord := serveringress.New(dir, svc, nil, nil)
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })
	return NewVlessServerHandler(svc, coord), svc
}

func TestRealityRoute_GetUpdateReset(t *testing.T) {
	h, _ := setupRealityAPITestHandler(t)

	// 1. GET state (primary /api/servers/vless/reality + compatibility alias /api/servers/xray/reality)
	for _, p := range []string{"/api/servers/vless/reality", "/api/servers/xray/reality"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rec := httptest.NewRecorder()
		h.RealityRoute(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", p, rec.Code, rec.Body.String())
		}
		var getEnv struct {
			Success bool                     `json:"success"`
			Data    vlessserver.RealityState `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &getEnv); err != nil {
			t.Fatalf("unmarshal GET %s: %v", p, err)
		}
		if !getEnv.Success || getEnv.Data.Network == nil || !getEnv.Data.Network.WhiteIP {
			t.Fatalf("unexpected GET %s data: %+v", p, getEnv.Data)
		}
	}

	// Disallowed method returns JSON API envelope with 405
	reqMethod := httptest.NewRequest(http.MethodDelete, "/api/servers/vless/reality", nil)
	recMethod := httptest.NewRecorder()
	h.RealityRoute(recMethod, reqMethod)
	if recMethod.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE reality: want 405, got %d %s", recMethod.Code, recMethod.Body.String())
	}

	// 2. POST update (save keys & port without enabling runtime yet)
	port := freeAPITestPort(t)
	body, _ := json.Marshal(map[string]any{
		"port":      port,
		"transport": "tcp",
		"dest":      "www.microsoft.com:443",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/servers/vless/reality", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.RealityRoute(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST update reality: %d %s", rec.Code, rec.Body.String())
	}
	var updEnv struct {
		Success bool                     `json:"success"`
		Data    vlessserver.RealityState `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &updEnv); err != nil {
		t.Fatalf("unmarshal update: %v", err)
	}
	if updEnv.Data.Settings.PublicKey == "" || updEnv.Data.Settings.Port != port {
		t.Fatalf("expected generated key and port %d, got %+v", port, updEnv.Data.Settings)
	}

	// 3. POST reset clears settings
	resetBody, _ := json.Marshal(map[string]any{"reset": true})
	req = httptest.NewRequest(http.MethodPost, "/api/servers/vless/reality", bytes.NewReader(resetBody))
	rec = httptest.NewRecorder()
	h.RealityRoute(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST reset reality: %d %s", rec.Code, rec.Body.String())
	}
	var resetEnv struct {
		Success bool                     `json:"success"`
		Data    vlessserver.RealityState `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resetEnv); err != nil {
		t.Fatalf("unmarshal reset: %v", err)
	}
	if resetEnv.Data.Settings.Enabled || resetEnv.Data.Settings.PublicKey != "" || len(resetEnv.Data.Settings.ShortIDs) != 0 {
		t.Fatalf("expected cleared settings after reset, got %+v", resetEnv.Data.Settings)
	}
}

func TestRealityRoute_QuickSetupPreserveClientsAndOutboundMode(t *testing.T) {
	h, svc := setupRealityAPITestHandler(t)

	existing, err := svc.AddClient("ExistingPhone")
	if err != nil {
		t.Fatalf("AddClient: %v", err)
	}
	if err := svc.ToggleClient(existing.ID, false); err != nil {
		t.Fatalf("ToggleClient: %v", err)
	}

	const testDest = "dest.test:443"
	const testSNI = "dest.test"
	for _, alpn := range []string{"0", "1", "2"} {
		key := testDest + " " + testSNI + " " + alpn
		reality.GlobalPostHandshakeRecordsLens.Store(key, []int{})
		t.Cleanup(func() { reality.GlobalPostHandshakeRecordsLens.Delete(key) })
	}

	port := freeAPITestPort(t)
	reqBody, _ := json.Marshal(map[string]any{
		"client_remark":             "AnotherClient",
		"preserve_existing_clients": true,
		"outbound_mode":             "router",
		"reality": map[string]any{
			"port":         port,
			"transport":    "tcp",
			"dest":         testDest,
			"server_names": []string{testSNI},
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/servers/vless/reality/quick-setup", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()
	h.RealityRoute(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("quick-setup failed: %d %s", rec.Code, rec.Body.String())
	}

	var respEnv struct {
		Success bool                           `json:"success"`
		Data    VlessRealityQuickSetupResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &respEnv); err != nil {
		t.Fatalf("unmarshal quick-setup: %v", err)
	}
	if respEnv.Data.Client == nil || respEnv.Data.Client.ID != existing.ID {
		t.Fatalf("expected existing client %s to be reused, got %+v", existing.ID, respEnv.Data.Client)
	}
	cfg := svc.GetConfig()
	if len(cfg.Clients) != 1 {
		t.Fatalf("expected no duplicate clients, got %d", len(cfg.Clients))
	}
	if !cfg.Clients[0].Enabled {
		t.Fatal("expected existing client to be enabled")
	}
	if cfg.OutboundMode != "router" {
		t.Fatalf("expected outbound_mode=router, got %q", cfg.OutboundMode)
	}
	if !respEnv.Data.State.Runtime.Running || respEnv.Data.Links == nil {
		t.Fatalf("expected running reality with generated links, got %+v", respEnv.Data)
	}
}
