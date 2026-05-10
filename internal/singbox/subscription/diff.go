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

// StableTag derives a deterministic tag from server identity. Two refreshes
// of the same provider produce the same tag for the same logical server.
func StableTag(subID string, p vlink.ParsedOutbound) string {
	subShort := subID
	if len(subShort) > 8 {
		subShort = subShort[:8]
	}
	identity := identityKey(p)
	hash := sha256.Sum256([]byte(identity))
	return "sub-" + subShort + "-" + hex.EncodeToString(hash[:4])
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

// ApplyDiff classifies parsed outbounds against the stored MemberTags slice.
func ApplyDiff(subID string, current []string, parsed []vlink.ParsedOutbound) DiffResult {
	currSet := make(map[string]bool, len(current))
	for _, t := range current {
		currSet[t] = true
	}
	out := DiffResult{}
	parsedSet := make(map[string]bool, len(parsed))
	for _, p := range parsed {
		t := StableTag(subID, p)
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
