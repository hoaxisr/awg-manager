package vlink

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// mapClashHysteria2 converts a Clash YAML "type: hysteria2" proxy into a
// ParsedOutbound. Required: server, port, password. obfs (if set) requires
// obfs-password.
//
// Hysteria2 in Clash always implies TLS. We force security=tls before
// delegating to BuildStreamFromQuery.
//
// Field reference: https://wiki.metacubex.one/en/config/proxies/hysteria2/
func mapClashHysteria2(p map[string]any) (*ParsedOutbound, error) {
	host := asString(p["server"])
	if host == "" {
		return nil, errors.New("clash hysteria2: missing server")
	}
	portN, ok := asInt(p["port"])
	if !ok || portN <= 0 || portN > 65535 {
		return nil, errors.New("clash hysteria2: missing or invalid port")
	}
	password := asString(p["password"])
	if password == "" {
		return nil, errors.New("clash hysteria2: missing password")
	}

	q := clashFieldsToValues(p)
	if q.Get("security") == "" {
		q.Set("security", "tls")
	}
	stream, err := BuildStreamFromQuery(q, host)
	if err != nil {
		return nil, fmt.Errorf("clash hysteria2: %w", err)
	}

	out := map[string]any{
		"type":        "hysteria2",
		"server":      host,
		"server_port": portN,
		"password":    password,
	}

	if obfsType := asString(p["obfs"]); obfsType != "" {
		obfsPass := asString(p["obfs-password"])
		if obfsPass == "" {
			return nil, errors.New("clash hysteria2: obfs requires obfs-password")
		}
		out["obfs"] = map[string]any{
			"type":     obfsType,
			"password": obfsPass,
		}
	}
	if ports := asString(p["ports"]); ports != "" {
		if parsed := parseMport(ports); len(parsed) > 0 {
			anyPorts := make([]any, 0, len(parsed))
			for _, spec := range parsed {
				anyPorts = append(anyPorts, spec)
			}
			out["server_ports"] = anyPorts
			out["hop_interval"] = firstNonEmpty(asString(p["hop-interval"]), "10s")
		}
	}

	if up, ok := parseMbps(p["up"]); ok {
		out["up_mbps"] = up
	}
	if down, ok := parseMbps(p["down"]); ok {
		out["down_mbps"] = down
	}

	if len(stream.ALPN()) == 0 {
		stream.SetALPN([]string{"h3"})
	}
	stream.MergeIntoOutbound(out)

	tag := fmt.Sprintf("hy2-%s-%d", host, portN)
	out["tag"] = tag

	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return &ParsedOutbound{
		Tag:      tag,
		Protocol: "hysteria2",
		Server:   host,
		Port:     uint16(portN),
		Outbound: raw,
		Label:    asString(p["name"]),
	}, nil
}

// parseMbps accepts int/float numbers and strings like "50", "50 Mbps", "50Mbps".
// Returns the leading integer and a presence flag.
func parseMbps(v any) (int, bool) {
	if n, ok := asInt(v); ok {
		return n, true
	}
	s := asString(v)
	if s == "" {
		return 0, false
	}
	// Pull leading digits.
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	n, ok := asInt(strings.TrimSpace(s[:end]))
	return n, ok
}
