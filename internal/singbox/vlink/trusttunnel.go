package vlink

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
)

func parseTrusttunnel(input string) (*ParsedOutbound, error) {
	u, err := url.Parse(input)
	if err != nil {
		return nil, fmt.Errorf("trusttunnel: parse: %w", err)
	}
	host := u.Hostname()
	if host == "" {
		return nil, errors.New("trusttunnel: missing host")
	}
	port, err := strconv.ParseUint(u.Port(), 10, 16)
	if err != nil || port == 0 {
		return nil, errors.New("trusttunnel: missing or invalid port")
	}

	username := u.User.Username()
	password, _ := u.User.Password()
	if username == "" {
		return nil, errors.New("trusttunnel: missing username")
	}
	if password == "" {
		return nil, errors.New("trusttunnel: missing password")
	}

	q := u.Query()
	out := map[string]any{
		"type":        "trusttunnel",
		"server":      host,
		"server_port": port,
		"username":    username,
		"password":    password,
	}

	tls := map[string]any{
		"enabled":     true,
		"server_name": firstNonEmpty(q.Get("sni"), host),
	}
	if alpn := q.Get("alpn"); alpn != "" {
		tls["alpn"] = splitCSV(alpn)
	} else {
		if boolish(q.Get("quic")) {
			tls["alpn"] = []string{"h3"}
		} else {
			tls["alpn"] = []string{"h2"}
		}
	}
	if boolish(q.Get("insecure")) {
		tls["insecure"] = true
	}
	if fp := firstNonEmpty(q.Get("fp"), q.Get("fingerprint")); fp != "" {
		tls["utls"] = map[string]any{
			"enabled":     true,
			"fingerprint": fp,
		}
	}
	out["tls"] = tls

	if boolish(q.Get("quic")) {
		out["quic"] = true
	}
	if cong := q.Get("congestion"); cong != "" {
		out["congestion_controller"] = cong
	}
	if mc := q.Get("max-connections"); mc != "" {
		if n, err := strconv.Atoi(mc); err == nil && n > 0 {
			out["max_connections"] = n
		}
	} else if mc := q.Get("max_connections"); mc != "" {
		if n, err := strconv.Atoi(mc); err == nil && n > 0 {
			out["max_connections"] = n
		}
	}
	if ms := q.Get("min-streams"); ms != "" {
		if n, err := strconv.Atoi(ms); err == nil && n > 0 {
			out["min_streams"] = n
		}
	} else if ms := q.Get("min_streams"); ms != "" {
		if n, err := strconv.Atoi(ms); err == nil && n > 0 {
			out["min_streams"] = n
		}
	}
	if ms := q.Get("max-streams"); ms != "" {
		if n, err := strconv.Atoi(ms); err == nil && n > 0 {
			out["max_streams"] = n
		}
	} else if ms := q.Get("max_streams"); ms != "" {
		if n, err := strconv.Atoi(ms); err == nil && n > 0 {
			out["max_streams"] = n
		}
	}

	tag := u.Fragment
	if tag == "" {
		tag = fmt.Sprintf("tt-%s-%d", host, port)
	}
	out["tag"] = tag

	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return &ParsedOutbound{
		Tag:      tag,
		Protocol: "trusttunnel",
		Server:   host,
		Port:     uint16(port),
		Outbound: raw,
		Label:    u.Fragment,
	}, nil
}
