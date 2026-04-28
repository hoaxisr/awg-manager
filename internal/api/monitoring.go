package api

import (
	"net/http"
	"strconv"

	"github.com/hoaxisr/awg-manager/internal/monitoring"
	"github.com/hoaxisr/awg-manager/internal/response"
)

// ── Response DTOs ────────────────────────────────────────────────

// MonitoringTargetDTO mirrors frontend MonitoringTarget.
type MonitoringTargetDTO struct {
	ID   string `json:"id" example:"target_google"`
	Host string `json:"host" example:"https://www.google.com"`
	Name string `json:"name" example:"Google"`
}

// MonitoringTunnelDTO mirrors frontend MonitoringTunnel.
type MonitoringTunnelDTO struct {
	ID              string `json:"id" example:"tun_abc123"`
	Name            string `json:"name" example:"My VPN"`
	IfaceName       string `json:"ifaceName" example:"nwg0"`
	PingcheckTarget string `json:"pingcheckTarget" example:"target_google"`
	SelfTarget      string `json:"selfTarget" example:"target_self_tun_abc123"`
	SelfMethod      string `json:"selfMethod" example:"http"`
}

// MonitoringCellDTO mirrors frontend MonitoringCell.
type MonitoringCellDTO struct {
	TargetID        string  `json:"targetId" example:"target_google"`
	TunnelID        string  `json:"tunnelId" example:"tun_abc123"`
	LatencyMs       *int    `json:"latencyMs" swaggertype:"integer" example:"42"`
	OK              bool    `json:"ok" example:"true"`
	ActiveForRestart bool   `json:"activeForRestart" example:"false"`
	IsSelf          bool    `json:"isSelf" example:"false"`
	Ts              string  `json:"ts" example:"2024-01-15T10:30:00Z"`
}

// MonitoringSnapshotResponse is the envelope for GET /monitoring/matrix.
type MonitoringSnapshotResponse struct {
	Success bool                  `json:"success" example:"true"`
	Data    MonitoringSnapshotData `json:"data"`
}

// MonitoringSnapshotData mirrors frontend MonitoringSnapshot.
type MonitoringSnapshotData struct {
	Targets   []MonitoringTargetDTO `json:"targets"`
	Tunnels   []MonitoringTunnelDTO `json:"tunnels"`
	Cells     []MonitoringCellDTO   `json:"cells"`
	UpdatedAt string                `json:"updatedAt" example:"2024-01-15T10:30:00Z"`
}

// MonitoringSampleDTO mirrors frontend MonitoringSample.
type MonitoringSampleDTO struct {
	Ts        string `json:"ts" example:"2024-01-15T10:30:00Z"`
	LatencyMs *int   `json:"latencyMs" swaggertype:"integer" example:"42"`
	OK        bool   `json:"ok" example:"true"`
}

// MonitoringHistoryResponse is the envelope for GET /monitoring/history.
type MonitoringHistoryResponse struct {
	Success bool                  `json:"success" example:"true"`
	Data    []MonitoringSampleDTO `json:"data"`
}

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
//	@Success		200	{object}	MonitoringSnapshotResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		503	{object}	APIErrorEnvelope
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
//	@Success		200			{object}	MonitoringHistoryResponse
//	@Failure		400			{object}	APIErrorEnvelope
//	@Failure		405			{object}	APIErrorEnvelope
//	@Failure		503			{object}	APIErrorEnvelope
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
