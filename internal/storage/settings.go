package storage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	CurrentSchemaVersion        = 35
	DefaultPort                 = 2222
	DefaultInterface            = "br0"
	DefaultPingCheckTarget      = "8.8.8.8"
	DefaultConnectivityCheckURL = "http://connectivitycheck.gstatic.com/generate_204"
	// DefaultSessionTTLHours is the fallback auth session lifetime — the
	// historical fixed value before SessionTtlHours became configurable.
	DefaultSessionTTLHours = 24
	// MinSessionTTLHours / MaxSessionTTLHours bound the configurable auth
	// session lifetime. Shared by the /settings/update validation, the
	// load-time self-heal and GetSessionTTL, so a stored out-of-range value
	// can never silently exceed the documented cap.
	MinSessionTTLHours = 1
	MaxSessionTTLHours = 720
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

// DataDir returns the awg-manager data directory (parent of settings.json).
func (s *SettingsStore) DataDir() string {
	return filepath.Dir(s.path)
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

	restoredFromBackup := false
	var settings Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		// Corrupt settings file (typically a torn write after power loss).
		// Quarantine it and fall back to the backup kept by saveUnlocked so
		// a single bad file does not leave the daemon permanently down.
		quarantine := s.path + ".corrupt"
		_ = os.Rename(s.path, quarantine)
		bak, bakErr := os.ReadFile(s.path + ".bak")
		if bakErr != nil {
			return nil, fmt.Errorf("parse %s (quarantined to %s, no usable backup): %w", s.path, quarantine, err)
		}
		settings = Settings{}
		if bakErr := json.Unmarshal(bak, &settings); bakErr != nil {
			return nil, fmt.Errorf("parse %s (quarantined to %s, backup also corrupt: %v): %w", s.path, quarantine, bakErr, err)
		}
		fmt.Fprintf(os.Stderr, "settings: %s was corrupt (%v), quarantined to %s, restored from backup\n", s.path, err, quarantine)
		recordNotice("backup-restore", s.path, fmt.Sprintf("settings file corrupt (%v), quarantined to %s, RESTORED FROM BACKUP — recent settings changes may be lost", err, quarantine))
		restoredFromBackup = true
	}

	needsSave := restoredFromBackup
	// Migrate if needed
	if settings.SchemaVersion < CurrentSchemaVersion {
		needsSave = true
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
		if settings.SchemaVersion < 16 {
			s.migrateToV16(&settings)
		}
		if settings.SchemaVersion < 17 {
			s.migrateToV17(&settings)
		}
		if settings.SchemaVersion < 18 {
			s.migrateToV18(&settings)
		}
		if settings.SchemaVersion < 19 {
			s.migrateToV19(&settings)
		}
		if settings.SchemaVersion < 20 {
			s.migrateToV20(&settings)
		}
		if settings.SchemaVersion < 21 {
			s.migrateToV21(&settings)
		}
		if settings.SchemaVersion < 22 {
			s.migrateToV22(&settings)
		}
		if settings.SchemaVersion < 23 {
			s.migrateToV23(&settings)
		}
		if settings.SchemaVersion < 24 {
			s.migrateToV24(&settings)
		}
		if settings.SchemaVersion < 25 {
			s.migrateToV25(&settings)
		}
		if settings.SchemaVersion < 26 {
			s.migrateToV26(&settings)
		}
		if settings.SchemaVersion < 27 {
			s.migrateToV27(&settings)
		}
		if settings.SchemaVersion < 28 {
			s.migrateToV28(&settings)
		}
		if settings.SchemaVersion < 29 {
			s.migrateToV29(&settings)
		}
		if settings.SchemaVersion < 30 {
			s.migrateToV30(&settings)
		}
		if settings.SchemaVersion < 31 {
			s.migrateToV31(&settings)
		}
		if settings.SchemaVersion < 32 {
			s.migrateToV32(&settings)
		}
		if settings.SchemaVersion < 33 {
			s.migrateToV33(&settings)
		}
		if settings.SchemaVersion < 34 {
			s.migrateToV34(&settings)
		}
		if settings.SchemaVersion < 35 {
			s.migrateToV35(&settings)
		}
	}

	// Self-heal duplicated managed servers — see dedupManagedServers comment.
	if deduped, removed := dedupManagedServers(settings.ManagedServers); removed > 0 {
		settings.ManagedServers = deduped
		needsSave = true
	}

	// Self-heal an out-of-range session TTL unconditionally (mirrors the
	// dedup self-heal above). migrateToV29 only backfills the default when
	// the file is below v29; a downgrade that rewrote settings.json AT v29
	// without the field leaves a stored 0 that migration never revisits,
	// and a hand-edited over-range value would otherwise let sessions
	// silently outlive the documented MaxSessionTTLHours cap forever
	// (/settings/update only validates the field when a patch carries it).
	// Heal here so the effective and persisted values converge.
	if settings.SessionTtlHours < MinSessionTTLHours || settings.SessionTtlHours > MaxSessionTTLHours {
		settings.SessionTtlHours = DefaultSessionTTLHours
		needsSave = true
	}

	// Self-heal ingress-ref'ы, оставшиеся от удалённых managed-серверов
	// (#670). Безусловно, как дедуп выше: миграцией не обойтись — ref мог
	// осироветь и после апгрейда.
	if pruneOrphanIngressRefs(&settings) {
		needsSave = true
	}

	if needsSave {
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
		SchemaVersion:   CurrentSchemaVersion,
		AuthEnabled:     false,
		SessionTtlHours: DefaultSessionTTLHours,
		UsageLevel:      UsageLevelBasic,
		Server: ServerSettings{
			Port:       DefaultPort,
			Interface:  DefaultInterface,
			Interfaces: []string{DefaultInterface},
		},
		PingCheck: PingCheckSettings{
			Enabled: false,
			Defaults: PingCheckDefaults{
				Method:        "http",
				Target:        DefaultPingCheckTarget,
				Interval:      45,
				DeadInterval:  120,
				FailThreshold: 3,
			},
		},
		Logging: LoggingSettings{
			Enabled:           true,
			MaxAge:            2,
			LogLevel:          "info",
			SingboxLogLevel:   DefaultSingboxLogLevel,
			AppMaxEntries:     5000,
			SingboxMaxEntries: 5000,
		},
		Updates: UpdateSettings{
			CheckEnabled:            true,
			Channel:                 "stable",
			AutoInstallIntervalDays: 7,
			AutoInstallTime:         "05:00",
		},
		Download: DownloadSettings{
			RouteTag:  "direct",
			RouteKind: "direct",
		},
		ConnectivityCheckURL: DefaultConnectivityCheckURL,
		SingboxRouter: SingboxRouterSettings{
			Enabled:        false,
			DeviceMode:     "policy",
			RoutingMode:    "tproxy",
			SnifferEnabled: true,
			WANAutoDetect:  true, // sing-box auto_detect_interface by default
			// KeenDNS/CrazeDNS: имена резолвит сам роутер, его адреса —
			// мимо sing-box.
			BypassPresets: []string{"keendns"},
			// Явный дефолт v6-пула: с v35 пустое значение ЗНАЧИМО («v6
			// выключен»), поэтому свежая установка обязана нести его дословно.
			// Литерал — дубль DefaultFakeIPTunParams().Inet6Range.
			FakeIPPool6: "fc00::/18",
		},
		CreateNDMSProxyForSingbox: true,
		// Fresh installs have no legacy peers — nothing to sweep. Only
		// pre-existing configs (field absent → false) run the one-time
		// peer allow-ips migration.
		ManagedPeerAllowIPsMigrated: true,
	}
}

// dedupManagedServers returns servers with duplicate InterfaceName entries
// removed (first occurrence wins). Second return value is how many entries
// were dropped. Pure: caller decides whether to persist.
//
// Defense-in-depth against pre-3.0 storage bugs that occasionally produced
// two or three copies of the same server on disk (root cause was the
// non-idempotent legacy migrate path coexisting with parallel writes).
func dedupManagedServers(servers []ManagedServer) ([]ManagedServer, int) {
	if len(servers) < 2 {
		return servers, 0
	}
	seen := make(map[string]struct{}, len(servers))
	out := make([]ManagedServer, 0, len(servers))
	for _, sv := range servers {
		if _, dup := seen[sv.InterfaceName]; dup {
			continue
		}
		seen[sv.InterfaceName] = struct{}{}
		out = append(out, sv)
	}
	removed := len(servers) - len(out)
	if removed == 0 {
		return servers, 0
	}
	return out, removed
}

// pruneOrphanIngressRefs drops singboxRouter.ingressInterfaces entries of the
// form "managed:<NDMS-name>" that no longer have a managed server behind them
// (#670). Reports whether anything changed.
//
// Такие ref'ы ставит только тумблер ingress на карточке managed-сервера, так
// что managed:X без сервера X — всегда мусор от удаления. Router-reconcile
// раз в 30 секунд дёргал по нему RCI show interface system-name, и NDMS на
// каждый запрос сыпал в журнал 'unable to find X'. iface:-ref'ы (kernel-имена)
// не трогаем: они резолвятся локально и по определению не привязаны к
// managed-серверам.
func pruneOrphanIngressRefs(settings *Settings) bool {
	refs := settings.SingboxRouter.IngressInterfaces
	if len(refs) == 0 {
		return false
	}
	known := make(map[string]struct{}, len(settings.ManagedServers)+1)
	for _, sv := range settings.ManagedServers {
		known[sv.InterfaceName] = struct{}{}
	}
	if settings.ManagedServer != nil {
		known[settings.ManagedServer.InterfaceName] = struct{}{}
	}
	kept := make([]string, 0, len(refs))
	for _, ref := range refs {
		name, isManaged := strings.CutPrefix(ref, "managed:")
		if isManaged {
			if _, ok := known[name]; !ok {
				continue
			}
		}
		kept = append(kept, ref)
	}
	if len(kept) == len(refs) {
		return false
	}
	settings.SingboxRouter.IngressInterfaces = kept
	return true
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
	deduped, _ := dedupManagedServers(s.settings.ManagedServers)
	out := make([]ManagedServer, len(deduped))
	for i, src := range deduped {
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
			// Снять ingress-ref удалённого сервера в той же транзакции —
			// иначе он висит до перезапуска демона (#670).
			pruneOrphanIngressRefs(s.settings)
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

// SetSingboxManuallyStopped atomically updates the sing-box sticky-stop
// flag under the store lock so concurrent Load→mutate→Save writers on
// other Settings fields (e.g. SingboxRouter toggles from router service)
// cannot silently overwrite the change. Mirrors SaveManagedServers.
func (s *SettingsStore) SetSingboxManuallyStopped(v bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settings == nil {
		return fmt.Errorf("settings not loaded")
	}
	s.settings.SingboxManuallyStopped = v
	return s.saveUnlocked(s.settings)
}

// SetAuthEnabled atomically turns authentication on/off under the store
// lock. Used by the exposure guard, which flips the flag outside the
// settings HTTP handler and must not clobber concurrent writes to other
// fields. Returns whether the value actually changed.
func (s *SettingsStore) SetAuthEnabled(v bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settings == nil {
		return false, fmt.Errorf("settings not loaded")
	}
	if s.settings.AuthEnabled == v {
		return false, nil
	}
	s.settings.AuthEnabled = v
	if err := s.saveUnlocked(s.settings); err != nil {
		return false, err
	}
	return true, nil
}

// SetSingboxCreateNDMSProxy atomically updates the toggle under the
// store lock. Mirrors SetSingboxManuallyStopped — required because
// the API handler is the single writer (CLAUDE.md single-writer
// storage pattern), and concurrent writers on other Settings fields
// must not silently overwrite this change.
func (s *SettingsStore) SetSingboxCreateNDMSProxy(v bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settings == nil {
		return fmt.Errorf("settings not loaded")
	}
	s.settings.CreateNDMSProxyForSingbox = v
	return s.saveUnlocked(s.settings)
}

// IsSingboxNDMSProxyEnabled returns the current toggle value, or true
// on read error (back-compat default — never fail-closed for this
// flag; we'd rather create a Proxy than silently break NDMS routing).
func (s *SettingsStore) IsSingboxNDMSProxyEnabled() bool {
	settings, err := s.Get()
	if err != nil {
		return true
	}
	return settings.CreateNDMSProxyForSingbox
}

// IsManagedPeerAllowIPsMigrated reports whether the one-time peer allow-ips
// sweep has completed. Returns true on read error (fail-safe: skip the sweep
// rather than risk re-running RCI mutations on every boot).
func (s *SettingsStore) IsManagedPeerAllowIPsMigrated() bool {
	settings, err := s.Get()
	if err != nil {
		return true
	}
	return settings.ManagedPeerAllowIPsMigrated
}

// SetManagedPeerAllowIPsMigrated atomically sets the migration flag under the
// store lock. Mirrors SetSingboxCreateNDMSProxy.
func (s *SettingsStore) SetManagedPeerAllowIPsMigrated(v bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settings == nil {
		return fmt.Errorf("settings not loaded")
	}
	s.settings.ManagedPeerAllowIPsMigrated = v
	return s.saveUnlocked(s.settings)
}

// SetOpkgTunState atomically persists the unified OpkgTun ownership record
// under the store lock (single-writer: lifecycle only). nil очищает запись.
// Mirrors SetSingboxManuallyStopped.
func (s *SettingsStore) SetOpkgTunState(st *OpkgTunState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settings == nil {
		return fmt.Errorf("settings not loaded")
	}
	s.settings.OpkgTun = st
	return s.saveUnlocked(s.settings)
}

// SetOpkgTunNATSegments пишет ТОЛЬКО policy-payload записи владения, не трогая
// ownership-поля (Mode/Provisioned/Index): у payload другой писатель
// (NAT-reconcile) и другие моменты записи. Пустой/nil список снимает payload.
// Copy-on-write: в кэш публикуется новая запись, старую могут параллельно
// маршалить читатели без нашего лока. Запись отсутствует → ошибка (payload
// без владельца не бывает).
func (s *SettingsStore) SetOpkgTunNATSegments(segs []PolicyTunNATSegment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settings == nil {
		return fmt.Errorf("settings not loaded")
	}
	if s.settings.OpkgTun == nil {
		return fmt.Errorf("no OpkgTun ownership record")
	}
	cp := *s.settings.OpkgTun
	if len(segs) == 0 {
		cp.PolicyTun = nil
	} else {
		cp.PolicyTun = &OpkgTunPolicyData{NATSegments: segs}
	}
	s.settings.OpkgTun = &cp
	return s.saveUnlocked(s.settings)
}

// SetDNSChainPresetState atomically persists the DNS-chain preset state under
// the store lock (single-writer pattern; the router service is the only
// writer). Pass nil to clear (preset off). Mirrors SetOpkgTunState.
func (s *SettingsStore) SetDNSChainPresetState(st *DNSChainPresetState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settings == nil {
		return fmt.Errorf("settings not loaded")
	}
	s.settings.DNSChainPreset = st
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

// GetServerInterfaceMeta returns AWG Manager metadata for a system server.
func (s *SettingsStore) GetServerInterfaceMeta(serverID string) (ServerInterfaceMeta, bool) {
	if _, err := s.Get(); err != nil {
		return ServerInterfaceMeta{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	meta, ok := s.settings.ServerInterfaceMeta[serverID]
	return meta, ok
}

// UpdateServerInterfaceMeta updates metadata for a system server.
func (s *SettingsStore) UpdateServerInterfaceMeta(serverID string, fn func(*ServerInterfaceMeta) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settings == nil {
		return fmt.Errorf("settings not loaded")
	}
	if s.settings.ServerInterfaceMeta == nil {
		s.settings.ServerInterfaceMeta = map[string]ServerInterfaceMeta{}
	}
	meta := s.settings.ServerInterfaceMeta[serverID]
	if err := fn(&meta); err != nil {
		return err
	}
	s.settings.ServerInterfaceMeta[serverID] = meta
	return s.saveUnlocked(s.settings)
}

// GetServerPeerSecret returns stored key material for a system-server peer.
func (s *SettingsStore) GetServerPeerSecret(serverID, pubkey string) (ServerPeerSecret, bool) {
	if _, err := s.Get(); err != nil {
		return ServerPeerSecret{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	peers, ok := s.settings.ServerPeerSecrets[serverID]
	if !ok {
		return ServerPeerSecret{}, false
	}
	sec, ok := peers[pubkey]
	return sec, ok
}

// SetServerPeerSecret stores key material for a system-server peer.
func (s *SettingsStore) SetServerPeerSecret(serverID, pubkey string, sec ServerPeerSecret) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settings == nil {
		return fmt.Errorf("settings not loaded")
	}
	if s.settings.ServerPeerSecrets == nil {
		s.settings.ServerPeerSecrets = map[string]map[string]ServerPeerSecret{}
	}
	if s.settings.ServerPeerSecrets[serverID] == nil {
		s.settings.ServerPeerSecrets[serverID] = map[string]ServerPeerSecret{}
	}
	s.settings.ServerPeerSecrets[serverID][pubkey] = sec
	return s.saveUnlocked(s.settings)
}

// DeleteServerPeerSecret removes stored key material for a system-server peer.
func (s *SettingsStore) DeleteServerPeerSecret(serverID, pubkey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settings == nil {
		return fmt.Errorf("settings not loaded")
	}
	peers, ok := s.settings.ServerPeerSecrets[serverID]
	if !ok {
		return nil
	}
	delete(peers, pubkey)
	if len(peers) == 0 {
		delete(s.settings.ServerPeerSecrets, serverID)
	}
	return s.saveUnlocked(s.settings)
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

	// Keep the previous good file as .bak (hardlink: no data copy, the old
	// inode survives the rename below). Load() falls back to it if the main
	// file is ever found corrupt after a power loss.
	bakPath := s.path + ".bak"
	if _, err := os.Stat(s.path); err == nil {
		_ = os.Remove(bakPath)
		_ = os.Link(s.path, bakPath)
	}

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

// Snapshot возвращает глубокую копию настроек (JSON round-trip под RLock).
// Для маршала наружу (HTTP-ответы): Get() возвращает ЖИВОЙ объект, и его
// map-поля (ServerPeerSecrets, ServerInterfaceMeta) нельзя читать
// одновременно с узкими мутаторами — concurrent map read/write валит
// процесс. На горячем пути (auth middleware) НЕ использовать — там
// остаётся дешёвый Get().
func (s *SettingsStore) Snapshot() (*Settings, error) {
	if _, err := s.Get(); err != nil { // гарантировать загрузку кэша
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, err := json.Marshal(s.settings)
	if err != nil {
		return nil, err
	}
	out := &Settings{}
	if err := json.Unmarshal(data, out); err != nil {
		return nil, err
	}
	return out, nil
}

// IsAuthEnabled returns whether authentication is enabled.
func (s *SettingsStore) IsAuthEnabled() bool {
	settings, err := s.Get()
	if err != nil {
		return true // Default to auth enabled on error
	}
	return settings.AuthEnabled
}

// GetSessionTTL returns the configured auth session lifetime. Falls back
// to the historical 24h default on load error or an unset/out-of-range
// value (defense in depth — Load already self-heals the stored field).
func (s *SettingsStore) GetSessionTTL() time.Duration {
	settings, err := s.Get()
	if err != nil || settings.SessionTtlHours < MinSessionTTLHours || settings.SessionTtlHours > MaxSessionTTLHours {
		return DefaultSessionTTLHours * time.Hour
	}
	return time.Duration(settings.SessionTtlHours) * time.Hour
}

// IsEntwareAuthEnabled returns whether login via Entware system
// credentials (/opt/etc/shadow) is enabled. Defaults to false on error.
func (s *SettingsStore) IsEntwareAuthEnabled() bool {
	settings, err := s.Get()
	if err != nil {
		return false
	}
	return settings.EntwareAuthEnabled
}

// GetApiKey returns the configured API key, or empty string if none.
// Used by the auth middleware to accept `Authorization: Bearer <key>` as
// an alternative to a session cookie. On error returns empty (no key
// match → request falls through to the session check).
func (s *SettingsStore) GetApiKey() string {
	if _, err := s.Get(); err != nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings.ApiKey
}

// SetApiKey сохраняет новый API-ключ под локом стора. Копия вместо правки
// s.settings по месту: указатель из Get() читают без лока, in-place
// запись строки гонялась бы с этими чтениями.
func (s *SettingsStore) SetApiKey(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settings == nil {
		return fmt.Errorf("settings not loaded")
	}
	updated := *s.settings
	updated.ApiKey = key
	return s.saveUnlocked(&updated)
}

// SetServerListen сохраняет адрес прослушивания HTTP-сервера под локом.
// Легаси-поле Interface — для downgrade-совместимости: старый бинарь
// биндится на FirstIPv4(Interface); при нескольких интерфейсах — первый,
// при «всех» — пусто (0.0.0.0). Копия — по той же причине, что в SetApiKey.
func (s *SettingsStore) SetServerListen(port int, interfaces []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settings == nil {
		return fmt.Errorf("settings not loaded")
	}
	updated := *s.settings
	updated.Server.Port = port
	updated.Server.Interfaces = interfaces
	if len(interfaces) > 0 {
		updated.Server.Interface = interfaces[0]
	} else {
		updated.Server.Interface = ""
	}
	return s.saveUnlocked(&updated)
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

// GetSingboxLogLevel returns normalized sing-box log level.
func (s *SettingsStore) GetSingboxLogLevel() string {
	settings, err := s.Get()
	if err != nil {
		return DefaultSingboxLogLevel
	}
	return NormalizeSingboxLogLevel(settings.Logging.SingboxLogLevel)
}

// GetSingboxBootstrapDNS returns the configured dns-bootstrap address.
// Empty means "not configured" — 00-base.json is left alone.
func (s *SettingsStore) GetSingboxBootstrapDNS() string {
	settings, err := s.Get()
	if err != nil {
		return ""
	}
	return settings.SingboxBootstrapDNS
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

// GetAppMaxEntries returns the cap for the app log buffer.
func (s *SettingsStore) GetAppMaxEntries() int {
	settings, err := s.Get()
	if err != nil {
		return 5000
	}
	if settings.Logging.AppMaxEntries <= 0 {
		return 5000
	}
	return settings.Logging.AppMaxEntries
}

// GetSingboxMaxEntries returns the cap for the sing-box log buffer.
func (s *SettingsStore) GetSingboxMaxEntries() int {
	settings, err := s.Get()
	if err != nil {
		return 5000
	}
	if settings.Logging.SingboxMaxEntries <= 0 {
		return 5000
	}
	return settings.Logging.SingboxMaxEntries
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
