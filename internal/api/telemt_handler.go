package api

import (
	"encoding/json"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/telemt"
)

type TelemtHandler struct {
	service *telemt.Service
}

func NewTelemtHandler(service *telemt.Service) *TelemtHandler {
	return &TelemtHandler{service: service}
}

func (h *TelemtHandler) RegisterRoutes(mux *http.ServeMux, guarded func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/telemt/status", guarded(h.handleStatus))
	mux.HandleFunc("GET /api/telemt/config", guarded(h.handleGetConfig))
	mux.HandleFunc("POST /api/telemt/config", guarded(h.handleSaveConfig))
	mux.HandleFunc("POST /api/telemt/install", guarded(h.handleInstall))
	mux.HandleFunc("POST /api/telemt/update", guarded(h.handleUpdate))
	mux.HandleFunc("POST /api/telemt/restart", guarded(h.handleRestart))
	mux.HandleFunc("POST /api/telemt/uninstall", guarded(h.handleUninstall))
}

func (h *TelemtHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "telemt service not initialized", "UNAVAILABLE")
		return
	}
	status := h.service.GetStatus(r.Context())
	response.Success(w, status)
}

func (h *TelemtHandler) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "telemt service not initialized", "UNAVAILABLE")
		return
	}
	cfg := h.service.GetConfig()
	response.Success(w, cfg)
}

func (h *TelemtHandler) handleSaveConfig(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "telemt service not initialized", "UNAVAILABLE")
		return
	}
	var req telemt.Config
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid request body", "BAD_REQUEST")
		return
	}
	if err := h.service.SaveConfig(r.Context(), req); err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "SAVE_CONFIG_FAILED")
		return
	}
	status := h.service.GetStatus(r.Context())
	response.Success(w, status)
}

func (h *TelemtHandler) handleInstall(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "telemt service not initialized", "UNAVAILABLE")
		return
	}
	if err := h.service.Install(r.Context()); err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "INSTALL_FAILED")
		return
	}
	status := h.service.GetStatus(r.Context())
	response.Success(w, status)
}

func (h *TelemtHandler) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "telemt service not initialized", "UNAVAILABLE")
		return
	}
	if err := h.service.Update(r.Context()); err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "UPDATE_FAILED")
		return
	}
	status := h.service.GetStatus(r.Context())
	response.Success(w, status)
}

func (h *TelemtHandler) handleRestart(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "telemt service not initialized", "UNAVAILABLE")
		return
	}
	if err := h.service.Restart(r.Context()); err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "RESTART_FAILED")
		return
	}
	status := h.service.GetStatus(r.Context())
	response.Success(w, status)
}

func (h *TelemtHandler) handleUninstall(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "telemt service not initialized", "UNAVAILABLE")
		return
	}
	if err := h.service.Uninstall(r.Context()); err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "UNINSTALL_FAILED")
		return
	}
	status := h.service.GetStatus(r.Context())
	response.Success(w, status)
}
