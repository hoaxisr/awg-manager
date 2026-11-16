package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
)

// SetAutolistRunningProbe подключает проверку «сателлит запущен»: при
// работающем сателлите teardown объектов отклоняется (409), потому что
// работающий сателлит воссоздаёт их или держит ссылки.
func (h *SingboxRouterHandler) SetAutolistRunningProbe(fn func(r *http.Request) bool) {
	h.autolistRunning = fn
}

// autolistTeardownResponse — итог сноса с флагом снимка.
type autolistTeardownResponse struct {
	// BackupSaved — снимок удалённых объектов записан; false — восстановить
	// объекты будет не из чего (снос при этом выполнен).
	BackupSaved bool `json:"backupSaved"`
}

// TeardownAutolistObjects удаляет все объекты Autolist одним draft→apply:
// owner-правило, rule-set awgm-autolist, outbound awgm-autolist-target.
// Тело пустое; сателлит должен быть остановлен.
//
//	@Summary		Remove all Autolist router objects
//	@Description	Deletes the Autolist owner rule, the awgm-autolist rule set and the awgm-autolist-target outbound in one draft→apply. Refuses with 409 while the satellite is running or when the outbound is still referenced by another rule/group. Idempotent: absent objects are skipped. A snapshot of the removed objects is written best-effort before the deletes; backupSaved=false means the snapshot could not be written (the teardown still happened).
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	autolistTeardownResponse
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
	res, err := h.svc.TeardownAutolistObjects(r.Context())
	if err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("autolist-teardown", "", "Autolist router objects removed (owner rule, rule-set, outbound)")
	response.Success(w, autolistTeardownResponse{BackupSaved: res.BackupSaved})
}

// GetAutolistBackup отдаёт сведения о снимке удалённых объектов Autolist.
//
//	@Summary		Show the removed Autolist objects snapshot
//	@Description	Returns whether a snapshot taken by the last teardown exists and when it was captured; 404 when there is none.
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	autolistBackupInfoResponse
//	@Failure		404	{object}	APIErrorEnvelope
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/router/autolist/backup [get]
func (h *SingboxRouterHandler) GetAutolistBackup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	exists, capturedAt, err := h.svc.AutolistBackupInfo(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	if !exists {
		response.ErrorWithStatus(w, http.StatusNotFound, router.ErrAutolistBackupNotFound.Error(), "AUTOLIST_BACKUP_NOT_FOUND")
		return
	}
	response.Success(w, autolistBackupInfoResponse{Exists: true, CapturedAt: capturedAt})
}

type autolistBackupInfoResponse struct {
	Exists     bool      `json:"exists"`
	CapturedAt time.Time `json:"capturedAt"`
}

// RestoreAutolistObjects пересоздаёт объекты Autolist из снимка, оставленного
// последним сносом. Тело пустое; сателлит должен быть остановлен.
//
//	@Summary		Restore the removed Autolist objects
//	@Description	Recreates the Autolist owner rule, rule set and outbound from the snapshot left by the last teardown (draft→apply, partial existence tolerated). Refuses with 409 while the satellite is running and with 409 AUTOLIST_EXISTS when all objects already exist (the satellite recreated them itself). The snapshot is deleted on success.
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	OkResponse
//	@Failure		404	{object}	APIErrorEnvelope
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		409	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/router/autolist/restore [post]
func (h *SingboxRouterHandler) RestoreAutolistObjects(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.autolistRunning != nil && h.autolistRunning(r) {
		response.ErrorWithStatus(w, http.StatusConflict,
			"остановите сателлит Autolist перед восстановлением объектов", "AUTOLIST_RUNNING")
		return
	}
	if err := h.svc.RestoreAutolistObjects(r.Context()); err != nil {
		switch {
		case errors.Is(err, router.ErrAutolistBackupNotFound):
			response.ErrorWithStatus(w, http.StatusNotFound, err.Error(), "AUTOLIST_BACKUP_NOT_FOUND")
		case errors.Is(err, router.ErrAutolistBackupExists):
			response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "AUTOLIST_EXISTS")
		default:
			h.handleErr(w, "request", err)
		}
		return
	}
	h.log.Info("autolist-restore", "", "Autolist router objects restored from the snapshot")
	response.Success(w, OkData{Ok: true})
}

// DeleteAutolistBackup отказывается от снимка («начать заново»): файл
// удаляется, объекты не восстанавливаются. Отсутствующий снимок — не ошибка.
//
//	@Summary		Discard the removed Autolist objects snapshot
//	@Description	Deletes the teardown snapshot without restoring the objects — «start fresh». A missing snapshot is not an error.
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	OkResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/router/autolist/backup [delete]
func (h *SingboxRouterHandler) DeleteAutolistBackup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		response.MethodNotAllowed(w)
		return
	}
	if err := h.svc.DeleteAutolistBackup(r.Context()); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	h.log.Info("autolist-restore", "", "Autolist removed-objects snapshot discarded")
	response.Success(w, OkData{Ok: true})
}
