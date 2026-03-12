package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// ControlHandler handles tunnel start/stop/restart operations.
type ControlHandler struct {
	svc       TunnelService
	pingCheck PingCheckService
	logger    AppLogger
}

// NewControlHandler creates a new control handler.
func NewControlHandler(svc TunnelService) *ControlHandler {
	return &ControlHandler{svc: svc}
}

// SetLoggingService sets the logging service for the handler.
func (h *ControlHandler) SetLoggingService(logger LoggingService) {
	h.logger = logger
}

// SetPingCheckService sets the ping check service for monitoring control.
func (h *ControlHandler) SetPingCheckService(svc PingCheckService) {
	h.pingCheck = svc
}

func (h *ControlHandler) getStatus(r *http.Request, id string) string {
	state := h.svc.GetState(r.Context(), id)
	return stateToStatus(state.State)
}

// Start starts a tunnel.
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

	// Stop PingCheck dead monitor before starting — prevents race
	// where forced restart holds per-tunnel lock during our Start.
	if h.pingCheck != nil {
		h.pingCheck.StopMonitoring(id)
	}

	err := h.svc.Start(r.Context(), id)
	if errors.Is(err, tunnel.ErrAlreadyRunning) {
		err = nil // tunnel already running — user's intent fulfilled
	}
	if err != nil {
		if h.logger != nil {
			h.logger.LogError(logging.CategoryTunnel, "start", id, "Failed to start tunnel", err.Error())
		}
		response.Error(w, err.Error(), "START_FAILED")
		return
	}

	// Sync Enabled flag — Start means "ON" (autostart at boot)
	_ = h.svc.SetEnabled(r.Context(), id, true)

	// Start ping check monitoring if enabled
	if h.pingCheck != nil && h.pingCheck.IsEnabled() {
		if t, err := h.svc.Get(r.Context(), id); err == nil {
			h.pingCheck.StartMonitoring(id, t.Name)
		}
	}

	if h.logger != nil {
		h.logger.Log(logging.CategoryTunnel, "start", id, "Tunnel started")
	}

	response.Success(w, map[string]interface{}{
		"id":     id,
		"status": h.getStatus(r, id),
	})
}

// Stop stops a tunnel.
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

	// Fully stop monitoring — user explicitly stopped the tunnel.
	// StopMonitoring clears IsDeadByMonitoring in storage, preventing
	// stale dead flag from blocking future starts via ReconcileInterface.
	if h.pingCheck != nil {
		h.pingCheck.StopMonitoring(id)
	}

	if err := h.svc.Stop(r.Context(), id); err != nil {
		// Always sync Enabled=false — user's intent is "OFF" regardless of current state.
		// ErrNotRunning means tunnel is already stopped/disabled, but we still want Enabled=false
		// so it doesn't auto-start on boot.
		_ = h.svc.SetEnabled(r.Context(), id, false)
		if h.logger != nil {
			h.logger.LogError(logging.CategoryTunnel, "stop", id, "Failed to stop tunnel", err.Error())
		}
		response.Error(w, err.Error(), "STOP_FAILED")
		return
	}

	// Sync Enabled flag — Stop means "OFF" (no autostart at boot)
	_ = h.svc.SetEnabled(r.Context(), id, false)

	if h.logger != nil {
		h.logger.Log(logging.CategoryTunnel, "stop", id, "Tunnel stopped")
	}

	response.Success(w, map[string]interface{}{
		"id":     id,
		"status": h.getStatus(r, id),
	})
}

// Restart restarts a tunnel.
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

	// Stop PingCheck dead monitor before restarting — prevents race
	// where forced restart holds per-tunnel lock during our Restart.
	if h.pingCheck != nil {
		h.pingCheck.StopMonitoring(id)
	}

	if err := h.svc.Restart(r.Context(), id); err != nil {
		if h.logger != nil {
			h.logger.LogError(logging.CategoryTunnel, "restart", id, "Failed to restart tunnel", err.Error())
		}
		response.Error(w, err.Error(), "RESTART_FAILED")
		return
	}

	// Resume ping check monitoring after successful restart.
	// StartMonitoring handles both resume (paused→running) and fresh start,
	// and clears isDead + failCount internally.
	if h.pingCheck != nil && h.pingCheck.IsEnabled() {
		if t, err := h.svc.Get(r.Context(), id); err == nil {
			h.pingCheck.StartMonitoring(id, t.Name)
		}
	}

	if h.logger != nil {
		h.logger.Log(logging.CategoryTunnel, "restart", id, "Tunnel restarted")
	}

	response.Success(w, map[string]interface{}{
		"id":     id,
		"status": h.getStatus(r, id),
	})
}

// RestartAll restarts all enabled tunnels.
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

		err := h.svc.Restart(r.Context(), t.ID)
		result := map[string]interface{}{
			"id":     t.ID,
			"status": h.getStatus(r, t.ID),
		}
		if err != nil {
			failed++
			result["error"] = err.Error()
			if h.logger != nil {
				h.logger.LogError(logging.CategoryTunnel, "restart", t.ID, "Failed to restart tunnel", err.Error())
			}
		} else {
			restarted++
		}
		results = append(results, result)
	}

	if h.logger != nil {
		h.logger.Log(logging.CategoryTunnel, "restart-all", "", fmt.Sprintf("Restart all: %d restarted, %d failed", restarted, failed))
	}

	response.Success(w, results)
}

// ToggleEnabled toggles the auto-start setting for a tunnel.
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
		if h.logger != nil {
			h.logger.LogError(logging.CategoryTunnel, "toggle-enabled", id, "Failed to toggle autostart", err.Error())
		}
		response.Error(w, err.Error(), "TOGGLE_FAILED")
		return
	}

	if h.logger != nil {
		if newEnabled {
			h.logger.Log(logging.CategoryTunnel, "toggle-enabled", id, "Autostart enabled")
		} else {
			h.logger.Log(logging.CategoryTunnel, "toggle-enabled", id, "Autostart disabled")
		}
	}

	response.Success(w, map[string]interface{}{
		"id":      id,
		"enabled": newEnabled,
	})
}

// ToggleDefaultRoute toggles the default route setting for a tunnel.
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
		if h.logger != nil {
			h.logger.LogError(logging.CategoryTunnel, "toggle-default-route", id, "Failed to toggle default route", err.Error())
		}
		response.Error(w, err.Error(), "TOGGLE_FAILED")
		return
	}

	if h.logger != nil {
		if newValue {
			h.logger.Log(logging.CategoryTunnel, "toggle-default-route", t.Name, "Добавлен маршрут по умолчанию")
		} else {
			h.logger.Log(logging.CategoryTunnel, "toggle-default-route", t.Name, "Удалён маршрут по умолчанию")
		}
	}

	response.Success(w, map[string]interface{}{
		"id":           id,
		"defaultRoute": newValue,
	})
}
