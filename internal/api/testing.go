package api

import (
	"net/http"
	"strconv"

	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
	"github.com/hoaxisr/awg-manager/internal/testing"
)

// TestingHandler handles tunnel testing operations.
type TestingHandler struct {
	testingService *testing.Service
}

// NewTestingHandler creates a new testing handler.
func NewTestingHandler(testingService *testing.Service) *TestingHandler {
	return &TestingHandler{testingService: testingService}
}

// CheckIP tests if traffic goes through tunnel by comparing IPs.
func (h *TestingHandler) CheckIP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" {
		response.Error(w, "missing id parameter", "MISSING_ID")
		return
	}
	if !isValidTunnelID(id) {
		response.Error(w, "invalid tunnel ID", "INVALID_ID")
		return
	}

	service := r.URL.Query().Get("service")

	result, err := h.testingService.CheckIP(r.Context(), id, service)
	if err != nil {
		response.Error(w, err.Error(), "IP_CHECK_FAILED")
		return
	}

	response.Success(w, result)
}

// IPCheckServices returns the list of available IP check services.
func (h *TestingHandler) IPCheckServices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	response.Success(w, h.testingService.GetIPCheckServices())
}

// CheckConnectivity performs a quick connectivity test through tunnel.
func (h *TestingHandler) CheckConnectivity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" {
		response.Error(w, "missing id parameter", "MISSING_ID")
		return
	}
	if !isValidTunnelID(id) {
		response.Error(w, "invalid tunnel ID", "INVALID_ID")
		return
	}

	result, err := h.testingService.CheckConnectivity(r.Context(), id)
	if err != nil {
		response.Error(w, err.Error(), "CONNECTIVITY_CHECK_FAILED")
		return
	}

	response.Success(w, result)
}

// SpeedTestServers returns iperf3 availability and server list.
func (h *TestingHandler) SpeedTestServers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	if !osdetect.Is5() {
		response.Success(w, &testing.SpeedTestInfo{
			Available: false,
			Servers:   []testing.SpeedTestServer{},
		})
		return
	}

	response.Success(w, h.testingService.GetSpeedTestInfo())
}

// SpeedTest runs iperf3 speed test through a tunnel.
func (h *TestingHandler) SpeedTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	if !osdetect.Is5() {
		response.Error(w, "speed test is only available on OS 5.x", "NOT_AVAILABLE")
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" {
		response.Error(w, "missing id parameter", "MISSING_ID")
		return
	}
	if !isValidTunnelID(id) {
		response.Error(w, "invalid tunnel ID", "INVALID_ID")
		return
	}

	server := r.URL.Query().Get("server")
	if server == "" {
		response.Error(w, "missing server parameter", "MISSING_SERVER")
		return
	}

	portStr := r.URL.Query().Get("port")
	if portStr == "" {
		response.Error(w, "missing port parameter", "MISSING_PORT")
		return
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		response.Error(w, "invalid port", "INVALID_PORT")
		return
	}

	direction := r.URL.Query().Get("direction")
	if direction != "download" && direction != "upload" {
		response.Error(w, "direction must be 'download' or 'upload'", "INVALID_DIRECTION")
		return
	}

	result, err := h.testingService.SpeedTest(r.Context(), id, server, port, direction)
	if err != nil {
		response.Error(w, err.Error(), "SPEED_TEST_FAILED")
		return
	}

	response.Success(w, result)
}
