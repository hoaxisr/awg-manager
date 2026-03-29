package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/pingcheck"
)

// mockPingCheckService implements PingCheckService for testing.
type mockPingCheckService struct {
	enabled bool
	status  []pingcheck.TunnelStatus
	logs    []pingcheck.LogEntry
}

func (m *mockPingCheckService) GetStatus() []pingcheck.TunnelStatus {
	return m.status
}

func (m *mockPingCheckService) GetLogs() []pingcheck.LogEntry {
	return m.logs
}

func (m *mockPingCheckService) GetTunnelLogs(tunnelID string) []pingcheck.LogEntry {
	var result []pingcheck.LogEntry
	for _, log := range m.logs {
		if log.TunnelID == tunnelID {
			result = append(result, log)
		}
	}
	return result
}

func (m *mockPingCheckService) ClearLogs() { m.logs = nil }

func (m *mockPingCheckService) CheckAllNow() {}

func (m *mockPingCheckService) IsEnabled() bool {
	return m.enabled
}

func (m *mockPingCheckService) StartMonitoringAllRunning() {}

func (m *mockPingCheckService) StopMonitoringAll() {}

func (m *mockPingCheckService) StartMonitoring(tunnelID, tunnelName string) {}

func (m *mockPingCheckService) StopMonitoring(tunnelID string) {}

func (m *mockPingCheckService) Stop() {}

func TestPingCheckHandler_GetStatus(t *testing.T) {
	now := time.Now()
	svc := &mockPingCheckService{
		enabled: true,
		status: []pingcheck.TunnelStatus{
			{
				TunnelID:   "tunnel-1",
				TunnelName: "Test Tunnel",
				Enabled:    true,
				Status:     "alive",
				Method:     "http",
				LastCheck:  &now,
				FailCount:  0,
			},
		},
	}
	handler := NewPingCheckHandler(svc, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/pingcheck/status", nil)
	rec := httptest.NewRecorder()

	handler.GetStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status code = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp struct {
		Data struct {
			Enabled bool               `json:"enabled"`
			Tunnels []pingcheck.TunnelStatus `json:"tunnels"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if !resp.Data.Enabled {
		t.Error("enabled = false, want true")
	}
	if len(resp.Data.Tunnels) != 1 {
		t.Errorf("tunnels len = %d, want 1", len(resp.Data.Tunnels))
	}
	if resp.Data.Tunnels[0].TunnelID != "tunnel-1" {
		t.Errorf("tunnelId = %s, want tunnel-1", resp.Data.Tunnels[0].TunnelID)
	}
}

func TestPingCheckHandler_GetLogs(t *testing.T) {
	svc := &mockPingCheckService{
		enabled: true,
		logs: []pingcheck.LogEntry{
			{TunnelID: "tunnel-1", TunnelName: "Tunnel 1", Success: true},
			{TunnelID: "tunnel-2", TunnelName: "Tunnel 2", Success: false},
		},
	}
	handler := NewPingCheckHandler(svc, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/pingcheck/logs", nil)
	rec := httptest.NewRecorder()

	handler.GetLogs(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status code = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp struct {
		Data []pingcheck.LogEntry `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if len(resp.Data) != 2 {
		t.Errorf("logs len = %d, want 2", len(resp.Data))
	}
}

func TestPingCheckHandler_GetLogs_FilterByTunnel(t *testing.T) {
	svc := &mockPingCheckService{
		enabled: true,
		logs: []pingcheck.LogEntry{
			{TunnelID: "tunnel-1", TunnelName: "Tunnel 1"},
			{TunnelID: "tunnel-2", TunnelName: "Tunnel 2"},
			{TunnelID: "tunnel-1", TunnelName: "Tunnel 1"},
		},
	}
	handler := NewPingCheckHandler(svc, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/pingcheck/logs?tunnelId=tunnel-1", nil)
	rec := httptest.NewRecorder()

	handler.GetLogs(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status code = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp struct {
		Data []pingcheck.LogEntry `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if len(resp.Data) != 2 {
		t.Errorf("logs len = %d, want 2 (filtered)", len(resp.Data))
	}
}

func TestPingCheckHandler_CheckNow_AlwaysAllowed(t *testing.T) {
	svc := &mockPingCheckService{enabled: false}
	handler := NewPingCheckHandler(svc, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/pingcheck/check-now", nil)
	rec := httptest.NewRecorder()

	handler.CheckNow(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status code = %d, want %d (check-now always allowed)", rec.Code, http.StatusOK)
	}
}

func TestPingCheckHandler_CheckNow_Enabled(t *testing.T) {
	svc := &mockPingCheckService{enabled: true}
	handler := NewPingCheckHandler(svc, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/pingcheck/check-now", nil)
	rec := httptest.NewRecorder()

	handler.CheckNow(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status code = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestPingCheckHandler_MethodNotAllowed(t *testing.T) {
	svc := &mockPingCheckService{enabled: true}
	handler := NewPingCheckHandler(svc, nil, nil, nil)

	// Test GET on check-now (should be POST)
	req := httptest.NewRequest(http.MethodGet, "/api/pingcheck/check-now", nil)
	rec := httptest.NewRecorder()
	handler.CheckNow(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("CheckNow GET: status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}

	// Test POST on status (should be GET)
	req = httptest.NewRequest(http.MethodPost, "/api/pingcheck/status", nil)
	rec = httptest.NewRecorder()
	handler.GetStatus(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GetStatus POST: status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}
