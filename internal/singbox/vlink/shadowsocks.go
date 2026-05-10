package vlink

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

func parseShadowsocks(input string) (*ParsedOutbound, error) {
	const prefix = "ss://"
	if !strings.HasPrefix(strings.ToLower(input), prefix) {
		return nil, errors.New("ss: missing ss:// prefix")
	}

	tag := ""
	label := ""
	queryStr := ""
	body := input[len(prefix):]
	if hash := strings.Index(body, "#"); hash >= 0 {
		tag = body[hash+1:]
		body = body[:hash]
	}
	if qi := strings.Index(body, "?"); qi >= 0 {
		queryStr = body[qi+1:]
		body = body[:qi]
	}

	method, password, host, port, err := ssDecodeAuthority(body)
	if err != nil {
		return nil, err
	}
	if method == "" {
		return nil, errors.New("ss: missing method")
	}
	if host == "" || port == 0 {
		return nil, errors.New("ss: missing host or port")
	}

	out := map[string]any{
		"type":        "shadowsocks",
		"server":      host,
		"server_port": port,
		"method":      method,
		"password":    password,
	}

	if queryStr != "" {
		q, _ := url.ParseQuery(queryStr)
		if plugin := q.Get("plugin"); plugin != "" {
			pluginName, opts := splitSSPlugin(plugin)
			out["plugin"] = pluginName
			out["plugin_opts"] = opts
		}
		if uot := q.Get("uot"); uot != "" {
			if v, err := strconv.Atoi(uot); err == nil {
				out["udp_over_tcp"] = map[string]any{"version": v}
			}
		}
	}

	if tag == "" {
		tag = fmt.Sprintf("ss-%s-%d", host, port)
	} else {
		if decoded, err := url.QueryUnescape(tag); err == nil {
			tag = decoded
		}
		label = tag
	}
	out["tag"] = tag

	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return &ParsedOutbound{
		Tag:      tag,
		Protocol: "shadowsocks",
		Server:   host,
		Port:     uint16(port),
		Outbound: raw,
		Label:    label,
	}, nil
}

// ssDecodeAuthority extracts method/password/host/port from one of the three
// SS link encodings:
//  1. method:password@host:port           — plain
//  2. <base64(method:password)>@host:port — userinfo base64
//  3. <base64(method:password@host:port)> — full-authority base64
func ssDecodeAuthority(body string) (method, password, host string, port uint16, err error) {
	// Try forms 1 and 2 first: split on @
	if at := strings.LastIndex(body, "@"); at > 0 {
		userinfo := body[:at]
		hostport := body[at+1:]
		host, port, err = ssSplitHostPort(hostport)
		if err == nil {
			method, password = ssSplitMethodPassword(userinfo)
			if method != "" {
				return
			}
			// userinfo wasn't plain — try as base64
			if decoded, err2 := DecodeBase64Url(userinfo); err2 == nil {
				method, password = ssSplitMethodPassword(string(decoded))
				if method != "" {
					return
				}
			}
		}
	}
	// Form 3: whole body is base64 of "method:password@host:port"
	if decoded, err2 := DecodeBase64Url(body); err2 == nil {
		s := string(decoded)
		if at := strings.LastIndex(s, "@"); at > 0 {
			userinfo := s[:at]
			hostport := s[at+1:]
			method, password = ssSplitMethodPassword(userinfo)
			host, port, err = ssSplitHostPort(hostport)
			if err == nil && method != "" {
				return
			}
		}
	}
	return "", "", "", 0, errors.New("ss: unrecognized authority encoding")
}

func ssSplitMethodPassword(s string) (method, password string) {
	colon := strings.Index(s, ":")
	if colon < 0 {
		return "", ""
	}
	return s[:colon], s[colon+1:]
}

func ssSplitHostPort(hp string) (host string, port uint16, err error) {
	colon := strings.LastIndex(hp, ":")
	if colon < 0 {
		return "", 0, errors.New("ss: missing port separator")
	}
	host = hp[:colon]
	p, err := strconv.ParseUint(hp[colon+1:], 10, 16)
	if err != nil || p == 0 {
		return "", 0, fmt.Errorf("ss: invalid port: %w", err)
	}
	return host, uint16(p), nil
}

func splitSSPlugin(plugin string) (name, opts string) {
	if semi := strings.Index(plugin, ";"); semi >= 0 {
		return plugin[:semi], plugin[semi+1:]
	}
	return plugin, ""
}
