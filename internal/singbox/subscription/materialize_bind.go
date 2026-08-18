package subscription

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

var sysClassNet = "/sys/class/net" //nolint:gochecknoglobals // overridden in tests

// materializeMemberOutbound patches tag and optional bind_interface onto a
// parsed member outbound before it is committed to 40-subscriptions.json.
func materializeMemberOutbound(ctx context.Context, _ BindInterfaceValidator, raw []byte, tag, bindInterface string) []byte {
	var ob map[string]any
	if json.Unmarshal(raw, &ob) != nil {
		return replaceTag(raw, tag)
	}
	ob["tag"] = tag
	if bind := strings.TrimSpace(bindInterface); bind != "" {
		if !kernelInterfaceExists(bind) {
			// Drop missing interface to prevent FATAL crash in sing-box
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

func kernelInterfaceExists(name string) bool {
	if name == "" {
		return false
	}
	root := sysClassNet
	if root == "" {
		root = "/sys/class/net"
	}
	_, err := os.Stat(filepath.Join(root, name))
	return err == nil
}
