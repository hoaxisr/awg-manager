package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func loadFrom(t *testing.T, raw string) *Settings {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
	settings, err := NewSettingsStore(dir).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return settings
}

// migrateToV32: "0.0.0.0" — не имя интерфейса (#571, вписан руками ради
// «слушать всё»); нормализуется в пустой список = все интерфейсы.

// Пользователь уже на схеме 30/31 с ["0.0.0.0"] после V30.
func TestMigrateToV32_NormalizesWildcardInInterfaces(t *testing.T) {
	s := loadFrom(t, `{"schemaVersion":31,"server":{"port":2222,"interface":"0.0.0.0","interfaces":["0.0.0.0"]}}`)
	if len(s.Server.Interfaces) != 0 {
		t.Errorf("Interfaces = %v, want empty (bind all)", s.Server.Interfaces)
	}
	if s.Server.Interface != "" {
		t.Errorf("legacy Interface = %q, want \"\" (downgrade: пусто = 0.0.0.0)", s.Server.Interface)
	}
	if s.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", s.SchemaVersion, CurrentSchemaVersion)
	}
}

// Апгрейд напрямую с 2.15.x: V30 поднимает легаси "0.0.0.0" в список, V32
// тут же нормализует — на диск мусор не попадает.
func TestMigrateToV32_LegacyWildcardFrom29(t *testing.T) {
	s := loadFrom(t, `{"schemaVersion":29,"server":{"port":2222,"interface":"0.0.0.0"}}`)
	if len(s.Server.Interfaces) != 0 {
		t.Errorf("Interfaces = %v, want empty (bind all)", s.Server.Interfaces)
	}
	if s.Server.Interface != "" {
		t.Errorf("legacy Interface = %q, want \"\"", s.Server.Interface)
	}
}

// Смесь: wildcard выкидывается, настоящие интерфейсы остаются.
func TestMigrateToV32_KeepsRealInterfaces(t *testing.T) {
	s := loadFrom(t, `{"schemaVersion":31,"server":{"port":2222,"interface":"br0","interfaces":["br0","0.0.0.0"]}}`)
	if len(s.Server.Interfaces) != 1 || s.Server.Interfaces[0] != "br0" {
		t.Errorf("Interfaces = %v, want [br0]", s.Server.Interfaces)
	}
	if s.Server.Interface != "br0" {
		t.Errorf("legacy Interface = %q, want br0 (не тронут)", s.Server.Interface)
	}
}
