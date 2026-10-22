package vlink

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// TrustTunnel: tt://?<TLV> и connect-URL http(s)://<любой хост>/…?d=<TLV>.
// Голый base64 без схемы и trusttunnel://user:pass@… НЕ принимаются (спека, Q6/Q7).

func parseTrustTunnelLink(input string) ([]ParsedOutbound, error) {
	payload := strings.TrimPrefix(strings.TrimSpace(input)[len("tt://"):], "?")
	ep, err := decodeTTPayload(payload)
	if err != nil {
		return nil, err
	}
	return ttEndpointToOutbounds(ep, "")
}

func isTrustTunnelConnectURL(input string) bool {
	u, err := url.Parse(strings.TrimSpace(input))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return false
	}
	return u.Query().Get("d") != ""
}

func parseTrustTunnelConnectURL(input string) ([]ParsedOutbound, error) {
	u, err := url.Parse(strings.TrimSpace(input))
	if err != nil {
		return nil, fmt.Errorf("trusttunnel: connect-url: %w", err)
	}
	ep, err := decodeTTPayload(u.Query().Get("d"))
	if err != nil {
		return nil, err
	}
	return ttEndpointToOutbounds(ep, u.Query().Get("name"))
}

// ttEndpointToOutbounds — один адрес = один outbound. Поля outbound строго по
// spec-manager.md п. 3: ничего сверх того, что знает форк sing-box.
func ttEndpointToOutbounds(ep ttEndpoint, label string) ([]ParsedOutbound, error) {
	if label == "" {
		label = ep.Name
	}
	if label == "" {
		label = ep.Hostname
	}
	sni := ep.CustomSNI
	if sni == "" {
		sni = ep.Hostname
	}
	multi := len(ep.Addresses) > 1
	out := make([]ParsedOutbound, 0, len(ep.Addresses))
	for i, addr := range ep.Addresses {
		host, portStr, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("trusttunnel: адрес %q: %w", addr, err)
		}
		port, err := strconv.ParseUint(portStr, 10, 16)
		if err != nil || port == 0 {
			return nil, fmt.Errorf("trusttunnel: адрес %q: неверный порт", addr)
		}
		tag := label
		if multi {
			tag = fmt.Sprintf("%s-%d", label, i+1)
		}
		tls := map[string]any{"enabled": true, "server_name": sni}
		if ep.SkipVerification {
			tls["insecure"] = true
		}
		if ep.Certificate != "" {
			tls["certificate"] = []string{ep.Certificate}
		}
		if ep.AntiDPI {
			tls["fragment"] = true // решение карты: anti_dpi → штатная фрагментация sing-box
		}
		ob := map[string]any{
			"type":        "trusttunnel",
			"tag":         tag,
			"server":      host,
			"server_port": port,
			"username":    ep.Username,
			"password":    ep.Password,
			"quic":        false, // H2-only; http3 во входе намеренно понижается
			"tls":         tls,
		}
		raw, err := json.Marshal(ob)
		if err != nil {
			return nil, err
		}
		out = append(out, ParsedOutbound{
			Tag: tag, Protocol: "trusttunnel", Server: host, Port: uint16(port),
			Outbound: raw, Label: label, MultiAddress: multi,
		})
	}
	return out, nil
}
