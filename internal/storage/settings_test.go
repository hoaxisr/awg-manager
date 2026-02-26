package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSettingsStore_LoadDefault(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewSettingsStore(tmpDir)

	settings, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Check default values
	if settings.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", settings.SchemaVersion, CurrentSchemaVersion)
	}
	if settings.AuthEnabled {
		t.Error("AuthEnabled = true, want false")
	}
	if settings.Server.Port != DefaultPort {
		t.Errorf("Server.Port = %d, want %d", settings.Server.Port, DefaultPort)
	}
	if settings.Server.Interface != DefaultInterface {
		t.Errorf("Server.Interface = %s, want %s", settings.Server.Interface, DefaultInterface)
	}
	if settings.PingCheck.Enabled {
		t.Error("PingCheck.Enabled = true, want false")
	}
	if settings.PingCheck.Defaults.Method != "http" {
		t.Errorf("PingCheck.Defaults.Method = %s, want http", settings.PingCheck.Defaults.Method)
	}
	if settings.PingCheck.Defaults.FailThreshold != 3 {
		t.Errorf("PingCheck.Defaults.FailThreshold = %d, want 3", settings.PingCheck.Defaults.FailThreshold)
	}
}

func TestSettingsStore_MigrateFromV1(t *testing.T) {
	tmpDir := t.TempDir()

	// Create v1 settings file (without pingCheck and server)
	v1Settings := map[string]interface{}{
		"schemaVersion": 1,
		"authEnabled":   false,
	}
	data, _ := json.Marshal(v1Settings)
	os.WriteFile(filepath.Join(tmpDir, "settings.json"), data, 0644)

	store := NewSettingsStore(tmpDir)
	settings, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Should be migrated to v2
	if settings.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", settings.SchemaVersion, CurrentSchemaVersion)
	}

	// Original value should be preserved
	if settings.AuthEnabled {
		t.Error("AuthEnabled = true, want false (preserved from v1)")
	}

	// New fields should have defaults
	if settings.Server.Port != DefaultPort {
		t.Errorf("Server.Port = %d, want %d", settings.Server.Port, DefaultPort)
	}
	if settings.PingCheck.Defaults.Method != "http" {
		t.Errorf("PingCheck.Defaults.Method = %s, want http", settings.PingCheck.Defaults.Method)
	}
}

func TestSettingsStore_MigratePortFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create old port file
	os.WriteFile(filepath.Join(tmpDir, "port"), []byte("8888"), 0644)

	store := NewSettingsStore(tmpDir)
	settings, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Port should be read from port file
	if settings.Server.Port != 8888 {
		t.Errorf("Server.Port = %d, want 8888 (from port file)", settings.Server.Port)
	}

	// Port file should be removed
	if _, err := os.Stat(filepath.Join(tmpDir, "port")); !os.IsNotExist(err) {
		t.Error("Port file should be removed after migration")
	}
}

func TestSettingsStore_SaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewSettingsStore(tmpDir)

	// Load defaults
	settings, _ := store.Load()

	// Modify and save
	settings.PingCheck.Enabled = true
	settings.PingCheck.Defaults.Interval = 60
	settings.Server.Port = 3333

	if err := store.Save(settings); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Create new store and load
	store2 := NewSettingsStore(tmpDir)
	loaded, err := store2.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Check values persisted
	if !loaded.PingCheck.Enabled {
		t.Error("PingCheck.Enabled = false, want true")
	}
	if loaded.PingCheck.Defaults.Interval != 60 {
		t.Errorf("PingCheck.Defaults.Interval = %d, want 60", loaded.PingCheck.Defaults.Interval)
	}
	if loaded.Server.Port != 3333 {
		t.Errorf("Server.Port = %d, want 3333", loaded.Server.Port)
	}
}

func TestSettingsStore_DisableMemorySaving(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewSettingsStore(tmpDir)

	// Load defaults
	settings, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Default should be false (auto mode)
	if settings.DisableMemorySaving {
		t.Error("DisableMemorySaving = true, want false (default)")
	}

	// Toggle and save
	settings.DisableMemorySaving = true
	if err := store.Save(settings); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Reload and verify
	store2 := NewSettingsStore(tmpDir)
	loaded, err := store2.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if !loaded.DisableMemorySaving {
		t.Error("DisableMemorySaving = false, want true (saved)")
	}

	// Test helper method
	if !store2.IsMemorySavingDisabled() {
		t.Error("IsMemorySavingDisabled() = false, want true")
	}
}

func TestSettingsStore_BackwardCompatibility(t *testing.T) {
	tmpDir := t.TempDir()

	// Simulate old settings file without disableMemorySaving field
	oldSettings := `{
		"schemaVersion": 2,
		"authEnabled": true,
		"server": {"port": 2222, "interface": "br0"},
		"pingCheck": {"enabled": false, "defaults": {"method": "http", "target": "8.8.8.8", "interval": 45, "deadInterval": 120, "failThreshold": 3}}
	}`
	os.WriteFile(filepath.Join(tmpDir, "settings.json"), []byte(oldSettings), 0644)

	store := NewSettingsStore(tmpDir)
	settings, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// DisableMemorySaving should default to false when missing
	if settings.DisableMemorySaving {
		t.Error("DisableMemorySaving should be false for old settings files")
	}
}

func TestSettingsMigrationV8_SchemaVersion(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewSettingsStore(tmpDir)

	v7 := `{"schemaVersion":7,"authEnabled":false,"server":{"port":2222,"interface":"br0"},"pingCheck":{},"logging":{},"backendMode":"auto","bootDelaySeconds":0,"updates":{}}`
	if err := os.WriteFile(filepath.Join(tmpDir, "settings.json"), []byte(v7), 0644); err != nil {
		t.Fatal(err)
	}

	settings, err := store.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if settings.SchemaVersion != 8 {
		t.Errorf("SchemaVersion = %d, want 8", settings.SchemaVersion)
	}
	// BackendMode should be preserved (not forced to kernel)
	if settings.BackendMode != "auto" {
		t.Errorf("BackendMode = %q, want auto (preserved)", settings.BackendMode)
	}
}
