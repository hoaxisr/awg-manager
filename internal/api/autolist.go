package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/autolistdetect"
	"github.com/hoaxisr/awg-manager/internal/response"
)

// AutolistHandler отдаёт UI состояние сателлита awgmautolist и
// проксирует его команды (dismiss/add/config). Отсутствие сателлита —
// штатное состояние, а не ошибка: его ставят кнопкой (Install).
type AutolistHandler struct {
	svc *autolistdetect.Service
}

func NewAutolistHandler(svc *autolistdetect.Service) *AutolistHandler {
	return &AutolistHandler{svc: svc}
}

type autolistStatusResponse struct {
	AutolistStatus *autolistdetect.Status `json:"autolistStatus"`
}

func notRunningStatus() *autolistdetect.Status {
	return &autolistdetect.Status{Candidates: []autolistdetect.Candidate{}, History: []autolistdetect.HistoryEntry{}}
}

// Status возвращает состояние сателлита awgmautolist.
//
//	@Summary		Autolist satellite status
//	@Description	Состояние сателлита awgmautolist: жив ли процесс, режим auto, целевой outbound и таблица кандидатов. Сателлит ставится отдельно; отсутствие процесса — не ошибка, а пустой статус.
//	@Tags			autolist
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	autolistStatusResponse
//	@Router			/autolist/status [get]
func (h *AutolistHandler) Status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if h.svc == nil {
		response.Success(w, autolistStatusResponse{AutolistStatus: notRunningStatus()})
		return
	}
	response.Success(w, autolistStatusResponse{AutolistStatus: ptrAutolistStatus(h.svc.GetStatus(r.Context()))})
}

func ptrAutolistStatus(s autolistdetect.Status) *autolistdetect.Status {
	return &s
}

// AutolistControlRequest — сохраняемое включение/отключение сателлита.
type AutolistControlRequest struct {
	Action string `json:"action" example:"disable"`
}

// Control включает или отключает сателлит через его init script.
//
//	@Summary		Autolist satellite control
//	@Description	Сохраняет намерение enable/disable и запускает или останавливает сателлит. Отключение переживает перезагрузку.
//	@Tags			autolist
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body	AutolistControlRequest	true	"enable или disable"
//	@Success		200	{object}	autolistStatusResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		404	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/autolist/control [post]
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
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "AUTOLIST_CONTROL_FAILED")
		return
	}
	response.Success(w, autolistStatusResponse{AutolistStatus: ptrAutolistStatus(h.svc.GetStatus(r.Context()))})
}

// Install ставит пакет сателлита из entware-фида роутера и ждёт его запуска.
//
//	@Summary		Autolist satellite install
//	@Description	Ставит пакет awgmautolist через opkg (фид repo.hoaxisr.ru), кладёт сателлиту API-ключ и ждёт, пока он ответит. При включённой авторизации требует созданный API-ключ.
//	@Tags			autolist
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	autolistStatusResponse
//	@Failure		409	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/autolist/install [post]
func (h *AutolistHandler) Install(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
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
	response.Success(w, autolistStatusResponse{AutolistStatus: ptrAutolistStatus(h.svc.GetStatus(r.Context()))})
}

// AutolistDismissRequest — тело POST /autolist/dismiss.
type AutolistDismissRequest struct {
	Domain string `json:"domain" example:"blocked.example.com"`
}

// Dismiss прячет кандидата сателлита (тело {domain} пересылается сателлиту).
//
//	@Summary		Autolist dismiss candidate
//	@Description	Прячет кандидата сателлита awgmautolist на сутки.
//	@Tags			autolist
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body	AutolistDismissRequest	true	"Домен кандидата"
//	@Success		200	{object}	OkResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		502	{object}	APIErrorEnvelope
//	@Router			/autolist/dismiss [post]
func (h *AutolistHandler) Dismiss(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.svc == nil {
		response.ErrorWithStatus(w, http.StatusConflict, "сателлит не найден", "AUTOLIST_NOT_RUNNING")
		return
	}
	r.URL.Path = "/api/autolist/dismiss"
	autolistdetect.NewProxy(h.svc).ServeHTTP(w, r)
}

// AutolistProxyRequest — тело POST /autolist/proxy: какой путь сателлита
// вызвать и с каким телом. Белый список путей проверяет Proxy.
type AutolistProxyRequest struct {
	// Path — путь сателлита: /add, /probe, /exclude, /restore, /config, /forget или /dismiss.
	Path string `json:"path" example:"/add"`
	// Body — произвольный JSON-тело вызова (опционально).
	Body map[string]any `json:"body,omitempty"`
}

// readCloserOf — тело запроса из map (JSON) как ReadCloser.
func readCloserOf(v map[string]any) io.ReadCloser {
	if v == nil {
		v = map[string]any{}
	}
	b, err := json.Marshal(v)
	if err != nil {
		b = []byte("{}")
	}
	return io.NopCloser(bytes.NewReader(b))
}

// Proxy выполняет команду на сателлите (add/config/forget) и возвращает его
// ответ как есть. Список путей ограничен белым списком Proxy; команды,
// меняющие состояние (add/config), фронт вызывает на expert-уровне.
//
//	@Summary		Autolist satellite command
//	@Description	Проксирует команду к awgmautolist: add (добавить домены отдельными правилами), config (прочитать/применить конфигурацию), forget (убрать кандидата). Тело ответа — ответ сателлита.
//	@Tags			autolist
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body	AutolistProxyRequest	true	"Путь сателлита и тело"
//	@Success		200	{object}	OkResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		502	{object}	APIErrorEnvelope
//	@Router			/autolist/proxy [post]
func (h *AutolistHandler) Proxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.svc == nil {
		response.ErrorWithStatus(w, http.StatusConflict, "сателлит не найден", "AUTOLIST_NOT_RUNNING")
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
	// Переписываем путь; тело восстанавливаем КОНВЕРТОМ {"path","body"} —
	// его распаковывает autolistdetect.Proxy (он же решает POST/GET по
	// наличию body: без body /config уходит сателлиту как GET-чтение).
	r.URL.Path = "/api/autolist" + strings.TrimSuffix(req.Path, "/")
	r.Body = readCloserOf(map[string]any{"path": req.Path, "body": req.Body})
	r.Header.Set("Content-Type", "application/json")
	autolistdetect.NewProxy(h.svc).ServeHTTP(w, r)
}
