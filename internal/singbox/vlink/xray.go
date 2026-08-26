package vlink

import (
	"encoding/json"
	"fmt"
	"net/url"
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
	Network             string             `json:"network"`
	Security            string             `json:"security"`
	TLSSettings         *XrayTLSConfig     `json:"tlsSettings"`
	RealitySettings     *XrayRealityConfig `json:"realitySettings"`
	WSSettings          *XrayWSConfig      `json:"wsSettings"`
	GRPCSettings        *XrayGRPCConfig    `json:"grpcSettings"`
	HTTPSettings        *XrayHTTPConfig    `json:"httpSettings"`
	HTTPUpgradeSettings *XrayWSConfig      `json:"httpupgradeSettings"`
	// xhttp несёт настройки плоско (xmux, xPaddingBytes, ...) и/или внутри
	// "extra" — ключи в обеих формах те же, что в share-ссылке, поэтому объект
	// уезжает в разбор целиком.
	XHTTPSettings     json.RawMessage `json:"xhttpSettings"`
	SplitHTTPSettings json.RawMessage `json:"splithttpSettings"`
	Sockopt           map[string]any  `json:"sockopt"`
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
}

type XrayWSConfig struct {
	Host    string            `json:"host"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
}

type XrayGRPCConfig struct {
	ServiceName string `json:"serviceName"`
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

		if user.ID == "" {
			return nil, fmt.Errorf("vless: missing uuid")
		}
		if err := checkVlessEncryption(user.Encryption); err != nil {
			return nil, err
		}
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

		if srv.Password == "" {
			return nil, fmt.Errorf("trojan: missing password")
		}
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

		if srv.Method == "" || srv.Password == "" {
			return nil, fmt.Errorf("shadowsocks: missing method or password")
		}
		sbOutbound["type"] = "shadowsocks"
		sbOutbound["server"] = server
		sbOutbound["server_port"] = int(port)
		sbOutbound["method"] = srv.Method
		sbOutbound["password"] = srv.Password

	default:
		return nil, fmt.Errorf("unsupported protocol: %s", proto)
	}

	if server == "" {
		return nil, fmt.Errorf("%s: missing server", proto)
	}
	if port == 0 {
		return nil, fmt.Errorf("%s: missing or invalid port", proto)
	}

	// Транспорт и TLS собирает общий слой — тот же, что у share-ссылок и
	// Clash. Своей реализации здесь больше нет.
	if ob.StreamSettings != nil {
		stream, err := BuildStreamFromQuery(xrayStreamToValues(ob.StreamSettings, server), server)
		if err != nil {
			return nil, fmt.Errorf("xray: %w", err)
		}
		stream.MergeIntoOutbound(sbOutbound)
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

// xrayStreamToValues переводит streamSettings Xray-конфига в тот же набор
// query-параметров, что несёт share-ссылка, чтобы дальше работал общий слой
// (BuildStreamFromQuery + MergeIntoOutbound). Без этого Xray-путь пришлось бы
// держать второй реализацией транспорта и TLS, а она уже разъехалась с общей:
// теряла xhttp и httpupgrade целиком, early data и bind_interface.
func xrayStreamToValues(stream *XrayStream, defaultHost string) url.Values {
	v := url.Values{}
	if stream == nil {
		return v
	}

	network := strings.ToLower(stream.Network)
	if network == "splithttp" {
		network = "xhttp"
	}
	if network != "" {
		v.Set("type", network)
	}

	switch network {
	case "ws":
		if ws := stream.WSSettings; ws != nil {
			v.Set("path", ws.Path)
			v.Set("host", firstNonEmpty(ws.Host, ws.Headers["Host"], ws.Headers["host"]))
		}
	case "httpupgrade":
		if hu := stream.HTTPUpgradeSettings; hu != nil {
			v.Set("path", hu.Path)
			v.Set("host", firstNonEmpty(hu.Host, hu.Headers["Host"], hu.Headers["host"]))
		}
	case "grpc":
		if g := stream.GRPCSettings; g != nil {
			v.Set("serviceName", g.ServiceName)
		}
	case "http", "h2":
		if h := stream.HTTPSettings; h != nil {
			v.Set("path", h.Path)
			if len(h.Host) > 0 {
				v.Set("host", h.Host[0])
			}
		}
	case "xhttp":
		raw := stream.XHTTPSettings
		if len(raw) == 0 {
			raw = stream.SplitHTTPSettings
		}
		setXHTTPValues(v, raw)
	}

	switch strings.ToLower(stream.Security) {
	case "reality":
		if rs := stream.RealitySettings; rs != nil {
			v.Set("security", "reality")
			v.Set("sni", rs.ServerName)
			v.Set("pbk", rs.PublicKey)
			v.Set("sid", rs.ShortID)
			v.Set("fp", rs.Fingerprint)
		}
	case "tls", "true":
		v.Set("security", "tls")
		if ts := stream.TLSSettings; ts != nil {
			v.Set("sni", firstNonEmpty(ts.ServerName, defaultHost))
			v.Set("fp", ts.Fingerprint)
			if ts.AllowInsecure {
				v.Set("insecure", "1")
			}
			if len(ts.ALPN) > 0 {
				v.Set("alpn", strings.Join(ts.ALPN, ","))
			}
		}
	}

	if iface, _ := stream.Sockopt["interface"].(string); iface != "" {
		v.Set("bind_interface", iface)
	}
	return v
}

// setXHTTPValues раскладывает объект xhttpSettings: path/host/mode идут
// отдельными параметрами, остальное — тем же путём, что "extra" у ссылки.
// Вложенный "extra" перекрывает плоские поля, как и в самом Xray.
func setXHTTPValues(v url.Values, raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var settings map[string]any
	if json.Unmarshal(raw, &settings) != nil {
		return
	}
	if extra, ok := settings["extra"].(map[string]any); ok {
		for k, val := range extra {
			settings[k] = val
		}
	}
	delete(settings, "extra")

	for _, key := range []string{"path", "host", "mode"} {
		if s, ok := settings[key].(string); ok && s != "" {
			v.Set(key, s)
		}
		delete(settings, key)
	}
	if encoded, err := json.Marshal(settings); err == nil {
		v.Set("extra", string(encoded))
	}
}
