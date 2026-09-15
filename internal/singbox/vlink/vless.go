package vlink

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var uuidRegex = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

func parseVless(input string) (*ParsedOutbound, error) {
	u, err := url.Parse(input)
	if err != nil {
		return nil, fmt.Errorf("vless: parse: %w", err)
	}
	host := u.Hostname()
	if host == "" {
		return nil, errors.New("vless: missing host")
	}
	port, err := strconv.ParseUint(u.Port(), 10, 16)
	if err != nil || port == 0 {
		return nil, errors.New("vless: missing or invalid port")
	}

	q := u.Query()

	uuid, err := vlessUUIDFallback(u.User.Username(), q, u.Host)
	if err != nil {
		return nil, err
	}

	stream, err := BuildStreamFromQuery(q, host)
	if err != nil {
		return nil, fmt.Errorf("vless: %w", err)
	}

	return buildVlessOutbound(host, uint16(port), uuid, q.Get("flow"), q.Get("encryption"), stream, u.Fragment, u.Fragment)
}

// meaninglessEncryption lists encryption values that carry no meaning for
// VLESS: "none" from the spec itself, plus VMess ciphers that keep travelling
// through public subscriptions by copy-paste. They are dropped silently.
var meaninglessEncryption = map[string]bool{
	"":                  true,
	"none":              true,
	"auto":              true,
	"zero":              true,
	"aes-128-gcm":       true,
	"aes-256-gcm":       true,
	"aes-128-cfb":       true,
	"chacha20-poly1305": true,
}

// checkVlessEncryption rejects links that need real VLESS Encryption (issue
// #603). sing-box has no encryption field on a VLESS outbound at all
// (option.VLESSOutboundOptions) and decodes strictly, so passing the value
// through poisons the whole config with `json: unknown field "encryption"`.
// Dropping it silently would instead hand the user a server that cannot
// connect, so anything outside the meaningless set fails here — the list is a
// whitelist of junk on purpose: an unknown value is more likely a new
// encryption spec than new junk, and a visible refusal beats a silent break.
func checkVlessEncryption(encryption string) error {
	if meaninglessEncryption[strings.ToLower(strings.TrimSpace(encryption))] {
		return nil
	}
	return fmt.Errorf("vless: encryption=%q не поддерживается sing-box (VLESS Encryption) — сервер пропущен", encryption)
}

// buildVlessOutbound assembles the vless outbound shared by the share-link
// parser (parseVless) and the Clash mapper (mapClashVless), so flow
// normalization and encryption handling stay identical across both entry
// formats — previously the Clash path took flow raw and ignored encryption.
// tag falls back to vless-<host>-<port> when empty.
func buildVlessOutbound(host string, port uint16, uuid, flow, encryption string, stream *StreamBuilder, tag, label string) (*ParsedOutbound, error) {
	if err := checkVlessEncryption(encryption); err != nil {
		return nil, err
	}
	out := map[string]any{
		"type":        "vless",
		"server":      host,
		"server_port": port,
		"uuid":        uuid,
	}
	if f := normalizeFlow(flow); f != "" {
		if err := checkVlessFlow(f, stream); err != nil {
			return nil, err
		}
		out["flow"] = f
	}
	stream.MergeIntoOutbound(out)

	if tag == "" {
		tag = fmt.Sprintf("vless-%s-%d", host, port)
	}
	out["tag"] = tag

	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return &ParsedOutbound{
		Tag:      tag,
		Protocol: "vless",
		Server:   host,
		Port:     port,
		Outbound: raw,
		Label:    label,
	}, nil
}

// vlessUUIDFallback walks five sources for the UUID, in priority order:
//  1. URL userinfo that is a valid UUID
//  2. URL userinfo decoded as base64, search for UUID-shape
//  3. Query params id, uuid, u
//  4. URL authority (host:port) decoded as base64, search for UUID-shape
//  5. Raw userinfo as-is (non-standard credential; accepted without validation)
func vlessUUIDFallback(userinfo string, q url.Values, authority string) (string, error) {
	if userinfo != "" {
		if uuidRegex.MatchString(userinfo) {
			return strings.ToLower(uuidRegex.FindString(userinfo)), nil
		}
		// try base64 decode of userinfo
		if decoded, err := DecodeBase64Url(userinfo); err == nil && len(decoded) > 0 {
			if uuidRegex.Match(decoded) {
				return strings.ToLower(uuidRegex.FindString(string(decoded))), nil
			}
		}
	}
	// query params
	for _, key := range []string{"id", "uuid", "u"} {
		if v := q.Get(key); v != "" && uuidRegex.MatchString(v) {
			return strings.ToLower(uuidRegex.FindString(v)), nil
		}
	}
	// authority base64
	if decoded, err := DecodeBase64Url(authority); err == nil && len(decoded) > 0 {
		if uuidRegex.Match(decoded) {
			return strings.ToLower(uuidRegex.FindString(string(decoded))), nil
		}
	}
	// raw userinfo fallback — accept any non-empty credential as-is
	if userinfo != "" {
		return userinfo, nil
	}
	return "", errors.New("vless: uuid not found in any source")
}

// checkVlessFlow отсекает комбинации, которые sing-box принимает конфигом, но
// не может набрать:
//
//   - чужой flow (xtls-rprx-direct и прочие из старого Xray) — sing-vmess знает
//     ровно "" и xtls-rprx-vision, остальное валит СОЗДАНИЕ аутбаунда, то есть
//     и `sing-box check`, то есть применение ВСЕЙ конфигурации, а не одной
//     записи;
//   - vision без TLS — в vision уезжает голый TCP (protocol/vless/outbound.go:
//     157-162), а NewVisionConn требует TLS-соединение (sing-vmess
//     vless/vision.go: «vision: not a valid supported TLS connection»), то есть
//     падает КАЖДЫЙ dial;
//   - vision поверх любого транспорта — у ws/grpc/xhttp то же самое (их conn не
//     реализуют ReaderWithUpstream, и CastReader до TLS не доходит), а вот
//     httpupgrade под TLS ОТДАЁТ настоящий *tls.Conn, и клиент такой dial
//     набрал бы. Отвергаем всё равно: сервер этого не примет — Xray держит
//     vision только на raw/tcp («XTLS only supports TLS and REALITY directly
//     for now»), то есть собеседника у такой комбинации нет.
func checkVlessFlow(flow string, stream *StreamBuilder) error {
	if flow != "xtls-rprx-vision" {
		return fmt.Errorf("vlink: vless: unsupported flow %q (sing-box knows only xtls-rprx-vision)", flow)
	}
	if stream == nil || stream.TLS == nil {
		return fmt.Errorf("vlink: vless: flow %q requires TLS or Reality", flow)
	}
	if stream.Network != "tcp" {
		return fmt.Errorf("vlink: vless: flow %q works only over plain tcp, not %q", flow, stream.Network)
	}
	return nil
}

func normalizeFlow(f string) string {
	// Регистр значения приводим, как и у type/security: суффикс -udp443 тут
	// уже нормализуется, а панель, написавшая XTLS-RPRX-VISION, не должна
	// стоить пользователю узла — sing-box сравнивает flow точно.
	f = strings.ToLower(strings.TrimSpace(f))
	if f == "" || f == "none" {
		return ""
	}
	return strings.TrimSuffix(f, "-udp443")
}
