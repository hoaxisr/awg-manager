package api

import (
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/response"
	telemtinst "github.com/hoaxisr/awg-manager/internal/telemt/installer"
)

type TelemtHandler struct {
	installer *telemtinst.TelemtInstaller
}

func NewTelemtHandler(installer *telemtinst.TelemtInstaller) *TelemtHandler {
	return &TelemtHandler{installer: installer}
}

func (h *TelemtHandler) RegisterRoutes(mux *http.ServeMux, guarded func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/telemt/status", guarded(h.handleStatus))
	mux.HandleFunc("POST /api/telemt/install", guarded(h.handleInstall))
	mux.HandleFunc("POST /api/telemt/update", guarded(h.handleUpdate))
	mux.HandleFunc("POST /api/telemt/restart", guarded(h.handleRestart))
	mux.HandleFunc("POST /api/telemt/uninstall", guarded(h.handleUninstall))
}

func (h *TelemtHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	if h.installer == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "telemt installer not initialized", "UNAVAILABLE")
		return
	}
	status := h.installer.GetStatus(r.Context())
	response.Success(w, status)
}

func (h *TelemtHandler) handleInstall(w http.ResponseWriter, r *http.Request) {
	if h.installer == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "telemt installer not initialized", "UNAVAILABLE")
		return
	}
	if err := h.installer.Install(r.Context()); err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "INSTALL_FAILED")
		return
	}
	status := h.installer.GetStatus(r.Context())
	response.Success(w, status)
}

func (h *TelemtHandler) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if h.installer == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "telemt installer not initialized", "UNAVAILABLE")
		return
	}
	if err := h.installer.Update(r.Context()); err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "UPDATE_FAILED")
		return
	}
	status := h.installer.GetStatus(r.Context())
	response.Success(w, status)
}

func (h *TelemtHandler) handleRestart(w http.ResponseWriter, r *http.Request) {
	if h.installer == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "telemt installer not initialized", "UNAVAILABLE")
		return
	}
	if err := h.installer.Restart(r.Context()); err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "RESTART_FAILED")
		return
	}
	status := h.installer.GetStatus(r.Context())
	response.Success(w, status)
}

func (h *TelemtHandler) handleUninstall(w http.ResponseWriter, r *http.Request) {
	if h.installer == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "telemt installer not initialized", "UNAVAILABLE")
		return
	}
	if err := h.installer.Uninstall(r.Context()); err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "UNINSTALL_FAILED")
		return
	}
	status := h.installer.GetStatus(r.Context())
	response.Success(w, status)
}
