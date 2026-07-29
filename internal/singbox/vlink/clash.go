// Package vlink: Clash / mihomo YAML subscription support.
//
// Entry points: IsClashYAML detects the format; ParseClashBody parses the
// body and returns a BatchResult identical in shape to ParseBatch. Per-
// protocol mappers live in clash_<protocol>.go (mirrors existing per-
// protocol layout for share-link parsers).
package vlink

import (
	"bytes"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// scanLimit is how many bytes IsClashYAML inspects. A real Clash subscription
// has "proxies:" within the first few hundred bytes; 4 KB is a forgiving cap.
const scanLimit = 64 * 1024

// matches a top-level "proxies:" key — accepts block (proxies: + newline),
// inline ("proxies: []"), null marker ("proxies: null"), and any other
// permissive form. Tolerable false positives are documented above.
var proxiesHeaderRe = regexp.MustCompile(`(?m)^proxies:`)

// IsClashYAML reports whether body looks like a Clash/mihomo subscription
// (top-level "proxies:" key in valid YAML). Cheap: scans the first 4 KB only.
// False positives on bodies that happen to contain "proxies:" mid-document
// are tolerable because ParseClashBody will then parse and find no entries.
func IsClashYAML(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	head := body
	if len(head) > scanLimit {
		head = head[:scanLimit]
	}
	// Reject obvious non-YAML preludes.
	trimmed := bytes.TrimSpace(head)
	if len(trimmed) == 0 {
		return false
	}
	if trimmed[0] == '<' {
		return false // HTML
	}
	return proxiesHeaderRe.Match(head)
}

// clashFieldsToValues collapses Clash YAML proxy fields into a synthetic
// url.Values. The resulting Values is fed to BuildStreamFromQuery, so the
// keys must match what stream.go expects: type, path, host, serviceName,
// security, sni, alpn, fp, insecure, pbk, sid.
//
// Reality is detected when reality-opts is present; otherwise tls:true →
// security=tls. Network is read from "network" (ws/grpc/http/h2/xhttp/tcp).
func clashFieldsToValues(p map[string]any) url.Values {
	v := url.Values{}

	// Network / transport
	netRaw := strings.ToLower(asString(p["network"]))
	if netRaw == "" {
		netRaw = "tcp"
	}
	v.Set("type", netRaw)

	switch netRaw {
	case "ws":
		ws := nestedMap(p, "ws-opts")
		if path := asString(ws["path"]); path != "" {
			v.Set("path", path)
		}
		hdrs := nestedMap(ws, "headers")
		if host := asString(hdrs["Host"]); host != "" {
			v.Set("host", host)
		}
		// mihomo encodes httpupgrade as network: ws + ws-opts.v2ray-http-upgrade.
		// sing-box has a distinct httpupgrade transport, so remap to it (path/
		// host above are shared) instead of emitting a plain ws outbound.
		if asBool(ws["v2ray-http-upgrade"]) {
			v.Set("type", "httpupgrade")
		}
	case "grpc":
		gp := nestedMap(p, "grpc-opts")
		if name := asString(gp["grpc-service-name"]); name != "" {
			v.Set("serviceName", name)
		}
	case "http":
		hp := nestedMap(p, "http-opts")
		// http-opts.path and http-opts.host are []string per Clash spec.
		// Take the first non-empty entry to mirror h2-opts handling above.
		if paths := asStringSlice(hp["path"]); len(paths) > 0 {
			v.Set("path", paths[0])
		}
		if hosts := asStringSlice(hp["host"]); len(hosts) > 0 {
			v.Set("host", hosts[0])
		}
	case "h2":
		hp := nestedMap(p, "h2-opts")
		if path := asString(hp["path"]); path != "" {
			v.Set("path", path)
		}
		// h2-opts.host is []string per Clash spec; take the first non-empty entry.
		if hosts := asStringSlice(hp["host"]); len(hosts) > 0 {
			v.Set("host", hosts[0])
		}
	case "xhttp", "splithttp":
		// mihomo carries xhttp under xhttp-opts with top-level string path/host
		// (not []string) plus a mode. We don't run mihomo — convert to our
		// sing-box xhttp transport. x_padding_bytes is defaulted downstream.
		xh := nestedMap(p, "xhttp-opts")
		if path := asString(xh["path"]); path != "" {
			v.Set("path", path)
		}
		if host := asString(xh["host"]); host != "" {
			v.Set("host", host)
		}
		if mode := asString(xh["mode"]); mode != "" {
			v.Set("mode", mode)
		}
	}

	// TLS / Reality
	reality := nestedMap(p, "reality-opts")
	switch {
	case len(reality) > 0:
		v.Set("security", "reality")
		if pk := asString(reality["public-key"]); pk != "" {
			v.Set("pbk", pk)
		}
		if sid := asString(reality["short-id"]); sid != "" {
			v.Set("sid", sid)
		}
	case asBool(p["tls"]):
		v.Set("security", "tls")
	}

	if sni := firstNonEmpty(asString(p["servername"]), asString(p["sni"])); sni != "" {
		v.Set("sni", sni)
	}
	if asBool(p["skip-cert-verify"]) {
		v.Set("insecure", "1")
	}
	if alpn := asStringSlice(p["alpn"]); len(alpn) > 0 {
		v.Set("alpn", strings.Join(alpn, ","))
	}
	if fp := asString(p["client-fingerprint"]); fp != "" {
		v.Set("fp", fp)
	}

	return v
}

// asString returns v as a string, with light coercion: bool/int/float become
// their %v form, nil becomes "".
func asString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// asInt extracts v as an int with light coercion. Booleans never coerce.
func asInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	case string:
		s := strings.TrimSpace(x)
		if n, err := strconv.Atoi(s); err == nil {
			return n, true
		}
	}
	return 0, false
}

// asBool accepts bool, non-zero numerics, "true"/"yes"/"1".
func asBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case int:
		return x != 0
	case int64:
		return x != 0
	case float64:
		return x != 0
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "1", "true", "yes":
			return true
		}
	}
	return false
}

// asStringSlice normalises both YAML lists and scalar strings to []string.
// Anything else returns nil.
func asStringSlice(v any) []string {
	switch x := v.(type) {
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s := asString(e); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return append([]string(nil), x...)
	case string:
		if x == "" {
			return nil
		}
		return []string{x}
	}
	return nil
}

// nestedMap returns p[key] as map[string]any, or empty map if missing/wrong type.
func nestedMap(p map[string]any, key string) map[string]any {
	if p == nil {
		return map[string]any{}
	}
	if m, ok := p[key].(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

// clashRoot is the minimal subset of a Clash YAML document we care about.
// Other top-level keys (proxy-groups, rules, dns) are ignored.
type clashRoot struct {
	Proxies []map[string]any `yaml:"proxies"`
}

// ParseClashBody parses a Clash/mihomo YAML subscription body and returns a
// BatchResult identical in shape to ParseBatch.
//
//   - vmess entries are silently counted in SkippedVmess (matches share-link
//     parser policy)
//   - unknown types are counted in SkippedUnsupp and recorded in Errors
//   - per-protocol mapping failures (missing uuid etc.) land in Errors with
//     Scheme="clash:<type>" — they do NOT increment SkippedUnsupp because
//     the type IS supported, the entry is just broken
func ParseClashBody(body []byte) BatchResult {
	out := BatchResult{}

	var root clashRoot
	if err := yaml.Unmarshal(body, &root); err != nil {
		out.Errors = append(out.Errors, ParseError{
			LineIdx: 0,
			Scheme:  "clash",
			Message: fmt.Sprintf("yaml parse: %s", err.Error()),
		})
		return out
	}

	out.Outbounds = make([]ParsedOutbound, 0, len(root.Proxies))
	for i, p := range root.Proxies {
		t := strings.ToLower(asString(p["type"]))
		var (
			parsed *ParsedOutbound
			err    error
		)
		switch t {
		case "vless":
			parsed, err = mapClashVless(p)
		case "trojan":
			parsed, err = mapClashTrojan(p)
		case "ss":
			parsed, err = mapClashShadowsocks(p)
		case "hysteria2":
			parsed, err = mapClashHysteria2(p)
		case "mieru":
			parsed, err = mapClashMieru(p)
		case "vmess":
			out.SkippedVmess++
			continue
		default:
			out.SkippedUnsupp++
			out.Errors = append(out.Errors, ParseError{
				LineIdx: i,
				Scheme:  "clash:" + t,
				Message: fmt.Sprintf("unsupported clash type %q", t),
			})
			continue
		}
		if err != nil {
			out.Errors = append(out.Errors, ParseError{
				LineIdx: i,
				Scheme:  "clash:" + t,
				Message: err.Error(),
			})
			continue
		}
		out.Outbounds = append(out.Outbounds, *parsed)
	}
	return out
}
