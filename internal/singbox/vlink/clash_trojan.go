package vlink

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// mapClashTrojan converts a Clash YAML "type: trojan" proxy entry into a
// ParsedOutbound. Required: server, port, password.
//
// Trojan in Clash always implies TLS — even if `tls:` is omitted, the
// transport is TLS. We force security=tls into the synthetic url.Values
// before delegating to BuildStreamFromQuery.
//
// Field reference: https://wiki.metacubex.one/en/config/proxies/trojan/
func mapClashTrojan(p map[string]any) (*ParsedOutbound, error) {
	host := asString(p["server"])
	if host == "" {
		return nil, errors.New("clash trojan: missing server")
	}
	portN, ok := asInt(p["port"])
	if !ok || portN <= 0 || portN > 65535 {
		return nil, errors.New("clash trojan: missing or invalid port")
	}
	password := asString(p["password"])
	if password == "" {
		return nil, errors.New("clash trojan: missing password")
	}

	q := clashFieldsToValues(p)
	// mihomo trojan, в отличие от vless, без заголовка Host шлёт в ws sni
	// (adapter/outbound/trojan.go: wsOpts.Host = SNI).
	if sni := asString(p["sni"]); sni != "" && strings.EqualFold(asString(p["network"]), "ws") && asString(nestedMap(nestedMap(p, "ws-opts"), "headers")["Host"]) == "" {
		q.Set("host", sni)
	}
	if q.Get("security") == "" {
		q.Set("security", "tls")
	}
	stream, err := BuildStreamFromQuery(q, host)
	if err != nil {
		return nil, fmt.Errorf("clash trojan: %w", err)
	}

	out := map[string]any{
		"type":        "trojan",
		"server":      host,
		"server_port": portN,
		"password":    password,
	}
	stream.MergeIntoOutbound(out)

	tag := fmt.Sprintf("trojan-%s-%d", host, portN)
	out["tag"] = tag

	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return &ParsedOutbound{
		Tag:      tag,
		Protocol: "trojan",
		Server:   host,
		Port:     uint16(portN),
		Outbound: raw,
		Label:    asString(p["name"]),
	}, nil
}
