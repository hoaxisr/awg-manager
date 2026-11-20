package vlink

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
)

func parseAnyTLS(input string) (*ParsedOutbound, error) {
	u, err := url.Parse(input)
	if err != nil {
		return nil, fmt.Errorf("anytls: parse: %w", err)
	}
	host := u.Hostname()
	if host == "" {
		return nil, errors.New("anytls: missing host")
	}
	port, err := strconv.ParseUint(u.Port(), 10, 16)
	if err != nil || port == 0 {
		return nil, errors.New("anytls: missing or invalid port")
	}

	password := u.User.Username()
	if pw, ok := u.User.Password(); ok && pw != "" {
		password = password + ":" + pw
	}
	if password == "" {
		return nil, errors.New("anytls: missing password")
	}

	q := u.Query()
	out := map[string]any{
		"type":        "anytls",
		"server":      host,
		"server_port": port,
		"password":    password,
	}

	streamQuery := url.Values{}
	streamQuery.Set("security", "tls")
	if sni := firstNonEmpty(q.Get("sni"), q.Get("peer")); sni != "" {
		streamQuery.Set("sni", sni)
	}
	if alpn := q.Get("alpn"); alpn != "" {
		streamQuery.Set("alpn", alpn)
	}
	if fp := q.Get("fp"); fp != "" {
		streamQuery.Set("fp", fp)
	}
	if boolish(q.Get("insecure")) || boolish(q.Get("allowInsecure")) {
		streamQuery.Set("insecure", "1")
	}
	stream, err := BuildStreamFromQuery(streamQuery, host)
	if err != nil {
		return nil, fmt.Errorf("anytls: %w", err)
	}
	stream.MergeIntoOutbound(out)

	for _, key := range []string{"idle_session_check_interval", "idle_session_timeout"} {
		if v := q.Get(key); v != "" {
			out[key] = v
		}
	}
	if n, err := strconv.Atoi(q.Get("min_idle_session")); err == nil && n > 0 {
		out["min_idle_session"] = n
	}

	tag := u.Fragment
	if tag == "" {
		tag = fmt.Sprintf("anytls-%s-%d", host, port)
	}
	out["tag"] = tag

	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return &ParsedOutbound{
		Tag:      tag,
		Protocol: "anytls",
		Server:   host,
		Port:     uint16(port),
		Outbound: raw,
		Label:    u.Fragment,
	}, nil
}
