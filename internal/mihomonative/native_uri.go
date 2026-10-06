package mihomonative

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

func compileMihomoSpecificURI(raw string, preference, routingEngine EnginePreference) (*ProxyNode, bool, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, false, err
	}
	protocol := strings.ToLower(u.Scheme)
	if protocol != "hysteria" && protocol != "tuic" && protocol != "anytls" {
		return nil, false, nil
	}
	if u.Hostname() == "" {
		return nil, true, fmt.Errorf("mihomo %s: server is required", protocol)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, true, fmt.Errorf("mihomo %s: valid port is required", protocol)
	}
	compatibility := mihomoNativeCompatibility(protocol)
	selected, reason := SelectEngine(preference, compatibility, routingEngine)
	if selected == "" {
		return nil, true, fmt.Errorf("mihomo %s: %s", protocol, reason)
	}
	name, _ := url.PathUnescape(u.Fragment)
	if strings.TrimSpace(name) == "" {
		name = fmt.Sprintf("%s-%s-%d", protocol, u.Hostname(), port)
	}
	proxy := map[string]interface{}{"name": name, "type": protocol, "server": u.Hostname(), "port": port}
	q := u.Query()
	switch protocol {
	case "hysteria":
		if err := compileHysteriaURI(proxy, u, q); err != nil {
			return nil, true, err
		}
	case "tuic":
		if err := compileTUICURI(proxy, u, q); err != nil {
			return nil, true, err
		}
	case "anytls":
		if err := compileAnyTLSURI(proxy, u, q); err != nil {
			return nil, true, err
		}
	}
	return &ProxyNode{
		Name: name, Protocol: protocol, Transport: nativeTransport(protocol),
		EnginePreference: preference, SelectedEngine: selected, RawURI: raw,
		NativeConfig: proxy, Compatibility: compatibility, Enabled: true,
	}, true, nil
}

func compileHysteriaURI(proxy map[string]interface{}, u *url.URL, q url.Values) error {
	auth := firstQuery(q, "auth", "auth-str")
	if auth == "" && u.User != nil {
		auth = u.User.Username()
	}
	if auth == "" {
		return fmt.Errorf("mihomo hysteria: auth is required")
	}
	proxy["auth-str"] = auth
	copyQuery(proxy, q, "ports", "mport")
	copyQuery(proxy, q, "obfs", "obfs")
	copyQuery(proxy, q, "protocol", "protocol")
	if _, ok := proxy["protocol"]; !ok {
		proxy["protocol"] = "udp"
	}
	copyRate(proxy, q, "up", "upmbps", "up")
	copyRate(proxy, q, "down", "downmbps", "down")
	mapURICommonTLS(proxy, q)
	return nil
}

func compileTUICURI(proxy map[string]interface{}, u *url.URL, q url.Values) error {
	username, password := "", ""
	if u.User != nil {
		username = u.User.Username()
		password, _ = u.User.Password()
	}
	token := firstQuery(q, "token")
	if token != "" {
		proxy["token"] = token
	} else {
		if username == "" || password == "" {
			return fmt.Errorf("mihomo tuic: uuid and password, or token, are required")
		}
		proxy["uuid"], proxy["password"] = username, password
	}
	copyQuery(proxy, q, "udp-relay-mode", "udp_relay_mode", "udp-relay-mode")
	copyQuery(proxy, q, "congestion-controller", "congestion_control", "congestion-controller")
	copyBoolQuery(proxy, q, "reduce-rtt", "reduce_rtt", "reduce-rtt")
	copyBoolQuery(proxy, q, "disable-sni", "disable_sni", "disable-sni")
	mapURICommonTLS(proxy, q)
	return nil
}

func compileAnyTLSURI(proxy map[string]interface{}, u *url.URL, q url.Values) error {
	if strings.EqualFold(q.Get("security"), "reality") || q.Get("pbk") != "" {
		return fmt.Errorf("mihomo anytls: Reality is not supported by Mihomo")
	}
	password := ""
	if u.User != nil {
		password = u.User.Username()
		if pw, ok := u.User.Password(); ok {
			password += ":" + pw
		}
	}
	if password == "" {
		password = q.Get("password")
	}
	if password == "" {
		return fmt.Errorf("mihomo anytls: password is required")
	}
	proxy["password"], proxy["udp"] = password, true
	copyQuery(proxy, q, "client-metadata", "client-metadata", "client_metadata")
	copyIntQuery(proxy, q, "idle-session-check-interval", "idle-session-check-interval", "idle_session_check_interval")
	copyIntQuery(proxy, q, "idle-session-timeout", "idle-session-timeout", "idle_session_timeout")
	copyIntQuery(proxy, q, "min-idle-session", "min-idle-session", "min_idle_session")
	mapURICommonTLS(proxy, q)
	return nil
}

func mapURICommonTLS(proxy map[string]interface{}, q url.Values) {
	copyQuery(proxy, q, "sni", "sni", "peer")
	copyQuery(proxy, q, "client-fingerprint", "fp", "client-fingerprint")
	copyBoolQuery(proxy, q, "skip-cert-verify", "insecure", "allowInsecure")
	if alpn := splitCSV(firstQuery(q, "alpn")); len(alpn) > 0 {
		proxy["alpn"] = alpn
	}
}

func nativeTransport(protocol string) string {
	if protocol == "anytls" {
		return "tls"
	}
	return "quic"
}

func firstQuery(q url.Values, keys ...string) string {
	for _, key := range keys {
		if value := q.Get(key); value != "" {
			return value
		}
	}
	return ""
}

func copyQuery(proxy map[string]interface{}, q url.Values, target string, keys ...string) {
	if value := firstQuery(q, keys...); value != "" {
		proxy[target] = value
	}
}

func copyRate(proxy map[string]interface{}, q url.Values, target string, keys ...string) {
	if value := firstQuery(q, keys...); value != "" {
		proxy[target] = value
	}
}

func copyBoolQuery(proxy map[string]interface{}, q url.Values, target string, keys ...string) {
	value := strings.ToLower(firstQuery(q, keys...))
	if value == "1" || value == "true" || value == "yes" {
		proxy[target] = true
	}
	if value == "0" || value == "false" || value == "no" {
		proxy[target] = false
	}
}

func copyIntQuery(proxy map[string]interface{}, q url.Values, target string, keys ...string) {
	if value, err := strconv.Atoi(firstQuery(q, keys...)); err == nil {
		proxy[target] = value
	}
}
