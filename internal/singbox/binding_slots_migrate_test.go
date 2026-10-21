package singbox

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if string(got) != want {
		t.Errorf("%s = %s, want %s", path, got, want)
	}
}

func assertAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("%s still exists (err=%v)", path, err)
	}
}

func TestMigrateBindingSlotOrder_RenamesActiveAndDisabled(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "30-deviceproxy.json"), `{"dp":1}`)
	writeFile(t, filepath.Join(dir, "disabled", "40-subscriptions.json"), `{"sub":1}`)

	migrated, err := MigrateBindingSlotOrder(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !migrated {
		t.Error("migrated = false, want true")
	}
	assertFile(t, filepath.Join(dir, "12-deviceproxy.json"), `{"dp":1}`)
	assertFile(t, filepath.Join(dir, "disabled", "13-subscriptions.json"), `{"sub":1}`)
	assertAbsent(t, filepath.Join(dir, "30-deviceproxy.json"))
	assertAbsent(t, filepath.Join(dir, "disabled", "40-subscriptions.json"))
}

// Старое имя рядом с новым бывает только после даунгрейда и повторного
// апгрейда: старая версия писала в старое имя, значит оно свежее.
func TestMigrateBindingSlotOrder_OldNameWinsOverStaleNew(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "40-subscriptions.json"), `{"fresh":1}`)
	writeFile(t, filepath.Join(dir, "13-subscriptions.json"), `{"stale":1}`)

	if _, err := MigrateBindingSlotOrder(dir); err != nil {
		t.Fatal(err)
	}
	assertFile(t, filepath.Join(dir, "13-subscriptions.json"), `{"fresh":1}`)
	assertAbsent(t, filepath.Join(dir, "40-subscriptions.json"))
}

func TestMigrateBindingSlotOrder_NoOp(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "12-deviceproxy.json"), `{}`)

	migrated, err := MigrateBindingSlotOrder(dir)
	if err != nil {
		t.Fatal(err)
	}
	if migrated {
		t.Error("migrated = true, want false: мигрировать нечего")
	}
	assertFile(t, filepath.Join(dir, "12-deviceproxy.json"), `{}`)
}

// Переименование обязано случиться внутри NewOperator, до шагов примирения:
// stepOutboundCompat читает слот подписок уже по новому имени.
func TestNewOperator_MigratesBindingSlotsBeforeReconcile(t *testing.T) {
	dir := t.TempDir()
	cd := filepath.Join(dir, "config.d")
	// naive без udp_over_tcp лечит stepOutboundCompat — след того, что шаг
	// увидел слот уже под новым именем.
	writeFile(t, filepath.Join(cd, "40-subscriptions.json"),
		`{"outbounds":[{"type":"naive","tag":"nv","server":"s","server_port":443}]}`)

	op := NewOperator(OperatorDeps{Dir: dir, Binary: filepath.Join(dir, "sing-box")})

	if !op.BindingSlotsMigrated() {
		t.Error("BindingSlotsMigrated() = false, want true")
	}
	assertAbsent(t, filepath.Join(cd, "40-subscriptions.json"))
	m := readJSONMap(t, filepath.Join(cd, "13-subscriptions.json"))
	if m == nil {
		t.Fatal("13-subscriptions.json missing")
	}
	if _, ok := findOutboundByTag(t, m, "nv")["udp_over_tcp"]; !ok {
		t.Error("naive не пропатчен: stepOutboundCompat прошёл до переименования")
	}
}
