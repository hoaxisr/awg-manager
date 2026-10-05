package traffic

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/response"
)

// Handler handles HTTP requests for system traffic analysis and sniffing.
type Handler struct {
	svc *Service
}

// NewHandler creates a new traffic HTTP handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux, guarded func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("/api/system/traffic/devices", guarded(h.ListDevices))
	mux.HandleFunc("/api/system/traffic/snapshot", guarded(h.GetSnapshot))
	mux.HandleFunc("/api/system/traffic/export", guarded(h.Export))
}

// ListDevices returns all known LAN devices with their active connection counts.
func (h *Handler) ListDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	devices, err := h.svc.ListDevices(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}

	response.Success(w, devices)
}

// GetSnapshot returns the current traffic snapshot for a device.
func (h *Handler) GetSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	deviceIP := r.URL.Query().Get("device")
	if strings.TrimSpace(deviceIP) == "" {
		response.Error(w, "device query parameter is required", "MISSING_PARAM")
		return
	}

	snapshot, err := h.svc.GetSnapshot(r.Context(), deviceIP)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}

	response.Success(w, snapshot)
}

// Export processes exporting selected domains/IPs to the target subsystem with smart deduplication.
func (h *Handler) Export(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	var req TrafficExportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid JSON payload: "+err.Error(), "INVALID_PAYLOAD")
		return
	}

	totalItems := len(req.Domains) + len(req.IPs)
	if totalItems == 0 {
		response.Error(w, "no domains or IPs provided to export", "EMPTY_ITEMS")
		return
	}

	resp, err := h.svc.Export(r.Context(), req)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}

	response.Success(w, resp)
}
