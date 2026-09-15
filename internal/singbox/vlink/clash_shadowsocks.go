package vlink

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// mapClashShadowsocks converts a Clash YAML "type: ss" proxy into a
// ParsedOutbound. Required: server, port, cipher, password. Cipher "auto"
// is rejected — sing-box requires an explicit method.
//
// Plugins handled in v1: obfs (Clash) → obfs-local (sing-box), v2ray-plugin
// passes through with plugin name preserved.
//
// Field reference: https://wiki.metacubex.one/en/config/proxies/ss/
func mapClashShadowsocks(p map[string]any) (*ParsedOutbound, error) {
	host := asString(p["server"])
	if host == "" {
		return nil, errors.New("clash shadowsocks: missing server")
	}
	portN, ok := asInt(p["port"])
	if !ok || portN <= 0 || portN > 65535 {
		return nil, errors.New("clash shadowsocks: missing or invalid port")
	}
	cipher := asString(p["cipher"])
	if cipher == "" {
		return nil, errors.New("clash shadowsocks: missing cipher")
	}
	if strings.EqualFold(cipher, "auto") {
		return nil, errors.New("clash shadowsocks: unsupported cipher 'auto', specify explicit method")
	}
	password := asString(p["password"])
	if password == "" {
		return nil, errors.New("clash shadowsocks: missing password")
	}

	out := map[string]any{
		"type":        "shadowsocks",
		"server":      host,
		"server_port": portN,
		"method":      cipher,
		"password":    password,
	}

	if plugin := asString(p["plugin"]); plugin != "" {
		opts := nestedMap(p, "plugin-opts")
		// Сначала отказ (имя вне двух известных валит применение всей
		// конфигурации — см. checkSSPlugin), затем switch ровно по тем двум,
		// что остались: ветки default с прокидыванием больше нет.
		canonical := canonicalSSPlugin(plugin)
		if err := checkSSPlugin(canonical); err != nil {
			return nil, err
		}
		switch canonical {
		case "obfs-local":
			out["plugin"] = "obfs-local"
			out["plugin_opts"] = serialiseObfsOpts(opts)
		case "v2ray-plugin":
			out["plugin"] = "v2ray-plugin"
			out["plugin_opts"] = serialiseV2rayOpts(opts)
		}
	}

	tag := fmt.Sprintf("ss-%s-%d", host, portN)
	out["tag"] = tag

	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return &ParsedOutbound{
		Tag:      tag,
		Protocol: "shadowsocks",
		Server:   host,
		Port:     uint16(portN),
		Outbound: raw,
		Label:    asString(p["name"]),
	}, nil
}

func serialiseObfsOpts(opts map[string]any) string {
	var parts []string
	if mode := asString(opts["mode"]); mode != "" {
		parts = append(parts, "obfs="+mode)
	}
	if host := asString(opts["host"]); host != "" {
		parts = append(parts, "obfs-host="+host)
	}
	return strings.Join(parts, ";")
}

func serialiseV2rayOpts(opts map[string]any) string {
	var parts []string
	if mode := asString(opts["mode"]); mode != "" {
		parts = append(parts, "mode="+mode)
	}
	if host := asString(opts["host"]); host != "" {
		parts = append(parts, "host="+host)
	}
	if path := asString(opts["path"]); path != "" {
		parts = append(parts, "path="+path)
	}
	if asBool(opts["tls"]) {
		parts = append(parts, "tls")
	}
	return strings.Join(parts, ";")
}
