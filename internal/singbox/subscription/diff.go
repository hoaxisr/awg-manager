package subscription

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/hoaxisr/awg-manager/internal/singbox/vlink"
)

// DiffResult breaks a refresh into three buckets the service uses to mutate
// sing-box config: new members get added, existing get updated in-place,
// orphans are flagged but not removed (UI choice).
type DiffResult struct {
	New              []TaggedOutbound
	Existing         []TaggedOutbound
	Orphan           []string
	SkippedDuplicate int
}

// TaggedOutbound pairs a stable tag with a parsed outbound.
type TaggedOutbound struct {
	Tag string
	Out vlink.ParsedOutbound
}

// suffixOf is the subID-independent tail of a tag: first 4 bytes of
// sha256(key) as hex. It doubles as the exclusion key used by the import
// preview, so preview suffixes and refresh tags share one derivation.
func suffixOf(key string) string {
	hash := sha256.Sum256([]byte(key))
	return hex.EncodeToString(hash[:4])
}

// stableTagFromKey builds the full tag from an already-chosen identity key.
func stableTagFromKey(subID, key string) string {
	subShort := subID
	if len(subShort) > 8 {
		subShort = subShort[:8]
	}
	return "sub-" + subShort + "-" + suffixOf(key)
}

// StableTag derives a deterministic tag from server identity (narrow key).
// Two refreshes of the same provider produce the same tag for the same
// logical server.
func StableTag(subID string, p vlink.ParsedOutbound) string {
	return stableTagFromKey(subID, identityKey(p))
}

// IdentityHash returns the subID-independent suffix of StableTag for the
// narrow key: the first 4 bytes of sha256(identityKey) as hex. Import-time
// exclusion keys members by this hash because the full StableTag depends on
// the not-yet-allocated subID.
func IdentityHash(p vlink.ParsedOutbound) string {
	return suffixOf(identityKey(p))
}

// identityKey builds the input for the stable hash: protocol + server +
// port + the user-credential field appropriate to the protocol.
func identityKey(p vlink.ParsedOutbound) string {
	var ob map[string]any
	json.Unmarshal(p.Outbound, &ob)
	cred := ""
	for _, k := range []string{"uuid", "password", "username"} {
		if v, ok := ob[k].(string); ok && v != "" {
			cred = v
			break
		}
	}
	return p.Protocol + "|" + p.Server + "|" + itoa(p.Port) + "|" + cred
}

// extendedKey widens identityKey with the reality-masking fields that
// distinguish endpoints sharing one server:port:credential — SNI and the
// reality short_id. Used only for servers whose narrow key collides.
func extendedKey(p vlink.ParsedOutbound) string {
	var ob map[string]any
	json.Unmarshal(p.Outbound, &ob)
	sni, sid := "", ""
	if tls, ok := ob["tls"].(map[string]any); ok {
		sni, _ = tls["server_name"].(string)
		if r, ok := tls["reality"].(map[string]any); ok {
			sid, _ = r["short_id"].(string)
		}
	}
	return identityKey(p) + "|" + sni + "|" + sid
}

func itoa(p uint16) string {
	if p == 0 {
		return "0"
	}
	buf := make([]byte, 0, 5)
	for p > 0 {
		buf = append([]byte{byte('0' + p%10)}, buf...)
		p /= 10
	}
	return string(buf)
}

// chooseKeys returns the identity key for each parsed outbound: the narrow
// identityKey when it suffices, the extendedKey when masking actually
// distinguishes endpoints. A narrow-key group is widened only when its members
// carry >1 distinct extendedKey — i.e. the same server:port:credential is
// reused with different masking (issue #373). True byte-identical duplicates
// (one distinct extended key) stay on the narrow key so their tag remains
// stable across refreshes and matches previously stored narrow tags.
// Deterministic over the set (frequency is set-, not order-, dependent). Both
// ApplyDiff and the import preview build on this so the exclusion suffix
// (preview) and the final tag (refresh) agree.
func chooseKeys(parsed []vlink.ParsedOutbound) []string {
	distinctExt := make(map[string]map[string]struct{}, len(parsed))
	for _, p := range parsed {
		nk := identityKey(p)
		if distinctExt[nk] == nil {
			distinctExt[nk] = make(map[string]struct{})
		}
		distinctExt[nk][extendedKey(p)] = struct{}{}
	}
	keys := make([]string, len(parsed))
	for i, p := range parsed {
		nk := identityKey(p)
		if len(distinctExt[nk]) > 1 {
			keys[i] = extendedKey(p)
		} else {
			keys[i] = nk
		}
	}
	return keys
}

// assignTags maps each parsed outbound to its stable tag via chooseKeys.
func assignTags(subID string, parsed []vlink.ParsedOutbound) []string {
	keys := chooseKeys(parsed)
	tags := make([]string, len(keys))
	for i, k := range keys {
		tags[i] = stableTagFromKey(subID, k)
	}
	return tags
}

// ApplyDiff classifies parsed outbounds against the stored MemberTags slice.
func ApplyDiff(subID string, current []string, parsed []vlink.ParsedOutbound) DiffResult {
	currSet := make(map[string]bool, len(current))
	for _, t := range current {
		currSet[t] = true
	}
	out := DiffResult{}
	tags := assignTags(subID, parsed)
	parsedSet := make(map[string]bool, len(parsed))
	for i, p := range parsed {
		t := tags[i]
		if parsedSet[t] {
			out.SkippedDuplicate++
			continue
		}
		parsedSet[t] = true
		tagged := TaggedOutbound{Tag: t, Out: p}
		if currSet[t] {
			out.Existing = append(out.Existing, tagged)
		} else {
			out.New = append(out.New, tagged)
		}
	}
	for _, t := range current {
		if !parsedSet[t] {
			out.Orphan = append(out.Orphan, t)
		}
	}
	return out
}
