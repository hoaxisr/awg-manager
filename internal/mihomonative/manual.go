package mihomonative

import (
	"fmt"
	"strings"
)

// ManualProxyInput is the structured counterpart of a share link. Config uses
// Mihomo field names and is filtered through a protocol-specific allow-list.
type ManualProxyInput struct {
	Name             string                 `json:"name"`
	Protocol         string                 `json:"protocol"`
	Server           string                 `json:"server"`
	Port             int                    `json:"port"`
	EnginePreference EnginePreference       `json:"enginePreference"`
	Config           map[string]interface{} `json:"config"`
}

var manualAllowedFields = map[string]map[string]bool{
	"trusttunnel": fieldSet("username", "password", "health-check", "udp", "sni", "alpn", "skip-cert-verify", "name-cert-verify", "client-fingerprint", "quic", "congestion-controller", "bbr-profile", "max-connections", "min-streams", "max-streams"),
	"hysteria":    fieldSet("ports", "auth-str", "obfs", "alpn", "protocol", "up", "down", "sni", "skip-cert-verify", "name-cert-verify", "recv-window-conn", "recv-window", "disable-mtu-discovery", "fingerprint", "fast-open"),
	"hysteria2":   fieldSet("ports", "hop-interval", "password", "up", "down", "obfs", "obfs-password", "obfs-min-packet-size", "obfs-max-packet-size", "sni", "skip-cert-verify", "name-cert-verify", "fingerprint", "alpn", "handshake-timeout"),
	"trojan":      fieldSet("password", "udp", "sni", "alpn", "client-fingerprint", "fingerprint", "skip-cert-verify", "name-cert-verify", "network", "ws-opts", "grpc-opts", "reality-opts"),
	"tuic":        fieldSet("token", "uuid", "password", "ip", "heartbeat-interval", "alpn", "disable-sni", "reduce-rtt", "request-timeout", "udp-relay-mode", "congestion-controller", "bbr-profile", "max-udp-relay-packet-size", "fast-open", "skip-cert-verify", "name-cert-verify", "max-open-streams", "sni"),
	"anytls":      fieldSet("password", "client-fingerprint", "udp", "client-metadata", "idle-session-check-interval", "idle-session-timeout", "min-idle-session", "sni", "alpn", "skip-cert-verify", "name-cert-verify", "shadow-tls-opts", "restls-opts", "jls-opts"),
}

func CompileManual(in ManualProxyInput, routingEngine EnginePreference) (*ProxyNode, error) {
	in.Name, in.Protocol, in.Server = strings.TrimSpace(in.Name), strings.ToLower(strings.TrimSpace(in.Protocol)), strings.TrimSpace(in.Server)
	allowed, ok := manualAllowedFields[in.Protocol]
	if !ok {
		return nil, fmt.Errorf("mihomo manual proxy: unsupported protocol %q", in.Protocol)
	}
	if in.Name == "" || in.Server == "" || in.Port < 1 || in.Port > 65535 {
		return nil, fmt.Errorf("mihomo manual proxy: name, server and valid port are required")
	}
	compatibility := mihomoNativeCompatibility(in.Protocol)
	selected, reason := SelectEngine(in.EnginePreference, compatibility, routingEngine)
	if selected == "" {
		return nil, fmt.Errorf("mihomo manual proxy: %s", reason)
	}
	proxy := map[string]interface{}{"name": in.Name, "type": in.Protocol, "server": in.Server, "port": in.Port}
	for key, value := range in.Config {
		if !allowed[key] {
			return nil, fmt.Errorf("mihomo manual proxy: field %q is not allowed for %s", key, in.Protocol)
		}
		proxy[key] = value
	}
	if err := validateManualAuth(in.Protocol, proxy); err != nil {
		return nil, err
	}
	if in.Protocol == "anytls" {
		proxy["udp"] = true
	}
	return &ProxyNode{
		Name: in.Name, Protocol: in.Protocol, Transport: manualTransport(in.Protocol, proxy),
		EnginePreference: in.EnginePreference, SelectedEngine: selected,
		NativeConfig: proxy, Compatibility: compatibility, Enabled: true,
	}, nil
}

func validateManualAuth(protocol string, proxy map[string]interface{}) error {
	required := []string{"password"}
	switch protocol {
	case "trusttunnel":
		required = []string{"username", "password"}
	case "hysteria":
		required = []string{"auth-str"}
	case "tuic":
		if nonEmptyString(proxy["token"]) {
			return nil
		}
		required = []string{"uuid", "password"}
	}
	for _, key := range required {
		if !nonEmptyString(proxy[key]) {
			return fmt.Errorf("mihomo manual proxy: %s requires %s", protocol, key)
		}
	}
	return nil
}

func manualTransport(protocol string, proxy map[string]interface{}) string {
	switch protocol {
	case "hysteria", "hysteria2", "tuic":
		return "quic"
	case "trusttunnel":
		if value, _ := proxy["quic"].(bool); value {
			return "quic"
		}
		return "h2"
	case "trojan":
		if value, _ := proxy["network"].(string); value != "" {
			return value
		}
		return "tcp"
	default:
		return "tls"
	}
}

func nonEmptyString(value interface{}) bool {
	text, ok := value.(string)
	return ok && strings.TrimSpace(text) != ""
}

func fieldSet(fields ...string) map[string]bool {
	result := make(map[string]bool, len(fields))
	for _, field := range fields {
		result[field] = true
	}
	return result
}
