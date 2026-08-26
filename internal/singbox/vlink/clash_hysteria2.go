package vlink

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
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
	if q.Get("alpn") == "" {
		q.Set("alpn", "h3")
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
			hop, hopMax := clashHopInterval(p["hop-interval"])
			out["hop_interval"] = hop
			if hopMax != "" {
				out["hop_interval_max"] = hopMax
			}
		}
	}

	if up, ok := parseMbps(p["up"]); ok {
		out["up_mbps"] = up
	}
	if down, ok := parseMbps(p["down"]); ok {
		out["down_mbps"] = down
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

// clashHopInterval переводит hop-interval из формы mihomo (число секунд или
// диапазон "a-b") в Duration, которую ждёт sing-box. Голое "30" в конфиг
// пускать нельзя: движок разбирает поле как Duration и роняет ВСЮ
// конфигурацию, а не один аутбаунд.
func clashHopInterval(v any) (hop, hopMax string) {
	raw := asString(v)
	if n, ok := asInt(v); ok {
		raw = strconv.Itoa(n)
	}
	if raw == "" {
		return "10s", ""
	}
	lo, hi, isRange := strings.Cut(raw, "-")
	n, err := strconv.Atoi(strings.TrimSpace(lo))
	if err != nil || n <= 0 {
		return "10s", ""
	}
	hop = strconv.Itoa(n) + "s"
	if isRange {
		if m, err := strconv.Atoi(strings.TrimSpace(hi)); err == nil && m >= n {
			hopMax = strconv.Itoa(m) + "s"
		}
	}
	return hop, hopMax
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
