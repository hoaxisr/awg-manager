package api

import (
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/response"
)

// clearLinkedTunnels handles POST /api/wdtt/clients/{id}/linked-tunnels/clear.
//
//	@Summary	Delete AWG tunnels linked to a WDTT client
//	@Tags		wdtt
//	@Param		id	path		string	true	"Client instance id"
//	@Success	200	{object}	APIEnvelope
//	@Failure	500	{object}	APIErrorEnvelope
//	@Router		/wdtt/clients/{id}/linked-tunnels/clear [post]
func (h *WdttHandler) clearLinkedTunnels(w http.ResponseWriter, r *http.Request, clientID string) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	deleted, errs := h.deleteLinkedAwgTunnels(r.Context(), clientID)
	if h.tunnelsHandler != nil && len(deleted) > 0 {
		h.tunnelsHandler.publishTunnelList(r.Context())
	}
	response.Success(w, map[string]any{
		"deletedTunnels": deleted,
		"tunnelErrors":   errs,
		"message":        "linked AWG tunnels cleared",
	})
}
