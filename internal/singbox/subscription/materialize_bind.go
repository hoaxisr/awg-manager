package subscription

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var sysClassNet = "/sys/class/net" //nolint:gochecknoglobals // overridden in tests

// materializeMemberOutbound patches tag and optional bind_interface onto a
// parsed member outbound before it is committed to 40-subscriptions.json.
// If bindInterface is specified but does not exist in the kernel, it is dropped
// (fail-open to avoid sing-box FATAL) and a warning is logged.
func materializeMemberOutbound(raw []byte, tag, bindInterface string, logWarn func(action, target, msg string)) []byte {
	var ob map[string]any
	if json.Unmarshal(raw, &ob) != nil {
		return replaceTag(raw, tag)
	}
	ob["tag"] = tag
	if bind := strings.TrimSpace(bindInterface); bind != "" {
		if !kernelInterfaceExists(bind) {
			// Drop missing interface to prevent FATAL crash in sing-box (#709)
			delete(ob, "bind_interface")
			if logWarn != nil {
				logWarn("subscription-bind", tag, fmt.Sprintf("bind_interface %q does not exist in kernel, dropped to prevent sing-box crash", bind))
			}
		} else {
			ob["bind_interface"] = bind
		}
	} else {
		delete(ob, "bind_interface")
	}
	out, err := json.Marshal(ob)
	if err != nil {
		return replaceTag(raw, tag)
	}
	return out
}

func kernelInterfaceExists(name string) bool {
	if name == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(sysClassNet, name))
	return err == nil
}
