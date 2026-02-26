package api

import (
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/pingcheck"
	"github.com/hoaxisr/awg-manager/internal/response"
)

// PingCheckService defines the interface for ping check operations.
// Uses pingcheck types directly — no adapter needed.
type PingCheckService interface {
	GetStatus() []pingcheck.TunnelStatus
	GetLogs() []pingcheck.LogEntry
	GetTunnelLogs(tunnelID string) []pingcheck.LogEntry
	ClearLogs()
	CheckAllNow()
	IsEnabled() bool
	StartMonitoringAllRunning()
	StopMonitoringAll()
	Stop()
	// Per-tunnel monitoring control
	StartMonitoring(tunnelID, tunnelName string)
	StopMonitoring(tunnelID string)
	PauseMonitoring(tunnelID string)
	ResumeMonitoring(tunnelID string)
	ResetFailCount(tunnelID string)
}

// PingCheckHandler handles ping check API endpoints.
type PingCheckHandler struct {
	service PingCheckService
	logger  AppLogger
}

// SetLoggingService sets the logging service for the handler.
func (h *PingCheckHandler) SetLoggingService(logger LoggingService) {
	h.logger = logger
}

// NewPingCheckHandler creates a new ping check handler.
func NewPingCheckHandler(service PingCheckService) *PingCheckHandler {
	return &PingCheckHandler{service: service}
}

// GetStatus returns the current status of all monitored tunnels.
func (h *PingCheckHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}

	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "Ping check service not available", "SERVICE_UNAVAILABLE")
		return
	}

	status := h.service.GetStatus()
	if status == nil {
		status = []pingcheck.TunnelStatus{}
	}
	response.Success(w, map[string]interface{}{
		"enabled": h.service.IsEnabled(),
		"tunnels": status,
	})
}

// GetLogs returns ping check logs.
func (h *PingCheckHandler) GetLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}

	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "Ping check service not available", "SERVICE_UNAVAILABLE")
		return
	}

	tunnelID := r.URL.Query().Get("tunnelId")
	if tunnelID != "" && !isValidTunnelID(tunnelID) {
		response.Error(w, "invalid tunnel ID", "INVALID_ID")
		return
	}

	var logs []pingcheck.LogEntry
	if tunnelID != "" {
		logs = h.service.GetTunnelLogs(tunnelID)
	} else {
		logs = h.service.GetLogs()
	}

	response.Success(w, logs)
}

// ClearLogs removes all ping check log entries.
func (h *PingCheckHandler) ClearLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}

	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "Ping check service not available", "SERVICE_UNAVAILABLE")
		return
	}

	h.service.ClearLogs()

	if h.logger != nil {
		h.logger.Log(logging.CategorySystem, "pingcheck", "", "Ping check logs cleared")
	}

	response.Success(w, map[string]string{"message": "Logs cleared"})
}

// CheckNow triggers an immediate check on all tunnels.
func (h *PingCheckHandler) CheckNow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}

	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "Ping check service not available", "SERVICE_UNAVAILABLE")
		return
	}

	if !h.service.IsEnabled() {
		response.ErrorWithStatus(w, http.StatusBadRequest, "Ping check is disabled", "PING_CHECK_DISABLED")
		return
	}

	h.service.CheckAllNow()

	if h.logger != nil {
		h.logger.Log(logging.CategorySystem, "check-now", "", "Manual ping check triggered")
	}

	response.Success(w, map[string]string{"message": "Check triggered"})
}
