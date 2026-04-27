// Package api — NDMS-specific endpoints (non-routing, non-tunnel).
//
// Currently exposes GET /api/ndms/save-status, which mirrors the former
// save:status SSE event. The endpoint is part of the state-sync redesign:
// SaveCoordinator publishes a resource:invalidated hint on state changes
// and clients refetch this endpoint.
package api

import (
	"net/http"
	"time"

	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/response"
)

// NDMSHandler exposes read-only NDMS status endpoints that are not
// scoped to a more specific handler (routing, tunnels, managed, etc.).
type NDMSHandler struct {
	save *ndmscommand.SaveCoordinator
}

// NewNDMSHandler constructs a handler backed by the live SaveCoordinator.
// The coordinator pointer must be non-nil; main.go builds it before the
// HTTP server starts.
func NewNDMSHandler(save *ndmscommand.SaveCoordinator) *NDMSHandler {
	return &NDMSHandler{save: save}
}

// SaveStatusDTO is the wire shape returned by GET /api/ndms/save-status.
// State is one of: "idle" | "pending" | "saving" | "error" | "failed".
// Mirrors the shape UI consumers previously read from the SSE event bus
// so the polling store keeps the same keys.
type SaveStatusDTO struct {
	State        string    `json:"state"`
	LastError    string    `json:"lastError,omitempty"`
	LastSaveAt   time.Time `json:"lastSaveAt,omitempty"`
	PendingCount int       `json:"pendingCount"`
}

// GetSaveStatus returns the current NDMS save-coordinator status as JSON.
// GET /api/ndms/save-status
//
//	@Summary		NDMS save coordinator status
//	@Tags			ndms
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	SaveStatusDTO
//	@Router			/ndms/save-status [get]
func (h *NDMSHandler) GetSaveStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if h.save == nil {
		// Defensive: handler built without a coordinator. Return idle.
		response.Success(w, SaveStatusDTO{State: "idle"})
		return
	}
	s := h.save.Status()
	response.Success(w, SaveStatusDTO{
		State:        s.State.String(),
		LastError:    s.LastError,
		LastSaveAt:   s.LastSaveAt,
		PendingCount: s.PendingCount,
	})
}
