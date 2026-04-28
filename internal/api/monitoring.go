package api

import (
	"net/http"
	"strconv"

	"github.com/hoaxisr/awg-manager/internal/monitoring"
	"github.com/hoaxisr/awg-manager/internal/response"
)

// MonitoringHandler exposes the monitoring matrix endpoints.
type MonitoringHandler struct {
	svc *monitoring.Service
}

// NewMonitoringHandler builds a handler with the given service. svc may be
// nil during partial bootstrap — handlers respond 503 until Start.
func NewMonitoringHandler(svc *monitoring.Service) *MonitoringHandler {
	return &MonitoringHandler{svc: svc}
}

// GetMatrix returns the current matrix snapshot.
// GET /api/monitoring/matrix
//
//	@Summary		Get monitoring matrix snapshot
//	@Description	Returns the latest cross-tunnel × cross-target latency/loss matrix snapshot. Responds 503 until the monitoring service has finished bootstrap.
//	@Tags			monitoring
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	map[string]interface{}
//	@Failure		405	{object}	map[string]interface{}
//	@Failure		503	{object}	map[string]interface{}
//	@Router			/monitoring/matrix [get]
func (h *MonitoringHandler) GetMatrix(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	if h.svc == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "Monitoring service not available", "SERVICE_UNAVAILABLE")
		return
	}
	snap := h.svc.Snapshot()
	response.Success(w, snap)
}

// GetHistory returns up to limit (default 60) most-recent samples for
// (target, tunnelId).
// GET /api/monitoring/history?target=<id>&tunnelId=<id>&limit=<n>
//
//	@Summary		Get monitoring history
//	@Description	Returns up to `limit` (default 60) most-recent samples for a single (target, tunnelId) pair, oldest-first.
//	@Tags			monitoring
//	@Produce		json
//	@Security		CookieAuth
//	@Param			target		query		string	true	"Target identifier"
//	@Param			tunnelId	query		string	true	"Tunnel identifier"
//	@Param			limit		query		int		false	"Max samples to return (default 60)"
//	@Success		200			{object}	map[string]interface{}
//	@Failure		400			{object}	map[string]interface{}
//	@Failure		405			{object}	map[string]interface{}
//	@Failure		503			{object}	map[string]interface{}
//	@Router			/monitoring/history [get]
func (h *MonitoringHandler) GetHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	if h.svc == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "Monitoring service not available", "SERVICE_UNAVAILABLE")
		return
	}
	target := r.URL.Query().Get("target")
	tunnelID := r.URL.Query().Get("tunnelId")
	if target == "" || tunnelID == "" {
		response.Error(w, "target and tunnelId are required", "INVALID_PARAMS")
		return
	}
	limit := 60
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = v
		}
	}
	samples := h.svc.History(target, tunnelID, limit)
	if samples == nil {
		samples = []monitoring.Sample{}
	}
	response.Success(w, samples)
}
