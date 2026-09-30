package mihomonative

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/vlink"
)

// CompileURI dispatches share links to protocol-specific native Mihomo
// compilers. One source link may describe multiple endpoints.
func CompileURI(raw string, preference, routingEngine EnginePreference) ([]*ProxyNode, error) {
	trimmed := strings.TrimSpace(raw)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "vless://") {
		node, err := CompileVLESS(trimmed, preference, routingEngine)
		if err != nil {
			return nil, err
		}
		return []*ProxyNode{node}, nil
	}
	if strings.HasPrefix(lower, "vmess://") {
		node, err := CompileVMess(trimmed, preference, routingEngine)
		if err != nil {
			return nil, err
		}
		return []*ProxyNode{node}, nil
	}
	if strings.HasPrefix(lower, "socks5://") || strings.HasPrefix(lower, "socks://") {
		node, err := CompileSocks(trimmed, preference, routingEngine)
		if err != nil {
			return nil, err
		}
		return []*ProxyNode{node}, nil
	}
	if node, handled, err := compileMihomoSpecificURI(trimmed, preference, routingEngine); handled {
		if err != nil {
			return nil, err
		}
		return []*ProxyNode{node}, nil
	}
	parsed, err := vlink.ParseLinkMany(trimmed)
	if err != nil {
		return nil, fmt.Errorf("mihomo native: unsupported or invalid proxy URI: %w", err)
	}
	if len(parsed) == 0 {
		return nil, fmt.Errorf("mihomo native: proxy URI contains no endpoints")
	}
	nodes := make([]*ProxyNode, 0, len(parsed))
	for _, outbound := range parsed {
		var node *ProxyNode
		switch outbound.Protocol {
		case "trusttunnel":
			node, err = compileTrustTunnelOutbound(trimmed, outbound, preference, routingEngine)
		case "hysteria2":
			node, err = compileParsedOutbound(trimmed, outbound, preference, routingEngine, compileHysteria2Config)
		case "trojan":
			node, err = compileParsedOutbound(trimmed, outbound, preference, routingEngine, compileTrojanConfig)
		default:
			return nil, fmt.Errorf("mihomo native: protocol %q is not implemented yet", outbound.Protocol)
		}
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

type nativeConfigCompiler func(map[string]interface{}, vlink.ParsedOutbound) (map[string]interface{}, string, error)

func compileParsedOutbound(raw string, outbound vlink.ParsedOutbound, preference, routingEngine EnginePreference, compiler nativeConfigCompiler) (*ProxyNode, error) {
	var source map[string]interface{}
	if err := json.Unmarshal(outbound.Outbound, &source); err != nil {
		return nil, fmt.Errorf("mihomo %s: decode parsed endpoint: %w", outbound.Protocol, err)
	}
	compatibility := mihomoNativeCompatibility(outbound.Protocol)
	selected, reason := SelectEngine(preference, compatibility, routingEngine)
	if selected == "" {
		return nil, fmt.Errorf("mihomo %s: %s", outbound.Protocol, reason)
	}
	proxy, transport, err := compiler(source, outbound)
	if err != nil {
		return nil, err
	}
	return &ProxyNode{
		Name: proxy["name"].(string), Protocol: outbound.Protocol, Transport: transport,
		EnginePreference: preference, SelectedEngine: selected, RawURI: raw,
		NativeConfig: proxy, Compatibility: compatibility, Enabled: true,
	}, nil
}

func compileHysteria2Config(source map[string]interface{}, outbound vlink.ParsedOutbound) (map[string]interface{}, string, error) {
	proxy := baseParsedProxy("hysteria2", outbound)
	copyStringField(proxy, source, "password", "password")
	if ports, ok := source["server_ports"].([]interface{}); ok && len(ports) > 0 {
		values := make([]string, 0, len(ports))
		for _, item := range ports {
			if value, ok := item.(string); ok {
				values = append(values, strings.ReplaceAll(value, ":", "-"))
			}
		}
		if len(values) > 0 {
			proxy["ports"] = strings.Join(values, ",")
		}
	}
	if hop, ok := source["hop_interval"].(string); ok {
		if duration, err := time.ParseDuration(hop); err == nil {
			proxy["hop-interval"] = int(duration.Seconds())
		}
	}
	if obfs, ok := source["obfs"].(map[string]interface{}); ok {
		copyStringField(proxy, obfs, "obfs", "type")
		copyStringField(proxy, obfs, "obfs-password", "password")
	}
	if brutal, ok := source["brutal"].(map[string]interface{}); ok {
		if value, ok := numberAsInt(brutal["up_mbps"]); ok {
			proxy["up"] = value
		}
		if value, ok := numberAsInt(brutal["down_mbps"]); ok {
			proxy["down"] = value
		}
	}
	mapTLS(proxy, source)
	return proxy, "quic", nil
}

func compileTrojanConfig(source map[string]interface{}, outbound vlink.ParsedOutbound) (map[string]interface{}, string, error) {
	proxy := baseParsedProxy("trojan", outbound)
	proxy["udp"] = true
	copyStringField(proxy, source, "password", "password")
	mapTLS(proxy, source)
	transportName := "tcp"
	if transport, ok := source["transport"].(map[string]interface{}); ok {
		if value, ok := transport["type"].(string); ok && value != "" {
			transportName = strings.ToLower(value)
		}
		switch transportName {
		case "ws":
			opts := map[string]interface{}{}
			copyStringField(opts, transport, "path", "path")
			if headers, ok := transport["headers"].(map[string]interface{}); ok {
				opts["headers"] = headers
			}
			proxy["network"], proxy["ws-opts"] = "ws", opts
		case "grpc":
			opts := map[string]interface{}{}
			copyStringField(opts, transport, "grpc-service-name", "service_name")
			proxy["network"], proxy["grpc-opts"] = "grpc", opts
		default:
			transportName, proxy["network"] = "tcp", "tcp"
		}
	}
	return proxy, transportName, nil
}

func compileTrustTunnelOutbound(raw string, outbound vlink.ParsedOutbound, preference, routingEngine EnginePreference) (*ProxyNode, error) {
	var source map[string]interface{}
	if err := json.Unmarshal(outbound.Outbound, &source); err != nil {
		return nil, fmt.Errorf("mihomo trusttunnel: decode parsed endpoint: %w", err)
	}
	compatibility := mihomoNativeCompatibility("TrustTunnel")
	selected, reason := SelectEngine(preference, compatibility, routingEngine)
	if selected == "" {
		return nil, fmt.Errorf("mihomo trusttunnel: %s", reason)
	}
	proxy := baseParsedProxy("trusttunnel", outbound)
	proxy["udp"] = true
	copyStringField(proxy, source, "username", "username")
	copyStringField(proxy, source, "password", "password")
	copyBoolField(proxy, source, "health-check", "health_check")
	copyBoolField(proxy, source, "quic", "quic")
	mapTLS(proxy, source)
	transport := "h2"
	if quic, _ := proxy["quic"].(bool); quic {
		transport = "quic"
	}
	return &ProxyNode{
		Name: proxy["name"].(string), Protocol: "trusttunnel", Transport: transport,
		EnginePreference: preference, SelectedEngine: selected, RawURI: raw,
		NativeConfig: proxy, Compatibility: compatibility, Enabled: true,
	}, nil
}

func baseParsedProxy(protocol string, outbound vlink.ParsedOutbound) map[string]interface{} {
	name := strings.TrimSpace(outbound.Label)
	if name == "" {
		name = strings.TrimSpace(outbound.Tag)
	}
	if name == "" {
		name = fmt.Sprintf("%s:%d", outbound.Server, outbound.Port)
	}
	return map[string]interface{}{"name": name, "type": protocol, "server": outbound.Server, "port": int(outbound.Port)}
}

func mihomoNativeCompatibility(protocol string) Compatibility {
	return Compatibility{
		Mihomo:  Support{Supported: true},
		Singbox: Support{Supported: false, Reason: protocol + " was imported into the separate Mihomo-native store"},
	}
}

func mapTLS(proxy, source map[string]interface{}) {
	tls, ok := source["tls"].(map[string]interface{})
	if !ok {
		return
	}
	copyStringField(proxy, tls, "sni", "server_name")
	copyBoolField(proxy, tls, "skip-cert-verify", "insecure")
	if alpn, ok := tls["alpn"].([]interface{}); ok && len(alpn) > 0 {
		proxy["alpn"] = alpn
	}
	if alpn, ok := tls["alpn"].([]string); ok && len(alpn) > 0 {
		proxy["alpn"] = alpn
	}
	if utls, ok := tls["utls"].(map[string]interface{}); ok {
		copyStringField(proxy, utls, "client-fingerprint", "fingerprint")
	}
	if reality, ok := tls["reality"].(map[string]interface{}); ok {
		opts := map[string]interface{}{}
		copyStringField(opts, reality, "public-key", "public_key")
		copyStringField(opts, reality, "short-id", "short_id")
		if len(opts) > 0 {
			proxy["reality-opts"] = opts
		}
	}
}

func copyStringField(dst, src map[string]interface{}, dstKey, srcKey string) {
	if value, ok := src[srcKey].(string); ok && value != "" {
		dst[dstKey] = value
	}
}

func copyBoolField(dst, src map[string]interface{}, dstKey, srcKey string) {
	if value, ok := src[srcKey].(bool); ok {
		dst[dstKey] = value
	}
}

func numberAsInt(value interface{}) (int, bool) {
	switch number := value.(type) {
	case int:
		return number, true
	case float64:
		return int(number), true
	default:
		return 0, false
	}
}
