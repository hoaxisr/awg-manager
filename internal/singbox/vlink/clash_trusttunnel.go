package vlink

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
)

func mapClashTrusttunnel(p map[string]any) (*ParsedOutbound, error) {
	host := asString(p["server"])
	if host == "" {
		return nil, errors.New("clash trusttunnel: missing server")
	}
	portN, ok := asInt(p["port"])
	if !ok || portN <= 0 || portN > 65535 {
		return nil, errors.New("clash trusttunnel: missing or invalid port")
	}
	username := asString(p["username"])
	if username == "" {
		return nil, errors.New("clash trusttunnel: missing username")
	}
	password := asString(p["password"])
	if password == "" {
		return nil, errors.New("clash trusttunnel: missing password")
	}

	q := clashFieldsToValues(p)
	if q.Get("security") == "" {
		q.Set("security", "tls")
	}
	if q.Get("sni") == "" {
		if sni := asString(p["sni"]); sni != "" {
			q.Set("sni", sni)
		} else {
			q.Set("sni", host)
		}
	}
	stream, err := BuildStreamFromQuery(q, host)
	if err != nil {
		return nil, fmt.Errorf("clash trusttunnel: %w", err)
	}

	out := map[string]any{
		"type":        "trusttunnel",
		"server":      host,
		"server_port": portN,
		"username":    username,
		"password":    password,
	}

	if useQUIC, _ := p["quic"].(bool); useQUIC {
		out["quic"] = true
	} else if quicStr := asString(p["quic"]); quicStr != "" {
		if boolish(quicStr) {
			out["quic"] = true
		}
	}
	if cong := asString(p["congestion-controller"]); cong != "" {
		out["congestion_controller"] = cong
	} else if cong := asString(p["congestion_controller"]); cong != "" {
		out["congestion_controller"] = cong
	}
	if mc, ok := asInt(p["max-connections"]); ok && mc > 0 {
		out["max_connections"] = mc
	} else if mc, ok := asInt(p["max_connections"]); ok && mc > 0 {
		out["max_connections"] = mc
	}
	if ms, ok := asInt(p["min-streams"]); ok && ms > 0 {
		out["min_streams"] = ms
	} else if ms, ok := asInt(p["min_streams"]); ok && ms > 0 {
		out["min_streams"] = ms
	}
	if ms, ok := asInt(p["max-streams"]); ok && ms > 0 {
		out["max_streams"] = ms
	} else if ms, ok := asInt(p["max_streams"]); ok && ms > 0 {
		out["max_streams"] = ms
	}

	stream.MergeIntoOutbound(out)

	if tlsRaw, hasTLS := out["tls"].(map[string]any); hasTLS {
		if alpnAny, hasALPN := tlsRaw["alpn"]; !hasALPN || alpnAny == nil {
			if useQUIC, _ := out["quic"].(bool); useQUIC {
				tlsRaw["alpn"] = []string{"h3"}
			} else {
				tlsRaw["alpn"] = []string{"h2"}
			}
		}
	}

	tag := fmt.Sprintf("tt-%s-%d", host, portN)
	out["tag"] = tag

	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return &ParsedOutbound{
		Tag:      tag,
		Protocol: "trusttunnel",
		Server:   host,
		Port:     uint16(portN),
		Outbound: raw,
		Label:    asString(p["name"]),
	}, nil
}

func trusttunnelQueryFromOutbound(ob map[string]any) url.Values {
	q := url.Values{}
	if useQUIC, _ := ob["quic"].(bool); useQUIC {
		q.Set("quic", "1")
	}
	if cong, _ := ob["congestion_controller"].(string); cong != "" {
		q.Set("congestion", cong)
	}
	if mc, ok := ob["max_connections"].(int); ok && mc > 0 {
		q.Set("max_connections", strconv.Itoa(mc))
	}
	if ms, ok := ob["min_streams"].(int); ok && ms > 0 {
		q.Set("min_streams", strconv.Itoa(ms))
	}
	if ms, ok := ob["max_streams"].(int); ok && ms > 0 {
		q.Set("max_streams", strconv.Itoa(ms))
	}
	return q
}
