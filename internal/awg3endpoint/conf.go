package awg3endpoint

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// fieldKind — как значение .conf-ключа ложится в JSON endpoint.
type fieldKind int

const (
	kStr      fieldKind = iota // строка as-is
	kNum                       // целое → JSON number
	kRange                     // целое или диапазон "min-max" → JSON string
	kAddrCIDR                  // список CIDR (split ','), достраиваем /32|/128
	kBool                      // on|число → JSON true; off и мусор поле не выставляют
	kIgnore                    // известный ключ, но в endpoint не нужен
)

type field struct {
	json string
	kind fieldKind
}

// ключи lower-case (matching case-insensitive, как в amneziawg-tools key_match).
var ifaceFields = map[string]field{
	"privatekey":             {"private_key", kStr},
	"address":                {"address", kAddrCIDR},
	"mtu":                    {"mtu", kNum},
	"jc":                     {"jc", kNum},
	"jmin":                   {"jmin", kNum},
	"jmax":                   {"jmax", kNum},
	"s1":                     {"s1", kNum},
	"s2":                     {"s2", kNum},
	"s3":                     {"s3", kNum},
	"s4":                     {"s4", kNum},
	"h1":                     {"h1", kStr},
	"h2":                     {"h2", kStr},
	"h3":                     {"h3", kStr},
	"h4":                     {"h4", kStr},
	"i1":                     {"i1", kStr},
	"i2":                     {"i2", kStr},
	"i3":                     {"i3", kStr},
	"i4":                     {"i4", kStr},
	"i5":                     {"i5", kStr},
	"contentpaddingaddition": {"content_padding_addition", kStr},
	"rekeyaftertime":         {"rekey_after_time", kStr},
	"rekeytimeout":           {"rekey_timeout", kStr},
	"rejectaftertime":        {"reject_after_time", kStr},
	"keepalivetimeout":       {"keepalive_timeout", kStr},
	"maxhandshakeattempts":   {"max_handshake_attempts", kStr},
	"headerprotectionkey":    {"header_protection_key", kStr},
	"randomtrailers":         {"random_trailers", kBool},
	"disablecookies":         {"disable_cookies", kBool},
	// известные, но в endpoint не нужны:
	"dns":        {"", kIgnore},
	"listenport": {"", kIgnore},
	"fwmark":     {"", kIgnore},
	"table":      {"", kIgnore},
	"preup":      {"", kIgnore},
	"postup":     {"", kIgnore},
	"predown":    {"", kIgnore},
	"postdown":   {"", kIgnore},
	"saveconfig": {"", kIgnore},
}

var peerFields = map[string]field{
	"publickey":           {"public_key", kStr},
	"presharedkey":        {"preshared_key", kStr},
	"allowedips":          {"allowed_ips", kAddrCIDR},
	"persistentkeepalive": {"persistent_keepalive_interval", kRange},
	"advancedsecurity":    {"", kIgnore}, // per-peer awg-флаг tools; у sing-box endpoint нет
	// endpoint обрабатывается отдельно (см. assign)
}

// ParseConf разбирает нативный .conf AmneziaWG (INI/wg-quick) в голый endpoint-JSON.
// Инварианты (type/private_key/peers/HP⇒s) НЕ проверяет — это делает Parse.
func ParseConf(text string) ([]byte, error) {
	text = strings.TrimPrefix(text, "\ufeff") // срез UTF-8 BOM (FileReader его не убирает)

	iface := map[string]any{}
	var peers []map[string]any
	var cur map[string]any
	section := ""

	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			switch section {
			case "interface":
				cur = iface
			case "peer":
				p := map[string]any{}
				peers = append(peers, p)
				cur = p
			default:
				return nil, fmt.Errorf("неизвестная секция [%s]", line[1:len(line)-1])
			}
			continue
		}
		if cur == nil {
			return nil, fmt.Errorf("строка вне секции: %q", line)
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			return nil, fmt.Errorf("строка без '=': %q", line)
		}
		key := strings.ToLower(strings.TrimSpace(line[:eq]))
		val := strings.TrimSpace(line[eq+1:]) // trim краёв; внутренние пробелы I1-I5 сохраняются
		if err := assign(section, cur, key, val); err != nil {
			return nil, err
		}
	}

	if len(iface) == 0 {
		return nil, fmt.Errorf("нет секции [Interface]")
	}
	if len(peers) == 0 {
		return nil, fmt.Errorf("нет секции [Peer]")
	}

	iface["type"] = "awg"
	iface["useIntegratedTun"] = false
	// []map → []any, чтобы json.Marshal отдал массив объектов
	ps := make([]any, len(peers))
	for i, p := range peers {
		ps[i] = p
	}
	iface["peers"] = ps
	return json.Marshal(iface)
}

func assign(section string, dst map[string]any, key, val string) error {
	if section == "peer" && key == "endpoint" {
		host, port, err := splitEndpoint(val)
		if err != nil {
			return err
		}
		dst["address"] = host
		dst["port"] = port
		return nil
	}
	tbl := peerFields
	if section == "interface" {
		tbl = ifaceFields
	}
	f, ok := tbl[key]
	if !ok {
		return nil // неизвестный ключ — игнор (lenient, как UAPI amneziawg-go)
	}
	switch f.kind {
	case kIgnore:
		return nil
	case kStr:
		dst[f.json] = val
	case kNum:
		n, err := strconv.Atoi(val)
		if err != nil {
			return fmt.Errorf("поле %s ждёт число, получено %q", key, val)
		}
		dst[f.json] = n
	case kRange:
		n, err := parseRange(val)
		if err != nil {
			return fmt.Errorf("поле %s ждёт число или диапазон min-max, получено %q", key, val)
		}
		// Одиночное значение остаётся JSON-числом: такой конфиг переварит и
		// sing-box без поддержки диапазонов. Строку пишем только для диапазона,
		// который старая сборка всё равно не примет.
		if n != nil {
			dst[f.json] = *n
		} else {
			dst[f.json] = val
		}
	case kBool:
		// Выставляем поле только для включённого состояния. Выключенное —
		// умолчание устройства, а `awg showconf` печатает эти два ключа
		// ВСЕГДА (ядро кладёт их в дамп безусловно), поэтому пробрасывать
		// "off" значило бы протаскивать в endpoint мусор на каждом импорте.
		//
		// Литерал именно булев true, а не строка "on": endpoint отдаёт
		// значение движку через UAPI, где его читает strconv.ParseBool,
		// который "on" не понимает. В .conf наоборот on/off — там parse_bool
		// из amneziawg-tools, который не понимает "true".
		if confBool(val) {
			dst[f.json] = true
		}
	case kAddrCIDR:
		var out []string
		for _, part := range strings.Split(val, ",") {
			p := strings.TrimSpace(part)
			if p == "" {
				continue
			}
			out = append(out, normalizeCIDR(p))
		}
		dst[f.json] = out
	}
	return nil
}

// parseRange разбирает значение вида u16_range: одиночное число или "min-max".
// В AWG 3.0 PersistentKeepalive стал диапазоном, из которого устройство берёт
// случайное значение на каждый взвод таймера, и endpoint sing-box принимает
// такую строку как есть (option/awg.go, AwgKeepalive) — сужать не нужно.
// Для одиночного значения возвращает число, для диапазона nil.
func parseRange(val string) (*int, error) {
	loStr, hiStr, isRange := strings.Cut(val, "-")
	lo, err := strconv.ParseUint(strings.TrimSpace(loStr), 10, 16)
	if err != nil {
		return nil, err
	}
	if !isRange {
		n := int(lo)
		return &n, nil
	}
	hi, err := strconv.ParseUint(strings.TrimSpace(hiStr), 10, 16)
	if err != nil {
		return nil, err
	}
	if hi < lo {
		return nil, fmt.Errorf("верхняя граница меньше нижней")
	}
	return nil, nil
}

// normalizeCIDR достраивает маску, если её нет (sing-box парсит как netip.Prefix,
// который требует /N; wg-quick допускает bare-IP).
func normalizeCIDR(s string) string {
	if strings.Contains(s, "/") {
		return s
	}
	if strings.Contains(s, ":") {
		return s + "/128"
	}
	return s + "/32"
}

// splitEndpoint режет "host:port" на host и port. IPv6-литерал приходит в скобках
// ([::1]:51820) — снимаем скобки, порт по хвосту.
func splitEndpoint(s string) (string, int, error) {
	if strings.HasPrefix(s, "[") {
		end := strings.LastIndex(s, "]")
		if end < 0 {
			return "", 0, fmt.Errorf("битый IPv6 Endpoint: %q", s)
		}
		host := s[1:end]
		rest := s[end+1:]
		if !strings.HasPrefix(rest, ":") {
			return "", 0, fmt.Errorf("Endpoint без порта: %q", s)
		}
		port, err := strconv.Atoi(rest[1:])
		if err != nil {
			return "", 0, fmt.Errorf("невалидный порт в Endpoint %q: %w", s, err)
		}
		return host, port, nil
	}
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return "", 0, fmt.Errorf("Endpoint без порта: %q", s)
	}
	port, err := strconv.Atoi(s[i+1:])
	if err != nil {
		return "", 0, fmt.Errorf("невалидный порт в Endpoint %q: %w", s, err)
	}
	return s[:i], port, nil
}

// confBool разбирает булев ключ AWG 3.1 из .conf так же, как parse_bool в
// amneziawg-tools: "on" без учёта регистра либо ненулевое число. Всё
// остальное — включая "off", пустую строку и мусор — считается выключенным.
func confBool(value string) bool {
	v := strings.TrimSpace(value)
	if strings.EqualFold(v, "on") {
		return true
	}
	n, err := strconv.ParseUint(v, 10, 32)
	return err == nil && n != 0
}
