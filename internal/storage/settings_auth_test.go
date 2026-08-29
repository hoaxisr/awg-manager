package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSettingsMigrationV29_DefaultsSessionTTL(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewSettingsStore(tmpDir)

	v28 := `{"schemaVersion":28,"authEnabled":true,"server":{"port":2222,"interface":"br0"},"pingCheck":{},"logging":{},"updates":{}}`
	if err := os.WriteFile(filepath.Join(tmpDir, "settings.json"), []byte(v28), 0644); err != nil {
		t.Fatal(err)
	}

	settings, err := store.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if settings.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", settings.SchemaVersion, CurrentSchemaVersion)
	}
	if settings.SessionTtlHours != DefaultSessionTTLHours {
		t.Errorf("SessionTtlHours = %d, want %d (historical 24h default)", settings.SessionTtlHours, DefaultSessionTTLHours)
	}
	if settings.EntwareAuthEnabled {
		t.Error("EntwareAuthEnabled = true, want false (upgrade keeps NDMS-only login)")
	}
}

func TestSettingsMigrationV29_PreservesExplicitValue(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewSettingsStore(tmpDir)

	// A file that (somehow) already carries a value must not be reset.
	v28 := `{"schemaVersion":28,"sessionTtlHours":72,"entwareAuthEnabled":true,"server":{"port":2222,"interface":"br0"},"pingCheck":{},"logging":{},"updates":{}}`
	if err := os.WriteFile(filepath.Join(tmpDir, "settings.json"), []byte(v28), 0644); err != nil {
		t.Fatal(err)
	}

	settings, err := store.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if settings.SessionTtlHours != 72 {
		t.Errorf("SessionTtlHours = %d, want 72 (preserved)", settings.SessionTtlHours)
	}
	if !settings.EntwareAuthEnabled {
		t.Error("EntwareAuthEnabled = false, want true (preserved)")
	}
}

// TestSettingsLoad_SelfHealsZeroSessionTTL covers the rollback→re-upgrade
// hole (R1): a downgrade can rewrite settings.json AT the current
// schemaVersion but without sessionTtlHours, leaving a stored 0 that
// migrateToV29 never revisits (it only runs below v29). Load must
// unconditionally heal a non-positive value to the default and persist it, so
// the effective and on-disk values converge — otherwise every later settings
// save trips the TTL validation.
func TestSettingsLoad_SelfHealsZeroSessionTTL(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "settings.json")

	// Already at CurrentSchemaVersion but sessionTtlHours is 0 (field absent
	// when a downgrade rewrote the file).
	raw := `{"schemaVersion":29,"authEnabled":true,"sessionTtlHours":0,"server":{"port":2222,"interface":"br0"},"pingCheck":{},"logging":{},"updates":{}}`
	if err := os.WriteFile(path, []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}

	store := NewSettingsStore(tmpDir)
	settings, err := store.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if settings.SessionTtlHours != DefaultSessionTTLHours {
		t.Fatalf("in-memory SessionTtlHours = %d, want %d (self-healed)", settings.SessionTtlHours, DefaultSessionTTLHours)
	}

	// The heal must be persisted (needsSave), so a fresh store reads 24 too.
	reloaded, err := NewSettingsStore(tmpDir).Load()
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if reloaded.SessionTtlHours != DefaultSessionTTLHours {
		t.Fatalf("persisted SessionTtlHours = %d, want %d (heal not written to disk)", reloaded.SessionTtlHours, DefaultSessionTTLHours)
	}
}

// Out-of-range values (hand-edited or corrupt file already at the current
// schema version) must also self-heal: a negative TTL would brick saves and
// an over-range one would let sessions outlive the documented 720h cap.
func TestSettingsLoad_SelfHealsOutOfRangeSessionTTL(t *testing.T) {
	for _, bad := range []int{-5, MaxSessionTTLHours + 1, 1000000} {
		tmpDir := t.TempDir()
		raw := fmt.Sprintf(`{"schemaVersion":29,"authEnabled":true,"sessionTtlHours":%d,"server":{"port":2222,"interface":"br0"},"pingCheck":{},"logging":{},"updates":{}}`, bad)
		if err := os.WriteFile(filepath.Join(tmpDir, "settings.json"), []byte(raw), 0644); err != nil {
			t.Fatal(err)
		}
		store := NewSettingsStore(tmpDir)
		settings, err := store.Load()
		if err != nil {
			t.Fatalf("Load(%d) failed: %v", bad, err)
		}
		if settings.SessionTtlHours != DefaultSessionTTLHours {
			t.Errorf("SessionTtlHours = %d after loading %d, want %d (self-healed)", settings.SessionTtlHours, bad, DefaultSessionTTLHours)
		}
		if got := store.GetSessionTTL(); got != DefaultSessionTTLHours*time.Hour {
			t.Errorf("GetSessionTTL() = %v after loading %d, want %v", got, bad, DefaultSessionTTLHours*time.Hour)
		}
	}
}

func TestSettingsFreshInstall_SessionTTLDefault(t *testing.T) {
	store := NewSettingsStore(t.TempDir())
	settings, err := store.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if settings.SessionTtlHours != DefaultSessionTTLHours {
		t.Errorf("fresh install SessionTtlHours = %d, want %d", settings.SessionTtlHours, DefaultSessionTTLHours)
	}
	if settings.EntwareAuthEnabled {
		t.Error("fresh install EntwareAuthEnabled = true, want false")
	}
}

func TestGetSessionTTL(t *testing.T) {
	store := NewSettingsStore(t.TempDir())
	settings, err := store.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if got := store.GetSessionTTL(); got != 24*time.Hour {
		t.Errorf("GetSessionTTL() = %v, want 24h", got)
	}

	settings.SessionTtlHours = 3
	if err := store.save(settings); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	if got := store.GetSessionTTL(); got != 3*time.Hour {
		t.Errorf("GetSessionTTL() = %v, want 3h", got)
	}

	// Zero/invalid stored value falls back to the default.
	settings.SessionTtlHours = 0
	if err := store.save(settings); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	if got := store.GetSessionTTL(); got != 24*time.Hour {
		t.Errorf("GetSessionTTL() with zero value = %v, want 24h fallback", got)
	}
}

func TestIsEntwareAuthEnabled(t *testing.T) {
	store := NewSettingsStore(t.TempDir())
	settings, err := store.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if store.IsEntwareAuthEnabled() {
		t.Error("IsEntwareAuthEnabled() = true on defaults, want false")
	}
	settings.EntwareAuthEnabled = true
	if err := store.save(settings); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	if !store.IsEntwareAuthEnabled() {
		t.Error("IsEntwareAuthEnabled() = false after enabling, want true")
	}
}
