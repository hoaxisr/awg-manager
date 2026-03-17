package api

import (
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
)

// HookHandler handles NDM hook events.
type HookHandler struct {
	svc    TunnelService
	logger AppLogger
}

// NewHookHandler creates a new hook event handler.
func NewHookHandler(svc TunnelService) *HookHandler {
	return &HookHandler{svc: svc}
}

// SetLoggingService sets the logging service for the handler.
func (h *HookHandler) SetLoggingService(logger LoggingService) {
	h.logger = logger
}

// HandleIfaceChanged processes interface layer change events from iflayerchanged.d.
// POST /api/hook/iface-changed?id=OpkgTun0&layer=conf&level=running
func (h *HookHandler) HandleIfaceChanged(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	id := r.URL.Query().Get("id")
	layer := r.URL.Query().Get("layer")
	level := r.URL.Query().Get("level")

	if id == "" || layer == "" || level == "" {
		response.BadRequest(w, "id, layer, and level are required")
		return
	}

	if h.logger != nil {
		h.logger.Log(logging.CategorySystem, "hook", id, "iface-changed: layer="+layer+" level="+level)
	}

	if err := h.svc.ReconcileInterface(r.Context(), id, layer, level); err != nil {
		if h.logger != nil {
			h.logger.LogError(logging.CategorySystem, "hook", id, "ReconcileInterface failed", err.Error())
		}
		response.Error(w, err.Error(), "RECONCILE_FAILED")
		return
	}

	response.Success(w, map[string]interface{}{
		"ok": true,
	})
}
