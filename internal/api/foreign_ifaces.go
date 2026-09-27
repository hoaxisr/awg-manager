package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/response"
)

// ErrForeignIfaceRejected — отметку «Сторонний интерфейс» запрещает граница
// (имя, владелец номера, наше имя, интерфейс роутера). Ручка отвечает 400.
var ErrForeignIfaceRejected = errors.New("отметка стороннего интерфейса отклонена")

// ForeignIfaceCandidate — интерфейс, который можно отметить как сторонний.
type ForeignIfaceCandidate struct {
	Name  string `json:"name" example:"opkgtun7"`
	Label string `json:"label" example:"csqtt"`
	Kind  string `json:"kind" enums:"opkgtun,kernel"`
	Up    bool   `json:"up"`
}

// ForeignIfaceMarker — сервис отметки (cmd/awg-manager/foreign_iface.go).
type ForeignIfaceMarker interface {
	Mark(ctx context.Context, name string) error
	Unmark(ctx context.Context, name string) error
	Candidates(ctx context.Context) ([]ForeignIfaceCandidate, error)
}

type ForeignIfaceRequest struct {
	Name string `json:"name" example:"opkgtun7"`
}

type ForeignIfaceHandler struct {
	m                 ForeignIfaceMarker
	publishTunnelList func(ctx context.Context)
}

func NewForeignIfaceHandler(m ForeignIfaceMarker) *ForeignIfaceHandler {
	return &ForeignIfaceHandler{m: m}
}

// SetTunnelListPublisher — отметка меняет список внешних туннелей
// (значок, кнопка удаления), публикуем его как ручка удаления сирот.
func (h *ForeignIfaceHandler) SetTunnelListPublisher(fn func(ctx context.Context)) {
	h.publishTunnelList = fn
}

// Candidates — интерфейсы, которые можно отметить как сторонние.
//
//	@Summary		Кандидаты в сторонние интерфейсы
//	@Tags			interfaces
//	@Produce		json
//	@Success		200	{object}	APIEnvelope{data=[]ForeignIfaceCandidate}
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/interfaces/foreign/candidates [get]
func (h *ForeignIfaceHandler) Candidates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	list, err := h.m.Candidates(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, list)
}

// Mark — отметить интерфейс другой программы как сторонний.
//
//	@Summary		Отметить сторонний интерфейс
//	@Tags			interfaces
//	@Accept			json
//	@Produce		json
//	@Param			body	body		ForeignIfaceRequest	true	"Имя интерфейса ядра"
//	@Success		200		{object}	APIEnvelope
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/interfaces/foreign/mark [post]
func (h *ForeignIfaceHandler) Mark(w http.ResponseWriter, r *http.Request) {
	h.apply(w, r, h.m.Mark)
}

// Unmark — снять отметку; выходы и маршруты не трогаются.
//
//	@Summary		Снять отметку стороннего интерфейса
//	@Tags			interfaces
//	@Accept			json
//	@Produce		json
//	@Param			body	body		ForeignIfaceRequest	true	"Имя интерфейса ядра"
//	@Success		200		{object}	APIEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/interfaces/foreign/unmark [post]
func (h *ForeignIfaceHandler) Unmark(w http.ResponseWriter, r *http.Request) {
	h.apply(w, r, h.m.Unmark)
}

func (h *ForeignIfaceHandler) apply(w http.ResponseWriter, r *http.Request, op func(context.Context, string) error) {
	req, ok := parseJSON[ForeignIfaceRequest](w, r, http.MethodPost)
	if !ok {
		return
	}
	if err := op(r.Context(), req.Name); err != nil {
		if errors.Is(err, ErrForeignIfaceRejected) {
			response.Error(w, err.Error(), "FOREIGN_REJECTED")
			return
		}
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]bool{"ok": true})
	if h.publishTunnelList != nil {
		h.publishTunnelList(r.Context())
	}
}
