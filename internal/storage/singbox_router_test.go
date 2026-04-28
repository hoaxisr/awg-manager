package storage

import (
	"testing"
)

func TestSettingsDefaultsContainSingboxRouter(t *testing.T) {
	dir := t.TempDir()
	store := NewSettingsStore(dir)
	s, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if s.SingboxRouter.Enabled {
		t.Error("default Enabled should be false")
	}
	if s.SingboxRouter.PolicyName != "" {
		t.Errorf("default PolicyName should be empty, got %q", s.SingboxRouter.PolicyName)
	}
	if s.SingboxRouter.RefreshMode != "interval" {
		t.Errorf("default RefreshMode: %q", s.SingboxRouter.RefreshMode)
	}
	if s.SingboxRouter.RefreshInterval != 24 {
		t.Errorf("default RefreshInterval: %d", s.SingboxRouter.RefreshInterval)
	}
}

func TestMigrateToV15_ClearsDeprecated(t *testing.T) {
	s := &SettingsStore{}
	settings := &Settings{
		SchemaVersion: 14,
		SingboxRouter: SingboxRouterSettings{
			Enabled:    true,
			PolicyName: "",
		},
	}
	s.migrateToV15(settings)
	if settings.SchemaVersion != 15 {
		t.Errorf("want SchemaVersion 15, got %d", settings.SchemaVersion)
	}
	if settings.SingboxRouter.Enabled {
		t.Error("expected Enabled to be force-cleared to false")
	}
	if settings.SingboxRouter.PolicyName != "" {
		t.Errorf("expected PolicyName empty, got %q", settings.SingboxRouter.PolicyName)
	}
}
