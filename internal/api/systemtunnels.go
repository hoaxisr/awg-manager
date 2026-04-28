package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/hoaxisr/awg-manager/internal/logging"
	ndms "github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/testing"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
	"github.com/hoaxisr/awg-manager/internal/tunnel/systemtunnel"
)

// ── Response DTOs ────────────────────────────────────────────────

// SystemTunnelPeerDTO mirrors the peer field in SystemTunnel.
type SystemTunnelPeerDTO struct {
	PublicKey     string `json:"publicKey" example:"CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC="`
	Endpoint      string `json:"endpoint" example:"vpn2.example.com:51820"`
	RxBytes       int64  `json:"rxBytes" example:"2097152"`
	TxBytes       int64  `json:"txBytes" example:"1048576"`
	LastHandshake string `json:"lastHandshake" example:"2024-01-15T10:30:00Z"`
	Online        bool   `json:"online" example:"true"`
}

// SystemTunnelDTO mirrors frontend SystemTunnel.
type SystemTunnelDTO struct {
	ID            string               `json:"id" example:"Wireguard0"`
	InterfaceName string               `json:"interfaceName" example:"Wireguard0"`
	Description   string               `json:"description" example:"Home Server"`
	Status        string               `json:"status" example:"up"`
	Connected     bool                 `json:"connected" example:"true"`
	MTU           int                  `json:"mtu" example:"1420"`
	Peer          *SystemTunnelPeerDTO `json:"peer,omitempty"`
}

// SystemTunnelsResponse is the envelope for GET /system-tunnels.
type SystemTunnelsResponse struct {
	Success bool              `json:"success" example:"true"`
	Data    []SystemTunnelDTO `json:"data"`
}

// SystemTunnelsHandler handles system WireGuard tunnel operations.
type SystemTunnelsHandler struct {
	svc      systemtunnel.Service
	settings *storage.SettingsStore
	awgStore *storage.AWGTunnelStore
	appLog   *logging.ScopedLogger
}

// NewSystemTunnelsHandler creates a new system tunnels handler.
func NewSystemTunnelsHandler(svc systemtunnel.Service, settings *storage.SettingsStore, awgStore *storage.AWGTunnelStore, appLogger logging.AppLogger) *SystemTunnelsHandler {
	return &SystemTunnelsHandler{
		svc:      svc,
		settings: settings,
		awgStore: awgStore,
		appLog:   logging.NewScopedLogger(appLogger, logging.GroupSystem, logging.SubSystemTunnel),
	}
}

func (h *SystemTunnelsHandler) validateName(w http.ResponseWriter, name string) bool {
	if name == "" {
		response.Error(w, "missing name parameter", "MISSING_NAME")
		return false
	}
	if !isValidWireguardName(name) {
		response.Error(w, "invalid tunnel name", "INVALID_NAME")
		return false
	}
	return true
}

// listSystemTunnels builds the filtered system tunnel list for API response and SSE snapshots.
func (h *SystemTunnelsHandler) listSystemTunnels(ctx context.Context) ([]ndms.SystemWireguardTunnel, error) {
	tunnels, err := h.svc.List(ctx)
	if err != nil {
		return nil, err
	}

	// Filter out server-marked, managed server, and AWG Manager-managed NativeWG tunnels
	serverIfaces := h.settings.GetServerInterfaces()
	managedNWG := managedNativeWGNames(h.awgStore)
	excludeSet := make(map[string]bool, len(serverIfaces)+len(managedNWG)+1)
	for _, id := range serverIfaces {
		excludeSet[id] = true
	}
	for _, id := range managedNWG {
		excludeSet[id] = true
	}
	// Exclude managed server interfaces (shown on /servers page)
	for _, ms := range h.settings.GetManagedServers() {
		if ms.InterfaceName != "" {
			excludeSet[ms.InterfaceName] = true
		}
	}

	visible := make([]ndms.SystemWireguardTunnel, 0, len(tunnels))
	for _, t := range tunnels {
		if !excludeSet[t.ID] {
			visible = append(visible, t)
		}
	}

	if visible == nil {
		visible = []ndms.SystemWireguardTunnel{}
	}
	return visible, nil
}

// List returns all visible (non-hidden) system WireGuard tunnels.
// GET /api/system-tunnels
//
//	@Summary		List system tunnels
//	@Description	Returns all WireGuard tunnels managed by the router OS itself (non-hidden).
//	@Tags			system-tunnels
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	SystemTunnelsResponse
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/system-tunnels [get]
func (h *SystemTunnelsHandler) List(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	tunnels, err := h.listSystemTunnels(r.Context())
	if err != nil {
		response.Error(w, err.Error(), "LIST_FAILED")
		return
	}

	response.Success(w, tunnels)
}

// Get returns a single system WireGuard tunnel.
// GET /api/system-tunnels/get?name=Wireguard0
func (h *SystemTunnelsHandler) Get(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	name := r.URL.Query().Get("name")
	if !h.validateName(w, name) {
		return
	}
	tunnel, err := h.svc.Get(r.Context(), name)
	if err != nil {
		response.Error(w, err.Error(), "GET_FAILED")
		return
	}
	response.Success(w, tunnel)
}

// ASC handles ASC parameter operations.
// GET /api/system-tunnels/asc?name=X — read params
// POST /api/system-tunnels/asc?name=X — write params
func (h *SystemTunnelsHandler) ASC(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.getASC(w, r)
	case http.MethodPost:
		h.setASC(w, r)
	default:
		response.MethodNotAllowed(w)
	}
}

// getASC reads AWG signature/obfuscation params for a system tunnel.
//
//	@Summary		Get system tunnel ASC params
//	@Description	Reads AWG signature/obfuscation parameters for the named system (NativeWG / kernel-WG) tunnel. Data is the raw signature preset object whose shape depends on the active preset.
//	@Tags			system-tunnels
//	@Produce		json
//	@Security		CookieAuth
//	@Param			name	query		string	true	"Tunnel name"
//	@Success		200		{object}	ASCParamsResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/system-tunnels/asc [get]
func (h *SystemTunnelsHandler) getASC(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if !h.validateName(w, name) {
		return
	}
	params, err := h.svc.GetASCParams(r.Context(), name)
	if err != nil {
		response.Error(w, err.Error(), "GET_ASC_FAILED")
		return
	}
	response.Success(w, json.RawMessage(params))
}

// setASC writes AWG signature/obfuscation params for a system tunnel.
//
//	@Summary		Set system tunnel ASC params
//	@Description	Persists AWG signature/obfuscation parameters for the named system tunnel. Body is the raw ASC JSON object whose shape depends on the active signature preset.
//	@Tags			system-tunnels
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			name	query		string	true	"Tunnel name"
//	@Param			body	body		object	true	"ASC params object"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/system-tunnels/asc [post]
func (h *SystemTunnelsHandler) setASC(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if !h.validateName(w, name) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		response.Error(w, "invalid request body", "INVALID_BODY")
		return
	}
	if err := h.svc.SetASCParams(r.Context(), name, body); err != nil {
		response.Error(w, err.Error(), "SET_ASC_FAILED")
		return
	}
	response.Success(w, map[string]bool{"ok": true})
}

// CheckConnectivity performs connectivity test through system tunnel.
// GET /api/system-tunnels/test-connectivity?name=Wireguard0
func (h *SystemTunnelsHandler) CheckConnectivity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	name := r.URL.Query().Get("name")
	if !h.validateName(w, name) {
		return
	}
	tunnel, err := h.svc.Get(r.Context(), name)
	if err != nil {
		response.Error(w, err.Error(), "GET_FAILED")
		return
	}
	if tunnel.Status != "up" {
		response.Success(w, testing.ConnectivityResult{
			Connected: false,
			Reason:    testing.ReasonTunnelNotRunning,
		})
		return
	}
	h.appLog.Full("connectivity-check", name, fmt.Sprintf("Starting connectivity check for system tunnel %s", name))
	result := testing.CheckConnectivityByInterface(r.Context(), tunnel.InterfaceName)
	if result.Connected {
		h.appLog.Info("connectivity-check", name, fmt.Sprintf("Connectivity check passed: latency=%dms", *result.Latency))
	} else {
		h.appLog.Warn("connectivity-check", name, fmt.Sprintf("Connectivity check failed: reason=%s", result.Reason))
	}
	response.Success(w, result)
}

// CheckIP tests IP through system tunnel.
// GET /api/system-tunnels/test-ip?name=Wireguard0&service=optional
func (h *SystemTunnelsHandler) CheckIP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	name := r.URL.Query().Get("name")
	if !h.validateName(w, name) {
		return
	}
	tunnel, err := h.svc.Get(r.Context(), name)
	if err != nil {
		response.Error(w, err.Error(), "GET_FAILED")
		return
	}
	service := r.URL.Query().Get("service")
	result, err := testing.CheckIPByInterface(r.Context(), tunnel.InterfaceName, service)
	if err != nil {
		response.Error(w, err.Error(), "IP_CHECK_FAILED")
		return
	}
	response.Success(w, result)
}

// SpeedTestStream runs iperf3 speed test with SSE streaming through system tunnel.
// GET /api/system-tunnels/test-speed?name=Wireguard0&server=X&port=N&direction=download|upload
func (h *SystemTunnelsHandler) SpeedTestStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	name := r.URL.Query().Get("name")
	if !h.validateName(w, name) {
		return
	}
	tunnel, err := h.svc.Get(r.Context(), name)
	if err != nil {
		response.Error(w, err.Error(), "GET_FAILED")
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

	result, err := testing.SpeedTestStreamByInterface(r.Context(), tunnel.InterfaceName, server, port, direction,
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

// managedNativeWGNames returns NDMS interface names (e.g. "Wireguard0") of
// all NativeWG tunnels managed by AWG Manager. Used to exclude them from
// system tunnel and server lists.
func managedNativeWGNames(store *storage.AWGTunnelStore) []string {
	if store == nil {
		return nil
	}
	tunnels, err := store.List()
	if err != nil {
		return nil
	}
	var names []string
	for _, t := range tunnels {
		if t.Backend == "nativewg" {
			names = append(names, nwg.NewNWGNames(t.NWGIndex).NDMSName)
		}
	}
	return names
}
