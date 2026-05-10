package vlink

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
)

func parseTrojan(input string) (*ParsedOutbound, error) {
	u, err := url.Parse(input)
	if err != nil {
		return nil, fmt.Errorf("trojan: parse: %w", err)
	}
	host := u.Hostname()
	if host == "" {
		return nil, errors.New("trojan: missing host")
	}
	port, err := strconv.ParseUint(u.Port(), 10, 16)
	if err != nil || port == 0 {
		return nil, errors.New("trojan: missing or invalid port")
	}
	password := u.User.Username()
	if password == "" {
		return nil, errors.New("trojan: missing password")
	}

	q := u.Query()
	// Trojan defaults to TLS — if security is not set, force tls.
	if q.Get("security") == "" {
		q.Set("security", "tls")
	}

	stream, err := BuildStreamFromQuery(q, host)
	if err != nil {
		return nil, fmt.Errorf("trojan: %w", err)
	}

	out := map[string]any{
		"type":        "trojan",
		"server":      host,
		"server_port": port,
		"password":    password,
	}
	stream.MergeIntoOutbound(out)

	tag := u.Fragment
	if tag == "" {
		tag = fmt.Sprintf("trojan-%s-%d", host, port)
	}
	out["tag"] = tag

	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return &ParsedOutbound{
		Tag:      tag,
		Protocol: "trojan",
		Server:   host,
		Port:     uint16(port),
		Outbound: raw,
		Label:    u.Fragment,
	}, nil
}
