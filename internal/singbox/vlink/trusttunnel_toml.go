package vlink

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"github.com/BurntSushi/toml"
)

type trustTunnelTOML struct {
	Endpoint trustTunnelEndpoint `toml:"endpoint"`
}

type trustTunnelEndpoint struct {
	Hostname                string   `toml:"hostname"`
	Addresses               []string `toml:"addresses"`
	Username                string   `toml:"username"`
	Password                string   `toml:"password"`
	SkipVerification        bool     `toml:"skip_verification"`
	UpstreamProtocol        string   `toml:"upstream_protocol"`
	UpstreamFallbackProtocol string  `toml:"upstream_fallback_protocol"`
	AntiDPI                 bool     `toml:"anti_dpi"`
	CustomSNI               string   `toml:"custom_sni"`
	ClientRandomPrefix      string   `toml:"client_random_prefix"`
}

func hasTrustTunnelTOMLMarkers(s string) bool {
	hasEndpointSection := strings.Contains(s, "[endpoint]")
	hasListenerSection := strings.Contains(s, "[listener]")
	hasUsername := strings.Contains(s, "username") && strings.Contains(s, "password")
	hasHostname := strings.Contains(s, "hostname")
	return hasEndpointSection && (hasUsername || (hasHostname && hasListenerSection))
}

func IsTrustTunnelTOML(body []byte) bool {
	trimmed := trimLeadingSpace(stripUTF8BOM(body))
	if len(trimmed) == 0 {
		return false
	}
	s := string(trimmed)
	if !hasTrustTunnelTOMLMarkers(s) {
		return false
	}
	var probe trustTunnelTOML
	if err := toml.Unmarshal(trimmed, &probe); err != nil {
		return false
	}
	return probe.Endpoint.Username != "" && probe.Endpoint.Password != "" &&
		(probe.Endpoint.Hostname != "" || len(probe.Endpoint.Addresses) > 0)
}

func ParseTrustTunnelTOML(body []byte) BatchResult {
	out := BatchResult{}
	fail := func(msg string) BatchResult {
		out.Errors = append(out.Errors, ParseError{LineIdx: 0, Scheme: "trusttunnel-toml", Message: msg})
		return out
	}

	var cfg trustTunnelTOML
	dec := toml.NewDecoder(bytes.NewReader(stripUTF8BOM(body)))
	if _, err := dec.Decode(&cfg); err != nil {
		return fail(fmt.Sprintf("не удалось разобрать TrustTunnel TOML: %s", err))
	}

	ep := cfg.Endpoint
	if ep.Username == "" {
		return fail("в TrustTunnel-конфиге отсутствует username")
	}
	if ep.Password == "" {
		return fail("в TrustTunnel-конфиге отсутствует password")
	}

	server := ep.Hostname
	port := 443
	if len(ep.Addresses) > 0 && ep.Addresses[0] != "" {
		addr := ep.Addresses[0]
		if strings.HasPrefix(addr, "[") {
			if h, p, err := net.SplitHostPort(addr); err == nil {
				server = h
				if pn, err := net.LookupPort("tcp", p); err == nil {
					port = pn
				}
			}
		} else if strings.Contains(addr, ":") {
			if h, p, err := net.SplitHostPort(addr); err == nil {
				server = h
				if pn, err := net.LookupPort("tcp", p); err == nil {
					port = pn
				}
			}
		}
	}
	if server == "" {
		return fail("в TrustTunnel-конфиге не удалось определить server (hostname / addresses)")
	}

	ob := map[string]any{
		"type":        "trusttunnel",
		"server":      server,
		"server_port": port,
		"username":    ep.Username,
		"password":    ep.Password,
	}

	sni := ep.CustomSNI
	if sni == "" {
		sni = server
	}
	alpn := []string{"h2"}
	useQUIC := strings.EqualFold(ep.UpstreamProtocol, "http3") ||
		strings.EqualFold(ep.UpstreamProtocol, "quic")
	if useQUIC {
		alpn = []string{"h3"}
		ob["quic"] = true
	} else if strings.EqualFold(ep.UpstreamProtocol, "http2") || ep.UpstreamProtocol == "" {
		alpn = []string{"h2"}
	}

	tls := map[string]any{
		"enabled":     true,
		"server_name": sni,
		"alpn":        alpn,
	}
	if ep.SkipVerification {
		tls["insecure"] = true
	}
	ob["tls"] = tls

	tag := fmt.Sprintf("tt-%s-%d", server, port)
	ob["tag"] = tag

	raw, err := json.Marshal(ob)
	if err != nil {
		return fail(fmt.Sprintf("ошибка сериализации outbound: %s", err))
	}

	out.Outbounds = append(out.Outbounds, ParsedOutbound{
		Tag:      tag,
		Protocol: "trusttunnel",
		Server:   server,
		Port:     uint16(port),
		Outbound: raw,
		Label:    "",
	})
	return out
}
