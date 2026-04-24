package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/deviceproxy"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/singbox"
)

// DeviceProxyHandler handles /api/proxy/* endpoints.
type DeviceProxyHandler struct {
	svc *deviceproxy.Service
	log *logging.ScopedLogger
}

// NewDeviceProxyHandler wires a DeviceProxyHandler with the given service and logger.
func NewDeviceProxyHandler(svc *deviceproxy.Service, appLogger logging.AppLogger) *DeviceProxyHandler {
	return &DeviceProxyHandler{
		svc: svc,
		log: logging.NewScopedLogger(appLogger, logging.GroupRouting, "deviceproxy"),
	}
}

// GetConfig handles GET /api/proxy/config.
func (h *DeviceProxyHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	response.Success(w, h.svc.GetConfig())
}

// SaveConfig handles PUT /api/proxy/config.
func (h *DeviceProxyHandler) SaveConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		response.MethodNotAllowed(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var cfg deviceproxy.Config
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		response.Error(w, "invalid JSON", "INVALID_JSON")
		return
	}
	if err := h.svc.SaveConfig(r.Context(), cfg); err != nil {
		// The TOCTOU race between SaveConfig's IsRunning() guard and
		// the underlying ApplyConfigNoReload can surface this sentinel
		// when sing-box dies mid-save. Map to 409 so API clients can
		// retry without getting generic SAVE_FAILED — matches the
		// contract SelectRuntime exposes for the same condition.
		if errors.Is(err, singbox.ErrSingboxNotRunning) {
			response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "SINGBOX_DOWN")
			return
		}
		response.Error(w, err.Error(), "SAVE_FAILED")
		return
	}
	response.Success(w, h.svc.GetConfig())
}

// ForceApply — POST /api/proxy/apply
//
// Forces a full sing-box reload with the currently-persisted Config,
// bypassing the smart-reload diff in SaveConfig. Used by the UI
// "Применить сейчас" affordance when the user saved a new default via
// the no-reload surgical path and now wants the live selector to
// snap to that default.
func (h *DeviceProxyHandler) ForceApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if err := h.svc.ForceApply(r.Context()); err != nil {
		response.Error(w, err.Error(), "APPLY_FAILED")
		return
	}
	response.Success(w, map[string]bool{"applied": true})
}

// GetRuntime — GET /api/proxy/runtime
func (h *DeviceProxyHandler) GetRuntime(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	response.Success(w, h.svc.GetRuntimeState(r.Context()))
}

// SelectRuntime — POST /api/proxy/runtime/select  body {"tag":"..."}
func (h *DeviceProxyHandler) SelectRuntime(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var body struct {
		Tag string `json:"tag"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		response.Error(w, "invalid JSON", "INVALID_JSON")
		return
	}
	if err := h.svc.SelectRuntimeOutbound(r.Context(), body.Tag); err != nil {
		if errors.Is(err, deviceproxy.ErrOutboundUnavailable) {
			response.Error(w, err.Error(), "OUTBOUND_UNAVAILABLE")
			return
		}
		if errors.Is(err, singbox.ErrSingboxNotRunning) {
			response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "SINGBOX_DOWN")
			return
		}
		response.Error(w, err.Error(), "RUNTIME_SELECT_FAILED")
		return
	}
	response.Success(w, map[string]string{"active": body.Tag})
}

// ListOutbounds handles GET /api/proxy/outbounds.
func (h *DeviceProxyHandler) ListOutbounds(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	response.Success(w, h.svc.ListOutbounds(r.Context()))
}

// ListenChoices handles GET /api/proxy/listen-choices.
// Returns the bridge interface list, LAN IP, and singbox-running status
// needed by the frontend inbound settings form.
func (h *DeviceProxyHandler) ListenChoices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	choices, err := h.svc.ListenChoices(r.Context())
	if err != nil {
		response.Error(w, err.Error(), "LISTEN_CHOICES_FAILED")
		return
	}
	response.Success(w, choices)
}
