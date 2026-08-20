package subscription

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

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

// transportKey collects the transport fields that make two endpoints on one
// server:port:credential:SNI genuinely different routes: ws/httpupgrade/xhttp
// path and Host, gRPC service_name, xhttp mode (issue #625). Empty when the
// outbound carries no transport block (plain TCP).
//
// Field shapes differ per transport type (host is a string for httpupgrade and
// xhttp but a []string for http/h2, and ws puts it in headers.Host), so every
// spelling is read here rather than branching on the transport type.
func transportKey(ob map[string]any) string {
	tr, ok := ob["transport"].(map[string]any)
	if !ok {
		return ""
	}
	host := ""
	switch h := tr["host"].(type) {
	case string:
		host = h
	case []any:
		parts := make([]string, 0, len(h))
		for _, v := range h {
			if s, ok := v.(string); ok {
				parts = append(parts, s)
			}
		}
		host = strings.Join(parts, ",")
	}
	if host == "" {
		if headers, ok := tr["headers"].(map[string]any); ok {
			host, _ = headers["Host"].(string)
		}
	}
	typ, _ := tr["type"].(string)
	path, _ := tr["path"].(string)
	service, _ := tr["service_name"].(string)
	mode, _ := tr["mode"].(string)
	return typ + "|" + path + "|" + host + "|" + service + "|" + mode
}

// fullKey is the widest identity: extendedKey plus the transport fields.
func fullKey(p vlink.ParsedOutbound) string {
	var ob map[string]any
	json.Unmarshal(p.Outbound, &ob)
	return extendedKey(p) + "|" + transportKey(ob)
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
	distinctFull := make(map[string]map[string]struct{}, len(parsed))
	for _, p := range parsed {
		nk := identityKey(p)
		if distinctExt[nk] == nil {
			distinctExt[nk] = make(map[string]struct{})
			distinctFull[nk] = make(map[string]struct{})
		}
		distinctExt[nk][extendedKey(p)] = struct{}{}
		distinctFull[nk][fullKey(p)] = struct{}{}
	}
	keys := make([]string, len(parsed))
	for i, p := range parsed {
		nk := identityKey(p)
		switch {
		case len(distinctFull[nk]) <= 1:
			// Byte-identical duplicates: narrow key keeps the tag stable
			// across refreshes and matches previously stored narrow tags.
			keys[i] = nk
		case len(distinctExt[nk]) == len(distinctFull[nk]):
			// Masking alone tells them apart (issue #373). Staying on the
			// extended key keeps tags of already-widened groups unchanged —
			// widening further would re-tag subscriptions that never had the
			// bug.
			keys[i] = extendedKey(p)
		default:
			// Endpoints share masking and differ only by transport
			// (issue #625): only the full key separates them.
			keys[i] = fullKey(p)
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

// sameServer сравнивает метаданные двух членов: тот же ли это сервер.
// Первым аргументом идёт сохранённая запись, вторым — кандидат из выдачи:
// TransportKey сравнивается, только когда он есть у сохранённой (у записей,
// сделанных до появления поля, оно пустое — см. MemberInfo).
func sameServer(stored, candidate MemberInfo) bool {
	if stored.Protocol != candidate.Protocol || stored.Server != candidate.Server ||
		stored.Port != candidate.Port || stored.SNI != candidate.SNI ||
		stored.Transport != candidate.Transport {
		return false
	}
	return stored.TransportKey == "" || stored.TransportKey == candidate.TransportKey
}

// reassociateOrphans возвращает сироте её прежний тег, когда в выдаче есть тот
// же сервер под новым тегом (issue #745). Ключ идентичности (chooseKeys)
// выводится из полей выдачи и уровнем зависит от состава набора, поэтому тег
// едет там, где сервер не менялся: панель ротирует reality short_id, или
// провайдер убрал соседний эндпоинт и группа схлопнулась на ключ поуже. Тег —
// пользовательские данные (ExcludedTags, ActiveMember, теги в конфиге
// sing-box), поэтому сохраняем СТАРЫЙ: запись переезжает из New в Existing и
// обновляется на месте.
//
// known — метаданные членов прошлого refresh (sub.Members): сироты приходят из
// MemberTags, значит их описания лежат там же. Пересчитывать прежние ключи
// нечем — на диске их нет, поэтому сравнение идёт по метаданным, как в
// remapStaleTags. Сопоставляем строго 1:1 и только при единственном кандидате:
// лучше оставить лишнюю сироту, чем склеить два разных сервера под одним тегом.
func reassociateOrphans(diff DiffResult, known []MemberInfo) DiffResult {
	if len(diff.Orphan) == 0 || len(diff.New) == 0 {
		return diff
	}
	infoByTag := make(map[string]MemberInfo, len(known))
	for _, m := range known {
		infoByTag[m.Tag] = m
	}
	claimed := make(map[int]bool, len(diff.New))   // индексы New, уже забравшие тег
	renamed := make(map[int]string, len(diff.New)) // индекс New → прежний тег
	orphans := make([]string, 0, len(diff.Orphan))
	for _, tag := range diff.Orphan {
		mi, ok := infoByTag[tag]
		if !ok {
			orphans = append(orphans, tag) // нет метаданных — судить не о чем
			continue
		}
		match := -1
		for i, n := range diff.New {
			if claimed[i] || !sameServer(mi, toMemberInfo(n.Tag, n.Out)) {
				continue
			}
			if match >= 0 {
				match = -1 // неоднозначно — оставляем сиротой
				break
			}
			match = i
		}
		if match < 0 {
			orphans = append(orphans, tag)
			continue
		}
		claimed[match] = true
		renamed[match] = tag
	}
	if len(renamed) == 0 {
		return diff
	}
	out := diff
	out.Orphan = orphans
	out.New = make([]TaggedOutbound, 0, len(diff.New)-len(renamed))
	out.Existing = append(make([]TaggedOutbound, 0, len(diff.Existing)+len(renamed)), diff.Existing...)
	for i, n := range diff.New {
		if old, ok := renamed[i]; ok {
			n.Tag = old
			out.Existing = append(out.Existing, n)
			continue
		}
		out.New = append(out.New, n)
	}
	return out
}
