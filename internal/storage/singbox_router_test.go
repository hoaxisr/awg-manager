package storage

import (
	"encoding/json"
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

// TestSingboxRouterSettings_FakeIPFieldsRoundTrip ensures the backend-managed
// fakeip-tun persist fields survive a JSON marshal/unmarshal cycle, including
// the provisioned-with-index-0 case (FakeIPProvisioned is the validity gate,
// so omitempty on the int yielding 0 on read-back is correct).
func TestSingboxRouterSettings_FakeIPFieldsRoundTrip(t *testing.T) {
	for _, tc := range []SingboxRouterSettings{
		{
			FakeIPProvisioned: true,
			FakeIPIndex:       5,
			FakeIPInet4Range:  "10.128.0.0/10",
			FakeIPInet6Range:  "3f80::/10",
		},
		{
			FakeIPProvisioned: true,
			FakeIPIndex:       0, // valid index; omitempty drops it, reads back as 0
		},
	} {
		b, err := json.Marshal(tc)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var got SingboxRouterSettings
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got.FakeIPProvisioned != tc.FakeIPProvisioned ||
			got.FakeIPIndex != tc.FakeIPIndex ||
			got.FakeIPInet4Range != tc.FakeIPInet4Range ||
			got.FakeIPInet6Range != tc.FakeIPInet6Range {
			t.Errorf("round-trip mismatch:\n got  %+v\n want %+v", got, tc)
		}
	}
}
