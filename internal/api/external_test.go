package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/tunnel/sysinfo"
)

func TestExternalTunnelsHandler_List_MethodNotAllowed(t *testing.T) {
	handler := &ExternalTunnelsHandler{}

	req := httptest.NewRequest(http.MethodPost, "/api/external-tunnels", nil)
	rec := httptest.NewRecorder()

	handler.List(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("Status code = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}

	var resp response.APIResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if !resp.Error {
		t.Error("Expected error response")
	}
	if resp.Code != "METHOD_NOT_ALLOWED" {
		t.Errorf("Code = %s, want METHOD_NOT_ALLOWED", resp.Code)
	}
}

func TestExternalTunnelsHandler_Adopt_MethodNotAllowed(t *testing.T) {
	handler := &ExternalTunnelsHandler{}

	req := httptest.NewRequest(http.MethodGet, "/api/external-tunnels/adopt", nil)
	rec := httptest.NewRecorder()

	handler.Adopt(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("Status code = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestExternalTunnelsHandler_Adopt_MissingInterface(t *testing.T) {
	handler := &ExternalTunnelsHandler{}

	req := httptest.NewRequest(http.MethodPost, "/api/external-tunnels/adopt", nil)
	rec := httptest.NewRecorder()

	handler.Adopt(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("Status code = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var resp response.APIResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if !resp.Error {
		t.Error("Expected error response")
	}
	if resp.Code != "MISSING_INTERFACE" {
		t.Errorf("Code = %s, want MISSING_INTERFACE", resp.Code)
	}
}

func TestExternalTunnelsHandler_Adopt_InvalidBody(t *testing.T) {
	handler := &ExternalTunnelsHandler{}

	req := httptest.NewRequest(http.MethodPost, "/api/external-tunnels/adopt?interface=opkgtun5", strings.NewReader("invalid json"))
	rec := httptest.NewRecorder()

	handler.Adopt(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("Status code = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var resp response.APIResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp.Code != "INVALID_JSON" {
		t.Errorf("Code = %s, want INVALID_JSON", resp.Code)
	}
}

func TestExternalTunnelsHandler_Adopt_MissingContent(t *testing.T) {
	handler := &ExternalTunnelsHandler{}

	body := `{"name": "Test Tunnel"}`
	req := httptest.NewRequest(http.MethodPost, "/api/external-tunnels/adopt?interface=opkgtun5", strings.NewReader(body))
	rec := httptest.NewRecorder()

	handler.Adopt(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("Status code = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var resp response.APIResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp.Code != "MISSING_CONTENT" {
		t.Errorf("Code = %s, want MISSING_CONTENT", resp.Code)
	}
}

// TestExternalTunnelInfo_JSONSerialization verifies the ExternalTunnelInfo struct serializes correctly.
func TestExternalTunnelInfo_JSONSerialization(t *testing.T) {
	info := sysinfo.ExternalTunnelInfo{
		InterfaceName: "opkgtun5",
		TunnelNumber:  5,
		IsAWG:         true,
		PublicKey:     "abc123",
		Endpoint:      "1.2.3.4:51820",
		LastHandshake: "1 minute ago",
		RxBytes:       1024,
		TxBytes:       2048,
	}

	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var decoded sysinfo.ExternalTunnelInfo
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded.InterfaceName != info.InterfaceName {
		t.Errorf("InterfaceName = %s, want %s", decoded.InterfaceName, info.InterfaceName)
	}
	if decoded.TunnelNumber != info.TunnelNumber {
		t.Errorf("TunnelNumber = %d, want %d", decoded.TunnelNumber, info.TunnelNumber)
	}
	if decoded.RxBytes != info.RxBytes {
		t.Errorf("RxBytes = %d, want %d", decoded.RxBytes, info.RxBytes)
	}
}
