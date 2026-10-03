package mihomonative

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// vmessJSON represents the standard JSON schema embedded inside vmess://<base64> share links.
type vmessJSON struct {
	V    interface{} `json:"v"`
	Ps   string      `json:"ps"`
	Add  string      `json:"add"`
	Port interface{} `json:"port"`
	ID   string      `json:"id"`
	Aid  interface{} `json:"aid"`
	Scy  string      `json:"scy"`
	Net  string      `json:"net"`
	Type string      `json:"type"`
	Host string      `json:"host"`
	Path string      `json:"path"`
	TLS  string      `json:"tls"`
	SNI  string      `json:"sni"`
	ALPN string      `json:"alpn"`
	FP   string      `json:"fp"`
}

// CompileVMess parses a VMess share URI (base64 JSON or standard URI) for native Mihomo execution.
func CompileVMess(raw string, preference, routingEngine EnginePreference) (*ProxyNode, error) {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(strings.ToLower(trimmed), "vmess://") {
		return nil, fmt.Errorf("mihomo vmess: missing vmess:// scheme")
	}

	payload := trimmed[8:]
	// Try parsing as base64-encoded JSON first (most common format)
	if node, err := compileVMessBase64(trimmed, payload, preference, routingEngine); err == nil {
		return node, nil
	}

	// Fallback: try parsing as standard URI format vmess://uuid@host:port?query#name
	return compileVMessURI(trimmed, preference, routingEngine)
}

func decodeBase64Flexible(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	// Try standard padding
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	// Try raw standard (without padding)
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	// Try URL-safe with padding
	if b, err := base64.URLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	// Try raw URL-safe
	return base64.RawURLEncoding.DecodeString(s)
}

func compileVMessBase64(raw, payload string, preference, routingEngine EnginePreference) (*ProxyNode, error) {
	decoded, err := decodeBase64Flexible(payload)
	if err != nil {
		return nil, fmt.Errorf("mihomo vmess: base64 decode failed: %w", err)
	}

	var data vmessJSON
	if err := json.Unmarshal(decoded, &data); err != nil {
		return nil, fmt.Errorf("mihomo vmess: unmarshal json failed: %w", err)
	}

	server := strings.TrimSpace(data.Add)
	if server == "" {
		return nil, fmt.Errorf("mihomo vmess: server address is required")
	}

	uuid := strings.TrimSpace(data.ID)
	if uuid == "" {
		return nil, fmt.Errorf("mihomo vmess: uuid is required")
	}

	port := parsePort(data.Port)
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("mihomo vmess: invalid port %v", data.Port)
	}

	alterID := parseAlterID(data.Aid)

	cipher := strings.TrimSpace(data.Scy)
	if cipher == "" {
		cipher = "auto"
	}

	transport := strings.ToLower(strings.TrimSpace(data.Net))
	if transport == "" {
		transport = "tcp"
	}

	compatibility := CheckVMess(transport)
	selected, reason := SelectEngine(preference, compatibility, routingEngine)
	if selected == "" {
		return nil, fmt.Errorf("mihomo vmess: %s", reason)
	}

	name := strings.TrimSpace(data.Ps)
	if name == "" {
		name = fmt.Sprintf("%s:%d", server, port)
	}

	proxy := map[string]interface{}{
		"name":         name,
		"type":         "vmess",
		"server":       server,
		"port":         port,
		"uuid":         uuid,
		"alterId":      alterID,
		"cipher":       cipher,
		"udp":          true,
		"network":      transport,
		"routing-mark": 666,
	}

	// TLS options
	tlsVal := strings.ToLower(strings.TrimSpace(data.TLS))
	if tlsVal == "tls" || tlsVal == "reality" {
		proxy["tls"] = true
		sni := strings.TrimSpace(data.SNI)
		if sni == "" && data.Host != "" {
			sni = strings.TrimSpace(data.Host)
		}
		if sni != "" {
			proxy["servername"] = sni
		}
		if data.FP != "" {
			proxy["client-fingerprint"] = strings.TrimSpace(data.FP)
		}
		if data.ALPN != "" {
			if alpnList := splitCSV(data.ALPN); len(alpnList) > 0 {
				proxy["alpn"] = alpnList
			}
		}
	}

	// Transport options
	path := strings.TrimSpace(data.Path)
	host := strings.TrimSpace(data.Host)

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
		serviceName := path
		if serviceName == "" {
			serviceName = host
		}
		proxy["grpc-opts"] = map[string]interface{}{"grpc-service-name": serviceName}
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
			opts["headers"] = map[string][]string{"Host": {host}}
		}
		proxy["http-opts"] = opts
	}

	return &ProxyNode{
		Name:             name,
		Protocol:         "vmess",
		Transport:        transport,
		EnginePreference: preference,
		SelectedEngine:   selected,
		RawURI:           raw,
		NativeConfig:     proxy,
		Compatibility:    compatibility,
		Enabled:          true,
	}, nil
}

func compileVMessURI(raw string, preference, routingEngine EnginePreference) (*ProxyNode, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(u.Scheme, "vmess") {
		return nil, fmt.Errorf("mihomo vmess: invalid URI")
	}
	if u.User == nil || u.User.Username() == "" || u.Hostname() == "" {
		return nil, fmt.Errorf("mihomo vmess: uuid and server are required")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("mihomo vmess: valid port is required")
	}

	q := u.Query()
	transport := strings.ToLower(q.Get("type"))
	if transport == "" {
		transport = strings.ToLower(q.Get("net"))
	}
	if transport == "" {
		transport = "tcp"
	}

	compatibility := CheckVMess(transport)
	selected, reason := SelectEngine(preference, compatibility, routingEngine)
	if selected == "" {
		return nil, fmt.Errorf("mihomo vmess: %s", reason)
	}

	name, _ := url.PathUnescape(u.Fragment)
	if strings.TrimSpace(name) == "" {
		name = fmt.Sprintf("%s:%d", u.Hostname(), port)
	}

	alterID := 0
	if aidStr := q.Get("aid"); aidStr != "" {
		if v, e := strconv.Atoi(aidStr); e == nil {
			alterID = v
		}
	}
	if alterID == 0 {
		if aidStr := q.Get("alterId"); aidStr != "" {
			if v, e := strconv.Atoi(aidStr); e == nil {
				alterID = v
			}
		}
	}

	cipher := q.Get("encryption")
	if cipher == "" {
		cipher = q.Get("scy")
	}
	if cipher == "" {
		cipher = "auto"
	}

	proxy := map[string]interface{}{
		"name":         name,
		"type":         "vmess",
		"server":       u.Hostname(),
		"port":         port,
		"uuid":         u.User.Username(),
		"alterId":      alterID,
		"cipher":       cipher,
		"udp":          true,
		"network":      transport,
		"routing-mark": 666,
	}

	security := strings.ToLower(q.Get("security"))
	if security == "" {
		security = strings.ToLower(q.Get("tls"))
	}
	if security == "tls" || security == "reality" {
		proxy["tls"] = true
		if sni := q.Get("sni"); sni != "" {
			proxy["servername"] = sni
		}
		if fp := q.Get("fp"); fp != "" {
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
		Name:             name,
		Protocol:         "vmess",
		Transport:        transport,
		EnginePreference: preference,
		SelectedEngine:   selected,
		RawURI:           raw,
		NativeConfig:     proxy,
		Compatibility:    compatibility,
		Enabled:          true,
	}, nil
}

func parsePort(val interface{}) int {
	switch v := val.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		if p, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return p
		}
	case json.Number:
		if p, err := v.Int64(); err == nil {
			return int(p)
		}
	}
	return 0
}

func parseAlterID(val interface{}) int {
	switch v := val.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		if a, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return a
		}
	case json.Number:
		if a, err := v.Int64(); err == nil {
			return int(a)
		}
	}
	return 0
}
