package deviceproxy

import (
	"path/filepath"
	"testing"
)

func TestStore_LoadMissing_ReturnsDefault(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "deviceproxy.json"))
	got := s.Get()
	if got.Enabled {
		t.Fatalf("default config should not be enabled, got %#v", got)
	}
	if !got.ListenAll {
		t.Fatalf("default config should listen on all interfaces")
	}
	if got.Port != 1099 {
		t.Fatalf("default port = %d, want 1099", got.Port)
	}
}

func TestStore_SaveThenLoad_Roundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deviceproxy.json")

	s1 := NewStore(path)
	cfg := Config{
		Enabled:          true,
		ListenAll:        false,
		ListenInterface:  "Bridge0",
		Port:             1099,
		Auth:             AuthSpec{Enabled: true, Username: "u", Password: "p"},
		SelectedOutbound: "awg-abc",
	}
	if err := s1.Save(cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	s2 := NewStore(path)
	got := s2.Get()
	if got != cfg {
		t.Fatalf("roundtrip mismatch:\n got = %#v\nwant = %#v", got, cfg)
	}
}
