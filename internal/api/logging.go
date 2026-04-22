package api

import (
	"net/http"
	"strconv"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
)

// LoggingHandler handles logging API endpoints.
type LoggingHandler struct {
	svc *logging.Service
	bus *events.Bus
	log *logging.ScopedLogger
}

// NewLoggingHandler creates a new logging handler.
func NewLoggingHandler(svc *logging.Service, appLogger logging.AppLogger) *LoggingHandler {
	return &LoggingHandler{
		svc: svc,
		log: logging.NewScopedLogger(appLogger, logging.GroupSystem, logging.SubSettings),
	}
}

// SetEventBus sets the event bus for SSE snapshot publishing.
func (h *LoggingHandler) SetEventBus(bus *events.Bus) { h.bus = bus }

// PublishSnapshot is a retained no-op hook — the legacy `snapshot:logs`
// SSE event was removed (state-sync redesign), and the frontend now fetches
// logs via REST. Keeping the method preserves the callback wiring in
// server.go / settings.go without forcing a broader refactor.
func (h *LoggingHandler) PublishSnapshot() {}

// LogsResponse represents the response for get logs endpoint.
type LogsResponse struct {
	Enabled bool               `json:"enabled"`
	Logs    []logging.LogEntry `json:"logs"`
	Total   int                `json:"total"`
}

// GetLogs returns log entries with optional filtering.
// GET /api/logs?group=&subgroup=&level= (new) or ?category=&level= (backward compat)
func (h *LoggingHandler) GetLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}

	group := r.URL.Query().Get("group")
	subgroup := r.URL.Query().Get("subgroup")
	level := r.URL.Query().Get("level")

	// Backward compat for old "category" param
	if cat := r.URL.Query().Get("category"); cat != "" && group == "" {
		switch cat {
		case "tunnel":
			group = logging.GroupTunnel
		case "settings":
			group, subgroup = logging.GroupSystem, logging.SubSettings
		case "system":
			group = logging.GroupSystem
		case "dns-route":
			group, subgroup = logging.GroupRouting, logging.SubDnsRoute
		}
	}

	limit := 200
	offset := 0
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = v
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if v, err := strconv.Atoi(o); err == nil && v >= 0 {
			offset = v
		}
	}

	logs, total := h.svc.GetLogs(group, subgroup, level, limit, offset)
	if logs == nil {
		logs = []logging.LogEntry{}
	}

	response.Success(w, LogsResponse{
		Enabled: h.svc.IsEnabled(),
		Logs:    logs,
		Total:   total,
	})
}

// ClearLogs removes all log entries.
// POST /api/logs/clear
func (h *LoggingHandler) ClearLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}

	h.svc.Clear()
	h.log.Info("clear-logs", "", "Logs cleared")
	h.PublishSnapshot()
	response.Success(w, map[string]bool{"cleared": true})
}
