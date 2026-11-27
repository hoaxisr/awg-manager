package firc

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/fpclient"
	"github.com/hoaxisr/awg-manager/internal/singbox/subscription"
	"github.com/hoaxisr/awg-manager/internal/singbox/vlink"
)

func deterministicHex8ID(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	if sum[0] == 0 && sum[1] == 0 && sum[2] == 0 && sum[3] == 0 {
		sum[3] = 1
	}
	return fmt.Sprintf("%02x%02x%02x%02x", sum[0], sum[1], sum[2], sum[3])
}

// DefaultHappKeysPath is the default location of happ_keys.json on Keenetic / Entware.
const DefaultHappKeysPath = "/opt/etc/awg-manager/happ_keys.json"

// ResolveHappKeysPath locates happ_keys.json by checking explicitPath first, then
// searching searchDirs and their parent/grandparent directories, falling back to DefaultHappKeysPath.
func ResolveHappKeysPath(explicitPath string, searchDirs ...string) string {
	if p := strings.TrimSpace(explicitPath); p != "" {
		return p
	}
	for _, dir := range searchDirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		candidates := []string{
			filepath.Join(dir, "happ_keys.json"),
			filepath.Join(filepath.Dir(dir), "happ_keys.json"),
			filepath.Join(filepath.Dir(filepath.Dir(dir)), "happ_keys.json"),
		}
		for _, c := range candidates {
			if st, err := os.Stat(c); err == nil && !st.IsDir() {
				return c
			}
		}
	}
	return DefaultHappKeysPath
}

// NormalizeSubscriptionURLForFirc decrypts happ://crypt, crypt2, crypt3, crypt4 links
// using happ_keys.json and unwraps/rewrites wrapper schemes (happ://, clash://, sub://,
// sing-box://, v2ray://, sn://, GitHub/GitLab/Gitea blob URLs) into a raw http(s):// URL.
func NormalizeSubscriptionURLForFirc(rawURL, happKeysPath string) (normalized string, wasEncrypted bool, err error) {
	target := strings.TrimSpace(rawURL)
	if target == "" {
		return "", false, fmt.Errorf("empty subscription URL")
	}

	// First unwrap outer wrapper if it embeds a happ://crypt link (e.g. happ://add/happ://crypt3/...)
	if !subscription.IsHappCryptLink(target) {
		if unwrapped, changed := subscription.NormalizeSubscriptionURL(target); changed && unwrapped != "" {
			target = unwrapped
		}
	}

	if subscription.IsHappCryptLink(target) {
		wasEncrypted = true
		keysFile := ResolveHappKeysPath(happKeysPath)
		dec, decErr := subscription.DecryptHappLinkFromFile(keysFile, target)
		if decErr != nil {
			return "", true, fmt.Errorf("ошибка расшифровки ссылки Happ (%w) — проверьте RSA-ключи в happ_keys.json", decErr)
		}
		target = strings.TrimSpace(dec)
	}

	if norm, _ := subscription.NormalizeSubscriptionURL(target); norm != "" {
		target = norm
	}
	if raw, _ := subscription.RewriteForRaw(target); raw != "" {
		target = raw
	}
	return target, wasEncrypted, nil
}

// IsDirectFircProxyLink reports whether s is a single-line canonical proxy share link
// natively accepted by fircd's link parser.
func IsDirectFircProxyLink(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	lower := strings.ToLower(s)
	prefixes := []string{
		"vless://",
		"finiteproto://",
		"fp://",
		"vmess://",
		"trojan://",
		"ss://",
		"mieru://",
		"mierus://",
		"hysteria2://",
		"hy2://",
		"anytls://",
		"tt://",
		"naive+https://",
		"naive://",
		"socks5://",
		"socks://",
	}
	for _, pfx := range prefixes {
		if strings.HasPrefix(lower, pfx) && len(s) > len(pfx) {
			return true
		}
	}
	if (strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://")) &&
		(strings.Contains(s, "?d=") || strings.Contains(s, "&d=")) {
		return true
	}
	return false
}

// canonicalizeDirectFircProxyLink normalizes input scheme aliases (such as hy2:// -> hysteria2://,
// naive:// -> naive+https://, socks:// -> socks5://) to the canonical scheme emitted by vlink and tunvless.
func canonicalizeDirectFircProxyLink(s string) string {
	s = strings.TrimSpace(s)
	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "hy2://"):
		return "hysteria2://" + s[len("hy2://"):]
	case strings.HasPrefix(lower, "naive://"):
		return "naive+https://" + s[len("naive://"):]
	case strings.HasPrefix(lower, "socks://"):
		return "socks5://" + s[len("socks://"):]
	default:
		return s
	}
}

// resolveFiniteProtoLinkForFirc checks if rawLink is a FiniteProto v2 link (vless://...&mode=finiteproto...
// or fp://...). It converts the link to canonical VLESS FiniteProto URI directly parsed by fircd / tunvless.
func resolveFiniteProtoLinkForFirc(rawLink, defaultName string) (link string, label string, isFP bool, err error) {
	trimmed := strings.TrimSpace(rawLink)
	if trimmed == "" || strings.ContainsAny(trimmed, " \t\r\n") {
		return "", "", false, nil
	}
	spec, isFP, err := fpclient.ParseVLESSFiniteProto(trimmed)
	if !isFP {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", true, err
	}
	u, _ := url.Parse(trimmed)
	if u != nil {
		if fragLabel := ExtractLinkDisplayName(trimmed); strings.TrimSpace(fragLabel) != "" {
			label = strings.TrimSpace(fragLabel)
		}
	}
	if label == "" {
		label = strings.TrimSpace(defaultName)
	}
	if label == "" {
		label = fmt.Sprintf("FP %s:%d", spec.Server, spec.ServerPort)
	}
	canonLink, err := fpclient.ExportVLESSFiniteProtoURI(*spec, label)
	if err != nil {
		return "", "", true, err
	}
	return canonLink, label, true, nil
}

func isHTTPSubscriptionURL(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	lower := strings.ToLower(s)
	if !strings.HasPrefix(lower, "https://") && !strings.HasPrefix(lower, "http://") {
		return false
	}
	// TrustTunnel deep link (?d=...) is a node link, not a subscription feed URL
	if strings.Contains(s, "?d=") || strings.Contains(s, "&d=") {
		return false
	}
	return true
}

var cyrToLat = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "yo",
	'ж': "zh", 'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m",
	'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
	'ф': "f", 'х': "kh", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "shch",
	'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
	'А': "A", 'Б': "B", 'В': "V", 'Г': "G", 'Д': "D", 'Е': "E", 'Ё': "Yo",
	'Ж': "Zh", 'З': "Z", 'И': "I", 'Й': "Y", 'К': "K", 'Л': "L", 'М': "M",
	'Н': "N", 'О': "O", 'П': "P", 'Р': "R", 'С': "S", 'Т': "T", 'У': "U",
	'Ф': "F", 'Х': "Kh", 'Ц': "Ts", 'Ч': "Ch", 'Ш': "Sh", 'Щ': "Shch",
	'Ъ': "", 'Ы': "Y", 'Ь': "", 'Э': "E", 'Ю': "Yu", 'Я': "Ya",
}

// SanitizeFircSubName normalizes a subscription source name to the 1..63 byte
// [a-zA-Z0-9 ._-] character set required by fircd's tconfig.c sub_name_ok().
func SanitizeFircSubName(name string) string {
	name = strings.TrimSpace(name)
	var b strings.Builder
	lastSpace := false
	for _, r := range name {
		if tr, ok := cyrToLat[r]; ok {
			b.WriteString(tr)
			lastSpace = false
			continue
		}
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
			lastSpace = false
		} else if (r == ' ' || r == '\t') && !lastSpace && b.Len() > 0 {
			b.WriteByte(' ')
			lastSpace = true
		}
	}
	res := strings.TrimSpace(b.String())
	if len(res) > 63 {
		res = strings.TrimSpace(res[:63])
	}
	if res == "" {
		return "Subscription"
	}
	return res
}

// ParseInlineSourcesForFirc parses any user-provided input (happ://crypt..crypt4,
// wrapper URLs happ:///clash:///sub://, GitHub/GitLab blob URLs, single share links,
// multi-line share links, Base64/double-Base64, HTML with share links, AdGuard TrustTunnel TOML,
// Mieru Client JSON, Sing-box/Xray JSON, or Clash YAML) into one or more FIRC TunnelSource entries.
func ParseInlineSourcesForFirc(rawInput, defaultName, happKeysPath string) ([]TunnelSource, error) {
	trimmed := strings.TrimSpace(rawInput)
	if trimmed == "" {
		return nil, fmt.Errorf("пустой источник")
	}

	// 1. Check if it is a single FiniteProto v2 link or direct proxy share link first
	if fpLink, fpLabel, isFP, fpErr := resolveFiniteProtoLinkForFirc(trimmed, defaultName); isFP {
		if fpErr != nil {
			return nil, fpErr
		}
		return []TunnelSource{
			{
				ID:   deterministicHex8ID("src:link:" + fpLink),
				Kind: "link",
				Name: fpLabel,
				Link: fpLink,
			},
		}, nil
	}
	if IsDirectFircProxyLink(trimmed) {
		canon := canonicalizeDirectFircProxyLink(trimmed)
		return []TunnelSource{
			{
				ID:   deterministicHex8ID("src:link:" + canon),
				Kind: "link",
				Name: strings.TrimSpace(defaultName),
				Link: canon,
			},
		}, nil
	}

	// 2. If single-line without spaces, check if it's a happ://crypt or wrapper subscription URL
	if !strings.ContainsAny(trimmed, " \t\r\n") {
		if subscription.IsHappCryptLink(trimmed) {
			norm, _, err := NormalizeSubscriptionURLForFirc(trimmed, happKeysPath)
			if err != nil {
				return nil, err
			}
			if isHTTPSubscriptionURL(norm) {
				return []TunnelSource{
					{
						ID:       deterministicHex8ID("src:sub:" + norm),
						Kind:     "subscription",
						Name:     SanitizeFircSubName(defaultName),
						URL:      norm,
						Interval: DefaultSubscriptionInterval,
					},
				}, nil
			}
			if IsDirectFircProxyLink(norm) {
				canon := canonicalizeDirectFircProxyLink(norm)
				return []TunnelSource{
					{
						ID:   deterministicHex8ID("src:link:" + canon),
						Kind: "link",
						Name: strings.TrimSpace(defaultName),
						Link: canon,
					},
				}, nil
			}
			// Decrypted content is a multi-link / Base64 / JSON / YAML body
			trimmed = norm
		} else {
			norm, _, err := NormalizeSubscriptionURLForFirc(trimmed, happKeysPath)
			if err == nil && isHTTPSubscriptionURL(norm) {
				return []TunnelSource{
					{
						ID:       deterministicHex8ID("src:sub:" + norm),
						Kind:     "subscription",
						Name:     SanitizeFircSubName(defaultName),
						URL:      norm,
						Interval: DefaultSubscriptionInterval,
					},
				}, nil
			}
		}
	}

	// 3. If body consists of one or more line-by-line direct proxy links (plain or base64 lines),
	// parse them directly as native FIRC links without downgrading through sing-box outbounds.
	normLines := subscription.NormalizeBody([]byte(trimmed), "text/plain; charset=utf-8")
	allDirect := len(normLines) > 0
	for _, l := range normLines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if !IsDirectFircProxyLink(l) {
			allDirect = false
			break
		}
	}
	if allDirect {
		var out []TunnelSource
		for i, l := range normLines {
			l = strings.TrimSpace(l)
			if l == "" {
				continue
			}
			link := canonicalizeDirectFircProxyLink(l)
			label := extractFircLinkLabel(link, defaultName, i+1)
			if fpLink, fpLabel, isFP, fpErr := resolveFiniteProtoLinkForFirc(l, label); isFP && fpErr == nil {
				link = fpLink
				if fpLabel != "" {
					label = fpLabel
				}
			}
			out = append(out, TunnelSource{
				ID:   deterministicHex8ID(fmt.Sprintf("src:line:%d:%s", i, link)),
				Kind: "link",
				Name: label,
				Link: link,
			})
		}
		if len(out) > 0 {
			return out, nil
		}
	}

	// 4. Parse as rich body (Base64 / Double-Base64 / HTML / TrustTunnel TOML / Mieru JSON / Sing-box JSON / Xray JSON / Clash YAML / multi-links)
	parseRes := subscription.ParseSubscriptionBody([]byte(trimmed), "text/plain; charset=utf-8")
	var out []TunnelSource
	seenLinks := make(map[string]bool)
	for i, p := range parseRes.Outbounds {
		var ob map[string]any
		if err := json.Unmarshal(p.Outbound, &ob); err != nil {
			continue
		}
		if ob["type"] == "trusttunnel" && (strings.Contains(strings.ToLower(trimmed), "http3") || strings.Contains(strings.ToLower(trimmed), "\"quic\"")) {
			ob["quic"] = true
		}
		label := strings.TrimSpace(p.Label)
		if label == "" {
			if defaultName != "" {
				if len(parseRes.Outbounds) > 1 {
					label = fmt.Sprintf("%s #%d", strings.TrimSpace(defaultName), i+1)
				} else {
					label = strings.TrimSpace(defaultName)
				}
			} else {
				label = fmt.Sprintf("node-%d", i+1)
			}
		}
		link, err := encodeSingboxOutboundToFircLink(p.Outbound, ob, label)
		if err != nil || link == "" || seenLinks[link] {
			continue
		}
		seenLinks[link] = true
		out = append(out, TunnelSource{
			ID:   deterministicHex8ID(fmt.Sprintf("src:inline:%d:%s", i, link)),
			Kind: "link",
			Name: label,
			Link: link,
		})
	}
	// Also preserve any native vmess:// share links extracted by NormalizeBody
	// (vlink.ParseBatch counts vmess:// in SkippedVmess for legacy sing-box, whereas FIRC supports vmess:// natively).
	for idx, rawLine := range subscription.NormalizeBody([]byte(trimmed), "text/plain; charset=utf-8") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(strings.ToLower(line), "vmess://") && !seenLinks[line] {
			seenLinks[line] = true
			label := extractFircLinkLabel(line, defaultName, len(out)+idx+1)
			out = append(out, TunnelSource{
				ID:   deterministicHex8ID(fmt.Sprintf("src:vmess:%d:%s", idx, line)),
				Kind: "link",
				Name: label,
				Link: line,
			})
		}
	}
	if len(out) == 0 {
		if len(parseRes.Errors) > 0 {
			return nil, fmt.Errorf("не удалось разобрать источник: %w", parseRes.Errors[0])
		}
		return nil, fmt.Errorf("не найдено поддерживаемых прокси-узлов или URL подписки")
	}
	return out, nil
}

func extractFircLinkLabel(link, defaultName string, seq int) string {
	if frag := ExtractLinkDisplayName(link); strings.TrimSpace(frag) != "" {
		return strings.TrimSpace(frag)
	}
	if strings.TrimSpace(defaultName) != "" {
		return fmt.Sprintf("%s #%d", strings.TrimSpace(defaultName), seq)
	}
	return fmt.Sprintf("node-%d", seq)
}

// NormalizeTunnelSources decrypts any happ://crypt..crypt4 links, rewrites wrapper URLs
// (happ://, clash://, sub://, GitHub/GitLab/Gitea blob URLs), expands inline multi-node
// blocks (TOML/Mieru JSON/Clash/Sing-box/HTML/Base64), and sanitizes subscription names & IDs.
func NormalizeTunnelSources(t *Tunnel, happKeysPath string) error {
	if t == nil {
		return nil
	}
	if len(t.Sources) == 0 {
		t.Enable = false
		return nil
	}
	var normalized []TunnelSource
	usedIDs := make(map[string]bool)
	usedSubNames := make(map[string]bool)

	allocID := func(candidate, seed string) string {
		id := strings.ToLower(strings.TrimSpace(candidate))
		if len(id) != 8 || usedIDs[id] {
			id = ""
		} else {
			for _, ch := range id {
				if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
					id = ""
					break
				}
			}
		}
		for k := 0; id == "" || usedIDs[id]; k++ {
			s := seed
			if k > 0 {
				s = fmt.Sprintf("%s#%d", seed, k)
			}
			id = deterministicHex8ID(s)
		}
		usedIDs[id] = true
		return id
	}

	allocSubName := func(raw string) string {
		base := SanitizeFircSubName(raw)
		name := base
		for k := 2; usedSubNames[name]; k++ {
			suffix := fmt.Sprintf(" %d", k)
			maxBase := 63 - len(suffix)
			if len(base) > maxBase {
				name = strings.TrimSpace(base[:maxBase]) + suffix
			} else {
				name = base + suffix
			}
		}
		usedSubNames[name] = true
		return name
	}

	for idx, src := range t.Sources {
		isSub := src.Kind == "subscription" || (src.Kind == "" && strings.TrimSpace(src.URL) != "")
		if isSub {
			rawURL := strings.TrimSpace(src.URL)
			if rawURL == "" && strings.TrimSpace(src.Link) != "" {
				rawURL = strings.TrimSpace(src.Link)
			}
			normURL, _, err := NormalizeSubscriptionURLForFirc(rawURL, happKeysPath)
			if err != nil {
				return err
			}
			// What if the user pasted inline nodes or a direct link into the subscription URL box?
			if !isHTTPSubscriptionURL(normURL) {
				parsed, perr := ParseInlineSourcesForFirc(normURL, src.Name, happKeysPath)
				if perr != nil {
					return perr
				}
				for pi, psrc := range parsed {
					seed := fmt.Sprintf("src:%s:%d:%d:%s:%s", t.ID, idx, pi, psrc.Link, psrc.URL)
					psrc.ID = allocID(psrc.ID, seed)
					if psrc.Kind == "subscription" {
						psrc.Name = allocSubName(psrc.Name)
						psrc.Interval = NormalizeSubscriptionInterval(src.Interval)
						if len(src.Headers) > 0 && len(psrc.Headers) == 0 {
							psrc.Headers = src.Headers
						}
					}
					normalized = append(normalized, psrc)
				}
				continue
			}
			src.Kind = "subscription"
			src.URL = normURL
			src.Link = ""
			src.Name = allocSubName(src.Name)
			src.Interval = NormalizeSubscriptionInterval(src.Interval)
			src.ID = allocID(src.ID, fmt.Sprintf("src:sub:%s:%s", t.ID, src.URL))
			normalized = append(normalized, src)
			continue
		}

		// Kind == "link"
		rawLink := strings.TrimSpace(src.Link)
		if rawLink == "" && strings.TrimSpace(src.URL) != "" {
			rawLink = strings.TrimSpace(src.URL)
		}
		if fpLink, fpLabel, isFP, fpErr := resolveFiniteProtoLinkForFirc(rawLink, src.Name); isFP {
			if fpErr != nil {
				return fpErr
			}
			src.Kind = "link"
			src.Link = fpLink
			src.URL = ""
			if strings.TrimSpace(src.Name) == "" {
				src.Name = fpLabel
			}
			src.ID = allocID(src.ID, fmt.Sprintf("src:link:%s:%s", t.ID, src.Link))
			normalized = append(normalized, src)
			continue
		}
		if IsDirectFircProxyLink(rawLink) {
			canon := canonicalizeDirectFircProxyLink(rawLink)
			src.Kind = "link"
			src.Link = canon
			src.URL = ""
			src.ID = allocID(src.ID, fmt.Sprintf("src:link:%s:%s", t.ID, src.Link))
			normalized = append(normalized, src)
			continue
		}

		parsed, err := ParseInlineSourcesForFirc(rawLink, src.Name, happKeysPath)
		if err != nil {
			return err
		}
		for pi, psrc := range parsed {
			seed := fmt.Sprintf("src:%s:%d:%d:%s:%s", t.ID, idx, pi, psrc.Link, psrc.URL)
			candID := ""
			if len(parsed) == 1 {
				candID = src.ID
			}
			psrc.ID = allocID(candID, seed)
			if psrc.Kind == "subscription" {
				psrc.Name = allocSubName(psrc.Name)
				if src.Interval > 0 {
					psrc.Interval = NormalizeSubscriptionInterval(src.Interval)
				}
				if len(src.Headers) > 0 && len(psrc.Headers) == 0 {
					psrc.Headers = src.Headers
				}
			}
			normalized = append(normalized, psrc)
		}
	}

	t.Sources = normalized
	NormalizeTunnelNodeRefs(t)
	return nil
}

func encodeSingboxOutboundToFircLink(raw json.RawMessage, ob map[string]any, label string) (string, error) {
	typ, _ := ob["type"].(string)
	switch typ {
	case "vless", "vmess", "trojan", "shadowsocks", "naive", "mieru", "hysteria2", "hy2", "anytls":
		return vlink.EncodeOutbound(raw, label)
	case "trusttunnel":
		return encodeTrustTunnelOutbound(ob, label)
	case "socks":
		return encodeSocksOutbound(ob, label)
	default:
		return "", fmt.Errorf("protocol %q is not supported by firc", typ)
	}
}

func encodeTrustTunnelOutbound(ob map[string]any, label string) (string, error) {
	host, _ := ob["server"].(string)
	port := intFromMap(ob["server_port"])
	if host == "" || port <= 0 {
		return "", fmt.Errorf("trusttunnel: missing server or server_port")
	}
	user, _ := ob["username"].(string)
	pass, _ := ob["password"].(string)

	u := &url.URL{
		Scheme: "tt",
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
	}
	if user != "" || pass != "" {
		u.User = url.UserPassword(user, pass)
	}
	q := url.Values{}
	if tls, _ := ob["tls"].(map[string]any); tls != nil {
		if sni, _ := tls["server_name"].(string); sni != "" {
			q.Set("sni", sni)
		}
		if tls["insecure"] == true {
			q.Set("insecure", "1")
		}
		if tls["record_fragment"] == true || tls["fragment"] == true {
			q.Set("record_fragment", "1")
		}
	}
	if ob["quic"] == true {
		q.Set("quic", "1")
	}
	u.RawQuery = q.Encode()
	if label != "" {
		u.Fragment = label
	}
	return u.String(), nil
}

func encodeSocksOutbound(ob map[string]any, label string) (string, error) {
	host, _ := ob["server"].(string)
	port := intFromMap(ob["server_port"])
	if host == "" || port <= 0 {
		return "", fmt.Errorf("socks: missing server or server_port")
	}
	user, _ := ob["username"].(string)
	pass, _ := ob["password"].(string)

	u := &url.URL{
		Scheme: "socks5",
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
	}
	if user != "" || pass != "" {
		u.User = url.UserPassword(user, pass)
	}
	if label != "" {
		u.Fragment = label
	}
	return u.String(), nil
}

func intFromMap(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	default:
		return 0
	}
}
