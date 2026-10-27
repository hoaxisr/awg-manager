package subscription

import (
	"encoding/json"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/vlink"
)

func mkParsed(server string, port uint16, scheme string) vlink.ParsedOutbound {
	ob := map[string]any{"type": scheme, "server": server, "server_port": port}
	if scheme == "vless" {
		ob["uuid"] = "00000000-0000-0000-0000-000000000000"
	} else if scheme == "trojan" {
		ob["password"] = "p"
	}
	raw, _ := json.Marshal(ob)
	return vlink.ParsedOutbound{
		Tag:      "tmp",
		Protocol: scheme,
		Server:   server,
		Port:     port,
		Outbound: raw,
	}
}

func TestStableTag_DeterministicAcrossInputs(t *testing.T) {
	a := StableTag("subID", mkParsed("h", 443, "vless"))
	b := StableTag("subID", mkParsed("h", 443, "vless"))
	if a != b {
		t.Errorf("stable hash should be deterministic: %s vs %s", a, b)
	}
}

func TestStableTag_DifferentServerProducesDifferentTag(t *testing.T) {
	a := StableTag("subID", mkParsed("h1", 443, "vless"))
	b := StableTag("subID", mkParsed("h2", 443, "vless"))
	if a == b {
		t.Errorf("different server should yield different tag: %s == %s", a, b)
	}
}

func TestApplyDiff_NewMembers(t *testing.T) {
	current := []string{}
	parsed := []vlink.ParsedOutbound{
		mkParsed("h1", 443, "vless"),
		mkParsed("h2", 443, "trojan"),
	}
	diff := ApplyDiff("subID", current, parsed)
	if len(diff.New) != 2 {
		t.Errorf("New=%d want 2", len(diff.New))
	}
	if len(diff.Existing) != 0 || len(diff.Orphan) != 0 {
		t.Errorf("expected only New, got Existing=%d Orphan=%d", len(diff.Existing), len(diff.Orphan))
	}
}

func TestApplyDiff_ExistingDetected(t *testing.T) {
	parsed := []vlink.ParsedOutbound{
		mkParsed("h1", 443, "vless"),
	}
	tag := StableTag("subID", parsed[0])
	current := []string{tag}
	diff := ApplyDiff("subID", current, parsed)
	if len(diff.Existing) != 1 || len(diff.New) != 0 {
		t.Errorf("expected Existing=1 New=0, got %+v", diff)
	}
}

func TestApplyDiff_OrphanDetected(t *testing.T) {
	current := []string{"sub-subID-orphanhash"}
	parsed := []vlink.ParsedOutbound{}
	diff := ApplyDiff("subID", current, parsed)
	if len(diff.Orphan) != 1 || diff.Orphan[0] != "sub-subID-orphanhash" {
		t.Errorf("expected Orphan=[sub-subID-orphanhash], got %+v", diff.Orphan)
	}
}

func TestApplyDiff_SkipsDuplicates(t *testing.T) {
	current := []string{}
	// Three identical outbounds (same protocol+server+port+credential → same StableTag)
	parsed := []vlink.ParsedOutbound{
		mkParsed("h1", 443, "vless"),
		mkParsed("h1", 443, "vless"),
		mkParsed("h1", 443, "vless"),
	}
	diff := ApplyDiff("subID", current, parsed)
	if len(diff.New) != 1 {
		t.Errorf("New=%d want 1 (dedup)", len(diff.New))
	}
	if diff.SkippedDuplicate != 2 {
		t.Errorf("SkippedDuplicate=%d want 2", diff.SkippedDuplicate)
	}
}

func TestApplyDiff_DuplicatesAcrossNewAndExisting(t *testing.T) {
	parsed1 := mkParsed("h1", 443, "vless")
	tag := StableTag("subID", parsed1)
	current := []string{tag}
	parsed := []vlink.ParsedOutbound{parsed1, parsed1}
	diff := ApplyDiff("subID", current, parsed)
	if len(diff.Existing) != 1 || len(diff.New) != 0 {
		t.Errorf("expected Existing=1 New=0, got %+v", diff)
	}
	if diff.SkippedDuplicate != 1 {
		t.Errorf("SkippedDuplicate=%d want 1", diff.SkippedDuplicate)
	}
}

func mkReality(server string, port uint16, sni, sid string) vlink.ParsedOutbound {
	ob := map[string]any{
		"type": "vless", "server": server, "server_port": port,
		"uuid": "00000000-0000-0000-0000-000000000000",
		"tls": map[string]any{
			"enabled":     true,
			"server_name": sni,
			"reality":     map[string]any{"enabled": true, "short_id": sid},
		},
	}
	raw, _ := json.Marshal(ob)
	return vlink.ParsedOutbound{Tag: "tmp", Protocol: "vless", Server: server, Port: port, Outbound: raw}
}

func TestExtendedKey_DistinguishesSNIAndShortID(t *testing.T) {
	base := mkReality("h", 443, "eh1.vk.ru", "01ab")
	sameSNIsameSID := mkReality("h", 443, "eh1.vk.ru", "01ab")
	diffSNI := mkReality("h", 443, "io.ozone.ru", "01ab")
	diffSID := mkReality("h", 443, "eh1.vk.ru", "02cd")

	if keysOf(base).ext != keysOf(sameSNIsameSID).ext {
		t.Error("identical (SNI,short_id) must yield identical ext key")
	}
	if keysOf(base).ext == keysOf(diffSNI).ext {
		t.Error("different SNI must yield different ext key")
	}
	if keysOf(base).ext == keysOf(diffSID).ext {
		t.Error("different short_id must yield different ext key")
	}
	if keysOf(base).narrow != keysOf(diffSNI).narrow {
		t.Error("narrow key must ignore SNI")
	}
}

func TestApplyDiff_RealityCollisionDistinct(t *testing.T) {
	// 3 эндпоинта на одном server:port:uuid, разные SNI → 3 различимых тега.
	parsed := []vlink.ParsedOutbound{
		mkReality("h", 443, "eh1.vk.ru", "01ab"),
		mkReality("h", 443, "io.ozone.ru", "02cd"),
		mkReality("h", 443, "vk.com", "03ef"),
	}
	diff := ApplyDiff("subID", nil, parsed)
	if len(diff.New) != 3 {
		t.Errorf("New=%d want 3 (each masking distinct)", len(diff.New))
	}
	if diff.SkippedDuplicate != 0 {
		t.Errorf("SkippedDuplicate=%d want 0", diff.SkippedDuplicate)
	}
}

func TestApplyDiff_ExtendedKeyCollisionDedup(t *testing.T) {
	// Те же (SNI,short_id) → побайтово неразличимы → 1 тег, остальные skipped.
	parsed := []vlink.ParsedOutbound{
		mkReality("h", 443, "eh1.vk.ru", "01ab"),
		mkReality("h", 443, "eh1.vk.ru", "01ab"),
	}
	diff := ApplyDiff("subID", nil, parsed)
	if len(diff.New) != 1 || diff.SkippedDuplicate != 1 {
		t.Errorf("want New=1 Skipped=1, got New=%d Skipped=%d", len(diff.New), diff.SkippedDuplicate)
	}
}

func TestApplyDiff_NoCollisionUsesNarrowTag(t *testing.T) {
	// Сервер-одиночка (узкий ключ уникален) сохраняет узкий тег → 0 churn.
	p := mkReality("h", 443, "eh1.vk.ru", "01ab")
	parsed := []vlink.ParsedOutbound{p, mkParsed("other", 443, "vless")}
	tags := assignTags("subID", parsed)
	if tags[0] != StableTag("subID", p) {
		t.Errorf("unique narrow key must keep narrow tag: got %s want %s", tags[0], StableTag("subID", p))
	}
}

func TestStableTag_SuffixSubIDIndependent(t *testing.T) {
	p := vlink.ParsedOutbound{Protocol: "vless", Server: "a.example", Port: 443, Outbound: []byte(`{"uuid":"u1"}`)}
	h := suffixOf(keysOf(p).narrow)
	if len(h) != 8 {
		t.Fatalf("want 8 hex chars, got %q", h)
	}
	// Полный тег для разных subID должен совпадать суффиксом — ключом исключения.
	t1 := StableTag("aaaaaaaa1111", p)
	t2 := StableTag("bbbbbbbb2222", p)
	if t1[len(t1)-8:] != h || t2[len(t2)-8:] != h {
		t.Fatalf("suffix mismatch: t1=%s t2=%s h=%s", t1, t2, h)
	}
}

func TestPreviewSuffixMatchesRefreshTag(t *testing.T) {
	// Коллизирующая группа: суффикс, который превью кладёт в Key, обязан
	// совпасть с хвостом тега из assignTags (иначе exclude из превью не
	// сматчится после create+refresh).
	parsed := []vlink.ParsedOutbound{
		mkReality("h", 443, "eh1.vk.ru", "01ab"),
		mkReality("h", 443, "io.ozone.ru", "02cd"),
	}
	keys := chooseKeys(parsed)
	tags := assignTags("subID1234", parsed)
	for i := range parsed {
		previewKey := suffixOf(keys[i])
		tagTail := tags[i][len(tags[i])-8:]
		if previewKey != tagTail {
			t.Errorf("member %d: preview Key %s != tag tail %s", i, previewKey, tagTail)
		}
	}
}

// mkParsedWS строит vless-сервер с ws-транспортом: те же server/port/uuid,
// но своя пара path+Host — ровно случай issue #625.
func mkParsedWS(server string, port uint16, sni, path, host string) vlink.ParsedOutbound {
	ob := map[string]any{
		"type": "vless", "server": server, "server_port": port,
		"uuid": "00000000-0000-0000-0000-000000000000",
		"tls":  map[string]any{"enabled": true, "server_name": sni},
		"transport": map[string]any{
			"type": "ws", "path": path,
			"headers": map[string]any{"Host": host},
		},
	}
	raw, _ := json.Marshal(ob)
	return vlink.ParsedOutbound{
		Tag: "tmp", Protocol: "vless", Server: server, Port: port, Outbound: raw,
	}
}

// mkParsedReality — различие только в маскировке (issue #373): тег обязан
// остаться тем же, что и до правки, иначе у существующих подписок разъедутся
// все теги, а не только сломанные.
func mkParsedReality(server string, port uint16, sni, shortID string) vlink.ParsedOutbound {
	ob := map[string]any{
		"type": "vless", "server": server, "server_port": port,
		"uuid": "00000000-0000-0000-0000-000000000000",
		"tls": map[string]any{
			"enabled": true, "server_name": sni,
			"reality": map[string]any{"enabled": true, "short_id": shortID},
		},
	}
	raw, _ := json.Marshal(ob)
	return vlink.ParsedOutbound{
		Tag: "tmp", Protocol: "vless", Server: server, Port: port, Outbound: raw,
	}
}

// Issue #625: серверы на одном server:port:uuid:SNI, различающиеся только
// ws-путём и Host-заголовком, — разные эндпоинты, а не дубликаты.
func TestApplyDiff_TransportDistinguishesMembers(t *testing.T) {
	parsed := []vlink.ParsedOutbound{
		mkParsedWS("1.2.3.4", 443, "cdn.example.com", "/path1", "host1"),
		mkParsedWS("1.2.3.4", 443, "cdn.example.com", "/path2", "host2"),
	}
	got := ApplyDiff("sub", nil, parsed)
	if got.SkippedDuplicate != 0 {
		t.Errorf("SkippedDuplicate = %d, want 0", got.SkippedDuplicate)
	}
	if len(got.New) != 2 {
		t.Fatalf("len(New) = %d, want 2", len(got.New))
	}
	if got.New[0].Tag == got.New[1].Tag {
		t.Errorf("both endpoints got the same tag %q", got.New[0].Tag)
	}
}

// Байт-идентичные записи остаются дубликатами и держат УЗКИЙ ключ —
// иначе теги существующих подписок поедут без причины.
func TestApplyDiff_TrueDuplicatesKeepNarrowTag(t *testing.T) {
	p := mkParsedWS("1.2.3.4", 443, "cdn.example.com", "/p", "h")
	got := ApplyDiff("sub", nil, []vlink.ParsedOutbound{p, p})
	if got.SkippedDuplicate != 1 {
		t.Errorf("SkippedDuplicate = %d, want 1", got.SkippedDuplicate)
	}
	if len(got.New) != 1 {
		t.Fatalf("len(New) = %d, want 1", len(got.New))
	}
	if want := stableTagFromKey("sub", keysOf(p).narrow); got.New[0].Tag != want {
		t.Errorf("tag = %q, want narrow-key tag %q", got.New[0].Tag, want)
	}
}

// Регрессия на радиус поражения: когда серверы различает ОДНА маскировка,
// ключ обязан остаться расширенным (как до правки), а не поехать на полный.
func TestApplyDiff_RealityOnlyKeepsExtendedTag(t *testing.T) {
	a := mkParsedReality("1.2.3.4", 443, "a.example.com", "aa")
	b := mkParsedReality("1.2.3.4", 443, "b.example.com", "bb")
	got := ApplyDiff("sub", nil, []vlink.ParsedOutbound{a, b})
	if len(got.New) != 2 {
		t.Fatalf("len(New) = %d, want 2", len(got.New))
	}
	if want := stableTagFromKey("sub", keysOf(a).ext); got.New[0].Tag != want {
		t.Errorf("tag = %q, want extended-key tag %q (masking alone must not widen further)", got.New[0].Tag, want)
	}
}

// mkParsedRealm — hysteria2 за реле: Server/Port — адрес реле, общий у всех
// узлов за ним, различие только в realm_id (F366).
func mkParsedRealm(realmID string) vlink.ParsedOutbound {
	ob := map[string]any{
		"type": "hysteria2", "password": "p",
		"realm": map[string]any{"server_url": "https://relay.example.com:443", "token": "t", "realm_id": realmID},
	}
	raw, _ := json.Marshal(ob)
	return vlink.ParsedOutbound{
		Tag: "tmp", Protocol: "hysteria2", Server: "relay.example.com", Port: 443, Outbound: raw,
	}
}

// F366: два узла за одним реле под одним паролем — разные узлы, а не дубликат;
// одиночный realm-узел держит узкий ключ, как и прежде.
func TestApplyDiff_RealmIDDistinguishesMembers(t *testing.T) {
	got := ApplyDiff("sub", nil, []vlink.ParsedOutbound{mkParsedRealm("a"), mkParsedRealm("b")})
	if got.SkippedDuplicate != 0 || len(got.New) != 2 || got.New[0].Tag == got.New[1].Tag {
		t.Fatalf("SkippedDuplicate=%d New=%d — узлы за реле склеились", got.SkippedDuplicate, len(got.New))
	}
	single := mkParsedRealm("a")
	one := ApplyDiff("sub", nil, []vlink.ParsedOutbound{single})
	if want := stableTagFromKey("sub", keysOf(single).narrow); len(one.New) != 1 || one.New[0].Tag != want {
		t.Errorf("одиночный realm-узел сменил тег")
	}
}

// F660: узлы за одним реле hysteria2 — разные серверы для переассоциации
// сирот и переноса исключений; запись без RealmID (до появления поля)
// сравнивается грубо, как с TransportKey. RealmID берётся из блока realm.
func TestSameServer_RealmID(t *testing.T) {
	a := toMemberInfo("a", mkParsedRealm("a"))
	b := toMemberInfo("b", mkParsedRealm("b"))
	if a.RealmID != "a" {
		t.Fatalf("RealmID не извлечён: %+v", a)
	}
	if sameServer(a, b) {
		t.Error("узлы с разным realm_id признаны одним сервером")
	}
	if !sameServer(a, toMemberInfo("a2", mkParsedRealm("a"))) {
		t.Error("тот же узел не признан")
	}
	legacy := a
	legacy.RealmID = ""
	if !sameServer(legacy, b) {
		t.Error("запись без RealmID обязана сравниваться грубо")
	}
}
