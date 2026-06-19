package vlink

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// parseSocks parses socks5:// and socks:// share links into a sing-box socks
// outbound. The URI format is:
//
//	socks5://[user:pass@]host:port[#tag]
//	socks://[user:pass@]host:port[#tag]
//
// Authentication is optional. The fragment becomes the outbound tag and label.
func parseSocks(input string) (*ParsedOutbound, error) {
	u, err := url.Parse(input)
	if err != nil {
		return nil, fmt.Errorf("socks: parse: %w", err)
	}

	host := u.Hostname()
	if host == "" {
		return nil, errors.New("socks: missing host")
	}
	port, err := strconv.ParseUint(u.Port(), 10, 16)
	if err != nil || port == 0 {
		return nil, errors.New("socks: missing or invalid port")
	}

	out := map[string]any{
		"type":        "socks",
		"server":      host,
		"server_port": port,
		"version":     "5",
	}

	if u.User != nil {
		if username := u.User.Username(); username != "" {
			out["username"] = username
		}
		if password, set := u.User.Password(); set && password != "" {
			out["password"] = password
		}
	}

	tag := u.Fragment
	if tag == "" {
		tag = fmt.Sprintf("socks5-%s-%d", host, port)
	}
	out["tag"] = tag

	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}

	scheme := strings.ToLower(u.Scheme)
	return &ParsedOutbound{
		Tag:      tag,
		Protocol: scheme,
		Server:   host,
		Port:     uint16(port),
		Outbound: raw,
		Label:    u.Fragment,
	}, nil
}
