package storage

import "testing"

// migrateToV38: вшитый дефолт "gvisor" снимается, явный выбор остаётся.

func TestMigrateToV38_ClearsDefaultGvisorStack(t *testing.T) {
	s := loadFrom(t, `{"schemaVersion":37,"singboxRouter":{"enabled":true,"fakeipStack":"gvisor"}}`)
	if s.SingboxRouter.FakeIPStack != "" {
		t.Errorf("FakeIPStack = %q, want \"\" (собственный стек sing-tun)", s.SingboxRouter.FakeIPStack)
	}
	if s.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", s.SchemaVersion, CurrentSchemaVersion)
	}
}

func TestMigrateToV38_KeepsExplicitChoice(t *testing.T) {
	s := loadFrom(t, `{"schemaVersion":37,"singboxRouter":{"fakeipStack":"system"}}`)
	if s.SingboxRouter.FakeIPStack != "system" {
		t.Errorf("FakeIPStack = %q, want \"system\" (осознанный выбор не трогаем)", s.SingboxRouter.FakeIPStack)
	}
}

// migrateToV39: стеки на gVisor снимаются даже как осознанный выбор — бинарь
// собран без with_gvisor и такой конфиг не поднимется.

func TestMigrateToV39_ClearsGvisorStacks(t *testing.T) {
	for _, stack := range []string{"gvisor", "mixed"} {
		s := loadFrom(t, `{"schemaVersion":38,"singboxRouter":{"fakeipStack":"`+stack+`"}}`)
		if s.SingboxRouter.FakeIPStack != "" {
			t.Errorf("FakeIPStack = %q, want \"\" (движок без gVisor)", s.SingboxRouter.FakeIPStack)
		}
		if s.SchemaVersion != CurrentSchemaVersion {
			t.Errorf("SchemaVersion = %d, want %d", s.SchemaVersion, CurrentSchemaVersion)
		}
	}
}
