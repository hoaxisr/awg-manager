package api

import (
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/response"
)

// refreshSubscription handles POST /api/wdtt/clients/{id}/subscription/refresh.
//
//	@Summary	Refresh a WDTT client's subscription
//	@Tags		wdtt
//	@Param		id	path		string	true	"Client instance id"
//	@Success	200	{object}	APIEnvelope
//	@Failure	500	{object}	APIErrorEnvelope
//	@Router		/wdtt/clients/{id}/subscription/refresh [post]
func (h *WdttHandler) refreshSubscription(w http.ResponseWriter, r *http.Request, clientID string) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	inst, payload, err := h.svc.RefreshSubscription(clientID)
	if err != nil {
		response.Error(w, err.Error(), "WDTT_SUBSCRIPTION_REFRESH_FAILED")
		return
	}
	response.Success(w, map[string]any{
		"instance": inst,
		"payload":  payload,
		"message":  "Подписка обновлена — проверьте пароль и VK-хеши, при необходимости перезапустите клиент",
	})
}
