package singbox

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
)

func parseHysteria2(raw string) (*ParsedOutbound, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse hysteria2 url: %w", err)
	}
	if u.Scheme != "hysteria2" && u.Scheme != "hy2" {
		return nil, fmt.Errorf("not a hysteria2 link: scheme=%s", u.Scheme)
	}
	password := u.User.Username()
	if password == "" {
		return nil, fmt.Errorf("missing password")
	}
	host := u.Hostname()
	portStr := u.Port()
	if host == "" || portStr == "" {
		return nil, fmt.Errorf("missing host or port")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("bad port: %w", err)
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("port out of range: %d", port)
	}
	q := u.Query()
	tag := u.Fragment
	if tag == "" {
		tag = fmt.Sprintf("hysteria2-%s-%d", host, port)
	}

	ob := map[string]any{
		"type":        "hysteria2",
		"tag":         tag,
		"server":      host,
		"server_port": port,
		"password":    password,
	}
	tls := map[string]any{"enabled": true}
	if sni := q.Get("sni"); sni != "" {
		tls["server_name"] = sni
	}
	if ins := q.Get("insecure"); ins == "1" || ins == "true" {
		tls["insecure"] = true
	}
	ob["tls"] = tls

	b, err := json.Marshal(ob)
	if err != nil {
		return nil, fmt.Errorf("marshal hysteria2 outbound: %w", err)
	}
	return &ParsedOutbound{
		Tag: tag, Protocol: "hysteria2",
		Server: host, Port: port, Outbound: b,
	}, nil
}
