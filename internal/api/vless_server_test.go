package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/vlessserver"
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

}
