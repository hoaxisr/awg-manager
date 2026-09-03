package localdeps

import (
	"encoding/json"
	"fmt"
)

// convert copies src into a value of type T through a JSON round-trip.
// The mcp package's types carry the same json tags as the daemon's
// storage/service types, so this is the whole mapping for pass-through
// shapes and keeps localdeps free of field-by-field copies.
func convert[T any](src any) (T, error) {
	var out T
	raw, err := json.Marshal(src)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("convert %T: %w", out, err)
	}
	return out, nil
}

// asString renders any JSON scalar (string, number, time) as a string.
func asString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	default:
		return fmt.Sprint(x)
	}
}
