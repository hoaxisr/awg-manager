package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	if settings.PingCheck.Defaults.Target != DefaultPingCheckTarget {
		t.Errorf("PingCheck.Defaults.Target = %s, want %s", settings.PingCheck.Defaults.Target, DefaultPingCheckTarget)
	}
	if settings.PingCheck.Defaults.FailThreshold != 3 {
		t.Errorf("PingCheck.Defaults.FailThreshold = %d, want 3", settings.PingCheck.Defaults.FailThreshold)
	}
	if settings.ConnectivityCheckURL != DefaultConnectivityCheckURL {
		t.Errorf("ConnectivityCheckURL = %q, want %q", settings.ConnectivityCheckURL, DefaultConnectivityCheckURL)
	}
	if settings.SingboxRouter.DeviceMode != "policy" {
		t.Errorf("SingboxRouter.DeviceMode = %q, want policy", settings.SingboxRouter.DeviceMode)
	}
	if !settings.SingboxRouter.SnifferEnabled {
		t.Error("SingboxRouter.SnifferEnabled = false, want true")
	}
	if settings.Download.RouteTag != "direct" {
		t.Errorf("Download.RouteTag = %q, want direct", settings.Download.RouteTag)
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
	if settings.SingboxRouter.DeviceMode != "policy" {
		t.Errorf("SingboxRouter.DeviceMode = %q, want policy", settings.SingboxRouter.DeviceMode)
	}
	if !settings.SingboxRouter.SnifferEnabled {
		t.Error("SingboxRouter.SnifferEnabled = false, want true")
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

func TestSettings_MigrateLegacyManagedServer(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "settings.json")
	// Seed with legacy singular shape.
	legacy := `{
        "schemaVersion": 13,
        "managedServer": {"interfaceName":"Wireguard4","address":"10.66.66.1","mask":"255.255.255.0","listenPort":51820,"policy":"none","peers":[]}
    }`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	store := NewSettingsStore(tmpDir)
	if _, err := store.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	servers := store.GetManagedServers()
	if len(servers) != 1 {
		t.Fatalf("expected 1 migrated server, got %d", len(servers))
	}
	if servers[0].InterfaceName != "Wireguard4" {
		t.Errorf("interface mismatch: %s", servers[0].InterfaceName)
	}

	// Force a save and re-read raw bytes — legacy field must be gone.
	if err := store.SaveManagedServers(servers); err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), `"managedServer":`) {
		t.Errorf("legacy field not cleared:\n%s", raw)
	}
	if !strings.Contains(string(raw), `"managedServers":`) {
		t.Errorf("new field not written")
	}
}

func TestSettingsStore_LoadDedupesManagedServers(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "settings.json")
	// settings.json with three duplicate Wireguard4 entries (reproduces
	// the on-disk state observed on a real user's router).
	corrupt := `{
        "schemaVersion": 17,
        "managedServers": [
            {"interfaceName":"Wireguard4","address":"10.0.0.1","mask":"255.255.255.0","listenPort":51820,"policy":"none","peers":[]},
            {"interfaceName":"Wireguard4","address":"10.0.0.1","mask":"255.255.255.0","listenPort":51820,"policy":"none","peers":[]},
            {"interfaceName":"Wireguard4","address":"10.0.0.1","mask":"255.255.255.0","listenPort":51820,"policy":"none","peers":[]}
        ]
    }`
	if err := os.WriteFile(path, []byte(corrupt), 0o644); err != nil {
		t.Fatal(err)
	}

	store := NewSettingsStore(tmpDir)
	if _, err := store.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	servers := store.GetManagedServers()
	if len(servers) != 1 {
		t.Fatalf("expected 1 deduped server, got %d", len(servers))
	}
	if servers[0].InterfaceName != "Wireguard4" {
		t.Errorf("interface mismatch: %s", servers[0].InterfaceName)
	}

	// File on disk MUST be rewritten — open it raw and count.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed Settings
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if len(parsed.ManagedServers) != 1 {
		t.Errorf("expected disk to have 1 managed server after Load, got %d", len(parsed.ManagedServers))
	}
}

func TestSettings_MigrateV23toV24_KeepsLegacyNonDirectRouteKindEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "settings.json")
	legacy := `{"schemaVersion":23,"authEnabled":false,"usageLevel":"basic","download":{"routeTag":"sample-route","routeKind":""}}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	store := NewSettingsStore(tmpDir)
	s, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if s.SchemaVersion != CurrentSchemaVersion {
		t.Fatalf("schema = %d, want %d", s.SchemaVersion, CurrentSchemaVersion)
	}
	if s.Download.RouteTag != "sample-route" {
		t.Fatalf("download.routeTag = %q, want sample-route", s.Download.RouteTag)
	}
	if s.Download.RouteKind != "" {
		t.Fatalf("download.routeKind = %q, want empty", s.Download.RouteKind)
	}
}

func TestSettings_MigrateV24toV25_SetsPingTargets(t *testing.T) {
	tmpDir := t.TempDir()
	legacy := `{"schemaVersion":24,"authEnabled":false,"usageLevel":"basic","pingCheck":{"defaults":{"target":"  "}}}`
	if err := os.WriteFile(filepath.Join(tmpDir, "settings.json"), []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	store := NewSettingsStore(tmpDir)
	s, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if s.SchemaVersion != CurrentSchemaVersion {
		t.Fatalf("schema = %d, want %d", s.SchemaVersion, CurrentSchemaVersion)
	}
	if s.PingCheck.Defaults.Target != DefaultPingCheckTarget {
		t.Fatalf("ping target = %q, want %q", s.PingCheck.Defaults.Target, DefaultPingCheckTarget)
	}
	if s.ConnectivityCheckURL != DefaultConnectivityCheckURL {
		t.Fatalf("connectivity URL = %q, want %q", s.ConnectivityCheckURL, DefaultConnectivityCheckURL)
	}
}

func TestSettingsStore_GetManagedServersDedupes(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewSettingsStore(tmpDir)
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	// SaveManagedServers is the only API that lets the slice contain
	// dups (Add rejects). Used here to simulate a corrupted slice
	// reaching the read path without going through Load.
	dup := ManagedServer{InterfaceName: "Wireguard5", Address: "10.0.0.1", Mask: "255.255.255.0", ListenPort: 51820, Policy: "none"}
	if err := store.SaveManagedServers([]ManagedServer{dup, dup, dup}); err != nil {
		t.Fatal(err)
	}
	out := store.GetManagedServers()
	if len(out) != 1 {
		t.Errorf("expected GetManagedServers to dedupe, got %d entries", len(out))
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

	if settings.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", settings.SchemaVersion, CurrentSchemaVersion)
	}
}

func TestLoadFreshInstallSetsBasic(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewSettingsStore(tmpDir)

	settings, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if settings.UsageLevel != UsageLevelBasic {
		t.Errorf("fresh install UsageLevel = %q, want %q", settings.UsageLevel, UsageLevelBasic)
	}
	if settings.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("fresh install SchemaVersion = %d, want %d", settings.SchemaVersion, CurrentSchemaVersion)
	}

	// Verify the value was persisted on disk too.
	data, err := os.ReadFile(filepath.Join(tmpDir, "settings.json"))
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	if !strings.Contains(string(data), `"usageLevel": "basic"`) {
		t.Errorf("settings.json missing basic usageLevel:\n%s", data)
	}
}

func TestLoadUpgradeFromV15SetsAdvanced(t *testing.T) {
	tmpDir := t.TempDir()
	v15 := `{
		"schemaVersion": 15,
		"authEnabled": false,
		"onboardingCompleted": true,
		"server": {"port": 2222, "interface": "br0"},
		"pingCheck": {"enabled": false, "defaults": {"method":"http","target":"8.8.8.8","interval":45,"deadInterval":120,"failThreshold":3}},
		"logging": {"enabled": true, "maxAge": 2},
		"disableMemorySaving": false,
		"updates": {"checkEnabled": true},
		"dnsRoute": {"autoRefreshEnabled": false, "refreshIntervalHours": 0},
		"singboxRouter": {"enabled": false, "policyName": "", "refreshMode": "interval", "refreshIntervalHours": 24}
	}`
	if err := os.WriteFile(filepath.Join(tmpDir, "settings.json"), []byte(v15), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	store := NewSettingsStore(tmpDir)
	settings, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if settings.UsageLevel != UsageLevelAdvanced {
		t.Errorf("upgrade UsageLevel = %q, want %q", settings.UsageLevel, UsageLevelAdvanced)
	}
	if settings.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("upgrade SchemaVersion = %d, want %d", settings.SchemaVersion, CurrentSchemaVersion)
	}

	// Verify legacy field was dropped from the persisted file.
	data, _ := os.ReadFile(filepath.Join(tmpDir, "settings.json"))
	if strings.Contains(string(data), "onboardingCompleted") {
		t.Errorf("legacy onboardingCompleted field still present:\n%s", data)
	}
}

func TestSettings_CreateNDMSProxyForSingbox_DefaultTrue(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewSettingsStore(tmpDir)
	s, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !s.CreateNDMSProxyForSingbox {
		t.Errorf("CreateNDMSProxyForSingbox default = %v, want true (back-compat)", s.CreateNDMSProxyForSingbox)
	}
}

func TestSettings_MigrateV19toV20_SetsTrueOnExistingInstall(t *testing.T) {
	tmpDir := t.TempDir()
	// Write a v19 settings file without the new field.
	legacy := `{"schemaVersion":19,"authEnabled":false,"usageLevel":"basic"}`
	if err := os.WriteFile(filepath.Join(tmpDir, "settings.json"), []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	store := NewSettingsStore(tmpDir)
	s, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if s.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("schema = %d, want %d", s.SchemaVersion, CurrentSchemaVersion)
	}
	if !s.CreateNDMSProxyForSingbox {
		t.Errorf("migration should set CreateNDMSProxyForSingbox=true for existing installs (back-compat)")
	}
}

func TestSettings_MigrateV20toV21_SetsSingboxLogLevel(t *testing.T) {
	tmpDir := t.TempDir()
	legacy := `{"schemaVersion":20,"authEnabled":false,"usageLevel":"basic","logging":{"enabled":true,"maxAge":2,"logLevel":"info"}}`
	if err := os.WriteFile(filepath.Join(tmpDir, "settings.json"), []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	store := NewSettingsStore(tmpDir)
	s, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if s.SchemaVersion != CurrentSchemaVersion {
		t.Fatalf("schema = %d, want %d", s.SchemaVersion, CurrentSchemaVersion)
	}
	if s.Logging.SingboxLogLevel != DefaultSingboxLogLevel {
		t.Fatalf("singboxLogLevel = %q, want %q", s.Logging.SingboxLogLevel, DefaultSingboxLogLevel)
	}
}

func TestSettings_MigrateV21toV22_SetsDownloadRouteTag(t *testing.T) {
	tmpDir := t.TempDir()
	legacy := `{"schemaVersion":21,"authEnabled":false,"usageLevel":"basic","download":{"routeTag":""}}`
	if err := os.WriteFile(filepath.Join(tmpDir, "settings.json"), []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	store := NewSettingsStore(tmpDir)
	s, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if s.SchemaVersion != CurrentSchemaVersion {
		t.Fatalf("schema = %d, want %d", s.SchemaVersion, CurrentSchemaVersion)
	}
	if s.Download.RouteTag != "direct" {
		t.Fatalf("download.routeTag = %q, want direct", s.Download.RouteTag)
	}
}

func TestSettings_MigrateV23toV24_NormalizesDownloadRouteKind(t *testing.T) {
	tmpDir := t.TempDir()
	legacy := `{"schemaVersion":23,"authEnabled":false,"usageLevel":"basic","download":{"routeTag":"  ","routeKind":""}}`
	if err := os.WriteFile(filepath.Join(tmpDir, "settings.json"), []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	store := NewSettingsStore(tmpDir)
	s, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if s.SchemaVersion != CurrentSchemaVersion {
		t.Fatalf("schema = %d, want %d", s.SchemaVersion, CurrentSchemaVersion)
	}
	if s.Download.RouteTag != "direct" {
		t.Fatalf("download.routeTag = %q, want direct", s.Download.RouteTag)
	}
	if s.Download.RouteKind != "direct" {
		t.Fatalf("download.routeKind = %q, want direct", s.Download.RouteKind)
	}
}

func TestSettingsStore_GetSingboxLogLevel_Normalized(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewSettingsStore(tmpDir)
	s, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	s.Logging.SingboxLogLevel = " WARN "
	if err := store.Save(s); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := store.GetSingboxLogLevel(); got != "warn" {
		t.Fatalf("GetSingboxLogLevel() = %q, want warn", got)
	}

	s.Logging.SingboxLogLevel = "verbose"
	if err := store.Save(s); err != nil {
		t.Fatalf("save invalid: %v", err)
	}
	if got := store.GetSingboxLogLevel(); got != DefaultSingboxLogLevel {
		t.Fatalf("GetSingboxLogLevel() invalid fallback = %q, want %q", got, DefaultSingboxLogLevel)
	}
}

func TestNormalizeUsageLevel(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"basic", "basic"},
		{"advanced", "advanced"},
		{"expert", "expert"},
		{"", "advanced"},
		{"garbage", "advanced"},
		{"BASIC", "advanced"}, // case-sensitive
	}
	for _, tc := range tests {
		got := NormalizeUsageLevel(tc.in)
		if got != tc.want {
			t.Errorf("NormalizeUsageLevel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDefaultSettingsChannelIsStable(t *testing.T) {
	s := (&SettingsStore{}).defaultSettings()
	if s.Updates.Channel != "stable" {
		t.Errorf("default Updates.Channel = %q, want \"stable\"", s.Updates.Channel)
	}
}

func TestMigrateToV23SetsStableChannel(t *testing.T) {
	var s Settings
	s.SchemaVersion = 22
	(&SettingsStore{}).migrateToV23(&s)
	if s.Updates.Channel != "stable" {
		t.Errorf("after migrateToV23 Channel = %q, want \"stable\"", s.Updates.Channel)
	}
	if s.SchemaVersion != 23 {
		t.Errorf("SchemaVersion = %d, want 23", s.SchemaVersion)
	}
}

func TestSettingsStore_SetSingboxCreateNDMSProxy_PersistsAtomic(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewSettingsStore(tmpDir)
	if _, err := store.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := store.SetSingboxCreateNDMSProxy(false); err != nil {
		t.Fatalf("set: %v", err)
	}
	// Re-open from disk to confirm persistence.
	fresh := NewSettingsStore(tmpDir)
	s, err := fresh.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if s.CreateNDMSProxyForSingbox {
		t.Errorf("persisted = %v, want false", s.CreateNDMSProxyForSingbox)
	}
	// Toggle back.
	if err := fresh.SetSingboxCreateNDMSProxy(true); err != nil {
		t.Fatalf("set back: %v", err)
	}
	if !fresh.IsSingboxNDMSProxyEnabled() {
		t.Errorf("getter sees = false after set true")
	}
}
