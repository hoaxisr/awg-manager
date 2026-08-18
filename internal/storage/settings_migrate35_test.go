package storage

import "testing"

// migrateToV35: хранимое "" до сих пор означало «дефолт» (нормализация
// дефолтила его на каждом чтении) — материализуем явно, чтобы смена семантики
// "" → «v6 выключен» не выключила v6 молча ни у кого при обновлении.
func TestMigrateToV35_EmptyPool6Materialized(t *testing.T) {
	s := &Settings{SchemaVersion: 34}
	(&SettingsStore{}).migrateToV35(s)
	if s.SingboxRouter.FakeIPPool6 != "fc00::/18" {
		t.Fatalf("pool6 = %q, want explicit default", s.SingboxRouter.FakeIPPool6)
	}
	// Пользовательское значение не трогается.
	s2 := &Settings{SchemaVersion: 34}
	s2.SingboxRouter.FakeIPPool6 = "fd00::/18"
	(&SettingsStore{}).migrateToV35(s2)
	if s2.SingboxRouter.FakeIPPool6 != "fd00::/18" {
		t.Fatalf("custom pool6 clobbered: %q", s2.SingboxRouter.FakeIPPool6)
	}
}
