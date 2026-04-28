package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/routing"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// ── Response DTOs ────────────────────────────────────────────────

// TunnelControlData is the response data for control operations (start/stop/restart).
type TunnelControlData struct {
	ID     string `json:"id" example:"tun_abc123"`
	Status string `json:"status" example:"connected"`
}

// TunnelControlResponse is the envelope for control operations.
type TunnelControlResponse struct {
	Success bool              `json:"success" example:"true"`
	Data    TunnelControlData `json:"data"`
}

// ControlHandler handles tunnel start/stop/restart operations.
type ControlHandler struct {
	svc            TunnelService
	orch           *orchestrator.Orchestrator
	pingCheck      PingCheckService
	tunnelsHandler *TunnelsHandler
	bus            *events.Bus
	log            *logging.ScopedLogger
}

// NewControlHandler creates a new control handler.
func NewControlHandler(svc TunnelService, appLogger logging.AppLogger) *ControlHandler {
	return &ControlHandler{
		svc: svc,
		log: logging.NewScopedLogger(appLogger, logging.GroupTunnel, logging.SubLifecycle),
	}
}

// SetPingCheckService sets the ping check service for monitoring control.
func (h *ControlHandler) SetPingCheckService(svc PingCheckService) {
	h.pingCheck = svc
}

// SetOrchestrator sets the orchestrator for lifecycle operations.
func (h *ControlHandler) SetOrchestrator(orch *orchestrator.Orchestrator) {
	h.orch = orch
}

// SetTunnelsHandler sets the tunnels handler for SSE list publishing.
func (h *ControlHandler) SetTunnelsHandler(th *TunnelsHandler) {
	h.tunnelsHandler = th
}

// SetEventBus sets the event bus for SSE publishing.
func (h *ControlHandler) SetEventBus(bus *events.Bus) { h.bus = bus }

// SetCatalog is a no-op retained for API compatibility. The control
// handler no longer needs direct catalog access — it just emits a
// resource:invalidated hint so the polling store refetches.
func (h *ControlHandler) SetCatalog(_ routing.Catalog) {}

// publishRoutingTunnels posts a resource:invalidated hint so clients
// refetch the routing tunnel list after a start/stop that changed
// which tunnels are available for routing dropdowns.
func (h *ControlHandler) publishRoutingTunnels(_ context.Context) {
	publishInvalidated(h.bus, ResourceRoutingTunnels, "state-changed")
}

func (h *ControlHandler) getStatus(r *http.Request, id string) string {
	state := h.svc.GetState(r.Context(), id)
	return stateToStatus(state.State)
}

// Start starts a tunnel.
//
//	@Summary		Start tunnel
//	@Tags			control
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id	query	string	true	"Tunnel id"
//	@Success		200	{object}	TunnelControlResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/control/start [post]
func (h *ControlHandler) Start(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" {
		response.Error(w, "missing id parameter", "MISSING_ID")
		return
	}
	if !isValidTunnelID(id) {
		response.Error(w, "invalid tunnel ID", "INVALID_ID")
		return
	}

	err := h.orch.HandleEvent(r.Context(), orchestrator.Event{
		Type:   orchestrator.EventStart,
		Tunnel: id,
	})
	if errors.Is(err, tunnel.ErrAlreadyRunning) {
		err = nil // tunnel already running — user's intent fulfilled
	}
	if err != nil {
		h.log.Warn("start", id, "Failed to start tunnel: "+err.Error())
		response.Error(w, err.Error(), "START_FAILED")
		return
	}

	h.log.Info("start", id, "Tunnel started")

	response.Success(w, map[string]interface{}{
		"id":     id,
		"status": h.getStatus(r, id),
	})

	if h.tunnelsHandler != nil {
		h.tunnelsHandler.publishTunnelList(r.Context())
		h.publishRoutingTunnels(r.Context())
	}
}

// Stop stops a tunnel.
//
//	@Summary		Stop tunnel
//	@Tags			control
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id	query	string	true	"Tunnel id"
//	@Success		200	{object}	TunnelControlResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/control/stop [post]
func (h *ControlHandler) Stop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" {
		response.Error(w, "missing id parameter", "MISSING_ID")
		return
	}
	if !isValidTunnelID(id) {
		response.Error(w, "invalid tunnel ID", "INVALID_ID")
		return
	}

	if err := h.orch.HandleEvent(r.Context(), orchestrator.Event{
		Type:   orchestrator.EventStop,
		Tunnel: id,
	}); err != nil {
		// Always sync Enabled=false — user's intent is "OFF" regardless of current state.
		// ErrNotRunning means tunnel is already stopped/disabled, but we still want Enabled=false
		// so it doesn't auto-start on boot.
		_ = h.svc.SetEnabled(r.Context(), id, false)
		h.log.Warn("stop", id, "Failed to stop tunnel: "+err.Error())
		response.Error(w, err.Error(), "STOP_FAILED")
		return
	}

	h.log.Info("stop", id, "Tunnel stopped")

	response.Success(w, map[string]interface{}{
		"id":     id,
		"status": h.getStatus(r, id),
	})

	if h.tunnelsHandler != nil {
		h.tunnelsHandler.publishTunnelList(r.Context())
		h.publishRoutingTunnels(r.Context())
	}
}

// Restart restarts a tunnel.
//
//	@Summary		Restart tunnel
//	@Tags			control
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id	query	string	true	"Tunnel id"
//	@Success		200	{object}	TunnelControlResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/control/restart [post]
func (h *ControlHandler) Restart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" {
		response.Error(w, "missing id parameter", "MISSING_ID")
		return
	}
	if !isValidTunnelID(id) {
		response.Error(w, "invalid tunnel ID", "INVALID_ID")
		return
	}

	if err := h.orch.HandleEvent(r.Context(), orchestrator.Event{
		Type:   orchestrator.EventRestart,
		Tunnel: id,
	}); err != nil {
		h.log.Warn("restart", id, "Failed to restart tunnel: "+err.Error())
		response.Error(w, err.Error(), "RESTART_FAILED")
		return
	}

	h.log.Info("restart", id, "Tunnel restarted")

	response.Success(w, map[string]interface{}{
		"id":     id,
		"status": h.getStatus(r, id),
	})

	if h.tunnelsHandler != nil {
		h.tunnelsHandler.publishTunnelList(r.Context())
		h.publishRoutingTunnels(r.Context())
	}
}

// RestartAll restarts all enabled tunnels.
//
//	@Summary		Restart all enabled tunnels
//	@Tags			control
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	APIEnvelope
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/control/restart-all [post]
func (h *ControlHandler) RestartAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	tunnels, err := h.svc.List(r.Context())
	if err != nil {
		response.Error(w, err.Error(), "LIST_FAILED")
		return
	}

	results := make([]map[string]interface{}, 0)
	var restarted, failed int

	for _, t := range tunnels {
		if !t.Enabled {
			continue
		}

		err := h.orch.HandleEvent(r.Context(), orchestrator.Event{
			Type:   orchestrator.EventRestart,
			Tunnel: t.ID,
		})
		result := map[string]interface{}{
			"id":     t.ID,
			"status": h.getStatus(r, t.ID),
		}
		if err != nil {
			failed++
			result["error"] = err.Error()
			h.log.Warn("restart", t.ID, "Failed to restart tunnel: "+err.Error())
		} else {
			restarted++
		}
		results = append(results, result)
	}

	h.log.Info("restart-all", "", fmt.Sprintf("Restart all: %d restarted, %d failed", restarted, failed))

	response.Success(w, results)

	if h.tunnelsHandler != nil {
		h.tunnelsHandler.publishTunnelList(r.Context())
		h.publishRoutingTunnels(r.Context())
	}
}

// ToggleEnabled toggles the auto-start setting for a tunnel.
//
//	@Summary		Toggle tunnel autostart
//	@Tags			control
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id	query	string	true	"Tunnel id"
//	@Success		200	{object}	APIEnvelope
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/control/toggle-enabled [post]
func (h *ControlHandler) ToggleEnabled(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" {
		response.Error(w, "missing id parameter", "MISSING_ID")
		return
	}
	if !isValidTunnelID(id) {
		response.Error(w, "invalid tunnel ID", "INVALID_ID")
		return
	}

	// Get current state and toggle
	t, err := h.svc.Get(r.Context(), id)
	if err != nil {
		response.Error(w, err.Error(), "NOT_FOUND")
		return
	}

	newEnabled := !t.Enabled
	if err := h.svc.SetEnabled(r.Context(), id, newEnabled); err != nil {
		h.log.Warn("toggle-enabled", id, "Failed to toggle autostart: "+err.Error())
		response.Error(w, err.Error(), "TOGGLE_FAILED")
		return
	}

	if newEnabled {
		h.log.Info("toggle-enabled", id, "Autostart enabled")
	} else {
		h.log.Info("toggle-enabled", id, "Autostart disabled")
	}

	response.Success(w, map[string]interface{}{
		"id":      id,
		"enabled": newEnabled,
	})

	if h.tunnelsHandler != nil {
		h.tunnelsHandler.publishTunnelList(r.Context())
		h.publishRoutingTunnels(r.Context())
	}
}

// ToggleDefaultRoute toggles the default route setting for a tunnel.
//
//	@Summary		Toggle default route
//	@Tags			control
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id	query	string	true	"Tunnel id"
//	@Success		200	{object}	APIEnvelope
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/control/toggle-default-route [post]
func (h *ControlHandler) ToggleDefaultRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" {
		response.Error(w, "missing id parameter", "MISSING_ID")
		return
	}
	if !isValidTunnelID(id) {
		response.Error(w, "invalid tunnel ID", "INVALID_ID")
		return
	}

	// Get current state and toggle
	t, err := h.svc.Get(r.Context(), id)
	if err != nil {
		response.Error(w, err.Error(), "NOT_FOUND")
		return
	}

	newValue := !t.DefaultRoute
	if err := h.svc.SetDefaultRoute(r.Context(), id, newValue); err != nil {
		h.log.Warn("toggle-default-route", id, "Failed to toggle default route: "+err.Error())
		response.Error(w, err.Error(), "TOGGLE_FAILED")
		return
	}

	if newValue {
		h.log.Info("toggle-default-route", t.Name, "Добавлен маршрут по умолчанию")
	} else {
		h.log.Info("toggle-default-route", t.Name, "Удалён маршрут по умолчанию")
	}

	response.Success(w, map[string]interface{}{
		"id":           id,
		"defaultRoute": newValue,
	})

	if h.tunnelsHandler != nil {
		h.tunnelsHandler.publishTunnelList(r.Context())
		h.publishRoutingTunnels(r.Context())
	}
}
