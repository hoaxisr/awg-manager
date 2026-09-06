package localdeps

import "fmt"

// asString renders any JSON scalar (string, number, time) as a string.
// Used for the loosely typed system-info map; every typed shape is mapped
// field by field instead of through a JSON round-trip.
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
