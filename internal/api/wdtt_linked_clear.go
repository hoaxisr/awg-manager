package api

import (
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/response"
)

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
