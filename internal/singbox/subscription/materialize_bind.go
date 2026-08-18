package subscription

import (
	"context"
	"encoding/json"
	"strings"
)

// materializeMemberOutbound patches tag and optional bind_interface onto a
// parsed member outbound before it is committed to 40-subscriptions.json.
func materializeMemberOutbound(ctx context.Context, validator BindInterfaceValidator, raw []byte, tag, bindInterface string) []byte {
	var ob map[string]any
	if json.Unmarshal(raw, &ob) != nil {
		return replaceTag(raw, tag)
	}
	ob["tag"] = tag
	if bind := strings.TrimSpace(bindInterface); bind != "" {
		if validator != nil && validator.ValidateBindInterface(ctx, bind) != nil {
			// Drop invalid/missing bind_interface to prevent FATAL crash in sing-box
			delete(ob, "bind_interface")
		} else {
			ob["bind_interface"] = bind
		}
	} else {
		delete(ob, "bind_interface")
	}
	out, _ := json.Marshal(ob)
	return out
}
