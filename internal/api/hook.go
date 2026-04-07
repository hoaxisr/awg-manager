package api

import (
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/response"
)

// HookHandler handles NDM hook events.
type HookHandler struct {
	svc  TunnelService
	orch *orchestrator.Orchestrator
	log  *logging.ScopedLogger
}

// NewHookHandler creates a new hook event handler.
func NewHookHandler(svc TunnelService, orch *orchestrator.Orchestrator, appLogger logging.AppLogger) *HookHandler {
	return &HookHandler{
		svc:  svc,
		orch: orch,
		log:  logging.NewScopedLogger(appLogger, logging.GroupSystem, logging.SubBoot),
	}
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

	h.log.Info("hook", id, "iface-changed: layer="+layer+" level="+level)

	if err := h.orch.HandleEvent(r.Context(), orchestrator.Event{
		Type:     orchestrator.EventNDMSHook,
		NDMSName: id,
		Layer:    layer,
		Level:    level,
	}); err != nil {
		h.log.Warn("hook", id, "ReconcileInterface failed: "+err.Error())
		response.Error(w, err.Error(), "RECONCILE_FAILED")
		return
	}

	response.Success(w, map[string]interface{}{
		"ok": true,
	})
}
