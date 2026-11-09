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
// parsed member outbound before it is committed to 13-subscriptions.json.
// If bindInterface is specified but does not exist in the kernel, it is dropped
// (fail-open to avoid sing-box FATAL) and a warning is logged.
func materializeMemberOutbound(raw []byte, tag, bindInterface string, mlkemOff bool, logWarn func(action, target, msg string)) []byte {
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
	// Снятая опция флаг не трогает: явный отказ мог прислать сам провайдер.
	if mlkemOff {
		disableRealityMLKEM(ob)
	}
	out, err := json.Marshal(ob)
	if err != nil {
		return replaceTag(raw, tag)
	}
	return out
}

// disableRealityMLKEM вырезает X25519MLKEM768 у outbound с включённым Reality:
// форк, как Xray, шлёт её по умолчанию, а ClientHello ~1,7 КБ на части сетей
// глушат (#944).
func disableRealityMLKEM(ob map[string]any) {
	tls, _ := ob["tls"].(map[string]any)
	reality, _ := tls["reality"].(map[string]any)
	if reality["enabled"] != true {
		return
	}
	reality["support_x25519mlkem768"] = false
}

// clearRealityMLKEM снимает флаг с копии тела: тела из SubscriptionOutbounds
// общие с конфигом и снимком батча, менять их на месте нельзя.
func clearRealityMLKEM(raw []byte) []byte {
	var ob map[string]any
	if json.Unmarshal(raw, &ob) != nil {
		return raw
	}
	tls, _ := ob["tls"].(map[string]any)
	reality, _ := tls["reality"].(map[string]any)
	delete(reality, "support_x25519mlkem768")
	out, err := json.Marshal(ob)
	if err != nil {
		return raw
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
