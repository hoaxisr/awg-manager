package tgwebproxy

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

var (
	ErrRecoveryRequired = errors.New("recovery_required: service requires manual or successful recovery before mutating configuration")
)

type EgressValidator func(device string) error
type TproxyValidator func(stagingDir string) error

type Service struct {
	mu                   sync.Mutex
	dataDir              string
	telemtDir            string
	binPath              string
	config               Config
	tc                   *TransactionCoordinator
	workers              *WorkerSupervisor
	mux                  *Mux
	legacyTimer          *time.Timer
	onReload             func()
	egressValidator      EgressValidator
	tproxyValidator      TproxyValidator
	runtimeDCProbe       func(device string) bool
	recoveryRequired     bool
	lastStartupError     string
	upstreamMu           sync.RWMutex
	cachedUpstreamStatus string
	lastUpstreamCheck    time.Time
	randReader           io.Reader
	csrfSecret           []byte
	closeOnce            sync.Once
	healthWg             sync.WaitGroup
	isClosed             bool
	stopCh               chan struct{}
}

func New(dataDir string, onReload func()) *Service {
	var telemtDir string
	if dataDir != "" && dataDir != "/opt/etc/awg-manager" {
		telemtDir = filepath.Join(dataDir, "telemt")
		_ = os.MkdirAll(telemtDir, 0755)
	} else {
		telemtDir = "/opt/etc/awg-manager/telemt"
		if _, err := os.Stat(telemtDir); os.IsNotExist(err) {
			if _, errLegacy := os.Stat("/opt/etc/telemt"); errLegacy == nil {
				telemtDir = "/opt/etc/telemt"
			} else {
				_ = os.MkdirAll(telemtDir, 0755)
			}
		}
	}
	tproxyBaseDir := filepath.Join(dataDir, "tproxy")

	workers := NewDefaultWorkerSupervisor()
	workers.SetTproxyDir(tproxyBaseDir)
	tc := NewTransactionCoordinator(tproxyBaseDir)

	s := &Service{
		dataDir:              dataDir,
		telemtDir:            telemtDir,
		binPath:              "/opt/bin/tproxy-server",
		tc:                   tc,
		workers:              workers,
		mux:                  NewMux(),
		onReload:             onReload,
		egressValidator:      DefaultEgressValidator,
		tproxyValidator:      defaultTproxyValidator,
		runtimeDCProbe:       defaultRuntimeDCProbe,
		cachedUpstreamStatus: "ok",
		randReader:           rand.Reader,
		stopCh:               make(chan struct{}),
	}

	// 1. CRITICAL: Startup crash reconciliation MUST run BEFORE loading or mutating config
	if err := s.tc.StartupReconcile(s.workers); err != nil {
		s.recoveryRequired = true
		s.lastStartupError = fmt.Sprintf("startup reconcile failed: %v", err)
		if s.workers != nil {
			_ = s.workers.RestoreWorkerStates(map[string]WorkerProcessState{})
		}
		return s
	}

	// 2. Ensure crypto keys exist and have valid size
	if err := s.ensureTokenKey(); err != nil {
		s.recoveryRequired = true
		s.lastStartupError = fmt.Sprintf("ensure token key failed: %v", err)
		return s
	}
	if err := s.ensureCSRFKey(); err != nil {
		s.recoveryRequired = true
		s.lastStartupError = fmt.Sprintf("ensure csrf key failed: %v", err)
		return s
	}

	// 3. Load and migrate config
	if err := s.loadAndMigrateConfig(); err != nil {
		s.recoveryRequired = true
		s.lastStartupError = fmt.Sprintf("load/migrate config failed: %v", err)
		return s
	}

	// 4. Background routines only start if healthy
	s.mu.Lock()
	s.scheduleLegacyExpiryLocked()
	s.mu.Unlock()

	s.healthWg.Add(1)
	go s.runBackgroundHealthWorker()

	if s.config.Enabled && s.config.IsWebEnabled() && s.mux != nil {
		_ = s.mux.StartWithSecrets(s.config.ListenPort, s.config.GetEndpoints(), s.config.ActiveSecrets())
	}

	return s
}

func (s *Service) cfgPath() string {
	return filepath.Join(s.tproxyDir(), "tgwebproxy-settings.json")
}

func (s *Service) tproxyDir() string {
	return filepath.Join(s.dataDir, "tproxy")
}

func (s *Service) telemtDirectPath() string {
	return filepath.Join(s.telemtDir, "config.toml")
}

func (s *Service) telemtRawPath() string {
	return filepath.Join(s.telemtDir, "raw.toml")
}

var renameKeyFile = os.Rename

func loadOrCreateKeyFile(keyPath string, expectedSize int, randReader io.Reader) ([]byte, error) {
	dir := filepath.Dir(keyPath)

	// Validate directory: strictly reject symlinks and non-directories
	dirFi, err := os.Lstat(dir)
	if err == nil {
		if dirFi.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("key directory %s is a symlink: not allowed", dir)
		}
		if !dirFi.IsDir() {
			return nil, fmt.Errorf("key directory %s is not a directory", dir)
		}
	} else if os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, fmt.Errorf("mkdir key dir: %w", err)
		}
		dirFi, err = os.Lstat(dir)
		if err != nil {
			return nil, fmt.Errorf("stat key dir: %w", err)
		}
		if dirFi.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("key directory %s is a symlink: not allowed", dir)
		}
		if !dirFi.IsDir() {
			return nil, fmt.Errorf("key directory %s is not a directory", dir)
		}
	} else {
		return nil, fmt.Errorf("stat key dir: %w", err)
	}
	if dirF, err := os.Open(dir); err == nil {
		_ = dirF.Chmod(0700)
		dirF.Close()
	}

	// Attempt to open existing file without following symlinks
	f, err := openFileNoFollow(keyPath, os.O_RDONLY, 0)
	if err == nil {
		fi, statErr := f.Stat()
		if statErr != nil {
			f.Close()
			return nil, fmt.Errorf("stat key file: %w", statErr)
		}
		if !fi.Mode().IsRegular() {
			f.Close()
			return nil, fmt.Errorf("key file %s is not a regular file (mode=%v)", keyPath, fi.Mode())
		}
		if fi.Size() == int64(expectedSize) {
			// Enforce 0600 on the verified open descriptor directly, preventing TOCTOU
			if chmodErr := f.Chmod(0600); chmodErr != nil {
				f.Close()
				return nil, fmt.Errorf("chmod key file: %w", chmodErr)
			}
			key := make([]byte, expectedSize)
			if _, readErr := io.ReadFull(f, key); readErr != nil {
				f.Close()
				return nil, fmt.Errorf("read key file: %w", readErr)
			}
			f.Close()
			return key, nil
		}
		// File exists but size is corrupt: close descriptor before replacing
		f.Close()
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("open key file: %w", err)
	}

	// 1. Generate new random key in memory first. If RNG fails, existing file is untouched.
	key := make([]byte, expectedSize)
	reader := randReader
	if reader == nil {
		reader = rand.Reader
	}
	if _, err := io.ReadFull(reader, key); err != nil {
		return nil, fmt.Errorf("read random bytes: %w", err)
	}

	// 2. Create temp file with unpredictable name and O_EXCL permissions (0600)
	tmpFile, err := os.CreateTemp(dir, ".key.tmp.*")
	if err != nil {
		return nil, fmt.Errorf("create tmp key file: %w", err)
	}
	tmpPath := tmpFile.Name()

	if _, err := tmpFile.Write(key); err != nil {
		tmpFile.Close()
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("write tmp key file: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("sync tmp key file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("close tmp key file: %w", err)
	}

	// 3. If target file already exists (corrupt size), backup with timestamp before replacing.
	// If backup fails, abort immediately: delete temp key and leave original file untouched!
	corruptPath := fmt.Sprintf("%s.corrupt.%d", keyPath, time.Now().UnixNano())
	backedUp := false
	if _, err := os.Lstat(keyPath); err == nil {
		if err := renameKeyFile(keyPath, corruptPath); err != nil {
			_ = os.Remove(tmpPath)
			return nil, fmt.Errorf("backup corrupt key: %w", err)
		}
		backedUp = true
	}

	if err := renameKeyFile(tmpPath, keyPath); err != nil {
		_ = os.Remove(tmpPath)
		if backedUp {
			_ = renameKeyFile(corruptPath, keyPath)
		}
		return nil, fmt.Errorf("atomic replace key file: %w", err)
	}

	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}

	return key, nil
}

func (s *Service) ensureTokenKey() error {
	dir := s.tproxyDir()
	keyPath := filepath.Join(dir, "token.key")
	_, err := loadOrCreateKeyFile(keyPath, 32, s.randReader)
	return err
}

func (s *Service) ensureCSRFKey() error {
	dir := s.tproxyDir()
	keyPath := filepath.Join(dir, "csrf.key")
	key, err := loadOrCreateKeyFile(keyPath, 32, s.randReader)
	if err != nil {
		return err
	}
	s.csrfSecret = key
	return nil
}

func (s *Service) GetCSRFSecret() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.csrfSecret) != 32 {
		_ = s.ensureCSRFKey()
	}
	if len(s.csrfSecret) != 32 {
		return nil
	}
	return append([]byte(nil), s.csrfSecret...)
}

func (s *Service) loadAndMigrateConfig() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	defSecret, err := randomHexSecret(s.randReader)
	if err != nil {
		return fmt.Errorf("generate default secret: %w", err)
	}

	defCfg := Config{
		SchemaVersion:  CurrentSchemaVersion,
		Enabled:        false,
		Scenario:       ScenarioDual,
		ListenPort:     DefaultListenPort,
		AdminPort:      DefaultAdminPort,
		PublicHostname: DefaultPublicHostname,
		DirectHost:     DefaultDirectHost,
		DirectPort:     DefaultDirectPort,
		Secret:         defSecret,
		Backend:        DefaultBackend,
		CarrierMode:    DefaultCarrierMode,
		UpstreamDevice: DefaultUpstreamDevice,
		TlsDomain:      DefaultTlsDomain,
	}

	cfgPath := s.cfgPath()
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			s.config = defCfg
			return s.saveSettingsFileLocked(defCfg)
		}
		return fmt.Errorf("read config file: %w", err)
	}

	// 1. Inspect raw JSON for schema_version explicitly
	var rawCheck struct {
		SchemaVersion *int `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &rawCheck); err != nil {
		return fmt.Errorf("decode config json: %w", err)
	}

	needsMigration := (rawCheck.SchemaVersion == nil) || (*rawCheck.SchemaVersion < CurrentSchemaVersion)

	var loaded Config
	if err := json.Unmarshal(data, &loaded); err != nil {
		return fmt.Errorf("decode full config json: %w", err)
	}

	if loaded.Scenario == "" {
		loaded.Scenario = ScenarioDual
		needsMigration = true
	}

	// An explicitly selected carrier is part of the live compatibility contract
	// with the deployed CDN.  In particular, GET is still used by providers
	// that do not pass WebSocket upgrades.  Only fill an absent value; never
	// silently replace a persisted carrier during a schema migration.
	if loaded.CarrierMode == "" {
		loaded.CarrierMode = DefaultCarrierMode
		needsMigration = true
	}

	// Migrate endpoints: ensure CDN endpoints (e.g. edge, cdn, selcdn) are marked as is_cdn: true
	// and sanitize any endpoint port collisions (especially legacy 8087 which collides with entware-server)
	for i := range loaded.Endpoints {
		hostLower := strings.ToLower(loaded.Endpoints[i].Hostname)
		idLower := strings.ToLower(loaded.Endpoints[i].ID)
		if !loaded.Endpoints[i].IsCDN {
			epPreset := loaded.Endpoints[i].Preset
			if epPreset == "" {
				epPreset = loaded.NetworkPreset
			}
			epCarrier := loaded.Endpoints[i].CarrierMode
			if epCarrier == "" {
				epCarrier = loaded.CarrierMode
			}
			isDirectCarrier := epPreset == PresetKeenDNS || epPreset == PresetStaticIP ||
				(epPreset == "" && strings.EqualFold(epCarrier, "websocket"))
			if idLower == "edge" || strings.Contains(hostLower, "edge.") ||
				strings.Contains(hostLower, "cdn.") || strings.Contains(hostLower, "selcdn") ||
				(!isDirectCarrier && len(loaded.Endpoints) == 1 && (loaded.Scenario == ScenarioCDNHTTP || loaded.Scenario == ScenarioDual)) {
				loaded.Endpoints[i].IsCDN = true
				needsMigration = true
			}
		}
		if i > 0 {
			if loaded.Endpoints[i].ListenPort <= 0 || (loaded.Endpoints[i].ListenPort >= 8080 && loaded.Endpoints[i].ListenPort < 9000) {
				loaded.Endpoints[i].ListenPort = 18084 + (i-1)*3
				needsMigration = true
			}
			if loaded.Endpoints[i].AdminPort <= 0 || (loaded.Endpoints[i].AdminPort >= 8080 && loaded.Endpoints[i].AdminPort < 9000) {
				loaded.Endpoints[i].AdminPort = 18086 + (i-1)*3
				needsMigration = true
			}
		}
	}

	if needsMigration {
		bakPath := cfgPath + ".v1.bak"
		// Write .v1.bak atomically with 0600 ONLY if it doesn't already exist
		if _, err := os.Stat(bakPath); os.IsNotExist(err) {
			tmpBak := bakPath + ".tmp"
			if err := os.WriteFile(tmpBak, data, 0600); err != nil {
				return fmt.Errorf("write v1 backup: %w", err)
			}
			if err := os.Rename(tmpBak, bakPath); err != nil {
				return fmt.Errorf("rename v1 backup: %w", err)
			}
			_ = fsyncDir(s.tproxyDir())
		}

		migrated := loaded
		if migrated.Scenario == "" {
			migrated.Scenario = ScenarioDual
		}
		if migrated.Backend == "" || migrated.Backend == "127.0.0.1:8443" {
			migrated.Backend = DefaultBackend
		}
		if migrated.CarrierMode == "" {
			migrated.CarrierMode = DefaultCarrierMode
		}
		if migrated.UpstreamDevice == "" {
			migrated.UpstreamDevice = DefaultUpstreamDevice
		}
		if migrated.TlsDomain == "" {
			migrated.TlsDomain = DefaultTlsDomain
		}
		if migrated.DirectHost == "" {
			migrated.DirectHost = DefaultDirectHost
		}
		if migrated.DirectPort <= 0 {
			migrated.DirectPort = DefaultDirectPort
		}
		if migrated.ListenPort <= 0 {
			migrated.ListenPort = DefaultListenPort
		}
		if migrated.AdminPort <= 0 {
			migrated.AdminPort = DefaultAdminPort
		}
		if migrated.Secret == "" {
			sec, err := randomHexSecret(s.randReader)
			if err != nil {
				return fmt.Errorf("generate migrated secret: %w", err)
			}
			migrated.Secret = sec
		}
		migrated.SchemaVersion = CurrentSchemaVersion
		s.config = migrated

		if err := s.saveSettingsFileLocked(migrated); err != nil {
			return fmt.Errorf("save migrated settings: %w", err)
		}
		return nil
	}

	// Sanitize any endpoint port collisions (especially legacy 8087 which collides with entware-server)
	for i := range loaded.Endpoints {
		if i > 0 {
			if loaded.Endpoints[i].ListenPort <= 0 || (loaded.Endpoints[i].ListenPort >= 8080 && loaded.Endpoints[i].ListenPort < 9000) {
				loaded.Endpoints[i].ListenPort = 18084 + (i-1)*3
			}
			if loaded.Endpoints[i].AdminPort <= 0 || (loaded.Endpoints[i].AdminPort >= 8080 && loaded.Endpoints[i].AdminPort < 9000) {
				loaded.Endpoints[i].AdminPort = 18086 + (i-1)*3
			}
		}
	}

	s.config = loaded
	return nil
}

func (s *Service) saveSettingsFileLocked(cfg Config) error {
	_ = os.MkdirAll(s.tproxyDir(), 0700)
	_ = os.Chmod(s.tproxyDir(), 0700)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.cfgPath(), data, 0600)
}

func randomHexSecret(r io.Reader) (string, error) {
	if r == nil {
		r = rand.Reader
	}
	b := make([]byte, 16)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", fmt.Errorf("crypto rand read secret: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func generateTxID(r io.Reader) (string, error) {
	if r == nil {
		r = rand.Reader
	}
	b := make([]byte, 8)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", fmt.Errorf("crypto rand read txid: %w", err)
	}
	return fmt.Sprintf("tx_%d_%s", time.Now().Unix(), hex.EncodeToString(b)), nil
}

func (s *Service) GetInternalConfig() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.config
}

func (s *Service) GetConfig() PublicConfig {
	s.mu.Lock()
	cfg := s.config
	s.mu.Unlock()

	return PublicConfig{
		SchemaVersion:      cfg.SchemaVersion,
		Enabled:            cfg.Enabled,
		Scenario:           cfg.Scenario,
		NetworkPreset:      cfg.NetworkPreset,
		ListenPort:         cfg.ListenPort,
		AdminPort:          cfg.AdminPort,
		PublicHostname:     cfg.PublicHostname,
		DirectHost:         cfg.DirectHost,
		DirectPort:         cfg.DirectPort,
		SecretMasked:       maskSecret(cfg.Secret),
		Backend:            cfg.Backend,
		CarrierMode:        cfg.CarrierMode,
		UpstreamDevice:     cfg.UpstreamDevice,
		TlsDomain:          cfg.TlsDomain,
		LegacyActive:       cfg.LegacySecret != "",
		LegacyExpiresAt:    cfg.LegacyExpiresAt,
		LegacySecretMasked: maskSecret(cfg.LegacySecret),
		Endpoints:          cfg.GetEndpoints(),
		Users:              cfg.GetPublicUsers(),
	}
}

func (s *Service) applyConfigLocked(newCfg Config) error {
	if s.recoveryRequired {
		return ErrRecoveryRequired
	}

	// 1. Pre-apply Egress & port/upstream validation
	if s.egressValidator != nil && newCfg.UpstreamDevice != "" {
		if err := s.egressValidator(newCfg.UpstreamDevice); err != nil {
			return fmt.Errorf("egress validation failed: %w", err)
		}
	}
	if err := ValidatePortsAndUpstreams(newCfg); err != nil {
		return err
	}

	// 2. Generate 5 files payloads
	settingsBytes, err := json.MarshalIndent(newCfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}

	telemtDirectTOML := GenerateTelemtDirectConfig(newCfg)
	telemtRawTOML := GenerateTelemtRawConfig(newCfg)

	profilesJSON, err := GenerateProfilesJSON(newCfg)
	if err != nil {
		return fmt.Errorf("generate profiles: %w", err)
	}

	tokenKeyPath := filepath.Join(s.tproxyDir(), "token.key")
	profilesPath := filepath.Join(s.tproxyDir(), "profiles.json")
	configJSON, err := GenerateTproxyConfigJSON(newCfg, profilesPath, tokenKeyPath)
	if err != nil {
		return fmt.Errorf("generate config.json: %w", err)
	}

	files := []FilePayload{
		{TargetPath: s.cfgPath(), Content: settingsBytes, Mode: 0600},
		{TargetPath: s.telemtDirectPath(), Content: []byte(telemtDirectTOML), Mode: 0600},
		{TargetPath: s.telemtRawPath(), Content: []byte(telemtRawTOML), Mode: 0600},
		{TargetPath: profilesPath, Content: profilesJSON, Mode: 0600},
		{TargetPath: filepath.Join(s.tproxyDir(), "config.json"), Content: configJSON, Mode: 0600},
	}

	endpoints := newCfg.GetEndpoints()
	for i, ep := range endpoints {
		if i == 0 || ep.ExternalBackend {
			continue
		}
		epProfilesFileName := fmt.Sprintf("profiles_%s.json", ep.ID)
		epProfilesPath := filepath.Join(s.tproxyDir(), epProfilesFileName)
		epProfilesJSON, err := GenerateEndpointProfilesJSON(ep, newCfg)
		if err != nil {
			return fmt.Errorf("generate endpoint profiles for %s: %w", ep.Hostname, err)
		}
		files = append(files, FilePayload{
			TargetPath: epProfilesPath,
			Content:    epProfilesJSON,
			Mode:       0600,
		})

		epConfigJSON, err := GenerateEndpointTproxyConfigJSON(ep, epProfilesPath, tokenKeyPath)
		if err != nil {
			return fmt.Errorf("generate endpoint config for %s: %w", ep.Hostname, err)
		}
		epFileName := fmt.Sprintf("config_%s.json", ep.ID)
		files = append(files, FilePayload{
			TargetPath: filepath.Join(s.tproxyDir(), epFileName),
			Content:    epConfigJSON,
			Mode:       0600,
		})
	}

	txID, err := generateTxID(s.randReader)
	if err != nil {
		return fmt.Errorf("generate txid: %w", err)
	}
	var validator TproxyValidator
	if newCfg.Enabled && newCfg.IsWebEnabled() {
		validator = s.tproxyValidator
	}

	tproxyChanged := hasTproxyFileChanges(s.tproxyDir(), files)
	tproxyWasRunning := false
	if s.workers != nil {
		if w := s.workers.findWorker("tproxy-server"); w != nil {
			tproxyWasRunning = s.workers.GetWorkerState(*w).Running
		}
	}
	prevCfg := s.config
	muxWasRunning := s.mux != nil && s.mux.IsRunning()

	txHook := &serviceTxHook{
		svc:              s,
		endpoints:        endpoints,
		tproxyChanged:    tproxyChanged,
		tproxyWasRunning: tproxyWasRunning,
		prevCfg:          prevCfg,
		muxWasRunning:    muxWasRunning,
	}

	if err := s.tc.ExecuteTransaction(txID, files, txHook, newCfg, validator); err != nil {
		return err
	}

	// Clean up obsolete config_*.json and profiles_*.json
	activeFiles := make(map[string]bool)
	activeFiles["config.json"] = true
	activeFiles["profiles.json"] = true
	for i, ep := range endpoints {
		if i > 0 && !ep.ExternalBackend {
			activeFiles[fmt.Sprintf("config_%s.json", ep.ID)] = true
			activeFiles[fmt.Sprintf("profiles_%s.json", ep.ID)] = true
		}
	}
	for _, pattern := range []string{"config_*.json", "profiles_*.json"} {
		if matches, err := filepath.Glob(filepath.Join(s.tproxyDir(), pattern)); err == nil {
			for _, m := range matches {
				if !activeFiles[filepath.Base(m)] {
					_ = os.Remove(m)
				}
			}
		}
	}

	s.config = newCfg
	s.scheduleLegacyExpiryLocked()
	go tuneRuntimeNetwork(newCfg.UpstreamDevice)
	go s.checkUpstreamHealth()
	return nil
}

func hasTproxyFileChanges(tproxyDir string, files []FilePayload) bool {
	cleanDir := filepath.Clean(tproxyDir)
	for _, fp := range files {
		if filepath.Clean(filepath.Dir(fp.TargetPath)) != cleanDir {
			continue
		}
		base := filepath.Base(fp.TargetPath)
		if base == "tgwebproxy-settings.json" {
			continue
		}
		existing, err := os.ReadFile(fp.TargetPath)
		if err != nil || !bytes.Equal(existing, fp.Content) {
			return true
		}
	}
	return false
}

type serviceTxHook struct {
	svc              *Service
	endpoints        []WebProxyEndpoint
	tproxyChanged    bool
	tproxyWasRunning bool
	prevCfg          Config
	muxWasRunning    bool
}

func (h *serviceTxHook) CaptureWorkerStates() map[string]WorkerProcessState {
	if h.svc == nil || h.svc.workers == nil {
		return make(map[string]WorkerProcessState)
	}
	return h.svc.workers.CaptureWorkerStates()
}

func (h *serviceTxHook) ApplyWorkers(cfg Config) error {
	if h.svc != nil && h.svc.workers != nil {
		if err := h.svc.workers.ApplyWorkers(cfg); err != nil {
			return err
		}
		if !h.svc.workers.isMock() && cfg.Enabled && cfg.IsWebEnabled() && h.tproxyChanged && h.tproxyWasRunning {
			if err := h.svc.workers.RestartWorker("tproxy-server"); err != nil {
				return err
			}
			if err := h.svc.workers.ApplyWorkers(cfg); err != nil {
				return err
			}
		}
	}
	if h.svc != nil && h.svc.mux != nil {
		if cfg.Enabled && cfg.IsWebEnabled() {
			if err := h.svc.mux.StartWithSecrets(cfg.ListenPort, h.endpoints, cfg.ActiveSecrets()); err != nil {
				return err
			}
		} else {
			if err := h.svc.mux.Stop(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *serviceTxHook) CheckReadiness(cfg Config) error {
	if h.svc != nil && h.svc.workers != nil {
		if err := h.svc.workers.CheckReadiness(cfg); err != nil {
			return err
		}
	}
	if h.svc != nil && h.svc.mux != nil && cfg.Enabled && cfg.IsWebEnabled() {
		expectedPort := cfg.ListenPort
		if expectedPort <= 0 {
			expectedPort = DefaultListenPort
		}
		if !h.svc.mux.IsRunning() || h.svc.mux.ListenPort() != expectedPort {
			return fmt.Errorf("mux listener not ready on port %d", expectedPort)
		}
	}
	return nil
}

func (h *serviceTxHook) RestoreWorkerStates(states map[string]WorkerProcessState) error {
	var errs []error
	if h.svc != nil && h.svc.workers != nil {
		if err := h.svc.workers.RestoreWorkerStates(states); err != nil {
			errs = append(errs, err)
		}
	}
	if h.svc != nil && h.svc.mux != nil {
		if h.muxWasRunning {
			if err := h.svc.mux.StartWithSecrets(h.prevCfg.ListenPort, h.prevCfg.GetEndpoints(), h.prevCfg.ActiveSecrets()); err != nil {
				errs = append(errs, err)
			}
		} else {
			if err := h.svc.mux.Stop(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func tuneRuntimeNetwork(upstreamDevice string) {
	if runtime.GOOS != "linux" {
		return
	}
	_ = exec.Command("sysctl", "-w", "net.core.rmem_max=16777216").Run()
	_ = exec.Command("sysctl", "-w", "net.core.wmem_max=16777216").Run()
	_ = exec.Command("sysctl", "-w", "net.ipv4.tcp_rmem=4096 262144 16777216").Run()
	_ = exec.Command("sysctl", "-w", "net.ipv4.tcp_wmem=4096 262144 16777216").Run()
	_ = exec.Command("sysctl", "-w", "net.core.netdev_max_backlog=5000").Run()
	if upstreamDevice != "" && upstreamDevice != "direct" {
		_ = exec.Command("ip", "link", "set", "dev", upstreamDevice, "txqueuelen", "1000").Run()
	}
}

func (s *Service) UpdateConfig(cfg Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.recoveryRequired {
		return ErrRecoveryRequired
	}

	newCfg := s.config

	if cfg.NetworkPreset != "" {
		newCfg.NetworkPreset = strings.TrimSpace(strings.ToLower(cfg.NetworkPreset))
	}

	if cfg.Endpoints != nil {
		newCfg.Endpoints = cfg.Endpoints
		for i := range newCfg.Endpoints {
			if newCfg.Endpoints[i].ID == "" {
				newCfg.Endpoints[i].ID = fmt.Sprintf("ep-%d", i+1)
			}
			hostLower := strings.ToLower(newCfg.Endpoints[i].Hostname)
			idLower := strings.ToLower(newCfg.Endpoints[i].ID)
			if !newCfg.Endpoints[i].IsCDN {
				epPreset := newCfg.Endpoints[i].Preset
				if epPreset == "" {
					epPreset = newCfg.NetworkPreset
				}
				epCarrier := newCfg.Endpoints[i].CarrierMode
				if epCarrier == "" {
					epCarrier = newCfg.CarrierMode
				}
				isDirectCarrier := epPreset == PresetKeenDNS || epPreset == PresetStaticIP ||
					(epPreset == "" && strings.EqualFold(epCarrier, "websocket"))
				if idLower == "edge" || strings.Contains(hostLower, "edge.") ||
					strings.Contains(hostLower, "cdn.") || strings.Contains(hostLower, "selcdn") ||
					(!isDirectCarrier && len(newCfg.Endpoints) == 1 && (newCfg.Scenario == ScenarioCDNHTTP || newCfg.Scenario == ScenarioDual)) {
					newCfg.Endpoints[i].IsCDN = true
				}
			}
			if newCfg.Endpoints[i].Preset == "" && newCfg.NetworkPreset != "" {
				newCfg.Endpoints[i].Preset = InferEndpointPreset(newCfg.Endpoints[i], newCfg)
			}
			if spec, ok := ResolvePresetSpec(newCfg.Endpoints[i].Preset); ok {
				newCfg.Endpoints[i].Preset = spec.ID
				newCfg.Endpoints[i].IsCDN = spec.IsCDN
				if newCfg.Endpoints[i].CarrierMode == "" {
					newCfg.Endpoints[i].CarrierMode = spec.CarrierMode
				}
			}
			if newCfg.Endpoints[i].ListenPort <= 0 || (!newCfg.Endpoints[i].ExternalBackend && i > 0 && newCfg.Endpoints[i].ListenPort >= 8080 && newCfg.Endpoints[i].ListenPort < 9000) {
				if i == 0 {
					newCfg.Endpoints[i].ListenPort = 8084
				} else {
					newCfg.Endpoints[i].ListenPort = 18084 + (i-1)*3
				}
			}
			if !newCfg.Endpoints[i].ExternalBackend && (newCfg.Endpoints[i].AdminPort <= 0 || (i > 0 && newCfg.Endpoints[i].AdminPort >= 8080 && newCfg.Endpoints[i].AdminPort < 9000)) {
				if i == 0 {
					newCfg.Endpoints[i].AdminPort = 8086
				} else {
					newCfg.Endpoints[i].AdminPort = 18086 + (i-1)*3
				}
			}
		}
		if len(newCfg.Endpoints) > 0 && newCfg.Endpoints[0].Hostname != "" {
			newCfg.PublicHostname = newCfg.Endpoints[0].Hostname
		}
		if len(newCfg.Endpoints) > 0 && newCfg.Endpoints[0].Preset != "" && cfg.NetworkPreset == "" {
			newCfg.NetworkPreset = newCfg.Endpoints[0].Preset
		}
	}

	if cfg.Scenario != "" {
		newCfg.Scenario = cfg.Scenario
	}
	if cfg.PublicHostname != "" {
		newCfg.PublicHostname = cfg.PublicHostname
	}
	if cfg.DirectHost != "" {
		newCfg.DirectHost = cfg.DirectHost
	}
	if cfg.DirectPort > 0 {
		newCfg.DirectPort = cfg.DirectPort
	}
	if cfg.Secret != "" && cfg.Secret != s.config.Secret {
		newCfg.LegacySecret = s.config.Secret
		newCfg.Secret = cfg.Secret
		newCfg.LegacyExpiresAt = time.Now().UTC().Add(DefaultLegacyGraceTTL).Format(time.RFC3339)
	}
	if cfg.ListenPort > 0 {
		newCfg.ListenPort = cfg.ListenPort
	}
	if cfg.AdminPort > 0 {
		newCfg.AdminPort = cfg.AdminPort
	}
	if cfg.Backend != "" {
		newCfg.Backend = cfg.Backend
	}
	if cfg.CarrierMode != "" {
		newCfg.CarrierMode = cfg.CarrierMode
	} else if spec, ok := ResolvePresetSpec(newCfg.NetworkPreset); ok && cfg.NetworkPreset != "" {
		newCfg.CarrierMode = spec.CarrierMode
	} else if newCfg.CarrierMode == "" {
		newCfg.CarrierMode = DefaultCarrierMode
	}
	if cfg.UpstreamDevice != "" {
		newCfg.UpstreamDevice = cfg.UpstreamDevice
	}
	if cfg.TlsDomain != "" {
		newCfg.TlsDomain = cfg.TlsDomain
	}

	// Combine pending expired legacy secret reconciliation
	if newCfg.LegacyExpiresAt != "" {
		if expTime, err := time.Parse(time.RFC3339, newCfg.LegacyExpiresAt); err == nil {
			if time.Now().After(expTime) {
				newCfg.LegacySecret = ""
				newCfg.LegacyExpiresAt = ""
			}
		}
	}

	if err := s.applyConfigLocked(newCfg); err != nil {
		return err
	}

	s.notifyReload()
	return nil
}

// ApplyManagedIngress applies coordinator-managed ingress parameters (Enabled, ListenPort, PublicHostname).
// It strictly validates ListenPort > 0 and preserves all other unmanaged fields intact.
func (s *Service) ApplyManagedIngress(cfg ManagedIngressConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.recoveryRequired {
		return ErrRecoveryRequired
	}

	if cfg.ListenPort <= 0 {
		return fmt.Errorf("invalid listen port: %d (must be > 0)", cfg.ListenPort)
	}

	newCfg := s.config
	newCfg.Enabled = cfg.Enabled
	newCfg.ListenPort = cfg.ListenPort
	newCfg.PublicHostname = cfg.PublicHostname

	if err := s.applyConfigLocked(newCfg); err != nil {
		return err
	}

	s.notifyReload()
	return nil
}

func (s *Service) RotateSecret() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.recoveryRequired {
		return ErrRecoveryRequired
	}

	newCfg := s.config
	oldSecret := s.config.Secret
	newCfg.LegacySecret = oldSecret
	sec, err := randomHexSecret(s.randReader)
	if err != nil {
		return fmt.Errorf("generate secret: %w", err)
	}
	newCfg.Secret = sec
	if len(newCfg.Users) > 0 {
		updated := make([]TelegramUser, len(newCfg.Users))
		copy(updated, newCfg.Users)
		for i := range updated {
			if updated[i].ID == "default" || updated[i].Secret == oldSecret {
				updated[i].Secret = sec
				break
			}
		}
		newCfg.Users = updated
	}
	newCfg.LegacyExpiresAt = time.Now().UTC().Add(DefaultLegacyGraceTTL).Format(time.RFC3339)

	if err := s.applyConfigLocked(newCfg); err != nil {
		return err
	}

	s.notifyReload()
	return nil
}

func (s *Service) CreateUser(remark string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.recoveryRequired {
		return ErrRecoveryRequired
	}

	newCfg := s.config
	users := newCfg.GetUsers()
	trimmed := strings.TrimSpace(remark)
	if trimmed == "" {
		trimmed = fmt.Sprintf("Пользователь %d", len(users)+1)
	}

	sec, err := randomHexSecret(s.randReader)
	if err != nil {
		return fmt.Errorf("generate user secret: %w", err)
	}

	newUser := TelegramUser{
		ID:        fmt.Sprintf("tg-%d", time.Now().UnixNano()),
		Remark:    trimmed,
		Secret:    sec,
		Enabled:   true,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	users = append(users, newUser)
	newCfg.Users = users

	if err := s.applyConfigLocked(newCfg); err != nil {
		return err
	}

	s.notifyReload()
	return nil
}

func (s *Service) UpdateUser(id string, remark string, enabled *bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.recoveryRequired {
		return ErrRecoveryRequired
	}

	newCfg := s.config
	users := newCfg.GetUsers()
	found := false
	for i := range users {
		if users[i].ID == id {
			if trimmed := strings.TrimSpace(remark); trimmed != "" {
				users[i].Remark = trimmed
			}
			if enabled != nil {
				users[i].Enabled = *enabled
			}
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("пользователь %s не найден", id)
	}
	newCfg.Users = users

	if err := s.applyConfigLocked(newCfg); err != nil {
		return err
	}

	s.notifyReload()
	return nil
}

func (s *Service) DeleteUser(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.recoveryRequired {
		return ErrRecoveryRequired
	}

	newCfg := s.config
	users := newCfg.GetUsers()
	if len(users) <= 1 {
		return errors.New("нельзя удалить единственного пользователя")
	}

	var remaining []TelegramUser
	var deletedSecret string
	for _, u := range users {
		if u.ID == id {
			deletedSecret = u.Secret
			continue
		}
		remaining = append(remaining, u)
	}
	if len(remaining) == len(users) {
		return fmt.Errorf("пользователь %s не найден", id)
	}
	newCfg.Users = remaining
	if deletedSecret != "" && newCfg.Secret == deletedSecret && len(remaining) > 0 {
		newCfg.Secret = remaining[0].Secret
	}

	if err := s.applyConfigLocked(newCfg); err != nil {
		return err
	}

	s.notifyReload()
	return nil
}

func (s *Service) RevokeLegacySecret() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.recoveryRequired {
		return ErrRecoveryRequired
	}

	newCfg := s.config
	newCfg.LegacySecret = ""
	newCfg.LegacyExpiresAt = ""

	if err := s.applyConfigLocked(newCfg); err != nil {
		return err
	}

	s.notifyReload()
	return nil
}

func (s *Service) RotateTokenKey() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.recoveryRequired {
		return ErrRecoveryRequired
	}

	dir := s.tproxyDir()
	dirFi, err := os.Lstat(dir)
	if err != nil || dirFi.Mode()&os.ModeSymlink != 0 || !dirFi.IsDir() {
		return fmt.Errorf("invalid token key dir: %s", dir)
	}

	key := make([]byte, 32)
	reader := s.randReader
	if reader == nil {
		reader = rand.Reader
	}
	if _, err := io.ReadFull(reader, key); err != nil {
		return fmt.Errorf("generate token key: %w", err)
	}

	tmpFile, err := os.CreateTemp(dir, ".token.key.tmp.*")
	if err != nil {
		return fmt.Errorf("create tmp token key: %w", err)
	}
	tmpPath := tmpFile.Name()
	if _, err := tmpFile.Write(key); err != nil {
		tmpFile.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}

	keyPath := filepath.Join(dir, "token.key")
	if err := renameKeyFile(tmpPath, keyPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	_ = fsyncDir(dir)

	// Restart tproxy-server worker
	if s.config.Enabled && s.workers != nil {
		_ = s.workers.RestartWorker("tproxy-server")
	}

	s.notifyReload()
	return nil
}

func (s *Service) ClearScannerCache() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.recoveryRequired {
		return ErrRecoveryRequired
	}

	_ = os.Remove("/tmp/cache/beobachten.txt")

	if s.config.Enabled && s.workers != nil {
		_ = s.workers.RestartWorker("telemt-raw")
	}

	return nil
}

func (s *Service) scheduleLegacyExpiryLocked() {
	if s.legacyTimer != nil {
		s.legacyTimer.Stop()
		s.legacyTimer = nil
	}

	if s.config.LegacySecret == "" || s.config.LegacyExpiresAt == "" {
		return
	}

	expTime, err := time.Parse(time.RFC3339, s.config.LegacyExpiresAt)
	if err != nil {
		return
	}

	duration := time.Until(expTime)
	if duration <= 0 {
		go s.reconcileExpiredLegacySecret()
		return
	}

	s.legacyTimer = time.AfterFunc(duration, func() {
		s.reconcileExpiredLegacySecret()
	})
}

func (s *Service) reconcileExpiredLegacySecret() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.config.LegacySecret == "" {
		return
	}
	if expTime, err := time.Parse(time.RFC3339, s.config.LegacyExpiresAt); err == nil {
		if time.Now().Before(expTime) {
			return
		}
	}

	newCfg := s.config
	newCfg.LegacySecret = ""
	newCfg.LegacyExpiresAt = ""

	if err := s.applyConfigLocked(newCfg); err != nil {
		return
	}
	s.notifyReload()
}

func (s *Service) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.recoveryRequired {
		return ErrRecoveryRequired
	}

	newCfg := s.config
	newCfg.Enabled = true

	if err := s.applyConfigLocked(newCfg); err != nil {
		return err
	}
	s.notifyReload()
	return nil
}

func (s *Service) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.recoveryRequired {
		return ErrRecoveryRequired
	}

	newCfg := s.config
	newCfg.Enabled = false

	if err := s.applyConfigLocked(newCfg); err != nil {
		return err
	}
	s.notifyReload()
	return nil
}

// FactoryReset stops all Telegram proxy workers and mux, wipes tproxy state and
// telemt configs/runtime files (preserving the installed telemt binary), and
// re-initializes fresh factory default settings and keys.
func (s *Service) FactoryReset(ctx context.Context) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.legacyTimer != nil {
		s.legacyTimer.Stop()
		s.legacyTimer = nil
	}
	if s.mux != nil {
		_ = s.mux.Stop()
	}
	if s.workers != nil {
		stopCfg := s.config
		stopCfg.Enabled = false
		_ = s.workers.ApplyWorkers(stopCfg)
	}

	if s.dataDir != "" {
		_ = os.RemoveAll(s.tproxyDir())
		if err := os.MkdirAll(s.tproxyDir(), 0700); err != nil {
			return fmt.Errorf("recreate tproxy dir: %w", err)
		}
	}

	if s.telemtDir != "" {
		for _, name := range []string{"config.toml", "raw.toml", "settings.json", "telemt.pid"} {
			_ = os.Remove(filepath.Join(s.telemtDir, name))
		}
		for _, pat := range []string{"config.toml.*", "raw.toml.*", "settings.json.*"} {
			if matches, err := filepath.Glob(filepath.Join(s.telemtDir, pat)); err == nil {
				for _, m := range matches {
					_ = os.Remove(m)
				}
			}
		}
	}

	if s.dataDir == "/opt/etc/awg-manager" {
		for _, pat := range []string{
			"/opt/var/run/telemt*.pid*",
			"/opt/var/run/telemt*.limit.json",
			"/opt/var/run/tproxy-*.pid*",
		} {
			if matches, err := filepath.Glob(pat); err == nil {
				for _, m := range matches {
					_ = os.Remove(m)
				}
			}
		}
		_ = os.Remove("/tmp/cache/beobachten.txt")
	}

	if s.dataDir != "" {
		if err := s.ensureTokenKey(); err != nil {
			return fmt.Errorf("regenerate token key: %w", err)
		}
		if err := s.ensureCSRFKey(); err != nil {
			return fmt.Errorf("regenerate csrf key: %w", err)
		}
	}

	defSecret, err := randomHexSecret(s.randReader)
	if err != nil {
		return fmt.Errorf("generate default secret: %w", err)
	}

	defCfg := Config{
		SchemaVersion:  CurrentSchemaVersion,
		Enabled:        false,
		Scenario:       ScenarioDual,
		ListenPort:     DefaultListenPort,
		AdminPort:      DefaultAdminPort,
		PublicHostname: DefaultPublicHostname,
		DirectHost:     DefaultDirectHost,
		DirectPort:     DefaultDirectPort,
		Secret:         defSecret,
		Backend:        DefaultBackend,
		CarrierMode:    DefaultCarrierMode,
		UpstreamDevice: DefaultUpstreamDevice,
		TlsDomain:      DefaultTlsDomain,
	}

	s.config = defCfg
	s.recoveryRequired = false
	s.lastStartupError = ""

	s.upstreamMu.Lock()
	s.cachedUpstreamStatus = "ok"
	s.lastUpstreamCheck = time.Time{}
	s.upstreamMu.Unlock()

	if s.dataDir != "" {
		if err := s.saveSettingsFileLocked(defCfg); err != nil {
			return fmt.Errorf("save default settings: %w", err)
		}
	}

	s.notifyReload()
	return nil
}

func (s *Service) Restart() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.recoveryRequired {
		return ErrRecoveryRequired
	}

	if s.config.Enabled && s.config.IsWebEnabled() && s.workers != nil && !s.workers.isMock() {
		_ = s.workers.RestartWorker("tproxy-server")
	}
	if err := s.workers.ApplyWorkers(s.config); err != nil {
		return err
	}
	s.notifyReload()
	return nil
}

// StartConfigured starts the background daemon workers if configured Enabled, without modifying config or saving to disk.
func (s *Service) StartConfigured() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.recoveryRequired {
		return ErrRecoveryRequired
	}

	if !s.config.Enabled {
		return nil
	}

	if s.workers == nil {
		return nil
	}

	// profiles.json is consumed directly by tproxy-server, while the settings
	// file is the canonical source of truth.  In particular, older installations
	// may have been migrated from the legacy GET carrier in settings without
	// regenerating profiles.json.  Always refresh the generated profile before
	// starting/restarting workers so both files describe the same transport.
	if _, err := s.syncProfilesLocked(s.config); err != nil {
		return fmt.Errorf("sync tproxy profiles: %w", err)
	}

	tokenKeyPath := filepath.Join(s.tproxyDir(), "token.key")
	for i, ep := range s.config.GetEndpoints() {
		if i == 0 || ep.ExternalBackend {
			continue
		}
		epProfilesPath := filepath.Join(s.tproxyDir(), fmt.Sprintf("profiles_%s.json", ep.ID))
		if !fileExists(epProfilesPath) {
			if epProfilesJSON, err := GenerateEndpointProfilesJSON(ep, s.config); err == nil {
				_ = os.WriteFile(epProfilesPath, epProfilesJSON, 0600)
			}
		}
		epFileName := fmt.Sprintf("config_%s.json", ep.ID)
		epPath := filepath.Join(s.tproxyDir(), epFileName)
		if !fileExists(epPath) {
			if epConfigJSON, err := GenerateEndpointTproxyConfigJSON(ep, epProfilesPath, tokenKeyPath); err == nil {
				_ = os.WriteFile(epPath, epConfigJSON, 0600)
			}
		}
	}

	if err := s.workers.ApplyWorkers(s.config); err != nil {
		return err
	}
	if err := s.workers.CheckReadiness(s.config); err != nil {
		runtimeStopCfg := s.config
		runtimeStopCfg.Enabled = false
		stopErr := s.workers.ApplyWorkers(runtimeStopCfg)
		if stopErr != nil {
			return errors.Join(err, fmt.Errorf("rollback tg workers after readiness failure: %w", stopErr))
		}
		return err
	}
	if s.config.IsWebEnabled() && s.mux != nil {
		_ = s.mux.StartWithSecrets(s.config.ListenPort, s.config.GetEndpoints(), s.config.ActiveSecrets())
	}
	go tuneRuntimeNetwork(s.config.UpstreamDevice)
	return nil
}

// syncProfilesLocked writes the tproxy-server profile and runtime configs generated
// from cfg when their content differs. It reports whether tproxy files were changed.
// The caller must hold s.mu and must invoke it before workers read the files.
func (s *Service) syncProfilesLocked(cfg Config) (bool, error) {
	profilesJSON, err := GenerateProfilesJSON(cfg)
	if err != nil {
		return false, err
	}
	profilesPath := filepath.Join(s.tproxyDir(), "profiles.json")
	changed := false
	if existing, err := os.ReadFile(profilesPath); err != nil || !bytes.Equal(existing, profilesJSON) {
		if err := atomicWriteMetadata(profilesPath, profilesJSON, 0600); err != nil {
			return false, err
		}
		changed = true
	}

	tokenKeyPath := filepath.Join(s.tproxyDir(), "token.key")
	if configJSON, err := GenerateTproxyConfigJSON(cfg, profilesPath, tokenKeyPath); err == nil {
		configPath := filepath.Join(s.tproxyDir(), "config.json")
		if existing, err := os.ReadFile(configPath); err != nil || !bytes.Equal(existing, configJSON) {
			_ = atomicWriteMetadata(configPath, configJSON, 0600)
			changed = true
		}
	}

	for i, ep := range cfg.GetEndpoints() {
		if i == 0 || ep.ExternalBackend {
			continue
		}
		epProfilesPath := filepath.Join(s.tproxyDir(), fmt.Sprintf("profiles_%s.json", ep.ID))
		if epProfilesJSON, err := GenerateEndpointProfilesJSON(ep, cfg); err == nil {
			if existing, err := os.ReadFile(epProfilesPath); err != nil || !bytes.Equal(existing, epProfilesJSON) {
				_ = atomicWriteMetadata(epProfilesPath, epProfilesJSON, 0600)
				changed = true
			}
		}
		epFileName := fmt.Sprintf("config_%s.json", ep.ID)
		epPath := filepath.Join(s.tproxyDir(), epFileName)
		if epConfigJSON, err := GenerateEndpointTproxyConfigJSON(ep, epProfilesPath, tokenKeyPath); err == nil {
			if existing, err := os.ReadFile(epPath); err != nil || !bytes.Equal(existing, epConfigJSON) {
				_ = atomicWriteMetadata(epPath, epConfigJSON, 0600)
				changed = true
			}
		}
	}

	telemtDirectBytes := []byte(GenerateTelemtDirectConfig(cfg))
	if existing, err := os.ReadFile(s.telemtDirectPath()); err != nil || !bytes.Equal(existing, telemtDirectBytes) {
		_ = atomicWriteMetadata(s.telemtDirectPath(), telemtDirectBytes, 0600)
	}

	telemtRawBytes := []byte(GenerateTelemtRawConfig(cfg))
	if existing, err := os.ReadFile(s.telemtRawPath()); err != nil || !bytes.Equal(existing, telemtRawBytes) {
		_ = atomicWriteMetadata(s.telemtRawPath(), telemtRawBytes, 0600)
	}

	return changed, nil
}

// ReconcileRuntime refreshes generated tproxy-server files after an AWG Manager
// upgrade. If the profile changed, tproxy-server is restarted so it cannot keep
// serving a stale transport mode from a previous release.
func (s *Service) ReconcileRuntime() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.recoveryRequired {
		return ErrRecoveryRequired
	}
	if !s.config.Enabled || !s.config.IsWebEnabled() || s.workers == nil {
		return nil
	}

	changed, err := s.syncProfilesLocked(s.config)
	if err != nil {
		return fmt.Errorf("sync tproxy profiles: %w", err)
	}
	if changed {
		if err := s.workers.RestartWorker("tproxy-server"); err != nil {
			return fmt.Errorf("restart tproxy-server after profile update: %w", err)
		}
	}
	// Always ensure primary and extra endpoint workers are running from the
	// canonical configuration (even if telemt-raw was already running on boot).
	if err := s.workers.ApplyWorkers(s.config); err != nil {
		return fmt.Errorf("restore tproxy endpoints after profile update: %w", err)
	}
	if err := s.workers.CheckReadiness(s.config); err != nil {
		return fmt.Errorf("tproxy-server not ready after profile update: %w", err)
	}
	return nil
}

// ShutdownRuntime stops the daemon workers without mutating s.config.Enabled or saving config to disk.
func (s *Service) ShutdownRuntime(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.mux != nil {
		_ = s.mux.Stop()
	}

	if s.workers == nil {
		return nil
	}

	runtimeStopCfg := s.config
	runtimeStopCfg.Enabled = false
	return s.workers.ApplyWorkers(runtimeStopCfg)
}

// EnsureMuxRunning ensures the in-process multiplexer is listening if web proxy is enabled.
func (s *Service) EnsureMuxRunning() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.config.Enabled && s.config.IsWebEnabled() && s.mux != nil {
		if !s.mux.IsRunning() {
			_ = s.mux.StartWithSecrets(s.config.ListenPort, s.config.GetEndpoints(), s.config.ActiveSecrets())
		}
	}
}

// IsRunning reports whether any managed daemon workers are actively running.
func (s *Service) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.isRunningLocked()
}

func (s *Service) isRunningLocked() bool {
	if s.config.Enabled && s.config.IsWebEnabled() && s.mux != nil && !s.mux.IsRunning() {
		return false
	}
	if s.workers == nil {
		return false
	}
	states := s.workers.CaptureWorkerStates()
	for _, st := range states {
		if st.Running {
			return true
		}
	}
	return false
}

func (s *Service) runBackgroundHealthWorker() {
	defer s.healthWg.Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	s.checkUpstreamHealth()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.checkUpstreamHealth()
		}
	}
}

func (s *Service) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.isClosed = true
		if s.legacyTimer != nil {
			s.legacyTimer.Stop()
			s.legacyTimer = nil
		}
		if s.stopCh != nil {
			close(s.stopCh)
		}
		if s.mux != nil {
			_ = s.mux.Stop()
		}
		if s.workers != nil {
			if s.workers.tproxy != nil {
				s.workers.tproxy.Stop()
			}
			if s.workers.mtg != nil {
				s.workers.mtg.Stop()
			}
		}
		s.mu.Unlock()

		s.healthWg.Wait()
	})
	return nil
}

func (s *Service) checkUpstreamHealth() {
	s.mu.Lock()
	device := s.config.UpstreamDevice
	recovery := s.recoveryRequired
	s.mu.Unlock()

	if recovery {
		return
	}

	status := "ok"
	if device != "" && device != "direct" && s.egressValidator != nil {
		if err := s.egressValidator(device); err != nil {
			status = "interface_down"
		} else if s.runtimeDCProbe != nil && !s.runtimeDCProbe(device) {
			status = "degraded"
		}
	} else if s.runtimeDCProbe != nil && !s.runtimeDCProbe(device) {
		status = "degraded"
	}

	s.upstreamMu.Lock()
	s.cachedUpstreamStatus = status
	s.lastUpstreamCheck = time.Now()
	s.upstreamMu.Unlock()
}

func (s *Service) notifyReload() {
	if s.onReload != nil {
		go s.onReload()
	}
}

// DeriveBridgeURL generates the signed HTTPS bridge URL for tdesktop web proxy.
func DeriveBridgeURL(host, secretHex string) string {
	secretBytes, err := hex.DecodeString(secretHex)
	if err != nil || len(secretBytes) == 0 {
		return ""
	}
	context := []byte("tdesktop-web-proxy-bridge-v1\n" + host)
	mac := hmac.New(sha256.New, secretBytes)
	mac.Write(context)
	sum := mac.Sum(nil)
	cap := base64.RawURLEncoding.EncodeToString(sum)
	return fmt.Sprintf("https://%s/?bridge=%s", host, cap)
}

func (s *Service) deriveBridgeURL(host, secretHex string) string {
	return DeriveBridgeURL(host, secretHex)
}

func buildSingleUserLinks(cfg Config, activeSecret string) (string, string, string, string, string, string, []WebProxyLink) {
	hexDomain := hex.EncodeToString([]byte(cfg.TlsDomain))
	mtproxySecret := "ee" + activeSecret + hexDomain

	var webProxies []WebProxyLink
	endpoints := cfg.GetEndpoints()
	for _, ep := range endpoints {
		if !ep.Enabled && len(endpoints) > 1 {
			continue
		}
		if ep.Hostname == "" {
			continue
		}
		epTgURL := fmt.Sprintf("tg://webproxy?server=%s&secret=%s", ep.Hostname, activeSecret)
		epTmeURL := fmt.Sprintf("https://t.me/webproxy?server=%s&secret=%s", ep.Hostname, activeSecret)
		epBridgeURL := DeriveBridgeURL(ep.Hostname, activeSecret)
		webProxies = append(webProxies, WebProxyLink{
			ID:          ep.ID,
			Hostname:    ep.Hostname,
			Description: ep.Description,
			Preset:      InferEndpointPreset(ep, cfg),
			CarrierMode: EffectiveEndpointCarrierMode(ep, cfg),
			TgURL:       epTgURL,
			TmeURL:      epTmeURL,
			BridgeURL:   epBridgeURL,
			IsCDN:       ep.IsCDN,
		})
	}

	var tgURL, tmeURL, bridgeURL string
	if cfg.PublicHostname != "" {
		tgURL = fmt.Sprintf("tg://webproxy?server=%s&secret=%s", cfg.PublicHostname, activeSecret)
		tmeURL = fmt.Sprintf("https://t.me/webproxy?server=%s&secret=%s", cfg.PublicHostname, activeSecret)
		bridgeURL = DeriveBridgeURL(cfg.PublicHostname, activeSecret)
	} else if len(webProxies) > 0 {
		tgURL = webProxies[0].TgURL
		tmeURL = webProxies[0].TmeURL
		bridgeURL = webProxies[0].BridgeURL
	}

	var mtproxyURL, mtproxyTmeURL string
	if cfg.DirectHost != "" && cfg.DirectPort > 0 {
		mtproxyURL = fmt.Sprintf("tg://proxy?server=%s&port=%d&secret=%s", cfg.DirectHost, cfg.DirectPort, mtproxySecret)
		mtproxyTmeURL = fmt.Sprintf("https://t.me/proxy?server=%s&port=%d&secret=%s", cfg.DirectHost, cfg.DirectPort, mtproxySecret)
	}

	return mtproxySecret, mtproxyURL, mtproxyTmeURL, tgURL, tmeURL, bridgeURL, webProxies
}

// BuildTgLinks is a pure function that constructs connection URLs and credentials without side effects.
func BuildTgLinks(cfg Config, secret string) (RevealData, error) {
	activeSecret := cfg.Secret
	if secret != "" {
		activeSecret = secret
	} else if len(cfg.Users) > 0 && cfg.Users[0].Secret != "" {
		activeSecret = cfg.Users[0].Secret
	}

	mtproxySecret, mtproxyURL, mtproxyTmeURL, tgURL, tmeURL, bridgeURL, webProxies := buildSingleUserLinks(cfg, activeSecret)

	var userReveals []UserRevealData
	for _, u := range cfg.GetUsers() {
		uMtSec, uMtURL, uMtTme, uTg, uTme, uBridge, uWps := buildSingleUserLinks(cfg, u.Secret)
		userReveals = append(userReveals, UserRevealData{
			ID:            u.ID,
			Remark:        u.Remark,
			Enabled:       u.Enabled,
			Secret:        u.Secret,
			TgURL:         uTg,
			TmeURL:        uTme,
			BridgeURL:     uBridge,
			MtproxySecret: uMtSec,
			MtproxyURL:    uMtURL,
			MtproxyTmeURL: uMtTme,
			WebProxies:    uWps,
		})
	}

	return RevealData{
		Secret:        activeSecret,
		LegacySecret:  cfg.LegacySecret,
		DirectHost:    cfg.DirectHost,
		DirectPort:    cfg.DirectPort,
		TlsDomain:     cfg.TlsDomain,
		TgURL:         tgURL,
		TmeURL:        tmeURL,
		BridgeURL:     bridgeURL,
		MtproxySecret: mtproxySecret,
		MtproxyURL:    mtproxyURL,
		MtproxyTmeURL: mtproxyTmeURL,
		WebProxies:    webProxies,
		Users:         userReveals,
	}, nil
}

func maskSecret(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 8 {
		return "********"
	}
	return s[:4] + "********" + s[len(s)-4:]
}

func (s *Service) GetStatus() Status {
	s.mu.Lock()
	cfg := s.config
	workers := s.workers
	recovery := s.recoveryRequired
	startupErr := s.lastStartupError
	s.mu.Unlock()

	var directOnline, rawOnline, webProxyOnline bool
	var pid, directPID int

	if workers != nil && !recovery {
		for _, w := range workers.workers {
			st := workers.GetWorkerState(w)
			switch w.Name {
			case "telemt-direct":
				directOnline = st.Running
				if st.Running {
					directPID = st.PID
				}
			case "telemt-raw":
				rawOnline = st.Running
			case "tproxy-server":
				webProxyOnline = st.Running
				if st.Running {
					pid = st.PID
				}
			}
		}
	}

	s.upstreamMu.RLock()
	upstreamStatus := s.cachedUpstreamStatus
	if upstreamStatus == "" {
		upstreamStatus = "ok"
	}
	s.upstreamMu.RUnlock()

	legacyActive := cfg.LegacySecret != ""
	legacyExpiredPending := false
	if cfg.LegacyExpiresAt != "" {
		if exp, err := time.Parse(time.RFC3339, cfg.LegacyExpiresAt); err == nil && time.Now().After(exp) {
			legacyExpiredPending = true
		}
	}

	installedTelemt := true
	installedTproxy := true

	installed := installedTelemt && installedTproxy
	if cfg.Scenario == ScenarioDirectFakeTLS {
		installed = installedTelemt
	}

	running := directOnline && webProxyOnline && rawOnline
	switch cfg.Scenario {
	case ScenarioDirectFakeTLS:
		running = directOnline
	case ScenarioCDNHTTP:
		running = webProxyOnline && rawOnline
	}

	links, _ := BuildTgLinks(cfg, "")

	return Status{
		Installed:                     installed,
		InstalledTelemt:               installedTelemt,
		InstalledTproxy:               installedTproxy,
		Running:                       running,
		PID:                           pid,
		DirectPID:                     directPID,
		Port:                          cfg.ListenPort,
		DirectOnline:                  directOnline,
		RawOnline:                     rawOnline,
		WebProxyOnline:                webProxyOnline,
		BackendOnline:                 rawOnline,
		BackendAddr:                   cfg.Backend,
		UpstreamStatus:                upstreamStatus,
		NetworkPreset:                 cfg.NetworkPreset,
		CarrierMode:                   cfg.CarrierMode,
		PublicHost:                    cfg.PublicHostname,
		DirectHost:                    cfg.DirectHost,
		DirectPort:                    cfg.DirectPort,
		TlsDomain:                     cfg.TlsDomain,
		SecretMasked:                  maskSecret(cfg.Secret),
		LegacySecretMasked:            maskSecret(cfg.LegacySecret),
		LegacyActive:                  legacyActive,
		LegacyExpiresAt:               cfg.LegacyExpiresAt,
		LegacyExpiredPendingReconcile: legacyExpiredPending,
		RecoveryRequired:              recovery,
		LastStartupError:              startupErr,
		WebProxies:                    links.WebProxies,
		Users:                         cfg.GetPublicUsers(),
	}
}

func (s *Service) RevealSecret() (RevealData, error) {
	return s.RevealUserSecret("")
}

func (s *Service) RevealUserSecret(userID string) (RevealData, error) {
	s.mu.Lock()
	cfg := s.config
	recovery := s.recoveryRequired
	s.mu.Unlock()

	if recovery {
		return RevealData{}, ErrRecoveryRequired
	}

	var targetSecret string
	var targetRemark string
	var targetID string
	users := cfg.GetUsers()
	if userID != "" {
		for _, u := range users {
			if u.ID == userID {
				targetSecret = u.Secret
				targetRemark = u.Remark
				targetID = u.ID
				break
			}
		}
	} else if len(users) > 0 {
		targetSecret = users[0].Secret
		targetRemark = users[0].Remark
		targetID = users[0].ID
	}

	data, err := BuildTgLinks(cfg, targetSecret)
	if err != nil {
		return RevealData{}, err
	}
	data.UserID = targetID
	data.UserRemark = targetRemark
	return data, nil
}

func DefaultEgressValidator(device string) error {
	if device == "" {
		return nil
	}
	iface, err := net.InterfaceByName(device)
	if err != nil {
		return fmt.Errorf("interface %s not found: %w", device, err)
	}
	if iface.Flags&net.FlagUp == 0 {
		return fmt.Errorf("interface %s is down", device)
	}
	addrs, err := iface.Addrs()
	if err != nil || len(addrs) == 0 {
		return fmt.Errorf("interface %s has no IP addresses", device)
	}
	return nil
}

func defaultRuntimeDCProbe(device string) bool {
	conn, err := dialWithDevice("tcp", "149.154.175.50:443", device, 2*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func defaultTproxyValidator(stagingDir string) error {
	cfgPath := filepath.Join(stagingDir, "config.json")
	if !fileExists(cfgPath) {
		cfgPath = filepath.Join(stagingDir, "config.json.candidate")
	}
	if !fileExists(cfgPath) {
		return fmt.Errorf("configuration file not found in %s", stagingDir)
	}
	profPath := filepath.Join(stagingDir, "profiles.json")
	if !fileExists(profPath) {
		profPath = filepath.Join(stagingDir, "profiles.json.candidate")
	}
	return validateTproxyFiles(cfgPath, profPath)
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
