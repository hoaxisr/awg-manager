package singbox

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

func parseVLESS(raw string) (*ParsedOutbound, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse vless url: %w", err)
	}
	if u.Scheme != "vless" {
		return nil, fmt.Errorf("not a vless link: scheme=%s", u.Scheme)
	}
	uuid := u.User.Username()
	if uuid == "" {
		return nil, fmt.Errorf("missing uuid")
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
		tag = fmt.Sprintf("vless-%s-%d", host, port)
	}

	ob := map[string]any{
		"type":        "vless",
		"tag":         tag,
		"server":      host,
		"server_port": port,
		"uuid":        uuid,
	}
	if flow := q.Get("flow"); flow != "" {
		ob["flow"] = flow
	}

	// TLS / Reality
	security := q.Get("security")
	if security == "tls" || security == "reality" {
		tls := map[string]any{"enabled": true}
		if sni := q.Get("sni"); sni != "" {
			tls["server_name"] = sni
		}
		if alpn := q.Get("alpn"); alpn != "" {
			tls["alpn"] = strings.Split(alpn, ",")
		}
		if fp := q.Get("fp"); fp != "" {
			tls["utls"] = map[string]any{"enabled": true, "fingerprint": fp}
		}
		if security == "reality" {
			reality := map[string]any{"enabled": true}
			if pbk := q.Get("pbk"); pbk != "" {
				reality["public_key"] = pbk
			}
			if sid := q.Get("sid"); sid != "" {
				reality["short_id"] = sid
			}
			tls["reality"] = reality
		}
		ob["tls"] = tls
	}

	// Transport
	switch q.Get("type") {
	case "", "tcp":
		// no transport block for plain TCP
	case "grpc":
		tr := map[string]any{"type": "grpc"}
		if svc := q.Get("serviceName"); svc != "" {
			tr["service_name"] = svc
		}
		ob["transport"] = tr
	case "ws":
		tr := map[string]any{"type": "ws"}
		if path := q.Get("path"); path != "" {
			tr["path"] = path
		}
		if hostHdr := q.Get("host"); hostHdr != "" {
			tr["headers"] = map[string]any{"Host": hostHdr}
		}
		if ed := q.Get("ed"); ed != "" {
			tr["early_data_header_name"] = ed
		}
		ob["transport"] = tr
	default:
		return nil, fmt.Errorf("unsupported transport: %s", q.Get("type"))
	}

	b, err := json.Marshal(ob)
	if err != nil {
		return nil, fmt.Errorf("marshal vless outbound: %w", err)
	}
	return &ParsedOutbound{
		Tag:      tag,
		Protocol: "vless",
		Server:   host,
		Port:     port,
		Outbound: b,
	}, nil
}
