package vlink

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrAmneziaUnsupportedProtocol is returned when the inner Xray config in a
// vpn:// link uses a protocol other than vless (e.g. trojan, vmess). The
// caller can switch on this to decide between hard-fail and skip-with-count.
type ErrAmneziaUnsupportedProtocol struct {
	Protocol string
}

func (e *ErrAmneziaUnsupportedProtocol) Error() string {
	return fmt.Sprintf("amnezia: unsupported inner protocol %q", e.Protocol)
}

type xrayConfig struct {
	Outbounds []xrayOutbound `json:"outbounds"`
}

type xrayOutbound struct {
	Protocol       string          `json:"protocol"`
	Settings       json.RawMessage `json:"settings"`
	StreamSettings json.RawMessage `json:"streamSettings"`
	Tag            string          `json:"tag"`
}

func parseAmnezia(input string) (*ParsedOutbound, error) {
	const prefix = "vpn://"
	if !strings.HasPrefix(strings.ToLower(input), prefix) {
		return nil, errors.New("amnezia: missing vpn:// prefix")
	}
	tag := ""
	body := input[len(prefix):]
	if hash := strings.Index(body, "#"); hash >= 0 {
		tag = body[hash+1:]
		body = body[:hash]
	}
	decoded, err := DecodeBase64Url(body)
	if err != nil {
		return nil, fmt.Errorf("amnezia: base64: %w", err)
	}
	var cfg xrayConfig
	if err := json.Unmarshal(decoded, &cfg); err != nil {
		return nil, fmt.Errorf("amnezia: json: %w", err)
	}
	for _, ob := range cfg.Outbounds {
		switch strings.ToLower(ob.Protocol) {
		case "freedom", "blackhole", "":
			continue
		case "vless":
			return amneziaToOutbound(ob, tag)
		default:
			return nil, &ErrAmneziaUnsupportedProtocol{Protocol: ob.Protocol}
		}
	}
	return nil, errors.New("amnezia: no usable outbound found")
}

// amneziaToOutbound отдаёт внутренний Xray-аутбаунд общему конвертеру.
// Ограничение на vless сохранено выше: снять его — значит просто расширить
// switch, конвертер понимает и trojan, и shadowsocks.
func amneziaToOutbound(ob xrayOutbound, tag string) (*ParsedOutbound, error) {
	parsed, err := convertXrayOutbound(XrayOutbound{
		Tag:            firstNonEmpty(tag, ob.Tag),
		Protocol:       ob.Protocol,
		Settings:       ob.Settings,
		StreamSettings: streamSettingsFromRaw(ob.StreamSettings),
	})
	if err != nil {
		return nil, fmt.Errorf("amnezia: %w", err)
	}
	// Имя из фрагмента ссылки — единственное, что несёт сам vpn://.
	parsed.Label = tag
	return parsed, nil
}

// streamSettingsFromRaw разбирает streamSettings, который у vpn:// приезжает
// сырым. Битый блок роняет ссылку, а не молча превращает её в plain-TCP.
func streamSettingsFromRaw(raw json.RawMessage) *XrayStream {
	if len(raw) == 0 {
		return nil
	}
	var stream XrayStream
	if json.Unmarshal(raw, &stream) != nil {
		return nil
	}
	return &stream
}
