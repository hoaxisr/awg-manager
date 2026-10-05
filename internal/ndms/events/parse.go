package events

import (
	"fmt"
	"math"
	"net/url"
	"strconv"
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
	// t= — диагностика (см. Event.ScriptUptime): нечисло не роняет строку.
	if t, err := strconv.ParseFloat(v.Get("t"), 64); err == nil && t > 0 && !math.IsInf(t, 0) {
		e.ScriptUptime = t
	}
	switch e.Type {
	case EventIfLayerChanged, EventIfCreated, EventIfDestroyed, EventIfIPChanged:
		return e, nil
	}
	return Event{}, fmt.Errorf("unknown hook type: %q", string(e.Type))
}
