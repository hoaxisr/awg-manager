package ftlink

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// LinkPayload is the JSON structure embedded in a freeturn:// share link.
//
// Two link flavors exist in the wild and this type/DecodeLink accept both:
//
//   - The upstream free-turn-proxy format (see docs/uri.md in
//     samosvalishe/free-turn-proxy): base64url, no padding
//     (Go base64.RawURLEncoding), fields v/provider/peer/transport/mode/
//     bond/obf/key/n/spc/cid/listen/dns/dnss/mcap/name. Notably it never
//     includes the VK call link itself (unique per recipient) — the
//     receiving client still has to enter -links by hand.
//   - The informal freeturn-entware-installer format (install.sh's
//     generator.cgi): standard base64 alphabet, padding stripped
//     (JS btoa() с обрезанным хвостом =), fields v/provider/peer/obf/key/mtu/wg.
//   - The compact freeturn v2 share format: {"url":"host:port?obf-profile=…"}
//     with CLI flag names in the query string (no bundled wg when omitted).
//
// EncodeLink (our own "generate link" button) emits the upstream format so
// links we produce are consumable by the real client binary/app too; the
// extra `wg` field is additive — an official-format-only parser just
// ignores unknown JSON keys. DecodeLink accepts either.
type LinkPayload struct {
	V        int    `json:"v"`
	Provider string `json:"provider,omitempty"`
	Peer     string `json:"peer,omitempty"`

	Transport string `json:"transport,omitempty"`
	Mode      string `json:"mode,omitempty"`
	Bond      bool   `json:"bond,omitempty"`

	Obf string `json:"obf,omitempty"`
	Key string `json:"key,omitempty"`

	N              int    `json:"n,omitempty"`   // -n, parallel TURN streams
	StreamsPerCred int    `json:"spc,omitempty"` // -streams-per-cred
	ClientID       string `json:"cid,omitempty"` // -client-id — owner must allowlist this in clients.json
	Listen         string `json:"listen,omitempty"`
	DNSMode        string `json:"dns,omitempty"`
	DNSServers     string `json:"dnss,omitempty"`
	ManualCaptcha  bool   `json:"mcap,omitempty"`
	Name           string `json:"name,omitempty"` // comment for the owner's own clients.json entry

	// awg-manager extensions, not part of the upstream spec:
	MTU int    `json:"mtu,omitempty"`
	WG  string `json:"wg,omitempty"` // optional bundled WireGuard client config
}

// LinkScheme is the URI scheme prefix used by freeturn:// share links.
const LinkScheme = "freeturn://"

// EncodeLink builds a freeturn:// link from p using the upstream encoding:
// base64url, no padding (Go base64.RawURLEncoding) over the JSON bytes.
func EncodeLink(p LinkPayload) (string, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return LinkScheme + base64.RawURLEncoding.EncodeToString(raw), nil
}

// StripWGConfMTU removes MTU= lines (and comment-only lines) from a WG config
// embedded in freeturn:// links — mtu travels as a separate JSON field (Android parity).
func StripWGConfMTU(conf string) string {
	if conf == "" {
		return conf
	}
	lines := strings.Split(conf, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		upper := strings.ToUpper(trimmed)
		if strings.HasPrefix(upper, "MTU") && strings.Contains(trimmed, "=") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// HasBundledWgConfig reports whether a freeturn link carries a usable WG client
// config (not just an empty [Interface] stub).
func HasBundledWgConfig(wg string) bool {
	wg = strings.TrimSpace(wg)
	if wg == "" {
		return false
	}
	upper := strings.ToUpper(wg)
	return strings.Contains(upper, "PRIVATEKEY")
}

type linkWire struct {
	LinkPayload
	URL string `json:"url,omitempty"`
}

func DecodeLink(link string) (LinkPayload, error) {
	var p LinkPayload
	body := strings.TrimSpace(link)
	body = strings.TrimPrefix(body, LinkScheme)
	if body == "" {
		return p, fmt.Errorf("пустая ссылка")
	}

	// Normalize to the standard alphabet so both flavors decode the same
	// way, then re-pad (both flavors strip '=' before building the link).
	body = strings.TrimRight(body, "=")
	body = strings.NewReplacer("-", "+", "_", "/").Replace(body)
	if pad := len(body) % 4; pad != 0 {
		body += strings.Repeat("=", 4-pad)
	}

	raw, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return p, fmt.Errorf("не удалось декодировать base64: %w", err)
	}
	var wire linkWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return p, fmt.Errorf("не удалось разобрать JSON: %w", err)
	}
	p = wire.LinkPayload
	if u := strings.TrimSpace(wire.URL); u != "" {
		mergeURLFormat(&p, u)
	}
	if p.V == 0 {
		p.V = 1
	}
	if strings.TrimSpace(p.Provider) == "" {
		p.Provider = "vk"
	}
	return p, nil
}

func mergeURLFormat(p *LinkPayload, compact string) {
	hostPort, query, _ := strings.Cut(compact, "?")
	if p.Peer == "" {
		p.Peer = strings.TrimSpace(hostPort)
	}
	if query == "" {
		return
	}
	vals, err := url.ParseQuery(query)
	if err != nil {
		return
	}
	setIfEmpty := func(dst *string, keys ...string) {
		if strings.TrimSpace(*dst) != "" {
			return
		}
		for _, k := range keys {
			if v := strings.TrimSpace(vals.Get(k)); v != "" {
				*dst = v
				return
			}
		}
	}
	setIfEmpty(&p.Obf, "obf-profile", "obf")
	setIfEmpty(&p.Key, "obf-key", "key")
	setIfEmpty(&p.Transport, "transport")
	setIfEmpty(&p.Mode, "mode")
	setIfEmpty(&p.ClientID, "client-id", "cid")
	setIfEmpty(&p.Listen, "listen")
	setIfEmpty(&p.DNSMode, "dns-mode", "dns")
	setIfEmpty(&p.DNSServers, "dns-servers", "dnss")
	setIfEmpty(&p.Name, "name")
	setIfEmpty(&p.WG, "wg")
	if p.WG != "" {
		p.WG = decodeMaybeBase64WG(p.WG)
	}
	if p.N == 0 {
		if n, ok := intQuery(vals, "n"); ok {
			p.N = n
		}
	}
	if p.StreamsPerCred == 0 {
		if spc, ok := intQuery(vals, "streams-per-cred", "spc"); ok {
			p.StreamsPerCred = spc
		}
	}
	if p.MTU == 0 {
		if mtu, ok := intQuery(vals, "mtu"); ok {
			p.MTU = mtu
		}
	}
	if !p.Bond {
		if b, ok := boolQuery(vals, "bond"); ok {
			p.Bond = b
		}
	}
	if !p.ManualCaptcha {
		if m, ok := boolQuery(vals, "manual-captcha", "mcap"); ok {
			p.ManualCaptcha = m
		}
	}
	setIfEmpty(&p.Provider, "provider")
}

func intQuery(vals url.Values, keys ...string) (int, bool) {
	for _, k := range keys {
		raw := strings.TrimSpace(vals.Get(k))
		if raw == "" {
			continue
		}
		n, err := strconv.Atoi(raw)
		if err == nil {
			return n, true
		}
	}
	return 0, false
}

func boolQuery(vals url.Values, keys ...string) (bool, bool) {
	for _, k := range keys {
		raw := strings.ToLower(strings.TrimSpace(vals.Get(k)))
		switch raw {
		case "1", "true", "yes", "on":
			return true, true
		case "0", "false", "no", "off":
			return false, true
		}
	}
	return false, false
}

func decodeMaybeBase64WG(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.Contains(raw, "[") {
		return raw
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.RawURLEncoding} {
		if dec, err := enc.DecodeString(raw); err == nil {
			if s := strings.TrimSpace(string(dec)); s != "" {
				return s
			}
		}
	}
	return raw
}
