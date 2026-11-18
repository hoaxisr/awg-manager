// Package vlink parses VPN-share-link URIs (vless://, trojan://, ss://, etc.)
// into sing-box outbound configurations. Replaces the legacy flat parser_*.go
// files in internal/singbox/.
package vlink

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
)

// ParsedOutbound mirrors the legacy singbox.ParsedOutbound shape so callers
// can switch import paths without touching the rest of their code.
type ParsedOutbound struct {
	Tag      string // from URI fragment or auto-generated
	Protocol string // "vless"|"trojan"|"shadowsocks"|"hysteria2"|"naive"
	Server   string
	Port     uint16
	Outbound json.RawMessage // sing-box outbound JSON
	Label    string          // human-readable name: Clash "name" field, or URI #fragment for share-links (empty if no fragment)
	// LabelRank: насколько имя в Label принадлежит именно этому узлу.
	// 0 — имя дал провайдер (фрагмент share-link, name у Clash, remarks
	// одиночного профиля Xray). N>1 — имя выведено из профиля на N узлов
	// (узлы самого профиля, включая пропущенные при разборе)
	// («<remarks> #i», у всех узлов профиля один remarks), и чем крупнее
	// профиль, тем грубее имя. LabelRankNone — имени нет вовсе, в Label
	// лежит технический тег конфига («proxy») или заглушка разбора
	// («trojan-node»). Ранг решает, чьё имя останется у сервера, который
	// пришёл в подписке дважды: Happ/Remnawave отдают его и в сводном
	// профиле («Авто» на 13 узлов), и отдельной записью со своим именем —
	// имя берётся от второй (internal/singbox/subscription/diff.go).
	LabelRank int
	// MultiAddress: outbound — один из N адресов одного TrustTunnel-конфига.
	// «Один сервер» такой ввод отбивает в Группу серверов (spec-manager.md п. 4).
	MultiAddress bool
}

// LabelRankNone — ранг имени, которого провайдер не давал: технический тег
// конфига или заглушка разбора. Хуже любого выведенного имени.
const LabelRankNone = math.MaxInt

// ParseError describes a single failed link in ParseBatch.
type ParseError struct {
	LineIdx int    // 0-based index in the input slice
	Scheme  string // detected scheme prefix or "" if undetectable
	Message string
	// Node помечает индекс как номер УЗЛА, а не строки: у подписок в формате
	// JSON и YAML строк нет, и звать номер строкой значит посылать искать не
	// там. Ноль-значение — текстовая подписка, где это действительно строки.
	Node bool
}

func (e ParseError) Error() string {
	// Номер человеческий, с единицы: к тому же индексу прибавляет единицу и
	// одиночное добавление ссылки (internal/singbox/operator_tunnels.go), а
	// два разных отсчёта в одном интерфейсе читаются как ошибка.
	unit := "line"
	if e.Node {
		unit = "node"
	}
	return fmt.Sprintf("%s %d (%s): %s", unit, e.LineIdx+1, e.Scheme, e.Message)
}

// BatchResult aggregates successful parses with skipped/failed accounting.
type BatchResult struct {
	Outbounds     []ParsedOutbound
	SkippedVmess  int
	SkippedUnsupp int
	Errors        []ParseError
}

// Sentinel errors. Callers can errors.Is against these to make routing decisions.
var (
	ErrUnsupportedScheme = errors.New("vlink: unsupported scheme")
	ErrSchemeDropped     = errors.New("vlink: scheme intentionally dropped (vmess)")
	ErrEmptyInput        = errors.New("vlink: empty input")
)

// ParseLink dispatches a single share link to its scheme-specific parser.
// Returns ParsedOutbound on success. Returns ErrSchemeDropped for vmess so
// callers can count vs. report differently. Multi-outbound links such as
// mieru:// should use ParseLinkMany; this helper returns the first parsed
// outbound for backwards-compatible single-link call sites.
func ParseLink(input string) (*ParsedOutbound, error) {
	parsed, err := ParseLinkMany(input)
	if err != nil {
		return nil, err
	}
	if len(parsed) == 0 {
		return nil, ErrUnsupportedScheme
	}
	return &parsed[0], nil
}

// ParseLinkMany dispatches a single share link and may return multiple
// outbounds when the source format describes multiple usable endpoints.
func ParseLinkMany(input string) ([]ParsedOutbound, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, ErrEmptyInput
	}
	lower := strings.ToLower(input)
	switch {
	case strings.HasPrefix(lower, "vless://") || strings.HasPrefix(lower, "fp://") || strings.HasPrefix(lower, "finiteproto://"):
		return singleOutbound(parseVless(input))
	case strings.HasPrefix(lower, "anytls://"):
		return singleOutbound(parseAnyTLS(input))
	case strings.HasPrefix(lower, "trojan://"):
		return singleOutbound(parseTrojan(input))
	case strings.HasPrefix(lower, "ss://"):
		return singleOutbound(parseShadowsocks(input))
	case strings.HasPrefix(lower, "hysteria2://") || strings.HasPrefix(lower, "hy2://"):
		return singleOutbound(parseHysteria2(input))
	case strings.HasPrefix(lower, "naive+"):
		return singleOutbound(parseNaive(input))
	case strings.HasPrefix(lower, "vpn://"):
		return singleOutbound(parseAmnezia(input))
	case strings.HasPrefix(lower, "mieru://"):
		return parseMieruStandard(input)
	case strings.HasPrefix(lower, "mierus://"):
		return parseMieruSimple(input)
	case strings.HasPrefix(lower, "socks5://"):
		return singleOutbound(parseSocks(input))
	case strings.HasPrefix(lower, "socks://"):
		return singleOutbound(parseSocks(input))
	case strings.HasPrefix(lower, "tt://"):
		return parseTrustTunnelLink(input)
	case isTrustTunnelConnectURL(input):
		return parseTrustTunnelConnectURL(input)
	case strings.HasPrefix(lower, "vmess://"):
		return nil, ErrSchemeDropped
	}
	return nil, ErrUnsupportedScheme
}

func singleOutbound(out *ParsedOutbound, err error) ([]ParsedOutbound, error) {
	if err != nil {
		return nil, err
	}
	return []ParsedOutbound{*out}, nil
}

// ParseBatch processes a list of lines, aggregating results. Empty lines and
// lines starting with '#' are silently skipped. Each scheme-specific failure
// is reported in Errors; vmess gets its own counter (SkippedVmess) so callers
// can treat the deprecated-scheme path differently.
func ParseBatch(lines []string) BatchResult {
	out := BatchResult{
		Outbounds: make([]ParsedOutbound, 0, len(lines)),
	}
	// Строка без единой схемы ссылки рядом с годными ссылками — подсказка, а
	// не ошибка: сервер TrustTunnel печатает вокруг ссылки пустую строку и
	// «To connect on mobile, you can scan QR code on the page: https://…», и
	// вставка вывода целиком давала «ошибок: 1» (F400). Схема ищется по всей
	// строке, а не в начале: `- vless://…`, `1. vless://…`, ссылка в кавычках
	// — ссылка с мусором, её отказ обязан дойти до пользователя. Без единой
	// годной ссылки и проза остаётся ошибкой — иначе вставка чистого мусора
	// ответила бы молчанием.
	var prose []ParseError
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parsed, err := ParseLinkMany(line)
		if err != nil {
			scheme := detectScheme(line)
			if errors.Is(err, ErrSchemeDropped) {
				out.SkippedVmess++
				continue
			}
			if errors.Is(err, ErrUnsupportedScheme) {
				pe := ParseError{LineIdx: i, Scheme: scheme, Message: err.Error()}
				if !shareSchemeAnywhere.MatchString(line) {
					prose = append(prose, pe)
					continue
				}
				out.SkippedUnsupp++
				out.Errors = append(out.Errors, pe)
				continue
			}
			out.Errors = append(out.Errors, ParseError{LineIdx: i, Scheme: scheme, Message: err.Error()})
			continue
		}
		out.Outbounds = append(out.Outbounds, parsed...)
	}
	if len(out.Outbounds) == 0 && len(prose) > 0 {
		out.SkippedUnsupp += len(prose)
		out.Errors = append(out.Errors, prose...)
		slices.SortStableFunc(out.Errors, func(a, b ParseError) int { return a.LineIdx - b.LineIdx })
	}
	return out
}

// shareSchemeAnywhere — в строке есть схема ссылки, которую разбирает
// ParseLinkMany (включая отброшенную vmess), в любом месте строки.
var shareSchemeAnywhere = regexp.MustCompile(`(?i)(?:^|[^a-z0-9+.\-])(?:vless|finiteproto|fp|trojan|ss|hysteria2|hy2|anytls|naive\+[a-z0-9]+|vpn|mieru|mierus|socks5?|tt|vmess)://`)

func detectScheme(line string) string {
	if i := strings.Index(line, "://"); i > 0 {
		return strings.ToLower(line[:i])
	}
	return ""
}
