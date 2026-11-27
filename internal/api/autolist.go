package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/autolistdetect"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
)

// AutolistHandler exposes the satellite lifecycle and its operational proxy.
type AutolistHandler struct {
	svc *autolistdetect.Service
	// log — nil-safe: тесты строят хендлер без логгера.
	log *logging.ScopedLogger
	// hasRemovedBackup — «у панели есть снимок объектов Autolist,
	// удалённых кнопкой сноса» (routerSvc.AutolistBackupInfo). Опционально:
	// nil — поле статуса остаётся false. Ошибка чтения снимка не валит
	// статус: трактуется как «снимка нет», факт уходит в журнал.
	hasRemovedBackup func(ctx context.Context) (bool, time.Time, error)
}

func NewAutolistHandler(svc *autolistdetect.Service) *AutolistHandler {
	return &AutolistHandler{svc: svc}
}

// SetHasRemovedBackupProbe подключает проверку файла снимка удалённых
// объектов (wiring: routerSvc.AutolistBackupInfo). Панель ставит флаг сама —
// сателлит о снимке панели не знает.
func (h *AutolistHandler) SetHasRemovedBackupProbe(fn func(ctx context.Context) (bool, time.Time, error)) {
	h.hasRemovedBackup = fn
}

// SetLogger подключает журнал для диагностики отказов проверки снимка.
func (h *AutolistHandler) SetLogger(appLogger logging.AppLogger) {
	h.log = logging.NewScopedLogger(appLogger, logging.GroupRouting, logging.SubSingboxRouter)
}

// withRemovedBackup помечает статус флагом снимка; любая ошибка чтения —
// false без падения статуса (best-effort, как сам снимок).
func (h *AutolistHandler) withRemovedBackup(ctx context.Context, st *autolistdetect.Status) *autolistdetect.Status {
	if h.hasRemovedBackup == nil || st == nil {
		return st
	}
	exists, _, err := h.hasRemovedBackup(ctx)
	if err != nil {
		if h.log != nil {
			h.log.Warn("autolist-backup", "", "проверка снимка удалённых объектов не удалась: "+err.Error())
		}
		return st
	}
	st.HasRemovedBackup = exists
	return st
}

type autolistStatusResponse struct {
	AutolistStatus *autolistdetect.Status `json:"autolistStatus"`
}

func emptyAutolistStatus() *autolistdetect.Status {
	return &autolistdetect.Status{Protocols: []string{}, Candidates: []autolistdetect.Candidate{}, History: []autolistdetect.HistoryEntry{}}
}

// AutolistStatus returns process/capture state, candidates and history.
// @Summary Autolist satellite status
// @Tags autolist
// @Produce json
// @Security CookieAuth
// @Success 200 {object} autolistStatusResponse
// @Router /autolist/status [get]
func (h *AutolistHandler) Status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if h.svc == nil {
		response.Success(w, autolistStatusResponse{AutolistStatus: h.withRemovedBackup(r.Context(), emptyAutolistStatus())})
		return
	}
	status := h.svc.GetStatus(r.Context())
	response.Success(w, autolistStatusResponse{AutolistStatus: h.withRemovedBackup(r.Context(), &status)})
}

type AutolistControlRequest struct {
	Action string `json:"action" example:"enable"`
}

// AutolistControl persists and applies enable/disable intent.
// @Summary Autolist satellite control
// @Tags autolist
// @Accept json
// @Produce json
// @Security CookieAuth
// @Param body body AutolistControlRequest true "enable or disable"
// @Success 200 {object} autolistStatusResponse
// @Router /autolist/control [post]
func (h *AutolistHandler) Control(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.svc == nil {
		response.ErrorWithStatus(w, http.StatusNotFound, autolistdetect.ErrNotInstalled.Error(), "AUTOLIST_NOT_INSTALLED")
		return
	}
	var req AutolistControlRequest
	if err := decodeBody(r, &req); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	var enabled bool
	switch req.Action {
	case "enable":
		enabled = true
	case "disable":
		enabled = false
	default:
		response.BadRequest(w, "action must be enable or disable")
		return
	}
	if err := h.svc.SetEnabled(r.Context(), enabled); err != nil {
		if errors.Is(err, autolistdetect.ErrNotInstalled) {
			response.ErrorWithStatus(w, http.StatusNotFound, err.Error(), "AUTOLIST_NOT_INSTALLED")
			return
		}
		if errors.Is(err, autolistdetect.ErrNeedApiKey) {
			response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "AUTOLIST_NEED_API_KEY")
			return
		}
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "AUTOLIST_CONTROL_FAILED")
		return
	}
	status := h.svc.GetStatus(r.Context())
	response.Success(w, autolistStatusResponse{AutolistStatus: &status})
}

// AutolistInstall installs the satellite through opkg and confirms its status.
// @Summary Install Autolist satellite
// @Tags autolist
// @Produce json
// @Security CookieAuth
// @Success 200 {object} autolistStatusResponse
// @Router /autolist/install [post]
func (h *AutolistHandler) Install(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.svc == nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, "autolist installer is unavailable", "AUTOLIST_INSTALL_FAILED")
		return
	}
	if err := h.svc.Install(r.Context()); err != nil {
		if errors.Is(err, autolistdetect.ErrNeedApiKey) {
			response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "AUTOLIST_NEED_API_KEY")
			return
		}
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "AUTOLIST_INSTALL_FAILED")
		return
	}
	status := h.svc.GetStatus(r.Context())
	response.Success(w, autolistStatusResponse{AutolistStatus: &status})
}

// AutolistDismissRequest identifies a candidate by its stable ID.
type AutolistDismissRequest struct {
	ID string `json:"id" example:"domain|example.com|tcp|443"`
}

// AutolistDismiss hides a candidate through the operational proxy.
// @Summary Dismiss Autolist candidate
// @Tags autolist
// @Accept json
// @Produce json
// @Security CookieAuth
// @Param body body AutolistDismissRequest true "candidate ID"
// @Success 200 {object} OkResponse
// @Router /autolist/dismiss [post]
func (h *AutolistHandler) Dismiss(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.svc == nil {
		response.ErrorWithStatus(w, http.StatusConflict, "satellite is not running", "AUTOLIST_NOT_RUNNING")
		return
	}
	r.URL.Path = "/api/autolist/dismiss"
	autolistdetect.NewProxy(h.svc).ServeHTTP(w, r)
}

// AutolistProxyRequest selects an operational satellite endpoint.
type AutolistProxyRequest struct {
	Path string         `json:"path" example:"/config"`
	Body map[string]any `json:"body,omitempty"`
}

func autolistBody(v map[string]any) io.ReadCloser {
	data, err := json.Marshal(v)
	if err != nil {
		data = []byte("{}")
	}
	return io.NopCloser(bytes.NewReader(data))
}

// AutolistProxy forwards only whitelisted status/config/candidate actions.
// @Summary Autolist operational command
// @Tags autolist
// @Accept json
// @Produce json
// @Security CookieAuth
// @Param body body AutolistProxyRequest true "satellite path and optional body"
// @Success 200 {object} OkResponse
// @Router /autolist/proxy [post]
func (h *AutolistHandler) Proxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.svc == nil {
		response.ErrorWithStatus(w, http.StatusConflict, "satellite is not running", "AUTOLIST_NOT_RUNNING")
		return
	}
	var req AutolistProxyRequest
	if err := decodeBody(r, &req); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if req.Path == "" {
		response.BadRequest(w, "path required")
		return
	}
	r.URL.Path = "/api/autolist" + strings.TrimSuffix(req.Path, "/")
	if req.Body == nil {
		r.Method = http.MethodGet
		r.Body = http.NoBody
	} else {
		r.Body = autolistBody(req.Body)
		r.Header.Set("Content-Type", "application/json")
	}
	autolistdetect.NewProxy(h.svc).ServeHTTP(w, r)
}
