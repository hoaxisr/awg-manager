package vlink

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

func parseNaive(input string) (*ParsedOutbound, error) {
	const prefix = "naive+"
	if !strings.HasPrefix(strings.ToLower(input), prefix) {
		return nil, errors.New("naive: missing naive+ prefix")
	}
	stripped := input[len(prefix):]
	u, err := url.Parse(stripped)
	if err != nil {
		return nil, fmt.Errorf("naive: parse: %w", err)
	}
	innerScheme := strings.ToLower(u.Scheme)
	if innerScheme != "https" && innerScheme != "http" {
		return nil, fmt.Errorf("naive: unsupported inner scheme %q", innerScheme)
	}
	host := u.Hostname()
	if host == "" {
		return nil, errors.New("naive: missing host")
	}
	port, err := strconv.ParseUint(u.Port(), 10, 16)
	if err != nil || port == 0 {
		return nil, errors.New("naive: missing or invalid port")
	}
	username := u.User.Username()
	password, _ := u.User.Password()
	if username == "" && password == "" {
		return nil, errors.New("naive: missing credentials")
	}

	out := map[string]any{
		"type":        "naive",
		"server":      host,
		"server_port": port,
		"username":    username,
		"password":    password,
		"udp_over_tcp": map[string]any{
			"enabled": true,
			"version": 2,
		},
	}
	if innerScheme == "https" {
		out["tls"] = map[string]any{
			"enabled":     true,
			"server_name": host,
		}
	}
	tag := u.Fragment
	if tag == "" {
		tag = fmt.Sprintf("naive-%s-%d", host, port)
	}
	out["tag"] = tag

	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return &ParsedOutbound{
		Tag:      tag,
		Protocol: "naive",
		Server:   host,
		Port:     uint16(port),
		Outbound: raw,
		Label:    u.Fragment,
	}, nil
}
