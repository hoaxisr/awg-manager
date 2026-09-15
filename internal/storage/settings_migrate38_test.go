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
	for _, stack := range []string{"system", "mixed"} {
		s := loadFrom(t, `{"schemaVersion":37,"singboxRouter":{"fakeipStack":"`+stack+`"}}`)
		if s.SingboxRouter.FakeIPStack != stack {
			t.Errorf("FakeIPStack = %q, want %q (осознанный выбор не трогаем)", s.SingboxRouter.FakeIPStack, stack)
		}
	}
}
