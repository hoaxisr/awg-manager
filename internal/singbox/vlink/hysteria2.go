package vlink

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

func parseHysteria2(input string) (*ParsedOutbound, error) {
	u, err := url.Parse(input)
	if err != nil {
		return nil, fmt.Errorf("hysteria2: parse: %w", err)
	}
	host := u.Hostname()
	if host == "" {
		return nil, errors.New("hysteria2: missing host")
	}
	port, err := strconv.ParseUint(u.Port(), 10, 16)
	if err != nil || port == 0 {
		return nil, errors.New("hysteria2: missing or invalid port")
	}

	password := u.User.Username()
	if pw, ok := u.User.Password(); ok && pw != "" {
		password = password + ":" + pw
	}
	if password == "" {
		return nil, errors.New("hysteria2: missing password")
	}

	q := u.Query()
	out := map[string]any{
		"type":        "hysteria2",
		"server":      host,
		"server_port": port,
		"password":    password,
	}

	// Port hopping
	if mport := q.Get("mport"); mport != "" {
		ports := parseMport(mport)
		if len(ports) > 0 {
			anyPorts := make([]any, len(ports))
			for i, p := range ports {
				anyPorts[i] = p
			}
			out["server_ports"] = anyPorts
		}
	}
	if hop := q.Get("hop_interval"); hop != "" {
		out["hop_interval"] = hop
	} else if _, hasMport := out["server_ports"]; hasMport {
		out["hop_interval"] = "10s"
	}

	// TLS собирает общий слой — тот же, что у clash-пути. Здесь остаётся
	// только hysteria-специфика, которой общий слой не знает.
	stream, err := BuildStreamFromQuery(hysteria2StreamQuery(q), host)
	if err != nil {
		return nil, fmt.Errorf("hysteria2: %w", err)
	}
	stream.MergeIntoOutbound(out)
	// pinSHA256 намеренно игнорируется: hysteria пинит hex-отпечаток всего
	// сертификата, sing-box certificate_public_key_sha256 — base64 sha256
	// от SPKI публичного ключа. Эквивалента нет, а сырое значение валит
	// decode всего конфига (issue #350).
	if boolish(q.Get("ech")) {
		if tls, ok := out["tls"].(map[string]any); ok {
			tls["ech"] = map[string]any{"enabled": true}
		}
	}

	// Obfs
	if obfsType := q.Get("obfs"); obfsType != "" {
		out["obfs"] = map[string]any{
			"type":     obfsType,
			"password": q.Get("obfs-password"),
		}
	}

	// Brutal congestion
	if strings.EqualFold(q.Get("congestion"), "brutal") {
		brutal := map[string]any{}
		if up, err := strconv.Atoi(q.Get("brutal_up")); err == nil {
			brutal["up_mbps"] = up
		}
		if down, err := strconv.Atoi(q.Get("brutal_down")); err == nil {
			brutal["down_mbps"] = down
		}
		if len(brutal) > 0 {
			out["brutal"] = brutal
		}
	}

	tag := u.Fragment
	if tag == "" {
		tag = fmt.Sprintf("hy2-%s-%d", host, port)
	}
	out["tag"] = tag

	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return &ParsedOutbound{
		Tag:      tag,
		Protocol: "hysteria2",
		Server:   host,
		Port:     uint16(port),
		Outbound: raw,
		Label:    u.Fragment,
	}, nil
}

// parseMport accepts "20000-30000" or "20000,21000-22000,30000-31000"
// and returns sing-box port spec strings ("20000:30000" for ranges,
// single port as "20000:20000" — same convention as the spec example).
// hysteria2StreamQuery отбирает из ссылки то, что понимает общий слой. Список
// закрытый намеренно: fp= сюда не попадает — uTLS поверх QUIC неприменим, а
// общий слой добавил бы блок utls.
func hysteria2StreamQuery(q url.Values) url.Values {
	v := url.Values{}
	v.Set("security", "tls")
	v.Set("sni", q.Get("sni"))
	// hysteria2 без ALPN h3 сервер обычно не принимает; движок дефолт не ставит.
	v.Set("alpn", firstNonEmpty(q.Get("alpn"), "h3"))
	if boolish(q.Get("insecure")) {
		v.Set("insecure", "1")
	}
	if b := q.Get("bind_interface"); b != "" {
		v.Set("bind_interface", b)
	}
	return v
}

func parseMport(mport string) []string {
	parts := strings.Split(mport, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.Contains(p, "-") {
			rng := strings.SplitN(p, "-", 2)
			out = append(out, rng[0]+":"+rng[1])
		} else {
			out = append(out, p+":"+p)
		}
	}
	return out
}
