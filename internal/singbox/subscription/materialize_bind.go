package subscription

import (
	"encoding/json"
	"strings"
)

// materializeMemberOutbound patches tag and optional bind_interface onto a
// parsed member outbound before it is committed to 40-subscriptions.json.
func materializeMemberOutbound(raw []byte, tag, bindInterface string) []byte {
	var ob map[string]any
	if json.Unmarshal(raw, &ob) != nil {
		return replaceTag(raw, tag)
	}
	ob["tag"] = tag
	if bind := strings.TrimSpace(bindInterface); bind != "" {
		ob["bind_interface"] = bind
	} else {
		delete(ob, "bind_interface")
	}
	out, _ := json.Marshal(ob)
	return out
}
