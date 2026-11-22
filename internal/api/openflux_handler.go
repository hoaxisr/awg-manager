package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/hoaxisr/awg-manager/internal/openflux"
	"github.com/hoaxisr/awg-manager/internal/response"
)

// OpenFluxHandler handles OpenFlux server and client HTTP endpoints.
type OpenFluxHandler struct {
	service *openflux.Service
}

// NewOpenFluxHandler creates an OpenFluxHandler bound to svc.
func NewOpenFluxHandler(svc *openflux.Service) *OpenFluxHandler {
	return &OpenFluxHandler{service: svc}
}

// RegisterRoutes mounts OpenFlux API routes behind the server auth guard.
func (h *OpenFluxHandler) RegisterRoutes(mux *http.ServeMux, guard func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("/api/openflux/status", guard(h.handleStatus))
	mux.HandleFunc("/api/openflux/config", guard(h.handleSaveConfig))
	mux.HandleFunc("/api/openflux/install", guard(h.handleInstall))
	mux.HandleFunc("/api/openflux/start", guard(h.handleStart))
	mux.HandleFunc("/api/openflux/stop", guard(h.handleStop))
	mux.HandleFunc("/api/openflux/restart", guard(h.handleRestart))
	mux.HandleFunc("/api/openflux/link", guard(h.handleGetLink))
	mux.HandleFunc("/api/openflux/logs", guard(h.handleGetLogs))

	mux.HandleFunc("/api/openflux/client/status", guard(h.handleClientStatus))
	mux.HandleFunc("/api/openflux/client/config", guard(h.handleSaveClientConfig))
	mux.HandleFunc("/api/openflux/client/start", guard(h.handleClientStart))
	mux.HandleFunc("/api/openflux/client/stop", guard(h.handleClientStop))
	mux.HandleFunc("/api/openflux/client/restart", guard(h.handleClientRestart))
	mux.HandleFunc("/api/openflux/client/delete", guard(h.handleClientDelete))
	mux.HandleFunc("/api/openflux/client/parse-link", guard(h.handleParseLink))
	mux.HandleFunc("/api/openflux/client/logs", guard(h.handleGetClientLogs))
}

func (h *OpenFluxHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "openflux service not initialized", "UNAVAILABLE")
		return
	}
	response.Success(w, h.service.GetStatus(r.Context()))
}

func (h *OpenFluxHandler) handleSaveConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "openflux service not initialized", "UNAVAILABLE")
		return
	}
	var req openflux.Config
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid request body", "BAD_REQUEST")
		return
	}
	if err := h.service.SaveConfig(r.Context(), req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "SAVE_CONFIG_FAILED")
		return
	}
	response.Success(w, h.service.GetStatus(r.Context()))
}

func (h *OpenFluxHandler) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "openflux service not initialized", "UNAVAILABLE")
		return
	}
	if err := h.service.Start(r.Context()); err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "START_FAILED")
		return
	}
	response.Success(w, h.service.GetStatus(r.Context()))
}

func (h *OpenFluxHandler) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "openflux service not initialized", "UNAVAILABLE")
		return
	}
	if err := h.service.Stop(r.Context()); err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "STOP_FAILED")
		return
	}
	response.Success(w, h.service.GetStatus(r.Context()))
}

func (h *OpenFluxHandler) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "openflux service not initialized", "UNAVAILABLE")
		return
	}
	if err := h.service.Restart(r.Context()); err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "RESTART_FAILED")
		return
	}
	response.Success(w, h.service.GetStatus(r.Context()))
}

func (h *OpenFluxHandler) handleInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "openflux service not initialized", "UNAVAILABLE")
		return
	}
	if err := h.service.Install(r.Context()); err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "INSTALL_FAILED")
		return
	}
	response.Success(w, h.service.GetStatus(r.Context()))
}

func (h *OpenFluxHandler) handleGetLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "openflux service not initialized", "UNAVAILABLE")
		return
	}
	link, err := h.service.GetShareLink(r.Context())
	if err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "LINK_FAILED")
		return
	}
	response.Success(w, map[string]string{
		"link": link,
	})
}

func (h *OpenFluxHandler) handleGetLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "openflux service not initialized", "UNAVAILABLE")
		return
	}
	lines := 100
	if l := r.URL.Query().Get("lines"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			lines = parsed
		}
	}
	response.Success(w, map[string]string{
		"logs": h.service.GetLogTail(lines),
	})
}

func (h *OpenFluxHandler) handleClientStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "openflux service not initialized", "UNAVAILABLE")
		return
	}
	response.Success(w, h.service.GetClientStatus())
}

func (h *OpenFluxHandler) handleSaveClientConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "openflux service not initialized", "UNAVAILABLE")
		return
	}
	var req openflux.ClientConfig
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid request body", "BAD_REQUEST")
		return
	}
	if err := h.service.SaveClientConfig(r.Context(), req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "SAVE_CLIENT_CONFIG_FAILED")
		return
	}
	response.Success(w, h.service.GetStatus(r.Context()))
}

func (h *OpenFluxHandler) handleClientStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "openflux service not initialized", "UNAVAILABLE")
		return
	}
	if err := h.service.StartClient(r.Context()); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "CLIENT_START_FAILED")
		return
	}
	response.Success(w, h.service.GetStatus(r.Context()))
}

func (h *OpenFluxHandler) handleClientStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "openflux service not initialized", "UNAVAILABLE")
		return
	}
	if err := h.service.StopClient(r.Context()); err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "CLIENT_STOP_FAILED")
		return
	}
	response.Success(w, h.service.GetStatus(r.Context()))
}

func (h *OpenFluxHandler) handleClientRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "openflux service not initialized", "UNAVAILABLE")
		return
	}
	if err := h.service.RestartClient(r.Context()); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "CLIENT_RESTART_FAILED")
		return
	}
	response.Success(w, h.service.GetStatus(r.Context()))
}

func (h *OpenFluxHandler) handleClientDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "openflux service not initialized", "UNAVAILABLE")
		return
	}
	if err := h.service.DeleteClient(r.Context()); err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "CLIENT_DELETE_FAILED")
		return
	}
	response.Success(w, h.service.GetStatus(r.Context()))
}

func (h *OpenFluxHandler) handleParseLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "openflux service not initialized", "UNAVAILABLE")
		return
	}
	var req struct {
		Link string `json:"link"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid request body", "BAD_REQUEST")
		return
	}
	share, cfg, err := h.service.ParseClientLink(req.Link)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "PARSE_LINK_FAILED")
		return
	}
	response.Success(w, map[string]interface{}{
		"share":  share,
		"config": cfg,
	})
}

func (h *OpenFluxHandler) handleGetClientLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "openflux service not initialized", "UNAVAILABLE")
		return
	}
	lines := 100
	if l := r.URL.Query().Get("lines"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			lines = parsed
		}
	}
	response.Success(w, map[string]string{
		"logs": h.service.GetClientLogTail(lines),
	})
}
