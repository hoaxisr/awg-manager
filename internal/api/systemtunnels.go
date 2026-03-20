package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/testing"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
	"github.com/hoaxisr/awg-manager/internal/tunnel/systemtunnel"
)

// SystemTunnelsHandler handles system WireGuard tunnel operations.
type SystemTunnelsHandler struct {
	svc      systemtunnel.Service
	settings *storage.SettingsStore
	awgStore *storage.AWGTunnelStore
}

// NewSystemTunnelsHandler creates a new system tunnels handler.
func NewSystemTunnelsHandler(svc systemtunnel.Service, settings *storage.SettingsStore, awgStore *storage.AWGTunnelStore) *SystemTunnelsHandler {
	return &SystemTunnelsHandler{svc: svc, settings: settings, awgStore: awgStore}
}

func (h *SystemTunnelsHandler) validateName(w http.ResponseWriter, name string) bool {
	if name == "" {
		response.Error(w, "missing name parameter", "MISSING_NAME")
		return false
	}
	if !ndms.IsValidWireguardName(name) {
		response.Error(w, "invalid tunnel name", "INVALID_NAME")
		return false
	}
	return true
}

// List returns all visible (non-hidden) system WireGuard tunnels.
// GET /api/system-tunnels
func (h *SystemTunnelsHandler) List(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	tunnels, err := h.svc.List(r.Context())
	if err != nil {
		response.Error(w, err.Error(), "LIST_FAILED")
		return
	}

	// Filter out hidden, server-marked, managed server, and AWG Manager-managed NativeWG tunnels
	hidden := h.settings.GetHiddenSystemTunnels()
	serverIfaces := h.settings.GetServerInterfaces()
	managedNWG := managedNativeWGNames(h.awgStore)
	excludeSet := make(map[string]bool, len(hidden)+len(serverIfaces)+len(managedNWG)+1)
	for _, id := range hidden {
		excludeSet[id] = true
	}
	for _, id := range serverIfaces {
		excludeSet[id] = true
	}
	for _, id := range managedNWG {
		excludeSet[id] = true
	}
	// Exclude managed server interface (shown on /servers page)
	if ms := h.settings.GetManagedServer(); ms != nil {
		excludeSet[ms.InterfaceName] = true
	}

	visible := make([]ndms.SystemWireguardTunnel, 0, len(tunnels))
	for _, t := range tunnels {
		if !excludeSet[t.ID] {
			visible = append(visible, t)
		}
	}
	tunnels = visible

	response.Success(w, response.MustNotNil(tunnels))
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
	result := testing.CheckConnectivityByInterface(r.Context(), tunnel.InterfaceName)
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

// Hide handles hide/unhide operations for system tunnels.
// POST /api/system-tunnels/hide?name=Wireguard0 — hide
// DELETE /api/system-tunnels/hide?name=Wireguard0 — unhide
func (h *SystemTunnelsHandler) Hide(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if !h.validateName(w, name) {
		return
	}

	switch r.Method {
	case http.MethodPost:
		if err := h.settings.HideSystemTunnel(name); err != nil {
			response.Error(w, err.Error(), "HIDE_FAILED")
			return
		}
		response.Success(w, map[string]bool{"ok": true})
	case http.MethodDelete:
		if err := h.settings.UnhideSystemTunnel(name); err != nil {
			response.Error(w, err.Error(), "UNHIDE_FAILED")
			return
		}
		response.Success(w, map[string]bool{"ok": true})
	default:
		response.MethodNotAllowed(w)
	}
}

// Hidden returns the list of hidden system tunnel IDs.
// GET /api/system-tunnels/hidden
func (h *SystemTunnelsHandler) Hidden(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	hidden := h.settings.GetHiddenSystemTunnels()
	if hidden == nil {
		hidden = []string{}
	}
	response.Success(w, hidden)
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
