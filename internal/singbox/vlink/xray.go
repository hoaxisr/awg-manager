package vlink

import (
	"encoding/json"
	"fmt"
	"strings"
)

// XrayOutbound represents a single outbound in an Xray/V2Ray JSON config.
type XrayOutbound struct {
	Tag            string          `json:"tag"`
	Protocol       string          `json:"protocol"`
	Settings       json.RawMessage `json:"settings"`
	StreamSettings *XrayStream     `json:"streamSettings"`
}

type XrayStream struct {
	Network         string             `json:"network"`
	Security        string             `json:"security"`
	TLSSettings     *XrayTLSConfig     `json:"tlsSettings"`
	RealitySettings *XrayRealityConfig `json:"realitySettings"`
	WSSettings      *XrayWSConfig      `json:"wsSettings"`
	GRPCSettings    *XrayGRPCConfig    `json:"grpcSettings"`
	HTTPSettings    *XrayHTTPConfig    `json:"httpSettings"`
	Sockopt         map[string]any     `json:"sockopt"`
}

type XrayTLSConfig struct {
	ServerName    string   `json:"serverName"`
	AllowInsecure bool     `json:"allowInsecure"`
	ALPN          []string `json:"alpn"`
	Fingerprint   string   `json:"fingerprint"`
}

type XrayRealityConfig struct {
	ServerName  string `json:"serverName"`
	PublicKey   string `json:"publicKey"`
	ShortID     string `json:"shortId"`
	Fingerprint string `json:"fingerprint"`
	SpiderX     string `json:"spiderX"`
}

type XrayWSConfig struct {
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
}

type XrayGRPCConfig struct {
	ServiceName string `json:"serviceName"`
	MultiMode   bool   `json:"multiMode"`
}

type XrayHTTPConfig struct {
	Path string   `json:"path"`
	Host []string `json:"host"`
}

// VlessSettings represents settings block for VLESS protocol in Xray.
type VlessSettings struct {
	Vnext []struct {
		Address string `json:"address"`
		Port    uint16 `json:"port"`
		Users   []struct {
			ID         string `json:"id"`
			Encryption string `json:"encryption"`
			Flow       string `json:"flow"`
		} `json:"users"`
	} `json:"vnext"`
}

// TrojanSettings represents settings block for Trojan protocol in Xray.
type TrojanSettings struct {
	Servers []struct {
		Address  string `json:"address"`
		Port     uint16 `json:"port"`
		Password string `json:"password"`
	} `json:"servers"`
}

// ShadowsocksSettings represents settings block for Shadowsocks in Xray.
type ShadowsocksSettings struct {
	Servers []struct {
		Address  string `json:"address"`
		Port     uint16 `json:"port"`
		Method   string `json:"method"`
		Password string `json:"password"`
	} `json:"servers"`
}

type XrayConfigItem struct {
	Remarks   string         `json:"remarks"`
	Outbounds []XrayOutbound `json:"outbounds"`
}

// IsXrayJSON tests whether body is a valid JSON document structured as Xray/V2Ray configuration.
func IsXrayJSON(body []byte) bool {
	trimmed := trimLeadingSpace(body)
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return false
	}

	// 1. Single config object {"outbounds": [...]}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err == nil {
		if rawOutbounds, ok := root["outbounds"]; ok {
			var outbounds []XrayOutbound
			if err := json.Unmarshal(rawOutbounds, &outbounds); err == nil && len(outbounds) > 0 {
				for _, ob := range outbounds {
					proto := strings.ToLower(ob.Protocol)
					if proto == "vless" || proto == "trojan" || proto == "shadowsocks" || proto == "vmess" {
						return true
					}
				}
			}
		}
	}

	// 2. Array of config objects [{"remarks": "...", "outbounds": [...]}, ...]
	var configArr []XrayConfigItem
	if err := json.Unmarshal(body, &configArr); err == nil && len(configArr) > 0 {
		for _, item := range configArr {
			for _, ob := range item.Outbounds {
				proto := strings.ToLower(ob.Protocol)
				if proto == "vless" || proto == "trojan" || proto == "shadowsocks" || proto == "vmess" {
					return true
				}
			}
		}
	}

	// 3. Bare array of Xray outbounds
	var arr []XrayOutbound
	if err := json.Unmarshal(body, &arr); err == nil && len(arr) > 0 {
		for _, ob := range arr {
			proto := strings.ToLower(ob.Protocol)
			if (proto == "vless" || proto == "trojan" || proto == "shadowsocks" || proto == "vmess") && len(ob.Settings) > 0 {
				return true
			}
		}
	}

	return false
}

// ParseXrayBody extracts outbounds from an Xray/V2Ray JSON config and translates them to sing-box outbounds.
func ParseXrayBody(body []byte) BatchResult {
	var res BatchResult

	// Try 1: Array of config objects with remarks [{"remarks": "...", "outbounds": [...]}]
	var configArr []XrayConfigItem
	if err := json.Unmarshal(body, &configArr); err == nil && len(configArr) > 0 {
		hasAny := false
		for _, item := range configArr {
			if len(item.Outbounds) > 0 {
				hasAny = true
				break
			}
		}
		if hasAny {
			for _, item := range configArr {
				remarks := strings.TrimSpace(item.Remarks)
				var realOutbounds []XrayOutbound
				for _, ob := range item.Outbounds {
					proto := strings.ToLower(ob.Protocol)
					if proto != "" && proto != "freedom" && proto != "blackhole" && proto != "dns" && proto != "loopback" {
						realOutbounds = append(realOutbounds, ob)
					}
				}

				for idx, ob := range realOutbounds {
					proto := strings.ToLower(ob.Protocol)
					if proto == "vmess" {
						res.SkippedVmess++
						continue
					}

					tag := ob.Tag
					if remarks != "" {
						if len(realOutbounds) > 1 {
							tag = fmt.Sprintf("%s #%d", remarks, idx+1)
						} else {
							tag = remarks
						}
					}
					ob.Tag = tag

					parsed, err := convertXrayOutbound(ob)
					if err != nil {
						res.Errors = append(res.Errors, ParseError{
							LineIdx: idx,
							Scheme:  proto,
							Message: err.Error(),
						})
						continue
					}
					if parsed != nil {
						res.Outbounds = append(res.Outbounds, *parsed)
					}
				}
			}
			return res
		}
	}

	// Try 2: Single config object or bare array
	var outbounds []XrayOutbound
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err == nil {
		if rawOutbounds, ok := root["outbounds"]; ok {
			_ = json.Unmarshal(rawOutbounds, &outbounds)
		}
	}
	if len(outbounds) == 0 {
		_ = json.Unmarshal(body, &outbounds)
	}

	if len(outbounds) == 0 {
		return res
	}

	for idx, ob := range outbounds {
		proto := strings.ToLower(ob.Protocol)
		if proto == "" || proto == "freedom" || proto == "blackhole" || proto == "dns" || proto == "loopback" {
			continue
		}
		if proto == "vmess" {
			res.SkippedVmess++
			continue
		}

		parsed, err := convertXrayOutbound(ob)
		if err != nil {
			res.Errors = append(res.Errors, ParseError{
				LineIdx: idx,
				Scheme:  proto,
				Message: err.Error(),
			})
			continue
		}
		if parsed != nil {
			res.Outbounds = append(res.Outbounds, *parsed)
		}
	}

	return res
}

func convertXrayOutbound(ob XrayOutbound) (*ParsedOutbound, error) {
	proto := strings.ToLower(ob.Protocol)
	tag := ob.Tag
	if tag == "" {
		tag = fmt.Sprintf("%s-node", proto)
	}

	sbOutbound := map[string]any{
		"tag": tag,
	}

	var server string
	var port uint16

	switch proto {
	case "vless":
		var settings VlessSettings
		if err := json.Unmarshal(ob.Settings, &settings); err != nil || len(settings.Vnext) == 0 || len(settings.Vnext[0].Users) == 0 {
			return nil, fmt.Errorf("invalid vless settings")
		}
		vn := settings.Vnext[0]
		user := vn.Users[0]

		server = vn.Address
		port = vn.Port

		sbOutbound["type"] = "vless"
		sbOutbound["server"] = server
		sbOutbound["server_port"] = int(port)
		sbOutbound["uuid"] = user.ID
		if f := normalizeFlow(user.Flow); f != "" {
			sbOutbound["flow"] = f
		}

	case "trojan":
		var settings TrojanSettings
		if err := json.Unmarshal(ob.Settings, &settings); err != nil || len(settings.Servers) == 0 {
			return nil, fmt.Errorf("invalid trojan settings")
		}
		srv := settings.Servers[0]
		server = srv.Address
		port = srv.Port

		sbOutbound["type"] = "trojan"
		sbOutbound["server"] = server
		sbOutbound["server_port"] = int(port)
		sbOutbound["password"] = srv.Password

	case "shadowsocks":
		var settings ShadowsocksSettings
		if err := json.Unmarshal(ob.Settings, &settings); err != nil || len(settings.Servers) == 0 {
			return nil, fmt.Errorf("invalid shadowsocks settings")
		}
		srv := settings.Servers[0]
		server = srv.Address
		port = srv.Port

		sbOutbound["type"] = "shadowsocks"
		sbOutbound["server"] = server
		sbOutbound["server_port"] = int(port)
		sbOutbound["method"] = srv.Method
		sbOutbound["password"] = srv.Password

	default:
		return nil, fmt.Errorf("unsupported protocol: %s", proto)
	}

	// Process StreamSettings (TLS, Reality, WS, gRPC, etc.)
	if ob.StreamSettings != nil {
		stream := ob.StreamSettings
		sec := strings.ToLower(stream.Security)

		if sec == "reality" && stream.RealitySettings != nil {
			rs := stream.RealitySettings
			tlsMap := map[string]any{
				"enabled":     true,
				"server_name": rs.ServerName,
			}
			fp := rs.Fingerprint
			if fp == "" {
				fp = "chrome"
			}
			tlsMap["utls"] = map[string]any{
				"enabled":     true,
				"fingerprint": fp,
			}
			tlsMap["reality"] = map[string]any{
				"enabled":    true,
				"public_key": rs.PublicKey,
				"short_id":   rs.ShortID,
			}
			sbOutbound["tls"] = tlsMap
		} else if (sec == "tls" || sec == "true") && stream.TLSSettings != nil {
			ts := stream.TLSSettings
			tlsMap := map[string]any{
				"enabled":     true,
				"server_name": ts.ServerName,
			}
			if ts.AllowInsecure {
				tlsMap["insecure"] = true
			}
			if len(ts.ALPN) > 0 {
				tlsMap["alpn"] = ts.ALPN
			}
			if ts.Fingerprint != "" {
				tlsMap["utls"] = map[string]any{
					"enabled":     true,
					"fingerprint": ts.Fingerprint,
				}
			}
			sbOutbound["tls"] = tlsMap
		}

		net := strings.ToLower(stream.Network)
		if net == "ws" && stream.WSSettings != nil {
			ws := stream.WSSettings
			transportMap := map[string]any{
				"type": "ws",
				"path": ws.Path,
			}
			if len(ws.Headers) > 0 {
				transportMap["headers"] = ws.Headers
			}
			sbOutbound["transport"] = transportMap
		} else if net == "grpc" && stream.GRPCSettings != nil {
			grpc := stream.GRPCSettings
			transportMap := map[string]any{
				"type":         "grpc",
				"service_name": grpc.ServiceName,
			}
			sbOutbound["transport"] = transportMap
		} else if (net == "http" || net == "h2") && stream.HTTPSettings != nil {
			httpConfig := stream.HTTPSettings
			transportMap := map[string]any{
				"type": "http",
				"path": httpConfig.Path,
			}
			if len(httpConfig.Host) > 0 {
				transportMap["host"] = httpConfig.Host
			}
			sbOutbound["transport"] = transportMap
		}
	}

	rawJSON, err := json.Marshal(sbOutbound)
	if err != nil {
		return nil, err
	}

	return &ParsedOutbound{
		Tag:      tag,
		Protocol: proto,
		Server:   server,
		Port:     port,
		Outbound: rawJSON,
		Label:    tag,
	}, nil
}
