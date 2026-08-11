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

func TestMigrateAddressOrRules_MissingDir(t *testing.T) {
	changed, err := MigrateAddressOrRules(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("отсутствующий каталог не ошибка: %v", err)
	}
	if changed {
		t.Error("changed = true для отсутствующего каталога")
	}
}
