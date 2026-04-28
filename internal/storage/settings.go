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
	CurrentSchemaVersion = 15
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
		if settings.SchemaVersion < 14 {
			s.migrateToV14(&settings)
		}
		if settings.SchemaVersion < 15 {
			s.migrateToV15(&settings)
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
		SingboxRouter: SingboxRouterSettings{
			Enabled:         false,
			RefreshMode:     "interval",
			RefreshInterval: 24,
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

// migrateToV14 sets SingboxRouter defaults for the new TProxy routing
// engine. Idempotent — fields that are non-zero stay as-is.
// Note: Mode field was removed in V15; only RefreshMode/RefreshInterval are set here.
func (s *SettingsStore) migrateToV14(settings *Settings) {
	if settings.SingboxRouter.RefreshMode == "" {
		settings.SingboxRouter.RefreshMode = "interval"
	}
	if settings.SingboxRouter.RefreshInterval == 0 {
		settings.SingboxRouter.RefreshInterval = 24
	}
	settings.SchemaVersion = 14
}

// migrateToV15 wipes deprecated SingboxRouterSettings fields (Mode,
// ClientScope) and force-disables the router so the user re-selects
// a policy via the new policy-mode UI. Per redesign 2026-04-28:
// no users to preserve, simplest fail-safe.
func (s *SettingsStore) migrateToV15(settings *Settings) {
	settings.SingboxRouter.PolicyName = ""
	settings.SingboxRouter.Enabled = false
	settings.SchemaVersion = 15
}

// migrateManagedServers moves a legacy singular managedServer into the
// new ManagedServers slice. Idempotent. Caller holds s.mu.
func (s *SettingsStore) migrateManagedServers() {
	if s.settings == nil || s.settings.ManagedServer == nil {
		return
	}
	// Prepend so an existing slice (theoretically already migrated) keeps
	// its order — but in practice mass migration only fires once, when
	// the slice is empty.
	migrated := append([]ManagedServer{*s.settings.ManagedServer}, s.settings.ManagedServers...)
	s.settings.ManagedServers = migrated
	s.settings.ManagedServer = nil
}

// GetManagedServers returns a deep copy of all managed servers, ordered
// by creation time. Empty slice (never nil) when no servers exist.
func (s *SettingsStore) GetManagedServers() []ManagedServer {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settings == nil {
		return []ManagedServer{}
	}
	s.migrateManagedServers()
	out := make([]ManagedServer, len(s.settings.ManagedServers))
	for i, src := range s.settings.ManagedServers {
		cp := src
		cp.Peers = append([]ManagedPeer(nil), src.Peers...)
		if cp.Policy == "" {
			cp.Policy = "none"
		}
		out[i] = cp
	}
	return out
}

// GetManagedServerByID returns a deep copy of one server, or (nil, false)
// when not found. id == server.InterfaceName.
func (s *SettingsStore) GetManagedServerByID(id string) (*ManagedServer, bool) {
	for _, sv := range s.GetManagedServers() {
		if sv.InterfaceName == id {
			cp := sv
			return &cp, true
		}
	}
	return nil, false
}

// AddManagedServer appends a new server. Errors if interfaceName collides.
func (s *SettingsStore) AddManagedServer(server ManagedServer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settings == nil {
		return fmt.Errorf("settings not loaded")
	}
	s.migrateManagedServers()
	for _, existing := range s.settings.ManagedServers {
		if existing.InterfaceName == server.InterfaceName {
			return fmt.Errorf("server %q already exists", server.InterfaceName)
		}
	}
	s.settings.ManagedServers = append(s.settings.ManagedServers, server)
	return s.saveUnlocked(s.settings)
}

// UpdateManagedServer applies mut to the server with the given id and
// persists. Errors if id not found or mut returns error.
//
// mut MUST be effect-free on error: validate inputs before any mutation,
// because a returned error skips persistence and leaves the in-memory
// struct partially mutated otherwise — subsequent reads would observe
// the divergence.
func (s *SettingsStore) UpdateManagedServer(id string, mut func(*ManagedServer) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settings == nil {
		return fmt.Errorf("settings not loaded")
	}
	s.migrateManagedServers()
	for i := range s.settings.ManagedServers {
		if s.settings.ManagedServers[i].InterfaceName == id {
			if err := mut(&s.settings.ManagedServers[i]); err != nil {
				return err
			}
			return s.saveUnlocked(s.settings)
		}
	}
	return fmt.Errorf("server %q not found", id)
}

// DeleteManagedServer removes the server with the given id.
func (s *SettingsStore) DeleteManagedServer(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settings == nil {
		return fmt.Errorf("settings not loaded")
	}
	s.migrateManagedServers()
	for i, existing := range s.settings.ManagedServers {
		if existing.InterfaceName == id {
			s.settings.ManagedServers = append(s.settings.ManagedServers[:i], s.settings.ManagedServers[i+1:]...)
			return s.saveUnlocked(s.settings)
		}
	}
	return fmt.Errorf("server %q not found", id)
}

// SaveManagedServers replaces the entire slice — used by migration tests
// and bulk-rewrite callers. Most code should use Add/Update/Delete.
func (s *SettingsStore) SaveManagedServers(servers []ManagedServer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settings == nil {
		return fmt.Errorf("settings not loaded")
	}
	s.settings.ManagedServers = servers
	s.settings.ManagedServer = nil
	return s.saveUnlocked(s.settings)
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

// GetApiKey returns the configured API key, or empty string if none.
// Used by the auth middleware to accept `Authorization: Bearer <key>` as
// an alternative to a session cookie. On error returns empty (no key
// match → request falls through to the session check).
func (s *SettingsStore) GetApiKey() string {
	settings, err := s.Get()
	if err != nil {
		return ""
	}
	return settings.ApiKey
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
