package firc

import (
	"fmt"
	"strings"
)

const (
	// C tunvless VLESS_CTL_NAME_MAX (include/vlink.h) caps display names at 100 UTF-8-safe bytes.
	fircNodeDisplayMax = 100
	// C tunnodes.c uses `encode_name(enc, sizeof enc - 9 - KEY_SUFFIX_ROOM, c->row.name)` with
	// enc[192] and KEY_SUFFIX_ROOM=8, giving cap=175 (at most 174 encoded bytes before nul).
	fircNodeKeyBaseMax = 175
	// C tunnodes.c `snprintf(c->row.key, sizeof c->row.key, "%.183s%.8s", c->base, suffix)` with
	// FIRC_TUN_KEY_MAX = 191 caps the `<8hex>:<enc>` base before `#<k>` duplicate suffix at 183 bytes.
	fircNodeBaseWithSrcMax = 183
	// C sublink.c sl_set_pct decodes inside a 512-byte scratch buffer (`char tmp[512]`) before
	// copying up to 127 bytes and passing through vless_name_cut (100 bytes).
	fircPctDecodeScratchMax = 511
)

func asciiHexDigitVal(b byte) int {
	switch {
	case b >= '0' && b <= '9':
		return int(b - '0')
	case b >= 'a' && b <= 'f':
		return int(b-'a') + 10
	case b >= 'A' && b <= 'F':
		return int(b-'A') + 10
	default:
		return -1
	}
}

// PctDecodeRaw decodes %XX hexadecimal escape sequences without altering '+' or
// failing on malformed '%' sequences, matching C tunvless `sl_pct_decode`.
func PctDecodeRaw(s string) string {
	if strings.IndexByte(s, '%') < 0 {
		return s
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			hi := asciiHexDigitVal(s[i+1])
			lo := asciiHexDigitVal(s[i+2])
			if hi >= 0 && lo >= 0 {
				out = append(out, byte((hi<<4)|lo))
				i += 2
				continue
			}
		}
		out = append(out, s[i])
	}
	return string(out)
}

// TrimIncompleteUTF8Tail drops any dangling incomplete UTF-8 sequence from the end
// of b after byte-length truncation, matching C tunvless `sl_utf8_trim_tail`.
func TrimIncompleteUTF8Tail(b []byte) []byte {
	n := len(b)
	if n == 0 {
		return b
	}
	last := b[n-1]
	if last < 0x80 {
		return b
	}
	if (last & 0xC0) != 0x80 {
		return b[:n-1]
	}
	at := n - 1
	cont := 1
	for at > 0 && (b[at-1]&0xC0) == 0x80 {
		at--
		cont++
	}
	if at == 0 {
		return b[:0]
	}
	lead := b[at-1]
	if lead < 0x80 {
		return b[:at]
	}
	var need int
	switch {
	case (lead & 0xE0) == 0xC0:
		need = 1
	case (lead & 0xF0) == 0xE0:
		need = 2
	case (lead & 0xF8) == 0xF0:
		need = 3
	}
	if need > 0 && cont == need {
		return b
	}
	return b[:at-1]
}

// CanonicalFircNodeDisplayName reproduces C fircd's node display name from a raw
// URI fragment or label: `%XX`-decode up to 511 input bytes, truncate to 100 bytes,
// and trim any split trailing UTF-8 sequence. Whitespace is preserved verbatim.
func CanonicalFircNodeDisplayName(raw string) string {
	if len(raw) > fircPctDecodeScratchMax {
		raw = raw[:fircPctDecodeScratchMax]
	}
	dec := []byte(PctDecodeRaw(raw))
	if len(dec) > fircNodeDisplayMax {
		dec = dec[:fircNodeDisplayMax]
	}
	dec = TrimIncompleteUTF8Tail(dec)
	return string(dec)
}

// ExtractLinkDisplayName extracts and canonicalizes a proxy link's `#fragment`
// using first `#` (`strchr`) matching C `sublink.c`. Returns "" when absent.
func ExtractLinkDisplayName(link string) string {
	idx := strings.IndexByte(link, '#')
	if idx < 0 || idx+1 >= len(link) {
		return ""
	}
	return CanonicalFircNodeDisplayName(link[idx+1:])
}

// EncodeFircNodeKeyPart encodes a canonical node display name using C `tunnodes.c`'s
// `encode_name`: only `%`, `:`, `#`, `<0x20`, and `0x7f` are `%XX`-escaped (uppercase
// hex), stopping before total encoded bytes reach 175 (`192 - 9 - 8`).
func EncodeFircNodeKeyPart(displayName string) string {
	out := make([]byte, 0, min(len(displayName)*3, fircNodeKeyBaseMax))
	const hx = "0123456789ABCDEF"
	for i := 0; i < len(displayName); i++ {
		b := displayName[i]
		esc := b == '%' || b == ':' || b == '#' || b < 0x20 || b == 0x7f
		need := 1
		if esc {
			need = 3
		}
		if len(out)+need >= fircNodeKeyBaseMax {
			break
		}
		if esc {
			out = append(out, '%', hx[b>>4], hx[b&0x0f])
		} else {
			out = append(out, b)
		}
	}
	return string(out)
}

func isLowerOrMixedHex8(s string) bool {
	if len(s) != 8 {
		return false
	}
	for i := 0; i < 8; i++ {
		if asciiHexDigitVal(s[i]) < 0 {
			return false
		}
	}
	return true
}

// CanonicalFircNodeKeyFromName returns `<src8>:<encoded_display_name>` matching C `tunnodes.c`.
func CanonicalFircNodeKeyFromName(srcID, rawName string) string {
	src := strings.ToLower(strings.TrimSpace(srcID))
	if len(src) > 8 {
		src = src[:8]
	}
	if src == "" {
		return ""
	}
	disp := CanonicalFircNodeDisplayName(rawName)
	enc := EncodeFircNodeKeyPart(disp)
	return src + ":" + enc
}

// CanonicalFircNodeKeyFromLink derives the canonical firc node key for a source ID and URI link.
func CanonicalFircNodeKeyFromLink(srcID, link string) string {
	return CanonicalFircNodeKeyFromName(srcID, ExtractLinkDisplayName(link))
}

// CanonicalFircSourceNodeKeys assigns runtime node keys to an ordered slice of candidate
// names within a single source, appending `#2`, `#3`, ... on base collisions just like `number_source`.
func CanonicalFircSourceNodeKeys(srcID string, rawNames []string) []string {
	keys := make([]string, len(rawNames))
	counts := make(map[string]int, len(rawNames))
	for i, name := range rawNames {
		base := CanonicalFircNodeKeyFromName(srcID, name)
		counts[base]++
		k := counts[base]
		if k == 1 {
			keys[i] = base
			continue
		}
		if len(base) > fircNodeBaseWithSrcMax {
			base = base[:fircNodeBaseWithSrcMax]
		}
		keys[i] = fmt.Sprintf("%s#%d", base, k)
	}
	return keys
}

func canonicalLinkKeysBySource(t Tunnel) (bySource map[string]string, allKeys map[string]bool) {
	bySource = make(map[string]string, len(t.Sources))
	allKeys = make(map[string]bool, len(t.Sources))
	for _, s := range t.Sources {
		if s.Kind != "link" && strings.TrimSpace(s.URL) != "" {
			continue
		}
		link := strings.TrimSpace(s.Link)
		if link == "" {
			continue
		}
		srcID := strings.ToLower(strings.TrimSpace(s.ID))
		if srcID == "" {
			srcID = deterministicHex8ID("src:" + t.ID)
		}
		key := CanonicalFircNodeKeyFromLink(srcID, link)
		bySource[srcID] = key
		allKeys[key] = true
	}
	return bySource, allKeys
}

func splitDupKeySuffix(rest string) (stem, suffix string, ok bool) {
	hashIdx := strings.LastIndexByte(rest, '#')
	if hashIdx < 0 || hashIdx+1 >= len(rest) {
		return rest, "", false
	}
	digits := rest[hashIdx+1:]
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return rest, "", false
		}
	}
	// Duplicate suffixes start at #2 and never originate from empty digits.
	if len(digits) == 0 || (len(digits) == 1 && digits[0] < '2') {
		return rest, "", false
	}
	return rest[:hashIdx], rest[hashIdx:], true
}

// NormalizeFircNodeRef converts an arbitrary node selector (canonical key, stale URL-encoded
// fragment key, `*`, or legacy `default`) into C `fircd`'s canonical key representation.
func NormalizeFircNodeRef(ref string, t Tunnel) string {
	r := strings.TrimSpace(ref)
	if r == "" || r == TunnelNodePool {
		return r
	}
	if r == "default" {
		return TunnelNodePool
	}
	bySource, allKeys := canonicalLinkKeysBySource(t)
	if allKeys[r] {
		return r
	}
	colon := strings.IndexByte(r, ':')
	if colon <= 0 {
		return r
	}
	srcPart := r[:colon]
	if !isLowerOrMixedHex8(srcPart) {
		return r
	}
	srcID := strings.ToLower(srcPart)
	rest := r[colon+1:]

	if linkKey, ok := bySource[srcID]; ok {
		if strings.EqualFold(r, linkKey) {
			return linkKey
		}
		if expectedName, found := strings.CutPrefix(linkKey, srcID+":"); found {
			candFromRest := EncodeFircNodeKeyPart(CanonicalFircNodeDisplayName(rest))
			if candFromRest == expectedName {
				return linkKey
			}
		}
	}

	if stem, dupSuffix, hasDup := splitDupKeySuffix(rest); hasDup {
		stemNorm := EncodeFircNodeKeyPart(CanonicalFircNodeDisplayName(stem))
		if stemNorm == stem {
			return srcID + ":" + stem + dupSuffix
		}
	}

	return CanonicalFircNodeKeyFromName(srcID, rest)
}

// NormalizeTunnelNodeRefs canonicalizes Order, Exclude, and Inbounds[].Node using the unified C key rule.
func NormalizeTunnelNodeRefs(t *Tunnel) {
	if t == nil {
		return
	}
	for i, k := range t.Order {
		t.Order[i] = NormalizeFircNodeRef(k, *t)
	}
	for i, k := range t.Exclude {
		t.Exclude[i] = NormalizeFircNodeRef(k, *t)
	}
	for i := range t.Inbounds {
		if strings.TrimSpace(t.Inbounds[i].Node) != "" {
			t.Inbounds[i].Node = NormalizeFircNodeRef(t.Inbounds[i].Node, *t)
		}
	}
}

func tunnelHasSourceForRef(t Tunnel, ref string) bool {
	if ref == TunnelNodePool || ref == "default" {
		return true
	}
	colon := strings.IndexByte(ref, ':')
	if colon <= 0 || !isLowerOrMixedHex8(ref[:colon]) {
		return false
	}
	srcID := strings.ToLower(ref[:colon])
	for _, s := range t.Sources {
		if !strings.EqualFold(strings.TrimSpace(s.ID), srcID) {
			continue
		}
		if s.Kind == "subscription" || (s.Kind == "" && strings.TrimSpace(s.URL) != "") {
			return true
		}
		if CanonicalFircNodeKeyFromLink(srcID, s.Link) == ref {
			return true
		}
	}
	return false
}

// PreserveOrRepairInbounds retains existing tunnel inbound port bindings across merges
// while normalizing node keys and repairing stale refs that no longer resolve in t.Sources.
func PreserveOrRepairInbounds(prev []TunnelInbound, t Tunnel) []TunnelInbound {
	if len(prev) == 0 {
		return nil
	}
	out := make([]TunnelInbound, len(prev))
	for i, inb := range prev {
		norm := NormalizeFircNodeRef(inb.Node, t)
		if norm == "" || !tunnelHasSourceForRef(t, norm) {
			norm = defaultTunnelNodeKey(t)
		}
		inb.Node = norm
		out[i] = inb
	}
	return out
}

// verifyMigratedTunnelsResolved statically verifies that every configured inbound and priority/exclude
// rule referencing a direct link source uses a canonical key that exists on that tunnel.
func verifyMigratedTunnelsResolved(tunnels []Tunnel) error {
	for _, t := range tunnels {
		if !t.Enable {
			continue
		}
		linkKeys := make(map[string]map[string]bool)
		subIDs := make(map[string]bool)
		for _, s := range t.Sources {
			srcID := strings.ToLower(strings.TrimSpace(s.ID))
			if srcID == "" {
				continue
			}
			if s.Kind == "subscription" || (s.Kind == "" && strings.TrimSpace(s.URL) != "") {
				subIDs[srcID] = true
				continue
			}
			if strings.TrimSpace(s.Link) != "" {
				if linkKeys[srcID] == nil {
					linkKeys[srcID] = make(map[string]bool)
				}
				linkKeys[srcID][CanonicalFircNodeKeyFromLink(srcID, s.Link)] = true
			}
		}

		checkKey := func(ref, field string) error {
			ref = strings.TrimSpace(ref)
			if ref == "" {
				return fmt.Errorf("tunnel %q has empty %s", t.ID, field)
			}
			if ref == TunnelNodePool || ref == "default" {
				return nil
			}
			colon := strings.IndexByte(ref, ':')
			if colon <= 0 || !isLowerOrMixedHex8(ref[:colon]) {
				return fmt.Errorf("tunnel %q %s %q lacks valid <srcID>: prefix", t.ID, field, ref)
			}
			srcID := strings.ToLower(ref[:colon])
			if subIDs[srcID] {
				return nil
			}
			validSet, ok := linkKeys[srcID]
			if !ok {
				return fmt.Errorf("tunnel %q %s %q references unknown source %q", t.ID, field, ref, srcID)
			}
			if !validSet[ref] {
				return fmt.Errorf("tunnel %q %s %q does not match canonical link key in source %q", t.ID, field, ref, srcID)
			}
			return nil
		}

		for _, inb := range t.Inbounds {
			if err := checkKey(inb.Node, fmt.Sprintf("inbound[%d].node", inb.Port)); err != nil {
				return err
			}
		}
		for _, k := range t.Order {
			if err := checkKey(k, "order"); err != nil {
				return err
			}
		}
		for _, k := range t.Exclude {
			if err := checkKey(k, "exclude"); err != nil {
				return err
			}
		}
	}
	return nil
}

func defaultTunnelNodeKey(t Tunnel) string {
	for _, k := range t.Order {
		if strings.TrimSpace(k) != "" {
			if norm := NormalizeFircNodeRef(k, t); norm != "" {
				return norm
			}
		}
	}
	for _, s := range t.Sources {
		srcID := strings.ToLower(strings.TrimSpace(s.ID))
		if srcID == "" {
			srcID = deterministicHex8ID("src:" + t.ID)
		}
		if strings.TrimSpace(s.Link) != "" {
			return CanonicalFircNodeKeyFromLink(srcID, s.Link)
		}
		if s.Kind == "subscription" || (s.Kind == "" && strings.TrimSpace(s.URL) != "") {
			return TunnelNodePool
		}
		if strings.TrimSpace(s.Name) != "" {
			return CanonicalFircNodeKeyFromName(srcID, strings.TrimSpace(s.Name))
		}
		return srcID + ":"
	}
	return TunnelNodePool
}
