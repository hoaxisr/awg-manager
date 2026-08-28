// Package vlink: TrustTunnel (AdGuard VPN) share links and client TOML.
//
// Supported inputs:
//   - tt://?<base64url TLV payload>  (official deep link)
//   - https://trustunnel.ru/connect/?d=<payload>  (and similar connect pages)
//   - client TOML with [endpoint] section (AdGuard VPN export)
package vlink

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

func parseTrustTunnelLink(input string) ([]ParsedOutbound, error) {
	const prefixes = "tt://"
	lower := strings.ToLower(input)
	if !strings.HasPrefix(lower, prefixes) {
		return nil, fmt.Errorf("trusttunnel: missing tt:// prefix")
	}
	payload := strings.TrimSpace(input[len(prefixes):])
	payload = strings.TrimPrefix(payload, "?")
	return trustTunnelPayloadToOutbounds(payload, "")
}

func isTrustTunnelConnectURL(input string) bool {
	u, err := url.Parse(strings.TrimSpace(input))
	if err != nil || u.Scheme != "https" && u.Scheme != "http" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if !strings.Contains(host, "trustunnel") && !strings.Contains(host, "trutun") {
		return false
	}
	return strings.TrimSpace(u.Query().Get("d")) != ""
}

// IsTrustTunnelConnectURL reports AdGuard TrustTunnel connect page links
// (https://trustunnel.ru/connect/?d=…, trutun.online, etc.).
func IsTrustTunnelConnectURL(input string) bool {
	return isTrustTunnelConnectURL(input)
}

// IsTrustTunnelRawPayload reports bare base64url TLV blobs from TrustTunnel
// deep links (without tt:// or https:// wrapper).
func IsTrustTunnelRawPayload(input string) bool {
	input = strings.TrimSpace(input)
	if input == "" || strings.Contains(input, "://") {
		return false
	}
	_, err := decodeTrustTunnelPayload(input)
	return err == nil
}

func parseTrustTunnelConnectURL(input string) ([]ParsedOutbound, error) {
	u, err := url.Parse(strings.TrimSpace(input))
	if err != nil {
		return nil, fmt.Errorf("trusttunnel: parse url: %w", err)
	}
	payload := strings.TrimSpace(u.Query().Get("d"))
	if payload == "" {
		return nil, fmt.Errorf("trusttunnel: connect url missing ?d= payload")
	}
	label := strings.TrimSpace(u.Query().Get("name"))
	return trustTunnelPayloadToOutbounds(payload, label)
}

func trustTunnelPayloadToOutbounds(payload, label string) ([]ParsedOutbound, error) {
	ep, err := decodeTrustTunnelPayload(payload)
	if err != nil {
		return nil, err
	}
	if label == "" {
		label = ep.Name
	}
	return endpointToTrustTunnelOutbounds(ep, label)
}

func endpointToTrustTunnelOutbounds(ep trustTunnelEndpoint, label string) ([]ParsedOutbound, error) {
	if len(ep.Addresses) == 0 {
		return nil, fmt.Errorf("trusttunnel: no server addresses")
	}
	out := make([]ParsedOutbound, 0, len(ep.Addresses))
	for i, addr := range ep.Addresses {
		host, port, err := splitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("trusttunnel: address %q: %w", addr, err)
		}
		tagLabel := label
		if tagLabel == "" {
			tagLabel = ep.Name
		}
		if tagLabel == "" {
			tagLabel = ep.Hostname
		}
		tag := tagLabel
		if len(ep.Addresses) > 1 {
			tag = fmt.Sprintf("%s-%d", tagLabel, i+1)
		}
		sni := ep.CustomSNI
		if sni == "" {
			sni = ep.Hostname
		}
		ob := map[string]any{
			"type":                 "trusttunnel",
			"tag":                  tag,
			"server":               host,
			"server_port":          port,
			"username":             ep.Username,
			"password":             ep.Password,
			"health_check":         false,
			"quic":                 false, // sing-trusttunnel H2 path; QUIC pending parity
			"anti_dpi":             ep.AntiDPI,
			"client_random_prefix": ep.ClientRandomPrefix,
			"tls": map[string]any{
				"enabled":     true,
				"server_name": sni,
				"insecure":    ep.SkipVerification,
			},
		}
		raw, err := json.Marshal(ob)
		if err != nil {
			return nil, err
		}
		out = append(out, ParsedOutbound{
			Tag:      tag,
			Protocol: "trusttunnel",
			Server:   host,
			Port:     port,
			Outbound: raw,
			Label:    tagLabel,
		})
	}
	return out, nil
}

func splitHostPort(addr string) (string, uint16, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", 0, fmt.Errorf("empty address")
	}
	if !strings.Contains(addr, ":") {
		return "", 0, fmt.Errorf("missing port")
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		// Bare host:443 without brackets for IPv6-less hostnames.
		if i := strings.LastIndex(addr, ":"); i > 0 {
			host = addr[:i]
			portStr = addr[i+1:]
		} else {
			return "", 0, err
		}
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil || port == 0 {
		return "", 0, fmt.Errorf("invalid port %q", portStr)
	}
	if host == "" {
		return "", 0, fmt.Errorf("missing host")
	}
	return host, uint16(port), nil
}

type trustTunnelClientTOML struct {
	Endpoint struct {
		Hostname                 string   `toml:"hostname"`
		Addresses                []string `toml:"addresses"`
		HasIPv6                  *bool    `toml:"has_ipv6"`
		Username                 string   `toml:"username"`
		Password                 string   `toml:"password"`
		SkipVerification         bool     `toml:"skip_verification"`
		UpstreamProtocol         string   `toml:"upstream_protocol"`
		UpstreamFallbackProtocol string   `toml:"upstream_fallback_protocol"`
		AntiDPI                  bool     `toml:"anti_dpi"`
		CustomSNI                string   `toml:"custom_sni"`
		ClientRandomPrefix       string   `toml:"client_random_prefix"`
	} `toml:"endpoint"`
	DNSUpstreams []string `toml:"dns_upstreams"`
}

// IsTrustTunnelClientTOML reports whether body looks like an AdGuard TrustTunnel
// client export: TOML with [endpoint] hostname/username/password, without
// sing-box "outbounds" or Clash "proxies:".
func IsTrustTunnelClientTOML(body []byte) bool {
	trimmed := trimLeadingSpace(stripUTF8BOM(body))
	if len(trimmed) == 0 {
		return false
	}
	if trimmed[0] == '{' || trimmed[0] == '[' {
		return false
	}
	if IsClashYAML(trimmed) || IsSingboxJSON(trimmed) || IsMieruClientJSON(trimmed) {
		return false
	}
	if !bytesHasEndpointSection(trimmed) {
		return false
	}
	var probe trustTunnelClientTOML
	if err := toml.Unmarshal(trimmed, &probe); err != nil {
		return false
	}
	return probe.Endpoint.Hostname != "" &&
		probe.Endpoint.Username != "" &&
		probe.Endpoint.Password != "" &&
		len(probe.Endpoint.Addresses) > 0
}

func bytesHasEndpointSection(body []byte) bool {
	return strings.Contains(string(body), "[endpoint]")
}

// ParseTrustTunnelClientTOML parses AdGuard TrustTunnel client TOML export.
func ParseTrustTunnelClientTOML(body []byte) BatchResult {
	out := BatchResult{}
	fail := func(msg string) BatchResult {
		out.Errors = append(out.Errors, ParseError{LineIdx: 0, Scheme: "trusttunnel-toml", Message: msg})
		return out
	}
	var doc trustTunnelClientTOML
	if err := toml.Unmarshal(stripUTF8BOM(body), &doc); err != nil {
		return fail(fmt.Sprintf("не удалось разобрать TOML: %s", err))
	}
	ep := trustTunnelEndpoint{
		Hostname:           doc.Endpoint.Hostname,
		Addresses:          doc.Endpoint.Addresses,
		CustomSNI:          doc.Endpoint.CustomSNI,
		Username:           doc.Endpoint.Username,
		Password:           doc.Endpoint.Password,
		SkipVerification:   doc.Endpoint.SkipVerification,
		UpstreamProtocol:   normalizeTTUpstream(doc.Endpoint.UpstreamProtocol),
		AntiDPI:            doc.Endpoint.AntiDPI,
		ClientRandomPrefix: doc.Endpoint.ClientRandomPrefix,
		Name:               doc.Endpoint.Hostname,
		DNSUpstreams:       doc.DNSUpstreams,
	}
	if ep.UpstreamProtocol == "" {
		ep.UpstreamProtocol = normalizeTTUpstream(doc.Endpoint.UpstreamFallbackProtocol)
	}
	if ep.UpstreamProtocol == "" {
		ep.UpstreamProtocol = "http2"
	}
	if ep.ClientRandomPrefix == "" && strings.Contains(ep.Hostname, "trutun.") {
		return fail("trusttunnel TOML: missing client_random_prefix (required for trutun.online; re-export from AdGuard VPN or use a connect link)")
	}
	parsed, err := endpointToTrustTunnelOutbounds(ep, ep.Hostname)
	if err != nil {
		return fail(err.Error())
	}
	out.Outbounds = parsed
	return out
}

func normalizeTTUpstream(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "http2", "h2":
		return "http2"
	case "http3", "h3", "quic":
		return "http3"
	default:
		return ""
	}
}
