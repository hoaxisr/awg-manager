package subscription

import (
	"regexp"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/singbox/vlink"
)

// shareSchemeCore matches supported share-link scheme names (no ://).
const shareSchemeCore = `(?:vless|trojan|ss|hysteria2|hy2|naive\+\w+|vpn|mieru|mierus|tt)`

// shareURLStartPlain finds share URLs in plain text: after line start or ASCII whitespace.
// Used so fragments may contain spaces (e.g. "#📆 Осталось: 28 дней") and space-separated
// links on one line still split correctly (old [^\s]+ truncated at the first space anywhere).
var shareURLStartPlain = regexp.MustCompile(`(?i)(?:^|\s+)(` + shareSchemeCore + `://)`)

// shareURLStartHTML also accepts HTML/JSON delimiters and JSON-escaped
// slashes in the scheme delimiter.
var shareURLStartHTML = regexp.MustCompile(`(?i)(?:^|[\s"'<>=]+)(` + shareSchemeCore + `:(?://|\\/\\/))`)

// trimShareURLSuffix removes trailing delimiters often glued after a URL in HTML/JSON
// (quotes, commas, angle brackets, whitespace).
func trimShareURLSuffix(u string) string {
	u = strings.TrimSpace(u)
	lower := strings.ToLower(u)
	// Хвост после пробела у этих схем — подпись провайдера («— 28 дней»), а
	// не часть ссылки: base64 tt:// и mieru пробелов не содержат (F403).
	if strings.HasPrefix(lower, "mieru://") || strings.HasPrefix(lower, "mierus://") || strings.HasPrefix(lower, "tt://") {
		if fields := strings.Fields(u); len(fields) > 0 {
			u = fields[0]
		}
	}
	// React/JSON payloads: URL value often followed by "} after the fragment.
	if j := strings.LastIndex(u, "\"}"); j > 0 && strings.Contains(u, "://") {
		u = u[:j]
	}
	for u != "" {
		switch u[len(u)-1] {
		case '"', '\'', ',', '>', '}', ']', ' ', '\t', '\n', '\r':
			u = u[:len(u)-1]
		default:
			return u
		}
	}
	return u
}

// splitShareURLs slices s into individual share-link strings using startRe match positions.
func splitShareURLs(s string, startRe *regexp.Regexp) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	loc := startRe.FindAllStringSubmatchIndex(s, -1)
	if len(loc) == 0 {
		return nil
	}
	out := make([]string, 0, len(loc))
	for k := range loc {
		start := loc[k][2]
		var end int
		if k+1 < len(loc) {
			end = loc[k+1][2]
		} else {
			end = len(s)
		}
		seg := strings.TrimSpace(s[start:end])
		if seg != "" {
			out = append(out, seg)
		}
	}
	return out
}

// NormalizeBody decodes a subscription body via the standard cascade:
//  1. Try base64 (urlsafe-aware) once. If the decoded text looks like
//     another base64 string, try a second pass.
//  2. If body looks like HTML, extract scheme:// URLs from anchor hrefs
//     or plain text fragments.
//  3. Fall back: split as plain text, drop empty / "#"-prefixed lines.
//
// The returned slice is the list of share links the caller should then feed
// to vlink.ParseBatch.
func NormalizeBody(body []byte, contentType string) []string {
	links, _ := normalizeBodyCounted(body, contentType)
	return links
}

// normalizeBodyCounted — NormalizeBody плюс число строк текстового тела со
// схемой ссылки, которую подписка не принимает (F402): раньше они пропадали
// молча — ни в Errors, ни в счётчиках. HTML-страница не считается: ссылок
// на ней много и все посторонние.
func normalizeBodyCounted(body []byte, contentType string) (links []string, unsupported int) {
	if decoded, ok := vlink.DoubleDecode(body); ok {
		return splitLines(decoded)
	}
	if isHTML(body, contentType) {
		extracted := extractFromHTML(body)
		if len(extracted) > 0 {
			return extracted, 0
		}
	}
	return splitLines(body)
}

// anySchemeRe — первая схема ссылки в строке (после начала или пробела).
var anySchemeRe = regexp.MustCompile(`(?i)(?:^|\s)([a-z][a-z0-9+.\-]*)://`)

// foreignSchemeLine разбирает строку без поддерживаемой ссылки: vmess
// уходит ParseBatch целиком (его счётчик SkippedVmess), http(s) — проза
// (заголовки подписок, ссылки на каналы; connect-URL TrustTunnel в теле
// подписки не поддерживается — F401), прочая схема (socks5, wireguard, …) —
// неподдерживаемая: подписка её не принимает (решение владельца 14.10).
func foreignSchemeLine(p string) (forward string, unsupported bool) {
	m := anySchemeRe.FindStringSubmatchIndex(p)
	if m == nil {
		return "", false
	}
	switch strings.ToLower(p[m[2]:m[3]]) {
	case "vmess":
		return strings.TrimSpace(p[m[2]:]), false
	case "http", "https":
		return "", false
	}
	return "", true
}

func splitLines(b []byte) (out []string, unsupported int) {
	s := strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(string(b))
	parts := strings.Split(s, "\n")
	out = make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || strings.HasPrefix(p, "#") {
			continue
		}
		links := splitShareURLs(p, shareURLStartPlain)
		if len(links) == 0 {
			if fwd, unsup := foreignSchemeLine(p); fwd != "" {
				out = append(out, fwd)
			} else if unsup {
				unsupported++
			}
			continue
		}
		for _, link := range links {
			out = append(out, trimShareURLSuffix(link))
		}
	}
	return out, unsupported
}

func isHTML(body []byte, contentType string) bool {
	if strings.Contains(strings.ToLower(contentType), "text/html") {
		return true
	}
	trimmed := strings.TrimSpace(string(body))
	return strings.HasPrefix(trimmed, "<!DOCTYPE") || strings.HasPrefix(trimmed, "<html") || strings.HasPrefix(trimmed, "<")
}

// extractFromHTML pulls all scheme:// URLs out of an HTML body — anchor
// hrefs first, then any plain-text occurrence not already captured.
//
// Each match is run through jsonUnescapeURL to recover share-links
// embedded as JSON-escaped strings inside React/Vue/etc. payloads. The
// motivating case is GitHub's blob view: it serialises the file body
// as a JSON string inside a script tag, so a real `&` separator in a
// share-link surfaces in the HTML as the six-character literal `\u0026`.
// The legacy schemeLineRegex used [^\s]+ which truncated URLs at the first space
// (common in URI fragments: "#📆 Осталось: 28 дней"). We now slice from each scheme://
// to the next scheme boundary.
//
// Other JSON escapes worth unwrapping (`\u003c` / `\u003e` / `\u0027` /
// `\/` / `\"` / `\\`) appear in the same envelope for similar reasons.
// The order of replacements is significant — backslash must be last so
// we don't mangle the front of the other escapes mid-pass.
func extractFromHTML(body []byte) []string {
	raw := string(body)
	segments := splitShareURLs(raw, shareURLStartHTML)
	if len(segments) == 0 {
		return nil
	}
	out := make([]string, 0, len(segments))
	seen := make(map[string]bool)
	for _, seg := range segments {
		s := trimShareURLSuffix(jsonUnescapeURL(seg))
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// jsonUnescapeURL decodes the common JSON string-escape sequences that
// leak into share-link substrings when they're scraped from HTML pages
// whose content was serialised as JSON for a client-side framework.
// Returns the input unchanged when no escape markers are present, so
// the fast path is one substring check.
func jsonUnescapeURL(s string) string {
	if !strings.Contains(s, `\u`) && !strings.Contains(s, `\/`) &&
		!strings.Contains(s, `\"`) && !strings.Contains(s, `\\`) {
		return s
	}
	r := strings.NewReplacer(
		`\u0026`, "&",
		`\u003c`, "<",
		`\u003e`, ">",
		`\u0027`, "'",
		`\/`, "/",
		`\"`, `"`,
		`\\`, `\`, // last — must not chew the leading backslash of the others
	)
	return r.Replace(s)
}
