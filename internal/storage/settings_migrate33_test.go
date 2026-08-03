package storage

import (
	"slices"
	"testing"
)

// migrateToV33: пресет keendns включается по умолчанию (#490), если его ещё нет.

func TestMigrateToV33_AddsKeenDNSWhenMissing(t *testing.T) {
	s := loadFrom(t, `{"schemaVersion":32,"singboxRouter":{"enabled":true,"deviceMode":"policy","bypassPresets":["l2tp"]}}`)
	if !slices.Equal(s.SingboxRouter.BypassPresets, []string{"l2tp", "keendns"}) {
		t.Errorf("BypassPresets = %v, want [l2tp keendns]", s.SingboxRouter.BypassPresets)
	}
	if s.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", s.SchemaVersion, CurrentSchemaVersion)
	}
}

func TestMigrateToV33_EmptyPresetsGetsKeenDNS(t *testing.T) {
	s := loadFrom(t, `{"schemaVersion":32,"singboxRouter":{"enabled":true}}`)
	if !slices.Equal(s.SingboxRouter.BypassPresets, []string{"keendns"}) {
		t.Errorf("BypassPresets = %v, want [keendns]", s.SingboxRouter.BypassPresets)
	}
}

func TestMigrateToV33_KeepsExistingKeenDNS(t *testing.T) {
	s := loadFrom(t, `{"schemaVersion":32,"singboxRouter":{"bypassPresets":["keendns","ntp"]}}`)
	if !slices.Equal(s.SingboxRouter.BypassPresets, []string{"keendns", "ntp"}) {
		t.Errorf("BypassPresets = %v, want [keendns ntp] (no duplicate)", s.SingboxRouter.BypassPresets)
	}
}
