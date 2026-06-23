package vlink

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

var ErrEncodeUnsupported = errors.New("vlink: outbound type cannot be encoded as share-link")

// EncodeOutbound serializes a sing-box outbound JSON object into a share-link URI.
// label becomes the URI fragment (#name); when empty, the outbound "tag" is used.
func EncodeOutbound(raw json.RawMessage, label string) (string, error) {
	var ob map[string]any
	if err := json.Unmarshal(raw, &ob); err != nil {
		return "", fmt.Errorf("vlink: encode: %w", err)
	}
	if label == "" {
		if tag, _ := ob["tag"].(string); tag != "" {
			label = tag
		}
	}
	typ, _ := ob["type"].(string)
	switch typ {
	case "vless":
		return encodeVless(ob, label)
	case "trojan":
		return encodeTrojan(ob, label)
	case "shadowsocks":
		return encodeShadowsocks(ob, label)
	case "hysteria2":
		return encodeHysteria2(ob, label)
	case "naive":
		return encodeNaive(ob, label)
	case "mieru":
		return encodeMieru(ob, label)
	default:
		return "", fmt.Errorf("%w: %q", ErrEncodeUnsupported, typ)
	}
}

func encodeVless(ob map[string]any, label string) (string, error) {
	uuid, _ := ob["uuid"].(string)
	if uuid == "" {
		return "", errors.New("vlink: vless: missing uuid")
	}
	host, _ := ob["server"].(string)
	if host == "" {
		return "", errors.New("vlink: vless: missing server")
	}
	port := intFromAny(ob["server_port"])
	if port <= 0 || port > 65535 {
		return "", errors.New("vlink: vless: invalid server_port")
	}

	u := &url.URL{
		Scheme: "vless",
		Host:   netJoinHostPort(host, port),
		User:   url.User(uuid),
	}
	q, err := streamQueryFromOutbound(ob)
	if err != nil {
		return "", err
	}
	if flow, _ := ob["flow"].(string); flow != "" {
		q.Set("flow", flow)
	}
	if enc, _ := ob["encryption"].(string); enc != "" && !strings.EqualFold(enc, "none") {
		q.Set("encryption", enc)
	}
	u.RawQuery = q.Encode()
	if label != "" {
		u.Fragment = label
	}
	return u.String(), nil
}

func encodeTrojan(ob map[string]any, label string) (string, error) {
	password, _ := ob["password"].(string)
	if password == "" {
		return "", errors.New("vlink: trojan: missing password")
	}
	host, _ := ob["server"].(string)
	if host == "" {
		return "", errors.New("vlink: trojan: missing server")
	}
	port := intFromAny(ob["server_port"])
	if port <= 0 || port > 65535 {
		return "", errors.New("vlink: trojan: invalid server_port")
	}

	u := &url.URL{
		Scheme: "trojan",
		Host:   netJoinHostPort(host, port),
		User:   url.User(password),
	}
	q, err := streamQueryFromOutbound(ob)
	if err != nil {
		return "", err
	}
	if sec := q.Get("security"); sec == "" {
		q.Set("security", "tls")
	}
	u.RawQuery = q.Encode()
	if label != "" {
		u.Fragment = label
	}
	return u.String(), nil
}

func encodeShadowsocks(ob map[string]any, label string) (string, error) {
	method, _ := ob["method"].(string)
	password, _ := ob["password"].(string)
	if method == "" || password == "" {
		return "", errors.New("vlink: shadowsocks: missing method or password")
	}
	host, _ := ob["server"].(string)
	if host == "" {
		return "", errors.New("vlink: shadowsocks: missing server")
	}
	port := intFromAny(ob["server_port"])
	if port <= 0 || port > 65535 {
		return "", errors.New("vlink: shadowsocks: invalid server_port")
	}

	// SIP002: userinfo is base64url(method:password). Plain percent-encoded
	// userinfo (url.UserPassword) is not percent-decoded by standard SS clients
	// nor by this package's parser, so a password with reserved characters
	// (@ : / # %) would decode wrong. base64url is URL-safe → no extra escaping.
	userinfo := base64.RawURLEncoding.EncodeToString([]byte(method + ":" + password))
	q, err := streamQueryFromOutbound(ob)
	if err != nil {
		return "", err
	}
	u := &url.URL{
		Scheme: "ss",
		Host:   netJoinHostPort(host, port),
		User:   url.User(userinfo),
	}
	u.RawQuery = q.Encode()
	if label != "" {
		u.Fragment = label
	}
	return u.String(), nil
}

func encodeHysteria2(ob map[string]any, label string) (string, error) {
	password, _ := ob["password"].(string)
	if password == "" {
		return "", errors.New("vlink: hysteria2: missing password")
	}
	host, _ := ob["server"].(string)
	if host == "" {
		return "", errors.New("vlink: hysteria2: missing server")
	}
	port := intFromAny(ob["server_port"])
	if port <= 0 || port > 65535 {
		return "", errors.New("vlink: hysteria2: invalid server_port")
	}

	u := &url.URL{
		Scheme: "hysteria2",
		Host:   netJoinHostPort(host, port),
		User:   url.User(password),
	}
	q := url.Values{}

	if tls, _ := ob["tls"].(map[string]any); tls != nil {
		if sni, _ := tls["server_name"].(string); sni != "" {
			q.Set("sni", sni)
		}
		if alpn := stringSliceFromAny(tls["alpn"]); len(alpn) > 0 {
			q.Set("alpn", strings.Join(alpn, ","))
		}
		if tls["insecure"] == true {
			q.Set("insecure", "1")
		}
		if ech, _ := tls["ech"].(map[string]any); ech != nil && ech["enabled"] == true {
			q.Set("ech", "1")
		}
	}

	if ports := stringSliceFromAny(ob["server_ports"]); len(ports) > 0 {
		q.Set("mport", encodeMport(ports))
	}
	if hop, _ := ob["hop_interval"].(string); hop != "" {
		q.Set("hop_interval", hop)
	}
	if obfs, _ := ob["obfs"].(map[string]any); obfs != nil {
		if t, _ := obfs["type"].(string); t != "" {
			q.Set("obfs", t)
		}
		if pw, _ := obfs["password"].(string); pw != "" {
			q.Set("obfs-password", pw)
		}
	}
	if brutal, _ := ob["brutal"].(map[string]any); brutal != nil {
		q.Set("congestion", "brutal")
		if up := intFromAny(brutal["up_mbps"]); up > 0 {
			q.Set("brutal_up", strconv.Itoa(up))
		}
		if down := intFromAny(brutal["down_mbps"]); down > 0 {
			q.Set("brutal_down", strconv.Itoa(down))
		}
	}

	u.RawQuery = q.Encode()
	if label != "" {
		u.Fragment = label
	}
	return u.String(), nil
}

func encodeNaive(ob map[string]any, label string) (string, error) {
	username, _ := ob["username"].(string)
	password, _ := ob["password"].(string)
	if username == "" && password == "" {
		return "", errors.New("vlink: naive: missing credentials")
	}
	host, _ := ob["server"].(string)
	if host == "" {
		return "", errors.New("vlink: naive: missing server")
	}
	port := intFromAny(ob["server_port"])
	if port <= 0 || port > 65535 {
		return "", errors.New("vlink: naive: invalid server_port")
	}

	scheme := "http"
	if tls, _ := ob["tls"].(map[string]any); tls != nil && tls["enabled"] == true {
		scheme = "https"
	}

	u := &url.URL{
		Scheme: scheme,
		Host:   netJoinHostPort(host, port),
		User:   url.UserPassword(username, password),
	}
	if label != "" {
		u.Fragment = label
	}
	return "naive+" + u.String(), nil
}

func encodeMieru(ob map[string]any, label string) (string, error) {
	username, _ := ob["username"].(string)
	password, _ := ob["password"].(string)
	if username == "" {
		return "", errors.New("vlink: mieru: missing username")
	}
	if password == "" {
		return "", errors.New("vlink: mieru: missing password")
	}
	host, _ := ob["server"].(string)
	if host == "" {
		return "", errors.New("vlink: mieru: missing server")
	}
	transport, _ := ob["transport"].(string)
	if transport == "" {
		transport = "TCP"
	}

	q := url.Values{}
	profile := label
	if profile == "" {
		if tag, _ := ob["tag"].(string); tag != "" {
			profile = tag
		} else {
			profile = "default"
		}
	}
	q.Set("profile", profile)
	q.Add("protocol", transport)

	port := intFromAny(ob["server_port"])
	if port > 0 {
		q.Add("port", strconv.Itoa(port))
	}
	for _, spec := range stringSliceFromAny(ob["server_ports"]) {
		q.Add("port", decodeMieruPortSpec(spec))
	}
	if port <= 0 && len(q["port"]) == 0 {
		return "", errors.New("vlink: mieru: missing server port")
	}
	if mux, _ := ob["multiplexing"].(string); mux != "" {
		q.Set("multiplexing", mux)
	}
	if tp, _ := ob["traffic_pattern"].(string); tp != "" {
		q.Set("traffic-pattern", tp)
	}

	u := &url.URL{
		Scheme:   "mierus",
		Host:     host,
		User:     url.UserPassword(username, password),
		RawQuery: q.Encode(),
	}
	return u.String(), nil
}

func streamQueryFromOutbound(ob map[string]any) (url.Values, error) {
	q := url.Values{}

	network := "tcp"
	if transport, _ := ob["transport"].(map[string]any); transport != nil {
		switch ttype := strings.ToLower(stringFromAny(transport["type"])); ttype {
		case "", "tcp":
			// no transport / explicit tcp — plain network, nothing to add
		case "ws":
			network = "ws"
			if path, _ := transport["path"].(string); path != "" {
				if ed := intFromAny(transport["max_early_data"]); ed > 0 {
					path = path + "?ed=" + strconv.Itoa(ed)
				}
				q.Set("path", path)
			}
			if headers, _ := transport["headers"].(map[string]any); headers != nil {
				if host, _ := headers["Host"].(string); host != "" {
					q.Set("host", host)
				}
			}
		case "grpc":
			network = "grpc"
			if svc, _ := transport["service_name"].(string); svc != "" {
				q.Set("serviceName", svc)
			}
		case "http":
			network = "h2"
			if path, _ := transport["path"].(string); path != "" {
				q.Set("path", path)
			}
			if hosts := stringSliceFromAny(transport["host"]); len(hosts) > 0 {
				q.Set("host", hosts[0])
			}
		case "httpupgrade":
			network = "httpupgrade"
			if path, _ := transport["path"].(string); path != "" {
				q.Set("path", path)
			}
			// httpupgrade host is a top-level string (not headers.Host like ws).
			if host, _ := transport["host"].(string); host != "" {
				q.Set("host", host)
			}
		case "xhttp":
			network = "xhttp"
			if path, _ := transport["path"].(string); path != "" {
				q.Set("path", path)
			}
			// xhttp host is a top-level string (not headers.Host like ws).
			if host, _ := transport["host"].(string); host != "" {
				q.Set("host", host)
			}
			if mode, _ := transport["mode"].(string); mode != "" {
				q.Set("mode", mode)
			}
			// x_padding_bytes is an internal/server-negotiated default; not part
			// of the share-link standard, so it is intentionally not emitted.
		default:
			// Unknown transport (e.g. quic) — fail closed rather than silently
			// emitting a plain-tcp link that misroutes.
			return nil, fmt.Errorf("vlink: unsupported transport type %q", ttype)
		}
	}
	if network != "tcp" {
		q.Set("type", network)
	}

	if tls, _ := ob["tls"].(map[string]any); tls != nil {
		reality, _ := tls["reality"].(map[string]any)
		hasReality := reality != nil && reality["enabled"] == true
		hasTLS := tls["enabled"] == true
		if hasReality || hasTLS {
			if hasReality {
				q.Set("security", "reality")
				if pk, _ := reality["public_key"].(string); pk != "" {
					q.Set("pbk", pk)
				}
				if sid, _ := reality["short_id"].(string); sid != "" {
					q.Set("sid", sid)
				}
			} else {
				q.Set("security", "tls")
			}
			if sni, _ := tls["server_name"].(string); sni != "" {
				q.Set("sni", sni)
			}
			if alpn := stringSliceFromAny(tls["alpn"]); len(alpn) > 0 {
				q.Set("alpn", strings.Join(alpn, ","))
			}
			if tls["insecure"] == true {
				q.Set("insecure", "1")
			}
			if utls, _ := tls["utls"].(map[string]any); utls != nil {
				if fp, _ := utls["fingerprint"].(string); fp != "" {
					q.Set("fp", fp)
				}
			}
		}
	}

	return q, nil
}

func encodeMport(specs []string) string {
	parts := make([]string, 0, len(specs))
	for _, spec := range specs {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			continue
		}
		if strings.Contains(spec, ":") {
			rng := strings.SplitN(spec, ":", 2)
			parts = append(parts, rng[0]+"-"+rng[1])
		} else {
			parts = append(parts, spec)
		}
	}
	return strings.Join(parts, ",")
}

func decodeMieruPortSpec(spec string) string {
	spec = strings.TrimSpace(spec)
	if strings.Contains(spec, ":") {
		rng := strings.SplitN(spec, ":", 2)
		return rng[0] + "-" + rng[1]
	}
	return spec
}

func netJoinHostPort(host string, port int) string {
	if strings.Contains(host, ":") {
		return fmt.Sprintf("[%s]:%d", host, port)
	}
	return fmt.Sprintf("%s:%d", host, port)
}

func intFromAny(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return 0
	}
}

func stringFromAny(v any) string {
	s, _ := v.(string)
	return s
}

func stringSliceFromAny(v any) []string {
	switch arr := v.(type) {
	case []string:
		return arr
	case []any:
		out := make([]string, 0, len(arr))
		for _, item := range arr {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}
