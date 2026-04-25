package storage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const (
	CurrentSchemaVersion = 13
	DefaultPort          = 2222
	DefaultInterface     = "br0"
)

// SettingsStore manages application settings.
type SettingsStore struct {
	path     string
	mu       sync.RWMutex
	settings *Settings
}

// NewSettingsStore creates a new settings store.
func NewSettingsStore(dataDir string) *SettingsStore {
	return &SettingsStore{
		path: filepath.Join(dataDir, "settings.json"),
	}
}

// Load reads settings from disk. Returns default settings if file doesn't exist.
func (s *SettingsStore) Load() (*Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			// Return default settings with v2 schema
			s.settings = s.defaultSettings()
			// Try to migrate port from old port file
			s.migratePortFile(s.settings)
			// Save new settings
			if saveErr := s.saveUnlocked(s.settings); saveErr != nil {
				return nil, saveErr
			}
			return s.settings, nil
		}
		return nil, err
	}

	var settings Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, err
	}

	// Migrate if needed
	if settings.SchemaVersion < CurrentSchemaVersion {
		if settings.SchemaVersion < 2 {
			if err := s.migrateToV2(&settings); err != nil {
				return nil, err
			}
		}
		if settings.SchemaVersion < 3 {
			s.migrateToV3(&settings)
		}
		if settings.SchemaVersion < 4 {
			s.migrateToV4(&settings)
		}
		if settings.SchemaVersion < 5 {
			s.migrateToV5(&settings)
		}
		if settings.SchemaVersion < 6 {
			s.migrateToV6(&settings)
		}
		if settings.SchemaVersion < 7 {
			s.migrateToV7(&settings)
		}
		if settings.SchemaVersion < 8 {
			s.migrateToV8(&settings)
		}
		if settings.SchemaVersion < 9 {
			s.migrateToV9(&settings)
		}
		if settings.SchemaVersion < 10 {
			s.migrateToV10(&settings)
		}
		if settings.SchemaVersion < 11 {
			s.migrateToV11(&settings)
		}
		if settings.SchemaVersion < 12 {
			s.migrateToV12(&settings)
		}
		if settings.SchemaVersion < 13 {
			s.migrateToV13(&settings)
		}
		// Save migrated settings
		if err := s.saveUnlocked(&settings); err != nil {
			return nil, err
		}
	}

	s.settings = &settings
	return s.settings, nil
}

// defaultSettings returns settings with default values.
func (s *SettingsStore) defaultSettings() *Settings {
	return &Settings{
		SchemaVersion: CurrentSchemaVersion,
		AuthEnabled:   false,
		Server: ServerSettings{
			Port:      DefaultPort,
			Interface: DefaultInterface,
		},
		PingCheck: PingCheckSettings{
			Enabled: false,
			Defaults: PingCheckDefaults{
				Method:        "http",
				Target:        "8.8.8.8",
				Interval:      45,
				DeadInterval:  120,
				FailThreshold: 3,
			},
		},
		Logging: LoggingSettings{
			Enabled: true,
			MaxAge:  2,
		},
		Updates: UpdateSettings{
			CheckEnabled: true,
		},
	}
}

// migrateToV2 migrates settings from v1 to v2.
func (s *SettingsStore) migrateToV2(settings *Settings) error {
	// Migrate port from old port file
	s.migratePortFile(settings)

	// Set defaults for new fields if not set
	if settings.Server.Port == 0 {
		settings.Server.Port = DefaultPort
	}
	if settings.Server.Interface == "" {
		settings.Server.Interface = DefaultInterface
	}

	// Set PingCheck defaults
	if settings.PingCheck.Defaults.Method == "" {
		settings.PingCheck.Defaults.Method = "http"
	}
	if settings.PingCheck.Defaults.Target == "" {
		settings.PingCheck.Defaults.Target = "8.8.8.8"
	}
	if settings.PingCheck.Defaults.Interval == 0 {
		settings.PingCheck.Defaults.Interval = 45
	}
	if settings.PingCheck.Defaults.DeadInterval == 0 {
		settings.PingCheck.Defaults.DeadInterval = 120
	}
	if settings.PingCheck.Defaults.FailThreshold == 0 {
		settings.PingCheck.Defaults.FailThreshold = 3
	}

	settings.SchemaVersion = 2
	return nil
}

// migrateToV3 migrates settings from v2 to v3.
func (s *SettingsStore) migrateToV3(settings *Settings) {
	// Set Logging defaults
	if settings.Logging.MaxAge == 0 {
		settings.Logging.MaxAge = 2
	}
	// Logging.Enabled defaults to false (zero value)

	settings.SchemaVersion = 3
}

// migrateToV4 migrates settings from v3 to v4.
func (s *SettingsStore) migrateToV4(settings *Settings) {
	// Previously set default BackendMode (removed in v13)
	settings.SchemaVersion = 4
}

// migrateToV5 migrates settings from v4 to v5.
func (s *SettingsStore) migrateToV5(settings *Settings) {
	settings.SchemaVersion = 5
}

// migrateToV6 migrates settings from v5 to v6.
func (s *SettingsStore) migrateToV6(settings *Settings) {
	// Enable update checks by default
	settings.Updates.CheckEnabled = true
	settings.SchemaVersion = 6
}

// migrateToV7 migrates settings from v6 to v7.
func (s *SettingsStore) migrateToV7(settings *Settings) {
	// OnboardingCompleted defaults to false (zero value) — no action needed
	settings.SchemaVersion = 7
}

// migrateToV8 migrates settings from v7 to v8.
func (s *SettingsStore) migrateToV8(settings *Settings) {
	// v8 added ExcludedWANs (later removed) — bump version only
	settings.SchemaVersion = 8
}

// migrateToV9 migrates settings from v8 to v9.
func (s *SettingsStore) migrateToV9(settings *Settings) {
	// DNSRouteSettings zero value (disabled, interval 0) is correct default
	settings.SchemaVersion = 9
}

// migrateToV10 migrates settings from v9 to v10.
func (s *SettingsStore) migrateToV10(settings *Settings) {
	// HiddenSystemTunnels zero value (nil) is correct default
	settings.SchemaVersion = 10
}

// migrateToV11 migrates settings from v10 to v11.
func (s *SettingsStore) migrateToV11(settings *Settings) {
	// ServerInterfaces zero value (nil) is correct default
	settings.SchemaVersion = 11
}

// migrateToV12 migrates settings from v11 to v12.
func (s *SettingsStore) migrateToV12(settings *Settings) {
	// ManagedServer zero value (nil) is correct default
	settings.SchemaVersion = 12
}

// migrateToV13 removes deprecated BackendMode (now per-tunnel).
func (s *SettingsStore) migrateToV13(settings *Settings) {
	settings.SchemaVersion = 13
}

// GetManagedServer returns a deep copy of the managed server or nil if not created.
func (s *SettingsStore) GetManagedServer() *ManagedServer {
	settings, err := s.Get()
	if err != nil {
		return nil
	}
	if settings.ManagedServer == nil {
		return nil
	}
	// Deep copy to prevent mutation of shared state
	orig := settings.ManagedServer
	cp := *orig
	cp.Peers = make([]ManagedPeer, len(orig.Peers))
	copy(cp.Peers, orig.Peers)
	// Normalize Policy: legacy records (or fresh records before first
	// SetPolicy) carry an empty string; surface as "none" everywhere.
	// File is rewritten with "none" on next mutation, no migration needed.
	if cp.Policy == "" {
		cp.Policy = "none"
	}
	return &cp
}

// SaveManagedServer saves the managed server configuration.
func (s *SettingsStore) SaveManagedServer(server *ManagedServer) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	settings := s.settings
	if settings == nil {
		return fmt.Errorf("settings not loaded")
	}

	settings.ManagedServer = server
	return s.saveUnlocked(settings)
}

// DeleteManagedServer removes the managed server configuration.
func (s *SettingsStore) DeleteManagedServer() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	settings := s.settings
	if settings == nil {
		return fmt.Errorf("settings not loaded")
	}

	settings.ManagedServer = nil
	return s.saveUnlocked(settings)
}

// MarkServerInterface adds an interface ID to the server interfaces list.
func (s *SettingsStore) MarkServerInterface(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	settings := s.settings
	if settings == nil {
		return fmt.Errorf("settings not loaded")
	}

	next, added := appendUnique(settings.ServerInterfaces, id)
	if !added {
		return nil
	}
	settings.ServerInterfaces = next
	return s.saveUnlocked(settings)
}

// UnmarkServerInterface removes an interface ID from the server interfaces list.
func (s *SettingsStore) UnmarkServerInterface(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	settings := s.settings
	if settings == nil {
		return fmt.Errorf("settings not loaded")
	}

	settings.ServerInterfaces = filterOut(settings.ServerInterfaces, id)
	return s.saveUnlocked(settings)
}

// GetServerInterfaces returns the list of server interface IDs.
func (s *SettingsStore) GetServerInterfaces() []string {
	settings, err := s.Get()
	if err != nil {
		return nil
	}
	return settings.ServerInterfaces
}

// IsServerInterface checks if an interface ID is in the server interfaces list.
func (s *SettingsStore) IsServerInterface(id string) bool {
	settings, err := s.Get()
	if err != nil {
		return false
	}
	return contains(settings.ServerInterfaces, id)
}

// IsSystemTunnelHidden checks if a system tunnel ID is in the hidden list.
func (s *SettingsStore) IsSystemTunnelHidden(tunnelID string) bool {
	settings, err := s.Get()
	if err != nil {
		return false
	}
	return contains(settings.HiddenSystemTunnels, tunnelID)
}

// HideSystemTunnel adds a tunnel ID to the hidden list.
func (s *SettingsStore) HideSystemTunnel(tunnelID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	settings := s.settings
	if settings == nil {
		return fmt.Errorf("settings not loaded")
	}

	next, added := appendUnique(settings.HiddenSystemTunnels, tunnelID)
	if !added {
		return nil
	}
	settings.HiddenSystemTunnels = next
	return s.saveUnlocked(settings)
}

// UnhideSystemTunnel removes a tunnel ID from the hidden list.
func (s *SettingsStore) UnhideSystemTunnel(tunnelID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	settings := s.settings
	if settings == nil {
		return fmt.Errorf("settings not loaded")
	}

	settings.HiddenSystemTunnels = filterOut(settings.HiddenSystemTunnels, tunnelID)
	return s.saveUnlocked(settings)
}

// GetHiddenSystemTunnels returns the list of hidden system tunnel IDs.
func (s *SettingsStore) GetHiddenSystemTunnels() []string {
	settings, err := s.Get()
	if err != nil {
		return nil
	}
	return settings.HiddenSystemTunnels
}

// migratePortFile reads port from old port file and removes it.
func (s *SettingsStore) migratePortFile(settings *Settings) {
	portFile := filepath.Join(filepath.Dir(s.path), "port")
	data, err := os.ReadFile(portFile)
	if err != nil {
		return // No port file, use default
	}

	portStr := strings.TrimSpace(string(data))
	if port, err := strconv.Atoi(portStr); err == nil && port > 0 && port <= 65535 {
		settings.Server.Port = port
	}

	// Remove old port file after successful migration
	os.Remove(portFile)
}

// Save writes settings to disk.
func (s *SettingsStore) Save(settings *Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveUnlocked(settings)
}

// saveUnlocked writes settings to disk without acquiring lock.
// Caller must hold the lock.
func (s *SettingsStore) saveUnlocked(settings *Settings) error {
	settings.SchemaVersion = CurrentSchemaVersion

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(settings); err != nil {
		return err
	}

	s.settings = settings
	return AtomicWrite(s.path, buf.Bytes())
}

// Get returns cached settings or loads from disk.
func (s *SettingsStore) Get() (*Settings, error) {
	s.mu.RLock()
	if s.settings != nil {
		defer s.mu.RUnlock()
		return s.settings, nil
	}
	s.mu.RUnlock()

	return s.Load()
}

// IsAuthEnabled returns whether authentication is enabled.
func (s *SettingsStore) IsAuthEnabled() bool {
	settings, err := s.Get()
	if err != nil {
		return true // Default to auth enabled on error
	}
	return settings.AuthEnabled
}

// IsMemorySavingDisabled returns whether memory saving mode is disabled.
func (s *SettingsStore) IsMemorySavingDisabled() bool {
	settings, err := s.Get()
	if err != nil {
		return false // Default to auto mode on error
	}
	return settings.DisableMemorySaving
}

// IsLoggingEnabled returns whether application logging is enabled.
func (s *SettingsStore) IsLoggingEnabled() bool {
	settings, err := s.Get()
	if err != nil {
		return false // Default to disabled on error
	}
	return settings.Logging.Enabled
}

// GetLogLevel returns the configured log level.
func (s *SettingsStore) GetLogLevel() string {
	settings, err := s.Get()
	if err != nil || settings.Logging.LogLevel == "" {
		return "info"
	}
	return settings.Logging.LogLevel
}

// GetLoggingMaxAge returns the max age for log entries in hours.
func (s *SettingsStore) GetLoggingMaxAge() int {
	settings, err := s.Get()
	if err != nil {
		return 2 // Default 2 hours
	}
	if settings.Logging.MaxAge <= 0 {
		return 2
	}
	return settings.Logging.MaxAge
}

// AddManagedPolicy adds a policy name to the managed policies list.
func (s *SettingsStore) AddManagedPolicy(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	settings := s.settings
	if settings == nil {
		return fmt.Errorf("settings not loaded")
	}

	next, added := appendUnique(settings.ManagedPolicies, name)
	if !added {
		return nil
	}
	settings.ManagedPolicies = next
	return s.saveUnlocked(settings)
}

// RemoveManagedPolicy removes a policy name from the managed policies list.
func (s *SettingsStore) RemoveManagedPolicy(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	settings := s.settings
	if settings == nil {
		return fmt.Errorf("settings not loaded")
	}

	settings.ManagedPolicies = filterOut(settings.ManagedPolicies, name)
	return s.saveUnlocked(settings)
}

// GetManagedPolicies returns the list of policy names created by AWG Manager.
func (s *SettingsStore) GetManagedPolicies() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.settings == nil {
		return nil
	}
	return s.settings.ManagedPolicies
}
