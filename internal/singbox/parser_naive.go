package singbox

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

func parseNaive(raw string) (*ParsedOutbound, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse naive url: %w", err)
	}
	if !strings.HasPrefix(u.Scheme, "naive+") {
		return nil, fmt.Errorf("not a naive link: scheme=%s", u.Scheme)
	}
	username := u.User.Username()
	password, _ := u.User.Password()
	if username == "" || password == "" {
		return nil, fmt.Errorf("missing user:pass")
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
	tag := u.Fragment
	if tag == "" {
		tag = fmt.Sprintf("naive-%s-%d", host, port)
	}

	ob := map[string]any{
		"type":        "naive",
		"tag":         tag,
		"server":      host,
		"server_port": port,
		"username":    username,
		"password":    password,
		"network":     "tcp",
		"tls": map[string]any{
			"enabled":     true,
			"server_name": host,
		},
	}
	b, err := json.Marshal(ob)
	if err != nil {
		return nil, fmt.Errorf("marshal naive outbound: %w", err)
	}
	return &ParsedOutbound{
		Tag: tag, Protocol: "naive",
		Server: host, Port: port, Outbound: b,
	}, nil
}
