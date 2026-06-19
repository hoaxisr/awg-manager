// Package vlink parses VPN-share-link URIs (vless://, trojan://, ss://, etc.)
// into sing-box outbound configurations. Replaces the legacy flat parser_*.go
// files in internal/singbox/.
package vlink

import (
	"encoding/json"
	"errors"
	"fmt"
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
}

// ParseError describes a single failed link in ParseBatch.
type ParseError struct {
	LineIdx int    // 0-based index in the input slice
	Scheme  string // detected scheme prefix or "" if undetectable
	Message string
}

func (e ParseError) Error() string {
	return fmt.Sprintf("line %d (%s): %s", e.LineIdx, e.Scheme, e.Message)
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
	case strings.HasPrefix(lower, "vless://"):
		return singleOutbound(parseVless(input))
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
				out.SkippedUnsupp++
				out.Errors = append(out.Errors, ParseError{LineIdx: i, Scheme: scheme, Message: err.Error()})
				continue
			}
			out.Errors = append(out.Errors, ParseError{LineIdx: i, Scheme: scheme, Message: err.Error()})
			continue
		}
		out.Outbounds = append(out.Outbounds, parsed...)
	}
	return out
}

func detectScheme(line string) string {
	if i := strings.Index(line, "://"); i > 0 {
		return strings.ToLower(line[:i])
	}
	return ""
}
