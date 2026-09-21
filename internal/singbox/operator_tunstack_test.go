package singbox

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// F396: сборка без with_gvisor не поднимает tun-инбаунд со стеком
// gvisor/mixed — FATAL на старте движка. Значение приезжает из слотового
// файла, написанного старой версией, поэтому снимать его надо в файле.
func TestStripLegacyTunStack_RemovesUnexecutableStacks(t *testing.T) {
	cases := []struct {
		name  string
		stack string
		want  bool // ключ stack должен остаться
	}{
		{"gvisor", `"stack":"gvisor",`, false},
		{"mixed", `"stack":"mixed",`, false},
		{"system — исполним, выбор сохраняем", `"stack":"system",`, true},
		{"ключа нет — собственный стек sing-tun", ``, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configDir := t.TempDir()
			slot := filepath.Join(configDir, "21-fakeip.json")
			body := `{"inbounds":[{"type":"tun","tag":"tun-in",` + tc.stack +
				`"interface_name":"opkgtun0","mtu":1500,"auto_route":false}]}`
			if err := os.WriteFile(slot, []byte(body), 0644); err != nil {
				t.Fatal(err)
			}

			stripLegacyTunStack(configDir)

			raw, err := os.ReadFile(slot)
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatalf("слот перестал быть JSON: %v: %s", err, raw)
			}
			in := m["inbounds"].([]any)[0].(map[string]any)
			if _, ok := in["stack"]; ok != tc.want {
				t.Errorf("stack present = %v, want %v: %s", ok, tc.want, raw)
			}
			// Остальные поля инбаунда переживают правку — иначе «починка»
			// унесла бы интерфейс или MTU вместе со стеком.
			if in["interface_name"] != "opkgtun0" || in["mtu"].(float64) != 1500 {
				t.Errorf("поля инбаунда потеряны: %s", raw)
			}
			if in["tag"] != "tun-in" || in["auto_route"] != false {
				t.Errorf("поля инбаунда потеряны: %s", raw)
			}
		})
	}
}

// Ключ принадлежит tun-инбаунду: у соседей по файлу поле с тем же именем
// (чужой инбаунд, outbound) трогать нельзя.
func TestStripLegacyTunStack_TouchesOnlyTunInbounds(t *testing.T) {
	configDir := t.TempDir()
	slot := filepath.Join(configDir, "20-router.json")
	body := `{"inbounds":[
		{"type":"tproxy","tag":"tproxy-in","stack":"gvisor"},
		{"type":"tun","tag":"tun-in","stack":"gvisor"}
	],"outbounds":[{"type":"direct","tag":"direct","stack":"gvisor"}]}`
	if err := os.WriteFile(slot, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}

	stripLegacyTunStack(configDir)

	raw, _ := os.ReadFile(slot)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	ins := m["inbounds"].([]any)
	if _, ok := ins[0].(map[string]any)["stack"]; !ok {
		t.Errorf("stack у tproxy-инбаунда снят, а он не наш: %s", raw)
	}
	if _, ok := ins[1].(map[string]any)["stack"]; ok {
		t.Errorf("stack у tun-инбаунда остался: %s", raw)
	}
	ob := m["outbounds"].([]any)[0].(map[string]any)
	if _, ok := ob["stack"]; !ok {
		t.Errorf("stack у outbound'а снят, а он не наш: %s", raw)
	}
}

// Прогон по своему выходу ничего не меняет, а битый слот не роняет шаг и не
// переписывается: прологу нельзя терять чужой файл на своей ошибке разбора.
func TestStripLegacyTunStack_IdempotentAndSkipsBrokenSlot(t *testing.T) {
	configDir := t.TempDir()
	good := filepath.Join(configDir, "21-fakeip.json")
	if err := os.WriteFile(good, []byte(`{"inbounds":[{"type":"tun","tag":"tun-in","stack":"gvisor"}]}`), 0644); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(configDir, "90-user.json")
	brokenBody := `{"inbounds":[{"type":"tun",`
	if err := os.WriteFile(broken, []byte(brokenBody), 0644); err != nil {
		t.Fatal(err)
	}

	stripLegacyTunStack(configDir)
	first, _ := os.ReadFile(good)
	stripLegacyTunStack(configDir)
	second, _ := os.ReadFile(good)

	if string(first) != string(second) {
		t.Errorf("второй прогон изменил файл:\n1: %s\n2: %s", first, second)
	}
	if raw, _ := os.ReadFile(broken); string(raw) != brokenBody {
		t.Errorf("битый слот переписан: %s", raw)
	}
}

// Сама функция бесполезна, если её не зовут: эти два теста держат ТОЧКИ
// ВЫЗОВА. Без них вырезанный шаг набора и вырезанная строка в preflight
// проходят весь пакет зелёными, а дефект возвращается целиком.
func TestReconcileConfigSteps_StripsLegacyTunStack(t *testing.T) {
	dir := t.TempDir()
	slot := filepath.Join(dir, "config.d", "21-fakeip.json")
	writeFixtureJSON(t, slot, map[string]any{
		"inbounds": []any{
			map[string]any{"type": "tun", "tag": "tun-in", "stack": "gvisor",
				"interface_name": "opkgtun1", "mtu": float64(1400)},
		},
	})

	runReconcile(t, dir, false)

	in := readJSONMap(t, slot)["inbounds"].([]any)[0].(map[string]any)
	if _, ok := in["stack"]; ok {
		t.Errorf("набор шагов не снял legacy-стек: %v", in)
	}
}

func TestPreflightConfigDir_StripsLegacyTunStack(t *testing.T) {
	configDir := t.TempDir()
	slot := filepath.Join(configDir, "20-router.json")
	body := `{"inbounds":[{"type":"tun","tag":"tun-in","stack":"mixed","interface_name":"opkgtun0"}]}`
	if err := os.WriteFile(slot, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	op := &Operator{
		configPath: configDir,
		validator: &Validator{
			binary: "/nonexistent",
			exec:   func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
		},
	}

	if err := op.preflightConfigDir(); err != nil {
		t.Fatalf("preflightConfigDir: %v", err)
	}

	raw, _ := os.ReadFile(slot)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	in := m["inbounds"].([]any)[0].(map[string]any)
	if _, ok := in["stack"]; ok {
		t.Errorf("preflight не снял legacy-стек: %s", raw)
	}
}

// Журнал — единственный свидетель работы пролога, поэтому «стек снят» имеет
// право появиться только после удачной записи: на ro-разделе или ENOSPC
// строка рядом с Warn «write failed» утверждала бы починку, которой не было.
func TestStripLegacyTunStack_NoSuccessLogWhenWriteFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root игнорирует права каталога — запись не упадёт")
	}
	dir := t.TempDir()
	slot := filepath.Join(dir, "21-fakeip.json")
	if err := os.WriteFile(slot, []byte(`{"inbounds":[{"type":"tun","tag":"tun-in","stack":"gvisor"}]}`), 0644); err != nil {
		t.Fatal(err)
	}
	// Временный файл atomic-записи рождается в том же каталоге — ro-каталог
	// роняет запись, оставляя слот нетронутым.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	var buf bytes.Buffer
	stripLegacyTunStack(dir, slog.New(slog.NewTextHandler(&buf, nil)))

	out := buf.String()
	if strings.Contains(out, "legacy tun stack removed") {
		t.Errorf("починка объявлена при провалившейся записи: %s", out)
	}
	if !strings.Contains(out, "WARN") || !strings.Contains(out, "write failed") {
		t.Errorf("провал записи не попал в журнал: %s", out)
	}
	raw, _ := os.ReadFile(slot)
	if !strings.Contains(string(raw), `"stack"`) {
		t.Errorf("слот всё-таки переписан: %s", raw)
	}
}
