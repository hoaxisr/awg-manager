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
	// Копия внешнего среза: вызывающий волен править элементы у себя, и
	// такая правка не должна доезжать до кэша. Вложенные срезы разделяются —
	// сегодня их никто не мутирует (Merge только читает builtins).
	out := make([]Preset, len(builtinsVal))
	copy(out, builtinsVal)
	return out, nil
}
