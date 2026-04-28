package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/testing"
)

// ── Response DTOs ────────────────────────────────────────────────

// IPResultData mirrors frontend IPResult.
type IPResultData struct {
	DirectIp   string `json:"directIp" example:"203.0.113.1"`
	VpnIp      string `json:"vpnIp" example:"185.220.101.1"`
	EndpointIp string `json:"endpointIp" example:"203.0.113.42"`
	IpChanged  bool   `json:"ipChanged" example:"true"`
}

// IPResultResponse is the envelope for GET /test/ip.
type IPResultResponse struct {
	Success bool         `json:"success" example:"true"`
	Data    IPResultData `json:"data"`
}

// ConnectivityResultData mirrors frontend ConnectivityResult.
type ConnectivityResultData struct {
	Connected bool   `json:"connected" example:"true"`
	Latency   int    `json:"latency,omitempty" example:"42"`
	Reason    string `json:"reason,omitempty" example:""`
}

// ConnectivityResultResponse is the envelope for GET /test/connectivity.
type ConnectivityResultResponse struct {
	Success bool                   `json:"success" example:"true"`
	Data    ConnectivityResultData `json:"data"`
}

// IPCheckServiceDTO mirrors frontend IPCheckService.
type IPCheckServiceDTO struct {
	Label string `json:"label" example:"ipinfo.io"`
	URL   string `json:"url" example:"https://ipinfo.io/ip"`
}

// IPServicesResponse is the envelope for GET /test/ip/services.
type IPServicesResponse struct {
	Success bool                `json:"success" example:"true"`
	Data    []IPCheckServiceDTO `json:"data"`
}

// SpeedTestServerDTO mirrors frontend SpeedTestServer.
type SpeedTestServerDTO struct {
	Label string `json:"label" example:"Moscow, RU"`
	Host  string `json:"host" example:"speedtest.msk.ru"`
	Port  int    `json:"port" example:"5201"`
}

// SpeedTestInfoData mirrors frontend SpeedTestInfo.
type SpeedTestInfoData struct {
	Available bool                 `json:"available" example:"true"`
	Servers   []SpeedTestServerDTO `json:"servers"`
}

// SpeedTestInfoResponse is the envelope for GET /test/speed/servers.
type SpeedTestInfoResponse struct {
	Success bool              `json:"success" example:"true"`
	Data    SpeedTestInfoData `json:"data"`
}

// SpeedTestResultData mirrors frontend SpeedTestResult.
type SpeedTestResultData struct {
	Server      string  `json:"server" example:"speedtest.msk.ru"`
	Direction   string  `json:"direction" example:"download"`
	Bandwidth   float64 `json:"bandwidth" example:"52428800"`
	Bytes       int64   `json:"bytes" example:"157286400"`
	Duration    float64 `json:"duration" example:"3.0"`
	Retransmits int     `json:"retransmits" example:"0"`
}

// SpeedTestResultResponse is the envelope for GET /test/speed.
type SpeedTestResultResponse struct {
	Success bool                `json:"success" example:"true"`
	Data    SpeedTestResultData `json:"data"`
}

// TestingHandler handles tunnel testing operations.
type TestingHandler struct {
	testingService *testing.Service
}

// NewTestingHandler creates a new testing handler.
func NewTestingHandler(testingService *testing.Service) *TestingHandler {
	return &TestingHandler{testingService: testingService}
}

// CheckIP tests if traffic goes through tunnel by comparing IPs.
//
//	@Summary		IP leak check
//	@Tags			test
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id		query	string	false	"Tunnel id"
//	@Param			service	query	string	false	"Check service id"
//	@Success		200	{object}	IPResultResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/test/ip [get]
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
//
//	@Summary		IP check services
//	@Tags			test
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	IPServicesResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/test/ip/services [get]
func (h *TestingHandler) IPCheckServices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	response.Success(w, h.testingService.GetIPCheckServices())
}

// CheckConnectivity performs a quick connectivity test through tunnel.
//
//	@Summary		Connectivity check
//	@Tags			test
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id	query	string	true	"Tunnel id"
//	@Success		200	{object}	ConnectivityResultResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/test/connectivity [get]
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
//
//	@Summary		Speed test servers
//	@Tags			test
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	SpeedTestInfoResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/test/speed/servers [get]
func (h *TestingHandler) SpeedTestServers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	response.Success(w, h.testingService.GetSpeedTestInfo())
}

// SpeedTest runs iperf3 speed test through a tunnel.
//
//	@Summary		Speed test (sync)
//	@Tags			test
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	SpeedTestResultResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/test/speed [get]
func (h *TestingHandler) SpeedTest(w http.ResponseWriter, r *http.Request) {
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

// SpeedTestStream runs iperf3 speed test with SSE streaming of per-second intervals.
//
//	@Summary		Speed test stream
//	@Tags			test
//	@Produce		text/event-stream
//	@Security		CookieAuth
//	@Success		200	{string}	string	"SSE stream"
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/test/speed/stream [get]
func (h *TestingHandler) SpeedTestStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" || !isValidTunnelID(id) {
		response.Error(w, "missing or invalid id parameter", "INVALID_ID")
		return
	}

	server := r.URL.Query().Get("server")
	if server == "" {
		response.Error(w, "missing server parameter", "MISSING_SERVER")
		return
	}

	portStr := r.URL.Query().Get("port")
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

	flusher, ok := w.(http.Flusher)
	if !ok {
		response.Error(w, "streaming not supported", "NO_STREAMING")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	result, err := h.testingService.SpeedTestStream(r.Context(), id, server, port, direction,
		func(interval testing.SpeedTestInterval) {
			data, _ := json.Marshal(interval)
			fmt.Fprintf(w, "event: interval\ndata: %s\n\n", data)
			flusher.Flush()
		},
	)

	if err != nil {
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", err.Error())
		flusher.Flush()
		return
	}

	data, _ := json.Marshal(result)
	fmt.Fprintf(w, "event: result\ndata: %s\n\n", data)
	flusher.Flush()
}
