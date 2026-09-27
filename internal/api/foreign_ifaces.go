package api

import "errors"

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
