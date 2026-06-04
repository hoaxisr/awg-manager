package vlink

import (
	"errors"
	"fmt"
)

// mapClashVless converts a Clash YAML "type: vless" proxy entry into a
// ParsedOutbound. Required fields: server, port, uuid. Optional: flow,
// network/transport, tls/reality blocks.
//
// Field reference: https://wiki.metacubex.one/en/config/proxies/vless/
func mapClashVless(p map[string]any) (*ParsedOutbound, error) {
	host := asString(p["server"])
	if host == "" {
		return nil, errors.New("clash vless: missing server")
	}
	portN, ok := asInt(p["port"])
	if !ok || portN <= 0 || portN > 65535 {
		return nil, errors.New("clash vless: missing or invalid port")
	}
	uuid := asString(p["uuid"])
	if uuid == "" {
		return nil, errors.New("clash vless: missing uuid")
	}

	q := clashFieldsToValues(p)
	stream, err := BuildStreamFromQuery(q, host)
	if err != nil {
		return nil, fmt.Errorf("clash vless: %w", err)
	}

	return buildVlessOutbound(host, uint16(portN), uuid, asString(p["flow"]), asString(p["encryption"]), stream, "", asString(p["name"]))
}
