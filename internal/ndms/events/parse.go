package events

import (
	"fmt"
	"net/url"
)

// ParseHookForm разбирает строку хука (форма application/x-www-form-urlencoded,
// которую пишет хук-скрипт) в Event. Неизвестный тип — ошибка с его текстом.
//
// up/connected форвардер тоже присылал, и мы их НЕ разбираем: состояние
// линка берётся из iflayerchanged, а этим полям доверять нельзя
// (InterfaceStore.OnIPChanged). Лишние поля формы безвредны.
func ParseHookForm(v url.Values) (Event, error) {
	e := Event{
		Type:       EventType(v.Get("type")),
		ID:         v.Get("id"),
		SystemName: v.Get("system_name"),
		Layer:      v.Get("layer"),
		Level:      v.Get("level"),
		Address:    v.Get("address"),
	}
	switch e.Type {
	case EventIfLayerChanged, EventIfCreated, EventIfDestroyed, EventIfIPChanged:
		return e, nil
	}
	return Event{}, fmt.Errorf("unknown hook type: %q", string(e.Type))
}
