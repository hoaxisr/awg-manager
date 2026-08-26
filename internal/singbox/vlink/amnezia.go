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
	Outbounds []XrayOutbound `json:"outbounds"`
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
func amneziaToOutbound(ob XrayOutbound, tag string) (*ParsedOutbound, error) {
	if tag != "" {
		ob.Tag = tag
	}
	parsed, err := convertXrayOutbound(ob)
	if err != nil {
		return nil, fmt.Errorf("amnezia: %w", err)
	}
	// Имя из фрагмента ссылки — единственное, что несёт сам vpn://.
	parsed.Label = tag
	return parsed, nil
}
