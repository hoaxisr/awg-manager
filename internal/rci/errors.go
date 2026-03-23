package rci

import "encoding/json"

// ExtractError recursively searches RCI JSON response for
// {"status":"error","message":"..."}.
// NDMS returns HTTP 200 even on errors — the error is inside the JSON body.
// Returns the error message if found, empty string otherwise.
func ExtractError(data []byte) string {
	var raw any
	if json.Unmarshal(data, &raw) != nil {
		return ""
	}
	return findError(raw)
}

func findError(v any) string {
	switch val := v.(type) {
	case map[string]any:
		if s, ok := val["status"]; ok {
			if str, ok := s.(string); ok && str == "error" {
				if msg, ok := val["message"].(string); ok {
					return msg
				}
				return "unknown RCI error"
			}
		}
		if arr, ok := val["status"].([]any); ok {
			for _, item := range arr {
				if msg := findError(item); msg != "" {
					return msg
				}
			}
		}
		for _, child := range val {
			if msg := findError(child); msg != "" {
				return msg
			}
		}
	case []any:
		for _, item := range val {
			if msg := findError(item); msg != "" {
				return msg
			}
		}
	}
	return ""
}
