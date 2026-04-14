package singbox

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

type xrayConfig struct {
	Outbounds []xrayOutbound `json:"outbounds"`
}

type xrayOutbound struct {
	Protocol       string          `json:"protocol"`
	Settings       json.RawMessage `json:"settings"`
	StreamSettings *xrayStream     `json:"streamSettings,omitempty"`
}

type xrayStream struct {
	Network         string       `json:"network"`
	Security        string       `json:"security"`
	RealitySettings *xrayReality `json:"realitySettings,omitempty"`
	TLSSettings     *xrayTLS     `json:"tlsSettings,omitempty"`
	GrpcSettings    *xrayGrpc    `json:"grpcSettings,omitempty"`
}

type xrayReality struct {
	ServerName  string `json:"serverName"`
	PublicKey   string `json:"publicKey"`
	ShortId     string `json:"shortId"`
	Fingerprint string `json:"fingerprint"`
}

type xrayTLS struct {
	ServerName  string   `json:"serverName"`
	ALPN        []string `json:"alpn,omitempty"`
	Fingerprint string   `json:"fingerprint,omitempty"`
}

type xrayGrpc struct {
	ServiceName string `json:"serviceName"`
}

type xrayVLESSSettings struct {
	Vnext []xrayVnext `json:"vnext"`
}

type xrayVnext struct {
	Address string          `json:"address"`
	Port    int             `json:"port"`
	Users   []xrayVLESSUser `json:"users"`
}

type xrayVLESSUser struct {
	ID         string `json:"id"`
	Flow       string `json:"flow,omitempty"`
	Encryption string `json:"encryption,omitempty"`
}

func parseAmneziaVPN(raw string) (*ParsedOutbound, error) {
	if !strings.HasPrefix(raw, "vpn://") {
		return nil, fmt.Errorf("not an amnezia vpn link")
	}
	rest := strings.TrimPrefix(raw, "vpn://")
	hashIdx := strings.Index(rest, "#")
	var tag string
	if hashIdx >= 0 {
		tag = rest[hashIdx+1:]
		tag, _ = url.PathUnescape(tag)
		rest = rest[:hashIdx]
	}

	// Try standard, URL-safe, and raw base64
	decoded, err := base64.StdEncoding.DecodeString(rest)
	if err != nil {
		decoded, err = base64.URLEncoding.DecodeString(rest)
		if err != nil {
			decoded, err = base64.RawStdEncoding.DecodeString(rest)
			if err != nil {
				return nil, fmt.Errorf("bad base64: %w", err)
			}
		}
	}

	var xc xrayConfig
	if err := json.Unmarshal(decoded, &xc); err != nil {
		return nil, fmt.Errorf("bad xray json: %w", err)
	}
	if len(xc.Outbounds) == 0 {
		return nil, fmt.Errorf("no outbounds in amnezia config")
	}

	// Pick first proxy outbound (skip direct/block)
	var target *xrayOutbound
	for i := range xc.Outbounds {
		p := xc.Outbounds[i].Protocol
		if p != "freedom" && p != "blackhole" {
			target = &xc.Outbounds[i]
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("no proxy outbound found")
	}

	switch target.Protocol {
	case "vless":
		return convertXrayVLESS(target, tag)
	default:
		return nil, fmt.Errorf("unsupported amnezia protocol: %s", target.Protocol)
	}
}

func convertXrayVLESS(o *xrayOutbound, tag string) (*ParsedOutbound, error) {
	var s xrayVLESSSettings
	if err := json.Unmarshal(o.Settings, &s); err != nil {
		return nil, fmt.Errorf("vless settings: %w", err)
	}
	if len(s.Vnext) == 0 || len(s.Vnext[0].Users) == 0 {
		return nil, fmt.Errorf("empty vnext/users")
	}
	v := s.Vnext[0]
	user := v.Users[0]

	if v.Port < 1 || v.Port > 65535 {
		return nil, fmt.Errorf("port out of range: %d", v.Port)
	}
	if tag == "" {
		tag = fmt.Sprintf("amnezia-%s-%d", v.Address, v.Port)
	}
	ob := map[string]any{
		"type":        "vless",
		"tag":         tag,
		"server":      v.Address,
		"server_port": v.Port,
		"uuid":        user.ID,
	}
	if user.Flow != "" {
		ob["flow"] = user.Flow
	}

	if o.StreamSettings != nil {
		ss := o.StreamSettings
		if ss.Security == "tls" || ss.Security == "reality" {
			tls := map[string]any{"enabled": true}
			if ss.RealitySettings != nil {
				if ss.RealitySettings.ServerName != "" {
					tls["server_name"] = ss.RealitySettings.ServerName
				}
				if ss.RealitySettings.Fingerprint != "" {
					tls["utls"] = map[string]any{"enabled": true, "fingerprint": ss.RealitySettings.Fingerprint}
				}
				if ss.Security == "reality" {
					tls["reality"] = map[string]any{
						"enabled":    true,
						"public_key": ss.RealitySettings.PublicKey,
						"short_id":   ss.RealitySettings.ShortId,
					}
				}
			} else if ss.TLSSettings != nil {
				if ss.TLSSettings.ServerName != "" {
					tls["server_name"] = ss.TLSSettings.ServerName
				}
				if len(ss.TLSSettings.ALPN) > 0 {
					tls["alpn"] = ss.TLSSettings.ALPN
				}
				if ss.TLSSettings.Fingerprint != "" {
					tls["utls"] = map[string]any{"enabled": true, "fingerprint": ss.TLSSettings.Fingerprint}
				}
			}
			ob["tls"] = tls
		}
		if ss.Network == "grpc" && ss.GrpcSettings != nil {
			ob["transport"] = map[string]any{"type": "grpc", "service_name": ss.GrpcSettings.ServiceName}
		}
	}

	b, err := json.Marshal(ob)
	if err != nil {
		return nil, fmt.Errorf("marshal amnezia outbound: %w", err)
	}
	return &ParsedOutbound{
		Tag: tag, Protocol: "vless",
		Server: v.Address, Port: v.Port, Outbound: b,
	}, nil
}
