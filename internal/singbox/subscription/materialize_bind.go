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
func materializeMemberOutbound(raw []byte, tag, bindInterface string, realityMLKEM bool, logWarn func(action, target, msg string)) []byte {
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
	// Выключенная опция флаг не снимает: его мог прислать сам провайдер в ссылке.
	if realityMLKEM {
		enableRealityMLKEM(ob)
	}
	out, err := json.Marshal(ob)
	if err != nil {
		return replaceTag(raw, tag)
	}
	return out
}

// enableRealityMLKEM ставит флаг у outbound с включённым Reality и заменяет его
// отпечаток на chrome: в utls 1.8.7 шара X25519MLKEM768 есть только у него, с
// другим отпечатком флаг ничего не даёт.
func enableRealityMLKEM(ob map[string]any) {
	tls, _ := ob["tls"].(map[string]any)
	reality, _ := tls["reality"].(map[string]any)
	if reality["enabled"] != true {
		return
	}
	reality["support_x25519mlkem768"] = true
	tls["utls"] = map[string]any{"enabled": true, "fingerprint": "chrome"}
}

// clearRealityMLKEM снимает флаг с копии тела: тела из SubscriptionOutbounds
// общие с конфигом и снимком батча, менять их на месте нельзя. Отпечаток не
// возвращается: прежнее значение знает только источник.
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
