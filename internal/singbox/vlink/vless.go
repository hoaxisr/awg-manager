package vlink

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var uuidRegex = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

func parseVless(input string) (*ParsedOutbound, error) {
	u, err := url.Parse(input)
	if err != nil {
		return nil, fmt.Errorf("vless: parse: %w", err)
	}
	host := u.Hostname()
	if host == "" {
		return nil, errors.New("vless: missing host")
	}
	port, err := strconv.ParseUint(u.Port(), 10, 16)
	if err != nil || port == 0 {
		return nil, errors.New("vless: missing or invalid port")
	}

	q := u.Query()

	uuid, err := vlessUUIDFallback(u.User.Username(), q, u.Host)
	if err != nil {
		return nil, err
	}

	flow := normalizeFlow(q.Get("flow"))

	stream, err := BuildStreamFromQuery(q, host)
	if err != nil {
		return nil, fmt.Errorf("vless: %w", err)
	}

	out := map[string]any{
		"type":        "vless",
		"server":      host,
		"server_port": port,
		"uuid":        uuid,
	}
	if flow != "" {
		out["flow"] = flow
	}
	if enc := q.Get("encryption"); enc != "" && enc != "none" {
		out["encryption"] = enc
	}
	stream.MergeIntoOutbound(out)

	tag := u.Fragment
	if tag == "" {
		tag = fmt.Sprintf("vless-%s-%d", host, port)
	}
	out["tag"] = tag

	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}

	return &ParsedOutbound{
		Tag:      tag,
		Protocol: "vless",
		Server:   host,
		Port:     uint16(port),
		Outbound: raw,
		Label:    u.Fragment,
	}, nil
}

// vlessUUIDFallback walks five sources for the UUID, in priority order:
//  1. URL userinfo that is a valid UUID
//  2. URL userinfo decoded as base64, search for UUID-shape
//  3. Query params id, uuid, u
//  4. URL authority (host:port) decoded as base64, search for UUID-shape
//  5. Raw userinfo as-is (non-standard credential; accepted without validation)
func vlessUUIDFallback(userinfo string, q url.Values, authority string) (string, error) {
	if userinfo != "" {
		if uuidRegex.MatchString(userinfo) {
			return strings.ToLower(uuidRegex.FindString(userinfo)), nil
		}
		// try base64 decode of userinfo
		if decoded, err := DecodeBase64Url(userinfo); err == nil && len(decoded) > 0 {
			if uuidRegex.Match(decoded) {
				return strings.ToLower(uuidRegex.FindString(string(decoded))), nil
			}
		}
	}
	// query params
	for _, key := range []string{"id", "uuid", "u"} {
		if v := q.Get(key); v != "" && uuidRegex.MatchString(v) {
			return strings.ToLower(uuidRegex.FindString(v)), nil
		}
	}
	// authority base64
	if decoded, err := DecodeBase64Url(authority); err == nil && len(decoded) > 0 {
		if uuidRegex.Match(decoded) {
			return strings.ToLower(uuidRegex.FindString(string(decoded))), nil
		}
	}
	// raw userinfo fallback — accept any non-empty credential as-is
	if userinfo != "" {
		return userinfo, nil
	}
	return "", errors.New("vless: uuid not found in any source")
}

func normalizeFlow(f string) string {
	if f == "" || strings.EqualFold(f, "none") {
		return ""
	}
	return strings.TrimSuffix(f, "-udp443")
}
