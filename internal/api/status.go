package api

import (
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/response"
)

// StatusHandler handles tunnel status queries.
type StatusHandler struct {
	svc TunnelService
}

// NewStatusHandler creates a new status handler.
func NewStatusHandler(svc TunnelService) *StatusHandler {
	return &StatusHandler{svc: svc}
}

// Get returns the status of a single tunnel.
//
//	@Summary		Tunnel status
//	@Tags			status
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id	query	string	true	"Tunnel id"
//	@Success		200	{object}	APIEnvelope
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/status/get [get]
func (h *StatusHandler) Get(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
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

	state := h.svc.GetState(r.Context(), id)

	resp := map[string]interface{}{
		"id":              id,
		"status":          stateToStatus(state.State),
		"rxBytes":         state.RxBytes,
		"txBytes":         state.TxBytes,
		"latestHandshake": formatHandshake(state.LastHandshake),
	}

	response.Success(w, resp)
}

// All returns the status of all tunnels.
//
//	@Summary		All tunnel statuses
//	@Tags			status
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	APIEnvelope
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/status/all [get]
func (h *StatusHandler) All(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	tunnels, err := h.svc.List(r.Context())
	if err != nil {
		response.Error(w, err.Error(), "LIST_FAILED")
		return
	}

	statuses := make([]map[string]interface{}, 0, len(tunnels))
	for _, t := range tunnels {
		item := map[string]interface{}{
			"id":              t.ID,
			"name":            t.Name,
			"status":          stateToStatus(t.State),
			"enabled":         t.Enabled,
			"rxBytes":         t.StateInfo.RxBytes,
			"txBytes":         t.StateInfo.TxBytes,
			"latestHandshake": formatHandshake(t.StateInfo.LastHandshake),
		}

		statuses = append(statuses, item)
	}

	response.Success(w, statuses)
}
