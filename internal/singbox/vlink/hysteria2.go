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

	// TLS — always enabled for Hysteria2
	tls := map[string]any{
		"enabled":     true,
		"server_name": firstNonEmpty(q.Get("sni"), host),
		"alpn":        []string{"h3"},
	}
	if alpn := q.Get("alpn"); alpn != "" {
		tls["alpn"] = splitCSV(alpn)
	}
	if boolish(q.Get("insecure")) {
		tls["insecure"] = true
	}
	if pin := q.Get("pinSHA256"); pin != "" {
		tls["certificate_public_key_sha256"] = pin
	}
	if boolish(q.Get("ech")) {
		tls["ech"] = map[string]any{"enabled": true}
	}
	out["tls"] = tls

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
