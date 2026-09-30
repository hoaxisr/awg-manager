package tgwebproxy

import (
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
	telemtDir := "/opt/etc/awg-manager/telemt"
	if _, err := os.Stat(telemtDir); os.IsNotExist(err) {
		if _, errLegacy := os.Stat("/opt/etc/telemt"); errLegacy == nil {
			telemtDir = "/opt/etc/telemt"
		} else {
			_ = os.MkdirAll(telemtDir, 0755)
		}
	}
	tproxyBaseDir := filepath.Join(dataDir, "tproxy")

	workers := NewDefaultWorkerSupervisor()
	tc := NewTransactionCoordinator(tproxyBaseDir)

	s := &Service{
		dataDir:              dataDir,
		telemtDir:            telemtDir,
		binPath:              "/opt/bin/tproxy-server",
		tc:                   tc,
		workers:              workers,
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
	}
}

func (s *Service) applyConfigLocked(newCfg Config) error {
	if s.recoveryRequired {
		return ErrRecoveryRequired
	}

	// 1. Pre-apply Egress validation
	if s.egressValidator != nil && newCfg.UpstreamDevice != "" {
		if err := s.egressValidator(newCfg.UpstreamDevice); err != nil {
			return fmt.Errorf("egress validation failed: %w", err)
		}
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

	txID, err := generateTxID(s.randReader)
	if err != nil {
		return fmt.Errorf("generate txid: %w", err)
	}
	var validator TproxyValidator
	if newCfg.Enabled && newCfg.IsWebEnabled() {
		validator = s.tproxyValidator
	}
	if err := s.tc.ExecuteTransaction(txID, files, s.workers, newCfg, validator); err != nil {
		return err
	}

	s.config = newCfg
	s.scheduleLegacyExpiryLocked()
	go s.checkUpstreamHealth()
	return nil
}

func (s *Service) UpdateConfig(cfg Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.recoveryRequired {
		return ErrRecoveryRequired
	}

	newCfg := s.config

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
	newCfg.LegacySecret = s.config.Secret
	sec, err := randomHexSecret(s.randReader)
	if err != nil {
		return fmt.Errorf("generate secret: %w", err)
	}
	newCfg.Secret = sec
	newCfg.LegacyExpiresAt = time.Now().UTC().Add(DefaultLegacyGraceTTL).Format(time.RFC3339)

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
		_ = s.workers.runner.RunCommand(s.workers.workers[2].InitScript, "restart")
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
		_ = s.workers.runner.RunCommand(s.workers.workers[0].InitScript, "restart")
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

func (s *Service) Restart() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.recoveryRequired {
		return ErrRecoveryRequired
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
	return nil
}

// ShutdownRuntime stops the daemon workers without mutating s.config.Enabled or saving config to disk.
func (s *Service) ShutdownRuntime(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.workers == nil {
		return nil
	}

	runtimeStopCfg := s.config
	runtimeStopCfg.Enabled = false
	return s.workers.ApplyWorkers(runtimeStopCfg)
}

// IsRunning reports whether any managed daemon workers are actively running.
func (s *Service) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.isRunningLocked()
}

func (s *Service) isRunningLocked() bool {
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
	if device != "" && s.egressValidator != nil {
		if err := s.egressValidator(device); err != nil {
			status = "interface_down"
		} else if s.runtimeDCProbe != nil && !s.runtimeDCProbe(device) {
			status = "degraded"
		}
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

// BuildTgLinks is a pure function that constructs connection URLs and credentials without side effects.
func BuildTgLinks(cfg Config, secret string) (RevealData, error) {
	activeSecret := cfg.Secret
	if secret != "" {
		activeSecret = secret
	}

	hexDomain := hex.EncodeToString([]byte(cfg.TlsDomain))
	mtproxySecret := "ee" + activeSecret + hexDomain

	var tgURL, tmeURL, bridgeURL string
	if cfg.PublicHostname != "" {
		tgURL = fmt.Sprintf("tg://webproxy?server=%s&secret=%s", cfg.PublicHostname, activeSecret)
		tmeURL = fmt.Sprintf("https://t.me/webproxy?server=%s&secret=%s", cfg.PublicHostname, activeSecret)
		bridgeURL = DeriveBridgeURL(cfg.PublicHostname, activeSecret)
	}

	var mtproxyURL, mtproxyTmeURL string
	if cfg.DirectHost != "" && cfg.DirectPort > 0 {
		mtproxyURL = fmt.Sprintf("tg://proxy?server=%s&port=%d&secret=%s", cfg.DirectHost, cfg.DirectPort, mtproxySecret)
		mtproxyTmeURL = fmt.Sprintf("https://t.me/proxy?server=%s&port=%d&secret=%s", cfg.DirectHost, cfg.DirectPort, mtproxySecret)
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

	installedTelemt := fileExists("/opt/etc/awg-manager/telemt/telemt") || fileExists("/opt/bin/telemt") || fileExists("/opt/usr/bin/telemt")
	installedTproxy := fileExists("/opt/bin/tproxy-server")

	return Status{
		Installed:                     installedTelemt && installedTproxy,
		InstalledTelemt:               installedTelemt,
		InstalledTproxy:               installedTproxy,
		Running:                       directOnline && webProxyOnline && rawOnline,
		PID:                           pid,
		DirectPID:                     directPID,
		Port:                          cfg.ListenPort,
		DirectOnline:                  directOnline,
		RawOnline:                     rawOnline,
		WebProxyOnline:                webProxyOnline,
		BackendOnline:                 rawOnline,
		BackendAddr:                   cfg.Backend,
		UpstreamStatus:                upstreamStatus,
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
	}
}

func (s *Service) RevealSecret() (RevealData, error) {
	s.mu.Lock()
	cfg := s.config
	recovery := s.recoveryRequired
	s.mu.Unlock()

	if recovery {
		return RevealData{}, ErrRecoveryRequired
	}

	return BuildTgLinks(cfg, "")
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
	binPath := "/opt/bin/tproxy-server"
	fi, err := os.Stat(binPath)
	if err != nil {
		return fmt.Errorf("tproxy-server binary missing at %s: %w", binPath, err)
	}
	if fi.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("tproxy-server binary at %s is not executable (mode %04o)", binPath, fi.Mode().Perm())
	}
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
	args := []string{"-check", "-config", cfgPath}
	if fileExists(profPath) {
		args = append(args, "-profiles-file", profPath)
	}
	cmd := exec.Command(binPath, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tproxy-server check failed: %s (%w)", string(out), err)
	}
	if !strings.Contains(string(out), "configuration is valid") {
		return fmt.Errorf("unexpected check output: %s", string(out))
	}
	return nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
