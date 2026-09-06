package storage

import "testing"

func TestMcpEnabled_DefaultFalse(t *testing.T) {
	store := NewSettingsStore(t.TempDir())
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if store.IsMcpEnabled() {
		t.Fatal("fresh install: IsMcpEnabled() = true, want false")
	}
}

func TestMcpEnabled_PatchRoundTrip(t *testing.T) {
	store := NewSettingsStore(t.TempDir())
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	on := true
	if err := store.Update(func(s *Settings) error {
		ApplyPatch(s, &SettingsPatch{McpEnabled: &on})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !store.IsMcpEnabled() {
		t.Fatal("after patch: IsMcpEnabled() = false, want true")
	}
	// Reload from disk: the flag must persist.
	reloaded := NewSettingsStore(store.DataDir())
	if _, err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	if !reloaded.IsMcpEnabled() {
		t.Fatal("after reload: IsMcpEnabled() = false, want true")
	}
}
