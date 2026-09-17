package presets

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"
)

//go:embed defaults.json
var defaultsJSON []byte

// LoadBuiltins parses the embedded default catalog and forces Origin=builtin.
// builtinsOnce разбирает вшитый JSON ОДИН раз за жизнь процесса: данные
// неизменны, а разбор стоил заметного времени на каждый GET /api/presets —
// в профиле на mipsel это была отдельная статья расхода.
var (
	builtinsOnce sync.Once
	builtinsVal  []Preset
	builtinsErr  error
)

func LoadBuiltins() ([]Preset, error) {
	builtinsOnce.Do(func() {
		var ps []Preset
		if err := json.Unmarshal(defaultsJSON, &ps); err != nil {
			builtinsErr = fmt.Errorf("parse embedded preset defaults: %w", err)
			return
		}
		for i := range ps {
			ps[i].Origin = OriginBuiltin
		}
		builtinsVal = ps
	})
	if builtinsErr != nil {
		return nil, builtinsErr
	}
	// Копия ВНЕШНЕГО среза: вызывающий волен править элементы у себя, и такая
	// правка не доедет до кэша.
	//
	// Глубже копия не идёт, и острый край тут не срезы, а указатели: Engines —
	// это *DNSEngine, *SingboxEngine, *HydraRouteEngine (types.go), общие для
	// кэша и для всех вызывающих. Строка вида
	// `p.Engines.Singbox.Action = "reject"` испортила бы каталог на весь
	// процесс И дала бы гонку между параллельными HTTP-запросами. Сегодня
	// таких мутаторов нет (Merge, presetFromUnified и api/presets только
	// читают); появится — копировать придётся глубоко.
	out := make([]Preset, len(builtinsVal))
	copy(out, builtinsVal)
	return out, nil
}
