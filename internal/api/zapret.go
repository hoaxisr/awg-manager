package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/zapret"
)

// ZapretService — узкий контракт, который нужен хендлеру. Объявлен
// интерфейсом, а не используется *zapret.Service напрямую, чтобы тесты
// подставляли заглушку без касания файловой системы.
type ZapretService interface {
	GetStatus() zapret.Status
	Recheck() zapret.Status
	GetStrategies() (zapret.Strategies, error)
	ReorderStrategies(from, to int, revision string) (zapret.Strategies, error)
	GetHostlists() ([]zapret.HostlistInfo, error)
	ReadHostlist(id zapret.HostlistID) (zapret.HostlistID, string, string, error)
	WriteHostlist(id zapret.HostlistID, content, revision string) (zapret.HostlistID, string, error)
	Control(action, base string) error
}

// ZapretHandler — HTTP-обвязка над zapret.
//
// AWGM не устанавливает zapret: пакет ставит пользователь. После правки
// списка запущенные демоны перечитывают конфиг без изменения firewall. Хендлер читает состояние,
// отдаёт разобранные стратегии и списки, и пишет только то, что
// принадлежит пользователю.
type ZapretHandler struct {
	svc ZapretService
	bus *events.Bus
	log *logging.ScopedLogger
}

// NewZapretHandler создаёт хендлер.
func NewZapretHandler(svc ZapretService, appLogger logging.AppLogger) *ZapretHandler {
	return &ZapretHandler{
		svc: svc,
		log: logging.NewScopedLogger(appLogger, logging.GroupRouting, logging.SubZapret),
	}
}

// SetEventBus подключает шину инвалидации.
func (h *ZapretHandler) SetEventBus(bus *events.Bus) { h.bus = bus }

func (h *ZapretHandler) publishZapretUpdated(reason string) {
	if h.bus == nil {
		return
	}
	h.bus.PublishInvalidated(events.ResourceRoutingZapretStatus, reason)
}

// zapretErrStatus переводит ошибку пакета в HTTP-код. Отсутствие zapret
// — это не поломка: 404 с понятным телом, чтобы UI показал карточку
// «не установлено», а не красную ошибку.
func zapretErrStatus(err error) (int, string, string) {
	if errors.Is(err, zapret.ErrConflict) {
		return http.StatusConflict, err.Error(), "ZAPRET_CONFLICT"
	}
	if errors.Is(err, zapret.ErrNotInstalled) {
		return http.StatusNotFound, err.Error(), "ZAPRET_NOT_INSTALLED"
	}
	return http.StatusBadRequest, err.Error(), "ZAPRET_ERROR"
}

// zapretStatusResponse — тело ответа статуса.
type zapretStatusResponse struct {
	ZapretStatus *zapret.Status `json:"zapretStatus"`
}

// Status returns zapret detection status.
//
//	@Summary		Zapret status
//	@Tags			zapret
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	zapretStatusResponse
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/zapret/status [get]
func (h *ZapretHandler) Status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if h.svc == nil {
		response.Success(w, zapretStatusResponse{ZapretStatus: notInstalledStatus()})
		return
	}
	response.Success(w, zapretStatusResponse{ZapretStatus: ptrStatus(h.svc.GetStatus())})
}

// Control starts/stops/restarts zapret via its own init script.
//
//	@Summary		Zapret control
//	@Tags			zapret
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	zapretStatusResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		409	{object}	APIErrorEnvelope
//	@Failure		404	{object}	APIErrorEnvelope
//	@Router			/zapret/control [post]
func (h *ZapretHandler) Control(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.svc == nil {
		response.ErrorWithStatus(w, http.StatusNotFound, zapret.ErrNotInstalled.Error(), "ZAPRET_NOT_INSTALLED")
		return
	}
	req, ok := parseJSON[zapretControlRequest](w, r, http.MethodPost)
	if !ok {
		return
	}
	if err := h.svc.Control(req.Action, req.Base); err != nil {
		status, msg, code := zapretErrStatus(err)
		response.ErrorWithStatus(w, status, msg, code)
		return
	}
	h.publishZapretUpdated("control-" + req.Action)
	response.Success(w, zapretStatusResponse{ZapretStatus: ptrStatus(h.svc.Recheck())})
}

type zapretControlRequest struct {
	Base   string `json:"base"`
	Action string `json:"action"`
}

// Strategies returns parsed strategy profiles in priority order.
//
//	@Summary		Zapret strategies
//	@Tags			zapret
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	zapret.Strategies
//	@Failure		409	{object}	APIErrorEnvelope
//	@Failure		404	{object}	APIErrorEnvelope
//	@Router			/zapret/strategies [get]
func (h *ZapretHandler) Strategies(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if h.svc == nil {
		response.ErrorWithStatus(w, http.StatusNotFound, zapret.ErrNotInstalled.Error(), "ZAPRET_NOT_INSTALLED")
		return
	}
	got, err := h.svc.GetStrategies()
	if err != nil {
		status, msg, code := zapretErrStatus(err)
		response.ErrorWithStatus(w, status, msg, code)
		return
	}
	response.Success(w, got)
}

// ReorderStrategy moves one card. Order is priority in nfqws2, so this
// rewrites the file rather than reordering in memory only.
//
//	@Summary		Reorder zapret strategy
//	@Tags			zapret
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	zapret.Strategies
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		409	{object}	APIErrorEnvelope
//	@Failure		404	{object}	APIErrorEnvelope
//	@Router			/zapret/strategies/reorder [post]
func (h *ZapretHandler) ReorderStrategy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.svc == nil {
		response.ErrorWithStatus(w, http.StatusNotFound, zapret.ErrNotInstalled.Error(), "ZAPRET_NOT_INSTALLED")
		return
	}
	req, ok := parseJSON[zapretReorderRequest](w, r, http.MethodPost)
	if !ok {
		return
	}
	got, err := h.svc.ReorderStrategies(req.From, req.To, req.Revision)
	if err != nil {
		status, msg, code := zapretErrStatus(err)
		response.ErrorWithStatus(w, status, msg, code)
		return
	}
	h.publishZapretUpdated("reorder-strategy")
	response.Success(w, got)
}

type zapretReorderRequest struct {
	Revision string `json:"revision"`
	From     int    `json:"from"`
	To       int    `json:"to"`
}

// Hostlists returns descriptions of all known zapret lists.
//
//	@Summary		Zapret hostlists
//	@Tags			zapret
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	zapretHostlistsResponse
//	@Failure		409	{object}	APIErrorEnvelope
//	@Failure		404	{object}	APIErrorEnvelope
//	@Router			/zapret/hostlists [get]
func (h *ZapretHandler) Hostlists(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if h.svc == nil {
		response.ErrorWithStatus(w, http.StatusNotFound, zapret.ErrNotInstalled.Error(), "ZAPRET_NOT_INSTALLED")
		return
	}
	lists, err := h.svc.GetHostlists()
	if err != nil {
		status, msg, code := zapretErrStatus(err)
		response.ErrorWithStatus(w, status, msg, code)
		return
	}
	response.Success(w, zapretHostlistsResponse{Hostlists: lists})
}

type zapretHostlistsResponse struct {
	Hostlists []zapret.HostlistInfo `json:"hostlists"`
}

// Hostlist returns the content of a user-owned list.
//
//	@Summary		Zapret hostlist content
//	@Tags			zapret
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	zapretHostlistResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		409	{object}	APIErrorEnvelope
//	@Failure		404	{object}	APIErrorEnvelope
//	@Router			/zapret/hostlist [get]
func (h *ZapretHandler) Hostlist(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if h.svc == nil {
		response.ErrorWithStatus(w, http.StatusNotFound, zapret.ErrNotInstalled.Error(), "ZAPRET_NOT_INSTALLED")
		return
	}
	id, err := zapretHostlistID(r.URL.Query().Get("id"))
	if err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	gotID, path, content, err := h.svc.ReadHostlist(id)
	if err != nil {
		status, msg, code := zapretErrStatus(err)
		response.ErrorWithStatus(w, status, msg, code)
		return
	}
	response.Success(w, zapretHostlistResponse{ID: gotID, Path: path, Content: content, Revision: zapret.Revision(path, content)})
}

// SaveHostlist writes a user-owned list. Generated lists are refused by
// the service: they are rebuilt by zapret's own jobs.
//
//	@Summary		Save zapret hostlist
//	@Tags			zapret
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	zapretHostlistResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		409	{object}	APIErrorEnvelope
//	@Failure		404	{object}	APIErrorEnvelope
//	@Router			/zapret/hostlist/save [post]
func (h *ZapretHandler) SaveHostlist(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.svc == nil {
		response.ErrorWithStatus(w, http.StatusNotFound, zapret.ErrNotInstalled.Error(), "ZAPRET_NOT_INSTALLED")
		return
	}
	req, ok := parseJSON[zapretHostlistWriteRequest](w, r, http.MethodPost)
	if !ok {
		return
	}
	id, err := zapretHostlistID(string(req.ID))
	if err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	gotID, path, err := h.svc.WriteHostlist(id, req.Content, req.Revision)
	var applyErr *zapret.HostlistApplyError
	if errors.As(err, &applyErr) {
		h.publishZapretUpdated("save-hostlist")
		response.Success(w, zapretHostlistResponse{ID: gotID, Path: path, RestartError: applyErr.Error()})
		return
	}
	if err != nil {
		status, msg, code := zapretErrStatus(err)
		response.ErrorWithStatus(w, status, msg, code)
		return
	}
	h.publishZapretUpdated("save-hostlist")
	response.Success(w, zapretHostlistResponse{ID: gotID, Path: path})
}

type zapretHostlistResponse struct {
	RestartError string            `json:"restartError,omitempty"`
	Revision     string            `json:"revision"`
	ID           zapret.HostlistID `json:"id"`
	Path         string            `json:"path"`
	Content      string            `json:"content,omitempty"`
}

type zapretHostlistWriteRequest struct {
	Revision string            `json:"revision"`
	ID       zapret.HostlistID `json:"id"`
	Content  string            `json:"content"`
}

// zapretHostlistID валидирует идентификатор списка: путь приходит от
// клиента, а ID задаёт сервер, поэтому произвольное имя файла сюда не
// проскочит.
// errBadHostlistID отделяем, чтобы отдать конкретный код, а не общий
// ZAPRET_ERROR: клиент послал мусор, это его ошибка, а не состояние
// роутера.
var errBadHostlistID = errors.New("unknown zapret hostlist")

func zapretHostlistID(raw string) (zapret.HostlistID, error) {
	switch zapret.HostlistID(raw) {
	case zapret.HostlistUser, zapret.HostlistExclude, zapret.HostlistAuto, zapret.HostlistMain:
		return zapret.HostlistID(raw), nil
	}
	return "", fmt.Errorf("%w: %q", errBadHostlistID, raw)
}

func notInstalledStatus() *zapret.Status {
	return &zapret.Status{ProcessState: zapret.StateNotInstalled}
}

func ptrStatus(st zapret.Status) *zapret.Status { return &st }
