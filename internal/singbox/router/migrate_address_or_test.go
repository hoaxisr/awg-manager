package router

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateAddressOrRules(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pending"), 0755); err != nil {
		t.Fatal(err)
	}

	broken := `{"route":{"rules":[
	  {"rule_set":["geosite-discord"],"ip_cidr":["66.22.192.0/18"],"action":"route","outbound":"vpn"}
	],"final":"direct"},"outbounds":[]}`
	untouched := `{"route":{"rules":[{"rule_set":["geosite-x"],"action":"route","outbound":"vpn"}]}}`
	// Незнакомое нашим структурам поле внутри правила: молча потерять его при
	// переписывании нельзя, поэтому такое правило миграция обязана пропустить.
	unknownField := `{"route":{"rules":[
	  {"rule_set":["geosite-y"],"ip_cidr":["1.2.3.0/24"],"outbound":"vpn","wifi_ssid":["home"]}
	]}}`

	files := map[string]string{
		"20-router.json":         broken,
		"pending/20-router.json": broken,
		"30-other.json":          untouched,
		"40-unknown.json":        unknownField,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}

	changed, err := MigrateAddressOrRules(dir)
	if err != nil {
		t.Fatalf("MigrateAddressOrRules: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}

	for _, name := range []string{"20-router.json", "pending/20-router.json"} {
		var cfg RouterConfig
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		r := cfg.Route.Rules[0]
		if r.Type != "logical" || r.Mode != "or" || len(r.Rules) != 2 {
			t.Errorf("%s: правило не нормализовано: %+v", name, r)
		}
		if r.Outbound != "vpn" || cfg.Route.Final != "direct" {
			t.Errorf("%s: потеряны поля вокруг правила: %+v final=%q", name, r, cfg.Route.Final)
		}
	}

	rawUntouched, err := os.ReadFile(filepath.Join(dir, "30-other.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(rawUntouched) != untouched {
		t.Errorf("файл без подходящих правил переписан:\n%s", rawUntouched)
	}

	rawUnknown, err := os.ReadFile(filepath.Join(dir, "40-unknown.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(rawUnknown) != unknownField {
		t.Errorf("правило с незнакомым полем переписано (потеря данных):\n%s", rawUnknown)
	}

	// Идемпотентность: второй прогон ничего не меняет.
	changed2, err := MigrateAddressOrRules(dir)
	if err != nil {
		t.Fatalf("второй прогон: %v", err)
	}
	if changed2 {
		t.Error("второй прогон отчитался об изменениях — миграция не идемпотентна")
	}
}

// Слот эксперт-редактора пишется руками и принадлежит пользователю —
// миграция не имеет права переписывать его текст.
func TestMigrateAddressOrRules_SkipsUserSlot(t *testing.T) {
	dir := t.TempDir()
	body := `{"route":{"rules":[{"rule_set":["geosite-x"],"ip_cidr":["1.2.3.0/24"],"outbound":"vpn"}]}}`
	path := filepath.Join(dir, "90-user.json")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}

	changed, err := MigrateAddressOrRules(dir)
	if err != nil {
		t.Fatalf("MigrateAddressOrRules: %v", err)
	}
	if changed {
		t.Error("changed = true — миграция тронула пользовательский слот")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != body {
		t.Errorf("90-user.json переписан:\n%s", raw)
	}

	if idx := FlatAddressOrRuleIndexes([]byte(body)); len(idx) != 1 || idx[0] != 0 {
		t.Errorf("FlatAddressOrRuleIndexes = %v, want [0]", idx)
	}
	normalized := `{"route":{"rules":[{"type":"logical","mode":"or","rules":[{"rule_set":["geosite-x"]},{"ip_cidr":["1.2.3.0/24"]}],"outbound":"vpn"}]}}`
	if idx := FlatAddressOrRuleIndexes([]byte(normalized)); len(idx) != 0 {
		t.Errorf("нормализованное правило попало в предупреждения: %v", idx)
	}
}

// В sing-box protocol/network/inbound — Listable: и скаляр, и массив
// валидны, а документация показывает массивную форму, так что в ручном
// слоте она встречается сплошь. Разбор конфига целиком спотыкался на таком
// правиле и гасил предупреждения по ВСЕМУ файлу.
func TestFlatAddressOrRuleIndexes_TolerantParsing(t *testing.T) {
	raw := []byte(`{"route":{"rules":[
	  {"protocol":["dns"],"outbound":"direct"},
	  {"rule_set":["geosite-x"],"ip_cidr":["1.2.3.0/24"],"outbound":"vpn"},
	  {"port":["50000:50100"],"outbound":"direct"},
	  {"rule_set":["geosite-y"],"domain_suffix":["a.com"],"outbound":"vpn"}
	]}}`)
	got := FlatAddressOrRuleIndexes(raw)
	want := []int{1, 3}
	if len(got) != len(want) {
		t.Fatalf("FlatAddressOrRuleIndexes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("FlatAddressOrRuleIndexes = %v, want %v", got, want)
		}
	}

	if idx := FlatAddressOrRuleIndexes([]byte(`{"route":{"rules":"мусор"}}`)); idx != nil {
		t.Errorf("битые правила: %v, want nil", idx)
	}
}

func TestMigrateAddressOrRules_MissingDir(t *testing.T) {
	changed, err := MigrateAddressOrRules(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("отсутствующий каталог не ошибка: %v", err)
	}
	if changed {
		t.Error("changed = true для отсутствующего каталога")
	}
}
