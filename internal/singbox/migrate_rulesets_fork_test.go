package singbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readMigrFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMigrateRuleSetURLsToFork(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{filepath.Join(dir, "disabled"), filepath.Join(dir, "pending")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	vern := `{"route":{"rule_set":[{"tag":"x","type":"remote","url":"https://github.com/vernette/rulesets/raw/master/srs/discord-full.srs","unknown_field":"keep-me"}]}}`
	clean := `{"route":{"rule_set":[]}}`
	// Пользовательский workaround через gh-прокси: vernette-URL внутри чужого —
	// НЕ трогать (замена сделала бы его нерабочим гибридом).
	proxied := `{"route":{"rule_set":[{"tag":"p","type":"remote","url":"https://ghproxy.com/https://github.com/vernette/rulesets/raw/master/srs/x.srs"}]}}`

	// vernette живёт не только в 20-router.json: 21-fakeip.json тоже держит
	// remote rule-set URL; миграция обязана подмести ВСЕ slot-файлы в active/
	// disabled/pending.
	withVernette := []string{
		filepath.Join(dir, "21-routing.json"),
		filepath.Join(dir, "20-fakeip.json"),
		filepath.Join(dir, "disabled", "21-routing.json"),
		filepath.Join(dir, "pending", "20-fakeip.json"),
	}
	for _, p := range withVernette {
		if err := os.WriteFile(p, []byte(vern), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	untouched := filepath.Join(dir, "30-deviceproxy.json")
	if err := os.WriteFile(untouched, []byte(clean), 0o644); err != nil {
		t.Fatal(err)
	}
	proxiedFile := filepath.Join(dir, "90-user.json")
	if err := os.WriteFile(proxiedFile, []byte(proxied), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := MigrateRuleSetURLsToFork(dir)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !changed {
		t.Fatal("ожидался changed=true")
	}

	for _, p := range withVernette {
		s := readMigrFile(t, p)
		if strings.Contains(s, "vernette") {
			t.Errorf("%s: vernette осталось", p)
		}
		if !strings.Contains(s, "https://repo.hoaxisr.ru/rulesets/srs/discord-full.srs") {
			t.Errorf("%s: нет URL зеркала", p)
		}
		if !strings.Contains(s, "keep-me") {
			t.Errorf("%s: неизвестное поле потеряно", p)
		}
	}
	if readMigrFile(t, untouched) != clean {
		t.Errorf("файл без vernette изменён")
	}
	if readMigrFile(t, proxiedFile) != proxied {
		t.Errorf("proxy-обёрнутый пользовательский URL изменён")
	}

	// Идемпотентность (и changed=false на втором прогоне).
	before := readMigrFile(t, withVernette[0])
	changed2, err := MigrateRuleSetURLsToFork(dir)
	if err != nil {
		t.Fatalf("migrate (2nd): %v", err)
	}
	if changed2 {
		t.Error("2-й прогон должен вернуть changed=false")
	}
	if readMigrFile(t, withVernette[0]) != before {
		t.Errorf("2-й прогон изменил файл")
	}
}

func TestMigrateRuleSetURLsToFork_NoDir(t *testing.T) {
	changed, err := MigrateRuleSetURLsToFork(filepath.Join(t.TempDir(), "nope"))
	if err != nil || changed {
		t.Fatalf("ожидался no-op, got changed=%v err=%v", changed, err)
	}
}
