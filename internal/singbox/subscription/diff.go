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
	return stableTagFromKey(subID, keysOf(p).narrow)
}

// memberKeys holds every identity tier of one outbound, computed from a single
// parse of its body: chooseKeys needs all four per member, and re-parsing the
// JSON per tier is measurable on MIPS with large subscriptions (F736).
type memberKeys struct {
	// narrow: protocol + server + port + the user-credential field
	// appropriate to the protocol.
	narrow string
	// ext widens narrow with the reality-masking fields that distinguish
	// endpoints sharing one server:port:credential — SNI and the reality
	// short_id. Used only for servers whose narrow key collides.
	ext string
	// full widens ext with the transport fields.
	//
	// realm_id дописывается только когда он есть: узлы за одним реле hysteria2
	// делят server:port (это адрес реле) и пароль и различаются лишь им — без
	// него один пропадал как дубликат (F366). Без realm ключ прежний, и теги уже
	// расширенных групп не переезжают.
	full string
	// cred separates accounts that share the credential selected by narrow,
	// such as SOCKS/Naive users with the same password. Keep the legacy keys
	// intact: only groups they cannot distinguish use this tier.
	cred string
	// wire widens cred with the on-wire cipher and obfuscation: shadowsocks
	// method/plugin/plugin_opts and hysteria2 obfs. Two servers on one
	// server:port:password with a different cipher are different servers
	// (F737). Its own tier, not part of cred: cred tags are already released.
	// vless flow is left out — it is a client mode of the same server.
	wire string
}

func keysOf(p vlink.ParsedOutbound) memberKeys {
	var ob map[string]any
	// Ошибку не проверяем: битое тело отбраковывает partitionParsedOutbounds
	// («invalid outbound json») раньше, чем до него доходят ключи.
	json.Unmarshal(p.Outbound, &ob)
	return keysFromOutbound(p.Protocol, p.Server, p.Port, ob)
}

// keysFromOutbound — то же по уже разобранному телу: ручное добавление берёт
// тела существующих членов из слота конфига, а не из выдачи (F734).
func keysFromOutbound(protocol, server string, port uint16, ob map[string]any) memberKeys {
	uuid, _ := ob["uuid"].(string)
	password, _ := ob["password"].(string)
	username, _ := ob["username"].(string)
	cred := ""
	for _, v := range []string{uuid, password, username} {
		if v != "" {
			cred = v
			break
		}
	}
	var k memberKeys
	k.narrow = protocol + "|" + server + "|" + itoa(port) + "|" + cred

	sni, sid := "", ""
	if tls, ok := ob["tls"].(map[string]any); ok {
		sni, _ = tls["server_name"].(string)
		if r, ok := tls["reality"].(map[string]any); ok {
			sid, _ = r["short_id"].(string)
		}
	}
	k.ext = k.narrow + "|" + sni + "|" + sid

	k.full = k.ext + "|" + transportKey(ob)
	if r, ok := ob["realm"].(map[string]any); ok {
		if id, _ := r["realm_id"].(string); id != "" {
			k.full += "|realm:" + id
		}
	}

	// Encode the tuple rather than joining credentials with a separator that
	// may itself occur in a username or password.
	credKey, _ := json.Marshal([4]string{k.full, uuid, password, username})
	k.cred = string(credKey)

	method, _ := ob["method"].(string)
	plugin, _ := ob["plugin"].(string)
	pluginOpts, _ := ob["plugin_opts"].(string)
	obfsType, obfsPassword := "", ""
	if o, ok := ob["obfs"].(map[string]any); ok {
		obfsType, _ = o["type"].(string)
		obfsPassword, _ = o["password"].(string)
	}
	wireKey, _ := json.Marshal([6]string{k.cred, method, plugin, pluginOpts, obfsType, obfsPassword})
	k.wire = string(wireKey)
	return k
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

// chooseKeys uses the narrowest tier that distinguishes every account in a
// narrow-key group: narrow, ext (masking, issue #373), full (transport,
// issue #625), cred (remaining credential fields) or wire (cipher and
// obfuscation, F737) — see memberKeys.
// Groups the legacy tiers already distinguish retain their previous tags;
// true duplicates stay on the narrow key.
// Deterministic over the set (frequency is set-, not order-, dependent). Both
// ApplyDiff and the import preview build on this so the exclusion suffix
// (preview) and the final tag (refresh) agree.
func chooseKeys(parsed []vlink.ParsedOutbound) []string {
	all := make([]memberKeys, len(parsed))
	distinctExt := make(map[string]map[string]struct{}, len(parsed))
	distinctFull := make(map[string]map[string]struct{}, len(parsed))
	distinctCredentials := make(map[string]map[string]struct{}, len(parsed))
	distinctWire := make(map[string]map[string]struct{}, len(parsed))
	for i, p := range parsed {
		k := keysOf(p)
		all[i] = k
		if distinctExt[k.narrow] == nil {
			distinctExt[k.narrow] = make(map[string]struct{})
			distinctFull[k.narrow] = make(map[string]struct{})
			distinctCredentials[k.narrow] = make(map[string]struct{})
			distinctWire[k.narrow] = make(map[string]struct{})
		}
		distinctExt[k.narrow][k.ext] = struct{}{}
		distinctFull[k.narrow][k.full] = struct{}{}
		distinctCredentials[k.narrow][k.cred] = struct{}{}
		distinctWire[k.narrow][k.wire] = struct{}{}
	}
	keys := make([]string, len(parsed))
	for i, k := range all {
		nk := k.narrow
		switch {
		case len(distinctWire[nk]) <= 1:
			// Byte-identical duplicates: narrow key keeps the tag stable
			// across refreshes and matches previously stored narrow tags.
			keys[i] = nk
		case len(distinctExt[nk]) == len(distinctWire[nk]):
			// Masking alone tells them apart (issue #373). Staying on the
			// extended key keeps tags of already-widened groups unchanged —
			// widening further would re-tag subscriptions that never had the
			// bug.
			keys[i] = k.ext
		case len(distinctFull[nk]) == len(distinctWire[nk]):
			// Endpoints share masking and differ only by transport
			// (issue #625): only the full key separates them.
			keys[i] = k.full
		case len(distinctCredentials[nk]) == len(distinctWire[nk]):
			// Shared passwords do not make different usernames duplicates.
			keys[i] = k.cred
		default:
			// Same account, different cipher or obfuscation (F737).
			keys[i] = k.wire
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

// betterLabel: точнее ли имя кандидата, чем имя уже принятого дубликата.
// Ранг — vlink.LabelRank: 0 (имя дал провайдер) лучше любого другого, а среди
// выведенных лучше пришедшее из меньшего профиля — сводный профиль подписки
// крупнее любого частного.
func betterLabel(cand, prev int) bool {
	if prev == 0 {
		return false
	}
	return cand == 0 || cand < prev
}

// ApplyDiff classifies parsed outbounds against the stored MemberTags slice.
func ApplyDiff(subID string, current []string, parsed []vlink.ParsedOutbound) DiffResult {
	currSet := make(map[string]bool, len(current))
	for _, t := range current {
		currSet[t] = true
	}
	out := DiffResult{}
	tags := assignTags(subID, parsed)
	// Представитель дубликата выбирается ДО классификации: подписки
	// Happ/Remnawave отдают один и тот же сервер дважды — в сводном профиле
	// («Авто», имя выдумано как «Авто #N») и отдельной записью с человеческим
	// именем. Побеждает первый встреченный, поэтому у него переписывается имя,
	// если своего он не имел. Берём ТОЛЬКО имя: тело аутбаунда остаётся от
	// победителя, иначе дубликат молча подменил бы конфиг выхода полями,
	// которых нет в ключе (flow, alpn, utls, multiplex).
	winner := make(map[string]vlink.ParsedOutbound, len(parsed))
	order := make([]string, 0, len(parsed))
	for i, p := range parsed {
		t := tags[i]
		prev, dup := winner[t]
		if !dup {
			winner[t] = p
			order = append(order, t)
			continue
		}
		out.SkippedDuplicate++
		if betterLabel(p.LabelRank, prev.LabelRank) {
			prev.Label, prev.LabelRank = p.Label, p.LabelRank
			winner[t] = prev
		}
	}
	parsedSet := make(map[string]bool, len(order))
	for _, t := range order {
		parsedSet[t] = true
		tagged := TaggedOutbound{Tag: t, Out: winner[t]}
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
// TransportKey и RealmID сравниваются, только когда они есть у сохранённой (у
// записей, сделанных до появления полей, они пустые — см. MemberInfo).
func sameServer(stored, candidate MemberInfo) bool {
	if stored.Protocol != candidate.Protocol || stored.Server != candidate.Server ||
		stored.Port != candidate.Port || stored.SNI != candidate.SNI ||
		stored.Transport != candidate.Transport {
		return false
	}
	if stored.RealmID != "" && stored.RealmID != candidate.RealmID {
		return false // разные узлы за одним реле (F660)
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
