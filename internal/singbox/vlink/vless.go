package vlink

import (
	"crypto/mlkem"
	"encoding/base64"
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

// vlessEncryption returns the encryption value for the outbound: "" for the
// meaningless set, the string itself for VLESS Encryption
// (mlkem768x25519plus, issue #603). The fork checks the string when the
// outbound is created and a bad one takes the whole config down, so it is
// checked here by the same rules (fork protocol/vless/encryption/config.go,
// Xray infra/conf/vless.go). Anything else is refused: the list of junk is a
// whitelist on purpose — an unknown value is more likely a new spec than new
// junk, and a visible refusal beats a silent break.
func vlessEncryption(encryption string) (string, error) {
	if meaninglessEncryption[strings.ToLower(strings.TrimSpace(encryption))] {
		return "", nil
	}
	if err := checkMLKEMEncryption(encryption); err != nil {
		return "", fmt.Errorf("vless: encryption=%q не поддерживается (VLESS Encryption: %v) — сервер пропущен", encryption, err)
	}
	return encryption, nil
}

func checkMLKEMEncryption(encryption string) error {
	s := strings.Split(encryption, ".")
	if len(s) < 4 || s[0] != "mlkem768x25519plus" {
		return errors.New("неизвестная схема")
	}
	switch s[1] {
	case "native", "xorpub", "random":
	default:
		return fmt.Errorf("режим %q", s[1])
	}
	switch s[2] {
	case "1rtt", "0rtt":
	default:
		return fmt.Errorf("rtt %q", s[2])
	}
	var keys, blocks, maxLen int
	for _, r := range s[3:] {
		if len(r) >= 20 {
			// 32 байта — ключ X25519 (форку годится любой), 1184 — ключ
			// ML-KEM-768, его кодировку форк проверяет так же.
			b, err := base64.RawURLEncoding.DecodeString(r)
			if err != nil || len(b) != 32 && len(b) != 1184 {
				return errors.New("ключ")
			}
			if len(b) == 1184 {
				if _, err := mlkem.NewEncapsulationKey768(b); err != nil {
					return errors.New("ключ ML-KEM")
				}
			}
			keys++
			continue
		}
		// Паддинг: блоки a-b-c, чётные — длины, нечётные — паузы
		// (Xray proxy/vless/encryption/common.go ParsePadding).
		x := strings.Split(r, "-")
		if len(x) < 3 {
			return fmt.Errorf("паддинг %q", r)
		}
		var y [3]int
		for i := range y {
			n, err := strconv.Atoi(x[i])
			if err != nil {
				return fmt.Errorf("паддинг %q", r)
			}
			y[i] = n
		}
		if blocks == 0 && (y[0] < 100 || y[1] < 35 || y[2] < 35) {
			return fmt.Errorf("первый блок паддинга %q", r)
		}
		if blocks%2 == 0 {
			maxLen += max(y[1], y[2])
		}
		blocks++
	}
	if keys == 0 {
		return errors.New("нет ключа")
	}
	if maxLen > 18+65535 {
		return errors.New("паддинг длиннее 65553")
	}
	return nil
}

// buildVlessOutbound assembles the vless outbound shared by the share-link
// parser (parseVless) and the Clash mapper (mapClashVless), so flow
// normalization and encryption handling stay identical across both entry
// formats — previously the Clash path took flow raw and ignored encryption.
// tag falls back to vless-<host>-<port> when empty.
func buildVlessOutbound(host string, port uint16, uuid, flow, encryption string, stream *StreamBuilder, tag, label string) (*ParsedOutbound, error) {
	enc, err := vlessEncryption(encryption)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"type":        "vless",
		"server":      host,
		"server_port": port,
		"uuid":        uuid,
	}
	if enc != "" {
		out["encryption"] = enc
	}
	if f := normalizeFlow(flow); f != "" {
		if err := checkVlessFlow(f, stream, enc); err != nil {
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
//   - vision поверх VLESS Encryption TLS не нужен: Vision работает поверх
//     шифрования (форк, protocol/vless/vision_encryption.go), но, как и
//     всякий vision, только на голом tcp.
func checkVlessFlow(flow string, stream *StreamBuilder, encryption string) error {
	if flow != "xtls-rprx-vision" {
		return fmt.Errorf("vlink: vless: unsupported flow %q (sing-box knows only xtls-rprx-vision)", flow)
	}
	// nil — Xray JSON без streamSettings, то есть голый tcp.
	if encryption != "" && (stream == nil || stream.Network == "tcp") {
		return nil
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
