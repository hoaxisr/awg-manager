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

	"github.com/hoaxisr/awg-manager/internal/vlessserver"
	"github.com/hoaxisr/awg-manager/internal/xhttpserver"
)

func TestVlessServerHandler_PrimaryAndCompatRoutes(t *testing.T) {
	tmpDir := t.TempDir()
	svc := vlessserver.New(tmpDir, nil)
	handler := NewVlessServerHandler(svc, nil)

	mux := http.NewServeMux()
	registerPrefix := func(base string) {
		mux.HandleFunc(base, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPut || r.Method == http.MethodPost {
				handler.UpdateConfig(w, r)
			} else {
				handler.GetConfig(w, r)
			}
		})
		mux.HandleFunc(base+"/status", handler.GetStatus)
		mux.HandleFunc(base+"/action", handler.Action)
		mux.HandleFunc(base+"/clients", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				handler.AddClient(w, r)
			} else {
				handler.GetConfig(w, r)
			}
		})
		mux.HandleFunc(base+"/clients/", handler.RouteClientSubpath)
		mux.HandleFunc(base+"/migration", handler.GetMigrationStatus)
		mux.HandleFunc(base+"/migration/resolve", handler.ResolveConflict)
		mux.HandleFunc(base+"/recovery/resolve", handler.ResolveRecovery)
	}
	registerPrefix("/api/servers/vless")
	registerPrefix("/api/servers/xray")

	// 1. Primary status endpoint reports C backend awgm-xhttpd
	req := httptest.NewRequest(http.MethodGet, "/api/servers/vless/status", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/servers/vless/status code=%d body=%s", rec.Code, rec.Body.String())
	}
	var stEnv struct {
		Data vlessserver.Status `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &stEnv); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	if stEnv.Data.Backend != "awgm-xhttpd" {
		t.Fatalf("expected backend=awgm-xhttpd, got %q", stEnv.Data.Backend)
	}

	// 2. Reject ws transport with 400 Bad Request
	wsBody := []byte(`{"enabled":false,"public_domain":"cdn.example.com","transport":"ws"}`)
	req = httptest.NewRequest(http.MethodPut, "/api/servers/vless", bytes.NewReader(wsBody))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for ws transport, got %d body=%s", rec.Code, rec.Body.String())
	}

	// 3. Configure valid xhttp domain via /api/servers/vless, then exercise client CRUD via both primary and compat alias
	cfgBody := []byte(`{"enabled":false,"public_domain":"cdn.example.com","public_port":443,"path":"/cdn-bridge","transport":"xhttp"}`)
	req = httptest.NewRequest(http.MethodPut, "/api/servers/vless", bytes.NewReader(cfgBody))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/servers/vless code=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/servers/vless/clients", bytes.NewReader([]byte(`{"remark":"Phone"}`)))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/servers/vless/clients code=%d body=%s", rec.Code, rec.Body.String())
	}
	var addEnv struct {
		Data vlessserver.Client `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &addEnv); err != nil {
		t.Fatalf("unmarshal client: %v", err)
	}
	origFPID := addEnv.Data.FPClientID
	if origFPID == "" || addEnv.Data.ID == "" {
		t.Fatalf("expected generated UUID and FPClientID, got %+v", addEnv.Data)
	}

	// Rotate FP credentials via primary route
	req = httptest.NewRequest(http.MethodPost, "/api/servers/vless/clients/"+addEnv.Data.ID+"/rotate-fp", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST rotate-fp code=%d body=%s", rec.Code, rec.Body.String())
	}
	var rotEnv struct {
		Data vlessserver.Client `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &rotEnv)
	if rotEnv.Data.FPClientID == "" || rotEnv.Data.FPClientID == origFPID {
		t.Fatalf("expected rotated FPClientID != %q, got %q", origFPID, rotEnv.Data.FPClientID)
	}

	// Get links via compatibility alias /api/servers/xray/clients/{id}/link
	req = httptest.NewRequest(http.MethodGet, "/api/servers/xray/clients/"+addEnv.Data.ID+"/link", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET compat link code=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "vless://") {
		t.Fatalf("expected vless:// share link, got %s", rec.Body.String())
	}

	// 4. Reject keep_legacy with 400 Bad Request
	req = httptest.NewRequest(http.MethodPost, "/api/servers/vless/migration/resolve", bytes.NewReader([]byte(`{"action":"keep_legacy"}`)))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for keep_legacy, got %d body=%s", rec.Code, rec.Body.String())
	}

	// 5. Unsupported legacy config (vmess) returns explainable 400 error and preserves file + .bak
	legacyPath := filepath.Join(tmpDir, "legacy-vmess.json")
	if err := os.WriteFile(legacyPath, []byte(`{"inbounds":[{"protocol":"vmess","port":443}],"outbounds":[{"protocol":"freedom"}]}`), 0600); err != nil {
		t.Fatalf("write legacy vmess: %v", err)
	}
	handler.SetLegacyConfigPath(legacyPath)
	req = httptest.NewRequest(http.MethodPost, "/api/servers/vless/migration/resolve", bytes.NewReader([]byte(`{"action":"import_legacy_draft"}`)))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "vmess") {
		t.Fatalf("expected 400 mentioning vmess, got %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(legacyPath); err != nil {
		t.Fatalf("expected original legacy config to be preserved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "xray", "migration-marker.json")); !os.IsNotExist(err) {
		t.Fatalf("expected no migration-marker.json after vmess conflict, err=%v", err)
	}

	// 6. Legacy inbound TLS termination is rejected before writing marker
	legacyTLSPath := filepath.Join(tmpDir, "legacy-tls.json")
	if err := os.WriteFile(legacyTLSPath, []byte(`{"inbounds":[{"listen":"0.0.0.0","port":443,"protocol":"vless","settings":{"clients":[{"id":"00000000-0000-4000-8000-000000000001"}]},"streamSettings":{"network":"xhttp","security":"tls","tlsSettings":{"certificates":[{"certificateFile":"/cert.pem","keyFile":"/key.pem"}]},"xhttpSettings":{"path":"/test/"}}}],"outbounds":[{"protocol":"freedom"}]}`), 0600); err != nil {
		t.Fatalf("write legacy tls: %v", err)
	}
	handler.SetLegacyConfigPath(legacyTLSPath)
	req = httptest.NewRequest(http.MethodPost, "/api/servers/vless/migration/resolve", bytes.NewReader([]byte(`{"action":"import_legacy_draft"}`)))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(strings.ToLower(rec.Body.String()), "tls termination") {
		t.Fatalf("expected 400 mentioning tls termination, got %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "xray", "migration-marker.json")); !os.IsNotExist(err) {
		t.Fatalf("expected no migration-marker.json after tls conflict, err=%v", err)
	}

	// 7. Valid legacy freedom outbound imports cleanly and preserves outbound_mode="direct" (port 0, not 1099)
	var appliedRuntimeCfg xhttpserver.Config
	svc.SetTestRuntimeHook(func(cfg xhttpserver.Config) error {
		appliedRuntimeCfg = cfg
		return nil
	})
	legacyDirectPath := filepath.Join(tmpDir, "legacy-direct.json")
	if err := os.WriteFile(legacyDirectPath, []byte(`{"inbounds":[{"listen":"127.0.0.1","port":9008,"protocol":"vless","settings":{"clients":[{"id":"00000000-0000-4000-8000-000000000009","email":"direct-client"}]},"streamSettings":{"network":"xhttp","security":"none","xhttpSettings":{"host":"edge.example.com","path":"/direct-path/"}}}],"outbounds":[{"protocol":"freedom"}]}`), 0600); err != nil {
		t.Fatalf("write legacy direct: %v", err)
	}
	handler.SetLegacyConfigPath(legacyDirectPath)
	req = httptest.NewRequest(http.MethodPost, "/api/servers/vless/migration/resolve", bytes.NewReader([]byte(`{"action":"import_legacy_draft"}`)))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid freedom legacy draft import, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.GetConfig().OutboundMode != "direct" || svc.GetConfig().OutboundSocksPort != 0 {
		t.Fatalf("expected direct outbound preserved after API import, got mode=%q port=%d", svc.GetConfig().OutboundMode, svc.GetConfig().OutboundSocksPort)
	}
	if appliedRuntimeCfg.Egress.Mode != "direct" {
		t.Fatalf("expected runtime xhttpserver egress mode=\"direct\", got %q", appliedRuntimeCfg.Egress.Mode)
	}
}
