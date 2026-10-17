package mihomonative

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// CompileVLESS parses a VLESS share URI without converting through sing-box,
// preserving Mihomo-specific transports such as XHTTP.
func CompileVLESS(raw string, preference, routingEngine EnginePreference) (*ProxyNode, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(u.Scheme, "vless") {
		return nil, fmt.Errorf("mihomo vless: invalid URI")
	}
	if u.User == nil || u.User.Username() == "" || u.Hostname() == "" {
		return nil, fmt.Errorf("mihomo vless: uuid and server are required")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("mihomo vless: valid port is required")
	}
	q := u.Query()
	transport := strings.ToLower(q.Get("type"))
	if transport == "" {
		transport = "tcp"
	}
	compatibility := CheckVLESS(transport)
	selected, reason := SelectEngine(preference, compatibility, routingEngine)
	if selected == "" {
		return nil, fmt.Errorf("mihomo vless: %s", reason)
	}
	name, _ := url.PathUnescape(u.Fragment)
	if strings.TrimSpace(name) == "" {
		name = u.Hostname() + ":" + strconv.Itoa(port)
	}
	proxy := map[string]interface{}{
		"name": name, "type": "vless", "server": u.Hostname(), "port": port,
		"uuid": u.User.Username(), "udp": true, "network": transport,
		"encryption": q.Get("encryption"),
	}
	if pe := firstNonEmpty(q.Get("packetEncoding"), q.Get("packet-encoding"), q.Get("packet_encoding")); pe != "" {
		proxy["packet-encoding"] = pe
	}
	if flow := q.Get("flow"); flow != "" && flow != "none" {
		proxy["flow"] = flow
	}
	if tfo := q.Get("tfo"); tfo == "1" || strings.EqualFold(tfo, "true") {
		proxy["tfo"] = true
	}
	security := strings.ToLower(q.Get("security"))
	if security == "tls" || security == "reality" {
		proxy["tls"] = true
		if sni := firstNonEmpty(q.Get("sni"), q.Get("servername")); sni != "" {
			proxy["servername"] = sni
		}
		if fp := firstNonEmpty(q.Get("fp"), q.Get("fingerprint")); fp != "" {
			proxy["client-fingerprint"] = fp
		}
	}
	if security == "reality" {
		proxy["reality-opts"] = map[string]interface{}{
			"public-key": q.Get("pbk"),
			"short-id":   q.Get("sid"),
		}
	}
	if alpn := splitCSV(q.Get("alpn")); len(alpn) > 0 {
		proxy["alpn"] = alpn
	}
	addTransportOptions(proxy, transport, q)
	return &ProxyNode{
		Name: name, Protocol: "vless", Transport: transport,
		EnginePreference: preference, SelectedEngine: selected,
		RawURI: raw, NativeConfig: proxy, Compatibility: compatibility, Enabled: true,
	}, nil
}

func addTransportOptions(proxy map[string]interface{}, transport string, q url.Values) {
	path, host := q.Get("path"), q.Get("host")
	switch transport {
	case "ws":
		opts := map[string]interface{}{}
		if path != "" {
			opts["path"] = path
		}
		if host != "" {
			opts["headers"] = map[string]string{"Host": host}
		}
		proxy["ws-opts"] = opts
	case "grpc":
		service := q.Get("serviceName")
		if service == "" {
			service = q.Get("service-name")
		}
		proxy["grpc-opts"] = map[string]interface{}{"grpc-service-name": service}
	case "h2":
		opts := map[string]interface{}{}
		if path != "" {
			opts["path"] = path
		}
		if host != "" {
			opts["host"] = splitCSV(host)
		}
		proxy["h2-opts"] = opts
	case "http":
		opts := map[string]interface{}{}
		if path != "" {
			opts["path"] = []string{path}
		}
		if host != "" {
			opts["headers"] = map[string][]string{"Host": []string{host}}
		}
		proxy["http-opts"] = opts
	case "xhttp", "splithttp":
		opts := map[string]interface{}{}
		if path != "" {
			opts["path"] = path
		}
		if host != "" {
			opts["host"] = host
		}
		if mode := q.Get("mode"); mode != "" {
			opts["mode"] = mode
		}
		populateXHTTPExtra(opts, q.Get("extra"))
		proxy["xhttp-opts"] = opts
	}
}

func populateXHTTPExtra(opts map[string]interface{}, extraRaw string) {
	extraRaw = strings.TrimSpace(extraRaw)
	if extraRaw == "" {
		return
	}
	var extra map[string]interface{}
	if err := json.Unmarshal([]byte(extraRaw), &extra); err != nil {
		return
	}

	mapping := map[string]string{
		"uplinkHTTPMethod":         "uplink-http-method",
		"uplink-http-method":       "uplink-http-method",
		"seqKey":                   "seq-key",
		"seq-key":                  "seq-key",
		"seqPlacement":             "seq-placement",
		"seq-placement":            "seq-placement",
		"sessionIDKey":             "session-key",
		"sessionIdKey":             "session-key",
		"sessionKey":               "session-key",
		"session-id-key":           "session-key",
		"session-key":              "session-key",
		"sessionIDPlacement":       "session-placement",
		"sessionIdPlacement":       "session-placement",
		"sessionPlacement":         "session-placement",
		"session-id-placement":     "session-placement",
		"session-placement":        "session-placement",
		"xPaddingBytes":            "x-padding-bytes",
		"x-padding-bytes":          "x-padding-bytes",
		"xPaddingHeader":           "x-padding-header",
		"x-padding-header":         "x-padding-header",
		"xPaddingKey":              "x-padding-key",
		"x-padding-key":            "x-padding-key",
		"xPaddingMethod":           "x-padding-method",
		"x-padding-method":         "x-padding-method",
		"xPaddingObfsMode":         "x-padding-obfs-mode",
		"x-padding-obfs-mode":      "x-padding-obfs-mode",
		"xPaddingPlacement":        "x-padding-placement",
		"x-padding-placement":      "x-padding-placement",
		"scMaxEachPostBytes":       "sc-max-each-post-bytes",
		"sc-max-each-post-bytes":   "sc-max-each-post-bytes",
		"scMinPostsIntervalMs":     "sc-min-posts-interval-ms",
		"sc-min-posts-interval-ms": "sc-min-posts-interval-ms",
		"noGRPCHeader":             "no-grpc-header",
		"no-grpc-header":           "no-grpc-header",
		"noSSEHeader":              "no-sse-header",
		"no-sse-header":            "no-sse-header",
	}

	for srcKey, targetKey := range mapping {
		if val, ok := extra[srcKey]; ok && val != nil {
			if _, exists := opts[targetKey]; !exists {
				opts[targetKey] = val
			}
		}
	}

	if headers, ok := extra["headers"].(map[string]interface{}); ok && len(headers) > 0 {
		opts["headers"] = headers
	}

	reuseRaw, hasReuse := extra["reuse-settings"].(map[string]interface{})
	if !hasReuse {
		reuseRaw, hasReuse = extra["reuseSettings"].(map[string]interface{})
	}
	if !hasReuse {
		reuseRaw, hasReuse = extra["xmux"].(map[string]interface{})
	}
	if hasReuse && len(reuseRaw) > 0 {
		reuseOpts := map[string]interface{}{}
		reuseMap := map[string]string{
			"maxConcurrency":      "max-concurrency",
			"max-concurrency":     "max-concurrency",
			"maxConnections":      "max-connections",
			"max-connections":     "max-connections",
			"cMaxReuseTimes":      "c-max-reuse-times",
			"c-max-reuse-times":   "c-max-reuse-times",
			"hMaxRequestTimes":    "h-max-request-times",
			"h-max-request-times": "h-max-request-times",
			"hMaxReusableSecs":    "h-max-reusable-secs",
			"h-max-reusable-secs": "h-max-reusable-secs",
			"hKeepAlivePeriod":    "h-keep-alive-period",
			"h-keep-alive-period": "h-keep-alive-period",
		}
		for rSrc, rTarget := range reuseMap {
			if rVal, ok := reuseRaw[rSrc]; ok && rVal != nil {
				if _, exists := reuseOpts[rTarget]; !exists {
					reuseOpts[rTarget] = rVal
				}
			}
		}
		if len(reuseOpts) > 0 {
			opts["reuse-settings"] = reuseOpts
		}
	}
}

func splitCSV(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
