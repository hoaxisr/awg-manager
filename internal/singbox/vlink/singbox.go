// Package vlink: sing-box native JSON subscription support.
//
// Real-world mobile apps (Happ, Hiddify, NekoBox, ...) deliver subscriptions
// as sing-box JSON when their detection logic decides the consumer can
// handle the native config format. The body comes in three shapes:
//
//  1. Single config object: {"outbounds":[...], "route":..., "dns":...}
//  2. Array of configs:     [{"outbounds":[...]}, {"outbounds":[...]}, ...]
//  3. Bare outbounds array: [{"type":"vless","tag":"...","server":...}, ...]
//
// Entry points: IsSingboxJSON detects the format; ParseSingboxBody flattens
// outbounds across all three shapes and returns a BatchResult identical in
// shape to ParseBatch / ParseClashBody. Outbounds are copied raw (option A
// from the design spec) — only validated, not re-derived through the
// share-link factory. The original "tag" goes to ParsedOutbound.Label so
// the UI can show the user-friendly name (often a flag emoji + country).
package vlink

import (
	"encoding/json"
	"fmt"
	"strings"
)

// supportedSingboxTypes is the set of outbound types we accept as members.
// Mirrors Clash policy: vless, trojan, shadowsocks, hysteria2.
var supportedSingboxTypes = map[string]bool{
	"vless":       true,
	"trojan":      true,
	"shadowsocks": true,
	"hysteria2":   true,
	"mieru":       true,
}

// servicedSingboxTypes is the set of outbound types that are infrastructural
// to a sing-box config (selector logic, DNS, blackhole, etc.) — not real
// proxies. Silently dropped, no error, no counter.
var servicedSingboxTypes = map[string]bool{
	"direct":   true,
	"block":    true,
	"dns":      true,
	"selector": true,
	"urltest":  true,
	"tor":      true,
	"ssh":      true,
	"socks":    true,
	"http":     true,
}

// IsSingboxJSON reports whether body looks like a sing-box native JSON
// subscription (in any of the three shapes documented above). Does a full
// json.Unmarshal on the body — note that ParseSingboxBody parses again
// downstream, so on the success path we pay the parse cost twice. For
// typical bodies (≤200 KB) this is acceptable; if it ever becomes a hot
// path on MIPS hardware, fold detection + parsing into a single pass.
//
// False-positives we tolerate: a JSON array containing a single object
// with empty outbounds returns true → ParseSingboxBody yields zero
// outbounds → caller emits "subscription empty". That's a correct UX.
func IsSingboxJSON(body []byte) bool {
	trimmed := trimLeadingSpace(body)
	if len(trimmed) == 0 {
		return false
	}
	if trimmed[0] != '{' && trimmed[0] != '[' {
		return false
	}
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		return false
	}
	return rootHasOutbounds(root)
}

// rootHasOutbounds checks the parsed JSON root for any of the three
// recognised sing-box shapes. Used by IsSingboxJSON.
func rootHasOutbounds(root any) bool {
	switch r := root.(type) {
	case map[string]any:
		// Shape 1: single config object with outbounds key.
		if _, ok := r["outbounds"].([]any); ok {
			return true
		}
		// A single bare outbound object (type + tag) without a config
		// envelope is not a recognised subscription shape — sing-box
		// JSON exports always use one of the three documented shapes.
		return false
	case []any:
		if len(r) == 0 {
			return false
		}
		for _, el := range r {
			obj, ok := el.(map[string]any)
			if !ok {
				continue
			}
			// Shape 2: array element is a config with outbounds.
			if _, ok := obj["outbounds"].([]any); ok {
				return true
			}
			// Shape 3: array element looks like a bare outbound
			// (type + tag pair). type alone is not enough — a Clash
			// proxy entry also has type, but sing-box outbounds also
			// have tag, while Clash uses "name". Require both to
			// avoid mis-detecting Clash-as-JSON.
			_, hasType := obj["type"].(string)
			_, hasTag := obj["tag"].(string)
			if hasType && hasTag {
				return true
			}
		}
		return false
	}
	return false
}

// ParseSingboxBody parses a sing-box native JSON subscription body and
// returns a BatchResult identical in shape to ParseBatch / ParseClashBody.
// See package doc for accepted shapes.
//
//   - Outbound is copied raw into ParsedOutbound.Outbound (option A).
//   - Original tag → ParsedOutbound.Label.
//   - Per-type validation (see validateSingboxOutbound) gates acceptance.
//   - vmess silently counted in SkippedVmess.
//   - Service types (direct/block/dns/selector/urltest/tor/ssh/socks/http)
//     silently dropped — these are sing-box infrastructure, not proxies.
//   - Other real-proxy types (tuic/wireguard/anytls/naive/...) counted in
//     SkippedUnsupp with a ParseError.
func ParseSingboxBody(body []byte) BatchResult {
	out := BatchResult{}

	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		out.Errors = append(out.Errors, ParseError{
			LineIdx: 0,
			Scheme:  "sing-box-json",
			Message: fmt.Sprintf("json parse: %s", err.Error()),
		})
		return out
	}

	flat := flattenSingboxOutbounds(root)
	out.Outbounds = make([]ParsedOutbound, 0, len(flat))

	for i, ob := range flat {
		t := strings.ToLower(asString(ob["type"]))
		if t == "" {
			out.SkippedUnsupp++
			out.Errors = append(out.Errors, ParseError{
				LineIdx: i,
				Scheme:  "sing-box-json",
				Message: "outbound has no type",
			})
			continue
		}
		if servicedSingboxTypes[t] {
			continue
		}
		if t == "vmess" {
			out.SkippedVmess++
			continue
		}
		if !supportedSingboxTypes[t] {
			out.SkippedUnsupp++
			out.Errors = append(out.Errors, ParseError{
				LineIdx: i,
				Scheme:  "sing-box:" + t,
				Message: fmt.Sprintf("unsupported sing-box outbound type %q", t),
			})
			continue
		}
		parsed, err := buildSingboxOutbound(ob, t)
		if err != nil {
			out.Errors = append(out.Errors, ParseError{
				LineIdx: i,
				Scheme:  "sing-box:" + t,
				Message: err.Error(),
			})
			continue
		}
		out.Outbounds = append(out.Outbounds, *parsed)
	}
	return out
}

// flattenSingboxOutbounds walks the parsed JSON root and returns a flat,
// ordered slice of outbound objects. Empty / unrecognised input yields
// an empty slice (not nil, to keep range loops obvious upstream).
func flattenSingboxOutbounds(root any) []map[string]any {
	switch r := root.(type) {
	case map[string]any:
		if list, ok := r["outbounds"].([]any); ok {
			return collectOutboundObjects(list)
		}
	case []any:
		out := make([]map[string]any, 0, len(r))
		for _, el := range r {
			obj, ok := el.(map[string]any)
			if !ok {
				continue
			}
			if list, ok := obj["outbounds"].([]any); ok {
				out = append(out, collectOutboundObjects(list)...)
				continue
			}
			// Bare outbound element (Shape 3). Require both type AND
			// tag to align with rootHasOutbounds detection — a bare
			// element with type alone never passed our detection gate
			// in the first place.
			_, hasType := obj["type"].(string)
			_, hasTag := obj["tag"].(string)
			if hasType && hasTag {
				out = append(out, obj)
			}
		}
		return out
	}
	return []map[string]any{}
}

// collectOutboundObjects filters a JSON array down to its object members.
func collectOutboundObjects(list []any) []map[string]any {
	out := make([]map[string]any, 0, len(list))
	for _, el := range list {
		if obj, ok := el.(map[string]any); ok {
			out = append(out, obj)
		}
	}
	return out
}

// buildSingboxOutbound runs validation and assembles the ParsedOutbound.
// Returns an error if validation fails.
func buildSingboxOutbound(ob map[string]any, typ string) (*ParsedOutbound, error) {
	if err := validateSingboxOutbound(ob, typ); err != nil {
		return nil, err
	}
	server := asString(ob["server"])
	port, _ := asInt(ob["server_port"])

	raw, err := json.Marshal(ob)
	if err != nil {
		return nil, fmt.Errorf("re-marshal outbound: %w", err)
	}

	return &ParsedOutbound{
		Protocol: typ,
		Server:   server,
		Port:     uint16(port),
		Outbound: raw,
		Label:    asString(ob["tag"]),
	}, nil
}

// validateSingboxOutbound enforces the minimum-viable invariants for an
// outbound to be safely committed to our config. Per the spec:
//
//   - server: non-empty
//   - server_port: int in [1..65535]
//   - per-type auth: vless→uuid, trojan→password, shadowsocks→method+password,
//     hysteria2→password, mieru→transport+username+password+port(s)
//
// Anything beyond this (transport/tls/multiplex shape) is left to sing-box
// itself — if it can't load the outbound at Reload time, the user sees the
// load error in subscription state.Err.
func validateSingboxOutbound(ob map[string]any, typ string) error {
	server := asString(ob["server"])
	if server == "" {
		return fmt.Errorf("missing server")
	}
	port, ok := asInt(ob["server_port"])
	if !ok {
		return fmt.Errorf("missing or non-numeric server_port")
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("server_port %d out of range", port)
	}
	switch typ {
	case "vless":
		if asString(ob["uuid"]) == "" {
			return fmt.Errorf("missing uuid")
		}
	case "trojan":
		if asString(ob["password"]) == "" {
			return fmt.Errorf("missing password")
		}
	case "shadowsocks":
		if asString(ob["method"]) == "" {
			return fmt.Errorf("missing method")
		}
		if asString(ob["password"]) == "" {
			return fmt.Errorf("missing password")
		}
	case "hysteria2":
		if asString(ob["password"]) == "" {
			return fmt.Errorf("missing password")
		}
	case "mieru":
		if asString(ob["transport"]) != "TCP" && asString(ob["transport"]) != "UDP" {
			return fmt.Errorf("missing or invalid transport")
		}
		if asString(ob["username"]) == "" {
			return fmt.Errorf("missing username")
		}
		if asString(ob["password"]) == "" {
			return fmt.Errorf("missing password")
		}
		_, hasServerPort := asInt(ob["server_port"])
		ports, hasServerPorts := ob["server_ports"].([]any)
		if !hasServerPort && (!hasServerPorts || len(ports) == 0) {
			return fmt.Errorf("missing server_port or server_ports")
		}
	}
	return nil
}

// LooksLikeJSON reports whether body parses as a JSON object or array,
// regardless of content. Used by callers to differentiate "JSON we
// couldn't recognize as sing-box" from "non-JSON body" — the former gets
// a precise error ("JSON without outbounds"), the latter falls through
// to share-link / Clash detection.
func LooksLikeJSON(body []byte) bool {
	trimmed := trimLeadingSpace(body)
	if len(trimmed) == 0 {
		return false
	}
	if trimmed[0] != '{' && trimmed[0] != '[' {
		return false
	}
	var root any
	return json.Unmarshal(body, &root) == nil
}

// trimLeadingSpace returns body with leading ASCII whitespace removed.
// We can't use bytes.TrimSpace because we want to inspect just the prefix
// for cheap shape detection without copying the whole body.
func trimLeadingSpace(body []byte) []byte {
	for i, b := range body {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		default:
			return body[i:]
		}
	}
	return nil
}
