package api

import (
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/response"
)

// SetAutolistRunningProbe подключает проверку «сателлит запущен»: при
// работающем сателлите teardown объектов отклоняется (409), потому что
// работающий сателлит воссоздаёт их или держит ссылки.
func (h *SingboxRouterHandler) SetAutolistRunningProbe(fn func(r *http.Request) bool) {
	h.autolistRunning = fn
}

// TeardownAutolistObjects удаляет все объекты Autolist одним draft→apply:
// owner-правило, rule-set awgm-autolist, outbound awgm-autolist-target.
// Тело пустое; сателлит должен быть остановлен.
//
//	@Summary		Remove all Autolist router objects
//	@Description	Deletes the Autolist owner rule, the awgm-autolist rule set and the awgm-autolist-target outbound in one draft→apply. Refuses with 409 while the satellite is running or when the outbound is still referenced by another rule/group. Idempotent: absent objects are skipped.
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	OkResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		409	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/router/autolist/teardown [post]
func (h *SingboxRouterHandler) TeardownAutolistObjects(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.autolistRunning != nil && h.autolistRunning(r) {
		response.ErrorWithStatus(w, http.StatusConflict,
			"остановите сателлит Autolist перед удалением объектов", "AUTOLIST_RUNNING")
		return
	}
	if err := h.svc.TeardownAutolistObjects(r.Context()); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("autolist-teardown", "", "Autolist router objects removed (owner rule, rule-set, outbound)")
	response.Success(w, OkData{Ok: true})
}
