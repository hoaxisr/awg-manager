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
	mux.HandleFunc("POST /api/telemt/start", guarded(h.handleStart))
	mux.HandleFunc("POST /api/telemt/stop", guarded(h.handleStop))
	mux.HandleFunc("POST /api/telemt/restart", guarded(h.handleRestart))
	mux.HandleFunc("POST /api/telemt/uninstall", guarded(h.handleUninstall))
}

// handleStatus returns the current telemt status.
//
//	@Summary		Get telemt service status
//	@Tags			telemt
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	APIEnvelope{data=telemt.Status}
//	@Failure		503	{object}	APIErrorEnvelope
//	@Router			/telemt/status [get]
func (h *TelemtHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "telemt service not initialized", "UNAVAILABLE")
		return
	}
	status := h.service.GetStatus(r.Context())
	response.Success(w, status)
}

// handleGetConfig returns the telemt configuration.
//
//	@Summary		Get telemt configuration
//	@Tags			telemt
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	APIEnvelope{data=telemt.Config}
//	@Failure		503	{object}	APIErrorEnvelope
//	@Router			/telemt/config [get]
func (h *TelemtHandler) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "telemt service not initialized", "UNAVAILABLE")
		return
	}
	cfg := h.service.GetConfig()
	response.Success(w, cfg)
}

// handleSaveConfig updates the telemt configuration.
//
//	@Summary		Save telemt configuration
//	@Tags			telemt
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		telemt.Config	true	"telemt configuration"
//	@Success		200		{object}	APIEnvelope{data=telemt.Status}
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Failure		503		{object}	APIErrorEnvelope
//	@Router			/telemt/config [post]
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

// handleInstall installs the telemt binary.
//
//	@Summary		Install telemt binary
//	@Tags			telemt
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	APIEnvelope{data=telemt.Status}
//	@Failure		500	{object}	APIErrorEnvelope
//	@Failure		503	{object}	APIErrorEnvelope
//	@Router			/telemt/install [post]
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

// handleUpdate updates the telemt binary.
//
//	@Summary		Update telemt binary
//	@Tags			telemt
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	APIEnvelope{data=telemt.Status}
//	@Failure		500	{object}	APIErrorEnvelope
//	@Failure		503	{object}	APIErrorEnvelope
//	@Router			/telemt/update [post]
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

// handleStart starts the telemt service.
//
//	@Summary		Start telemt service
//	@Tags			telemt
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	APIEnvelope{data=telemt.Status}
//	@Failure		500	{object}	APIErrorEnvelope
//	@Failure		503	{object}	APIErrorEnvelope
//	@Router			/telemt/start [post]
func (h *TelemtHandler) handleStart(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "telemt service not initialized", "UNAVAILABLE")
		return
	}
	if err := h.service.Start(r.Context()); err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "START_FAILED")
		return
	}
	status := h.service.GetStatus(r.Context())
	response.Success(w, status)
}

// handleStop stops the telemt service.
//
//	@Summary		Stop telemt service
//	@Tags			telemt
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	APIEnvelope{data=telemt.Status}
//	@Failure		500	{object}	APIErrorEnvelope
//	@Failure		503	{object}	APIErrorEnvelope
//	@Router			/telemt/stop [post]
func (h *TelemtHandler) handleStop(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "telemt service not initialized", "UNAVAILABLE")
		return
	}
	if err := h.service.Stop(r.Context()); err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "STOP_FAILED")
		return
	}
	status := h.service.GetStatus(r.Context())
	response.Success(w, status)
}

// handleRestart restarts the telemt service.
//
//	@Summary		Restart telemt service
//	@Tags			telemt
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	APIEnvelope{data=telemt.Status}
//	@Failure		500	{object}	APIErrorEnvelope
//	@Failure		503	{object}	APIErrorEnvelope
//	@Router			/telemt/restart [post]
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

// handleUninstall uninstalls the telemt service.
//
//	@Summary		Uninstall telemt service
//	@Tags			telemt
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	APIEnvelope{data=telemt.Status}
//	@Failure		500	{object}	APIErrorEnvelope
//	@Failure		503	{object}	APIErrorEnvelope
//	@Router			/telemt/uninstall [post]
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
