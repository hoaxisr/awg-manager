package presets

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed defaults.json
var defaultsJSON []byte

// LoadBuiltins parses the embedded default catalog and forces Origin=builtin.
func LoadBuiltins() ([]Preset, error) {
	var ps []Preset
	if err := json.Unmarshal(defaultsJSON, &ps); err != nil {
		return nil, fmt.Errorf("parse embedded preset defaults: %w", err)
	}
	for i := range ps {
		ps[i].Origin = OriginBuiltin
	}
	return ps, nil
}
