package api

import (
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/response"
)

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
