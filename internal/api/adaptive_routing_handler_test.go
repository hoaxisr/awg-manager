package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/adaptiverouting"
	sysexec "github.com/hoaxisr/awg-manager/internal/sys/exec"
)

type mockTP struct{}

func (m *mockTP) ListTunnels() []adaptiverouting.TunnelInfo {
	return []adaptiverouting.TunnelInfo{
		{ID: "awg-1", Name: "AWG Tunnel", Interface: "awg10", Active: true, Kind: "awg"},
	}
}

type mockMihomoProvider struct {
	groups []adaptiverouting.MihomoGroupInfo
}

func (m *mockMihomoProvider) ListProxies() []adaptiverouting.MihomoProxyInfo { return nil }
func (m *mockMihomoProvider) ListSubscriptions() []adaptiverouting.MihomoSubscriptionInfo { return nil }
func (m *mockMihomoProvider) ListGroups() []adaptiverouting.MihomoGroupInfo { return m.groups }

func setupTestAdaptiveRoutingHandler(t *testing.T) (*AdaptiveRoutingHandler, *http.ServeMux) {
	dir := t.TempDir()
	store, err := adaptiverouting.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	nativeStore := &mockMihomoProvider{
		groups: []adaptiverouting.MihomoGroupInfo{
			{
				ID:      "grp-auto",
				Name:    "GroupAuto",
				Enabled: true,
				Proxies: []string{"DIRECT"},
			},
		},
	}

	catalog := adaptiverouting.NewCatalog(nativeStore, &mockTP{})
	checker := adaptiverouting.NewReferenceChecker(store)

	svc := adaptiverouting.NewService(dir, catalog, checker, store)
	mockRunner := func(ctx context.Context, bin string, args ...string) (*sysexec.Result, error) {
		return &sysexec.Result{ExitCode: 0}, nil
	}
	dp := adaptiverouting.NewDatapathController(mockRunner)
	svc.SetDatapathController(dp)
	svc.SetProcessManager(nil)

	handler := NewAdaptiveRoutingHandler(svc)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })

	return handler, mux
}

func TestAdaptiveRoutingHandler_GetAndPutSettings(t *testing.T) {
	_, mux := setupTestAdaptiveRoutingHandler(t)

	// 1. GET /api/adaptive-routing/settings
	req := httptest.NewRequest(http.MethodGet, "/api/adaptive-routing/settings", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET settings status = %d, want 200", rec.Code)
	}

	var res struct {
		Success bool                       `json:"success"`
		Data    adaptiverouting.Settings   `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if res.Data.RoutingTableID != 105 {
		t.Fatalf("expected table ID 105, got %d", res.Data.RoutingTableID)
	}

	// 2. PUT /api/adaptive-routing/settings with valid egress
	body, _ := json.Marshal(adaptiverouting.Settings{
		Enabled:        true,
		RoutingTableID: 105,
		FwmarkMask:     "0x30000000",
		PrimaryEgress: adaptiverouting.EgressRef{
			Kind:       adaptiverouting.EgressKindKernelTunnel,
			ResourceID: "awg-1",
			Engine:     adaptiverouting.EngineSystem,
		},
	})
	req = httptest.NewRequest(http.MethodPut, "/api/adaptive-routing/settings", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("PUT settings status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
}

func TestAdaptiveRoutingHandler_ListEgressesAndPreview(t *testing.T) {
	_, mux := setupTestAdaptiveRoutingHandler(t)

	// GET /api/adaptive-routing/egresses
	req := httptest.NewRequest(http.MethodGet, "/api/adaptive-routing/egresses", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET egresses status = %d", rec.Code)
	}

	var listRes struct {
		Success bool `json:"success"`
		Data    struct {
			Items []adaptiverouting.ResolvedEgress `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listRes); err != nil {
		t.Fatalf("parse egresses: %v", err)
	}
	if len(listRes.Data.Items) < 2 {
		t.Fatalf("expected >= 2 egresses, got %d", len(listRes.Data.Items))
	}

	// POST /api/adaptive-routing/preview
	previewBody, _ := json.Marshal(adaptiverouting.Settings{
		PrimaryEgress: adaptiverouting.EgressRef{
			Kind:       adaptiverouting.EgressKindKernelTunnel,
			ResourceID: "awg-1",
			Engine:     adaptiverouting.EngineSystem,
		},
	})
	req = httptest.NewRequest(http.MethodPost, "/api/adaptive-routing/preview", bytes.NewReader(previewBody))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST preview status = %d, body: %s", rec.Code, rec.Body.String())
	}
}

func TestAdaptiveRoutingHandler_LearnedAndForget(t *testing.T) {
	_, mux := setupTestAdaptiveRoutingHandler(t)

	// GET /api/adaptive-routing/learned
	req := httptest.NewRequest(http.MethodGet, "/api/adaptive-routing/learned", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET learned status = %d", rec.Code)
	}

	// POST /api/adaptive-routing/forget
	forgetBody, _ := json.Marshal(map[string]string{"target": "1.1.1.1"})
	req = httptest.NewRequest(http.MethodPost, "/api/adaptive-routing/forget", bytes.NewReader(forgetBody))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST forget status = %d", rec.Code)
	}

	// POST /api/adaptive-routing/cache/clear
	req = httptest.NewRequest(http.MethodPost, "/api/adaptive-routing/cache/clear", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST clear cache status = %d", rec.Code)
	}

	// GET /api/adaptive-routing/logs
	req = httptest.NewRequest(http.MethodGet, "/api/adaptive-routing/logs?limit=10", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET logs status = %d", rec.Code)
	}
}

