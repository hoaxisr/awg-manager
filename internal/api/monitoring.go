package api

import (
	"net/http"

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
//	@Description	Returns the latest cross-tunnel × cross-target latency/loss matrix snapshot. Responds 503 until the monitoring service has finished bootstrap. Pass `?force=1` to invalidate the Clash cache and run a fresh probing tick before returning.
//	@Tags			monitoring
//	@Produce		json
//	@Security		CookieAuth
//	@Param			force	query		int		false	"Set to 1 to force-refresh ICMP/Clash data before snapshot read"
//	@Success		200		{object}	MonitoringSnapshotResponse
//	@Failure		405		{object}	APIErrorEnvelope
//	@Failure		503		{object}	APIErrorEnvelope
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
	if r.URL.Query().Get("force") == "1" {
		h.svc.RefreshNow(r.Context())
	}
	snap := h.svc.Snapshot()
	response.Success(w, snap)
}
