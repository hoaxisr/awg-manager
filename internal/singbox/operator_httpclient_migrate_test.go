package singbox

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Миграция sing-box 1.14: download_detour у remote rule-set'ов слота
// превращается в http_client.detour; detour "direct" схлопывается в
// default-клиент (поле исчезает); второй прогон байт-в-байт идемпотентен.
func TestPatchSlotRuleSetsDownloadDetour_MigratesAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "20-router.json")
	legacy := `{
		"inbounds": [{"type":"tproxy","tag":"tproxy-in","listen":"::","listen_port":51280}],
		"outbounds": [{"type":"selector","tag":"vpn","outbounds":["direct"]}],
		"route": {
			"final": "direct",
			"rule_set": [
				{"tag":"geo-vpn","type":"remote","format":"binary","url":"https://example.com/a.srs","download_detour":"vpn"},
				{"tag":"geo-direct","type":"remote","format":"binary","url":"https://example.com/b.srs","download_detour":"direct"},
				{"tag":"geo-auto","type":"remote","format":"binary","url":"https://example.com/c.srs"},
				{"tag":"inline-set","type":"inline","rules":[{"domain_suffix":[".example.com"]}]}
			],
			"rules": [{"rule_set":["geo-vpn"],"outbound":"vpn"}]
		}
	}`
	if err := os.WriteFile(p, []byte(legacy), 0644); err != nil {
		t.Fatal(err)
	}
	patchSlotRuleSetsDownloadDetour(p)

	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("download_detour")) {
		t.Fatalf("download_detour survived migration: %s", raw)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	route := m["route"].(map[string]any)
	ruleSets := route["rule_set"].([]any)
	if len(ruleSets) != 4 {
		t.Fatalf("rule_set count = %d", len(ruleSets))
	}
	byTag := map[string]map[string]any{}
	for _, v := range ruleSets {
		rs := v.(map[string]any)
		byTag[rs["tag"].(string)] = rs
	}
	hc, _ := byTag["geo-vpn"]["http_client"].(map[string]any)
	if hc == nil || hc["detour"] != "vpn" {
		t.Errorf("geo-vpn http_client = %v, want detour vpn", byTag["geo-vpn"]["http_client"])
	}
	if _, has := byTag["geo-direct"]["http_client"]; has {
		t.Errorf("geo-direct: detour direct must collapse to default client, got %v", byTag["geo-direct"]["http_client"])
	}
	if _, has := byTag["geo-auto"]["http_client"]; has {
		t.Errorf("geo-auto: no detour must stay without http_client, got %v", byTag["geo-auto"]["http_client"])
	}
	// Rules и inbounds/outbounds не тронуты.
	if rules, _ := route["rules"].([]any); len(rules) != 1 {
		t.Errorf("rules: %v", rules)
	}

	// Второй прогон — файл байт-в-байт тот же (mtime не проверяем,
	// патчер не пишет без изменений).
	patchSlotRuleSetsDownloadDetour(p)
	raw2, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, raw2) {
		t.Fatalf("second run not byte-identical:\n%s\n---\n%s", raw, raw2)
	}
}

// Слот без download_detour патчер не переписывает вовсе (байты сохранены,
// включая исходное форматирование).
func TestPatchSlotRuleSetsDownloadDetour_NoopWithoutLegacyField(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "21-fakeip.json")
	clean := `{"route":{"rule_set":[{"tag":"geo","type":"remote","url":"https://e/x.srs","http_client":{"detour":"vpn"}}]}}`
	if err := os.WriteFile(p, []byte(clean), 0644); err != nil {
		t.Fatal(err)
	}
	patchSlotRuleSetsDownloadDetour(p)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != clean {
		t.Fatalf("clean slot rewritten: %s", raw)
	}
}

// Конфликтная пара (download_detour + http_client, upstream её отвергает)
// разрешается в пользу http_client: legacy-поле просто удаляется.
func TestPatchSlotRuleSetsDownloadDetour_HTTPClientWins(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "20-router.json")
	conflicted := `{"route":{"rule_set":[{"tag":"geo","type":"remote","url":"https://e/x.srs","download_detour":"old","http_client":{"detour":"new"}}]}}`
	if err := os.WriteFile(p, []byte(conflicted), 0644); err != nil {
		t.Fatal(err)
	}
	patchSlotRuleSetsDownloadDetour(p)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("download_detour")) {
		t.Fatalf("download_detour survived: %s", raw)
	}
	if !bytes.Contains(raw, []byte(`"detour": "new"`)) && !bytes.Contains(raw, []byte(`"detour":"new"`)) {
		t.Fatalf("http_client detour lost: %s", raw)
	}
}

// Самолечение 00-base.json: добавляется явный default HTTP-клиент
// (top-level http_clients); существующий ключ не перетирается; повторный
// запуск идемпотентен.
func TestPatchBaseEnsureHTTPClients(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "00-base.json")
	old := `{"log":{"level":"info"},"outbounds":[{"type":"direct","tag":"direct"}]}`
	if err := os.WriteFile(p, []byte(old), 0644); err != nil {
		t.Fatal(err)
	}
	patchBaseEnsureHTTPClients(p)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	clients, _ := m["http_clients"].([]any)
	if len(clients) != 1 {
		t.Fatalf("http_clients = %v", m["http_clients"])
	}
	if tag := clients[0].(map[string]any)["tag"]; tag != defaultHTTPClientTag {
		t.Fatalf("tag = %v, want %s", tag, defaultHTTPClientTag)
	}

	// Идемпотентность: второй запуск не меняет байты.
	patchBaseEnsureHTTPClients(p)
	raw2, _ := os.ReadFile(p)
	if !bytes.Equal(raw, raw2) {
		t.Fatalf("second run changed bytes")
	}

	// Пользовательская правка ключа сохраняется.
	custom := `{"http_clients":[{"tag":"mine","detour":"vpn"}],"outbounds":[]}`
	if err := os.WriteFile(p, []byte(custom), 0644); err != nil {
		t.Fatal(err)
	}
	patchBaseEnsureHTTPClients(p)
	raw3, _ := os.ReadFile(p)
	if string(raw3) != custom {
		t.Fatalf("custom http_clients overwritten: %s", raw3)
	}
}

// Свежий 00-base.json из freshBaseConfig сразу содержит default-клиент.
func TestFreshBaseConfigHasDefaultHTTPClient(t *testing.T) {
	m := freshBaseConfig()
	clients, _ := m["http_clients"].([]any)
	if len(clients) != 1 {
		t.Fatalf("http_clients = %v", m["http_clients"])
	}
	if tag := clients[0].(map[string]any)["tag"]; tag != defaultHTTPClientTag {
		t.Fatalf("tag = %v", tag)
	}
}
