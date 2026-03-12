package api

import (
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
)

// LoggingService defines the interface for logging operations.
type LoggingService interface {
	IsEnabled() bool
	Log(category, action, target, message string)
	LogWarn(category, action, target, message string)
	LogError(category, action, target, message, errMsg string)
	GetLogs(category, level string) []logging.LogEntry
	Clear()
}

// LoggingHandler handles logging API endpoints.
type LoggingHandler struct {
	service LoggingService
}

// NewLoggingHandler creates a new logging handler.
func NewLoggingHandler(service LoggingService) *LoggingHandler {
	return &LoggingHandler{service: service}
}

// LogsResponse represents the response for get logs endpoint.
type LogsResponse struct {
	Enabled bool               `json:"enabled"`
	Logs    []logging.LogEntry `json:"logs"`
}

// GetLogs returns log entries with optional filtering.
// GET /api/logs?category=&level=
func (h *LoggingHandler) GetLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}

	category := r.URL.Query().Get("category")
	level := r.URL.Query().Get("level")

	logs := h.service.GetLogs(category, level)
	if logs == nil {
		logs = []logging.LogEntry{}
	}

	response.Success(w, LogsResponse{
		Enabled: h.service.IsEnabled(),
		Logs:    logs,
	})
}

// ClearLogs removes all log entries.
// POST /api/logs/clear
func (h *LoggingHandler) ClearLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}

	h.service.Clear()
	h.service.Log(logging.CategorySettings, "clear-logs", "", "Logs cleared")
	response.Success(w, map[string]bool{"cleared": true})
}
