package vlessserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/realityserver"
	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/xhttpserver"
)

type Service struct {
	mu                  sync.Mutex
	dataDir             string
	config              Config
	onReload            func()
	recoveryRequired    bool
	recoveryReason      string
	recoveryFingerprint string
	lastRuntimeErr      string
	secrets             SecretStore
	orch                *orchestrator.Orchestrator
	fircRoutingOverride bool
	daemon              *xhttpserver.Server
	configRev           uint64
	saveSettingsHook    func(path string, data []byte) error
	testRuntimeHook     func(cfg xhttpserver.Config) error

	reality            RealitySettings
	realityLastErr     string
	realitySrv         *realityserver.Server
	realityNetInfo     RealityNetworkInfoFunc
	realityNetCache    *RealityNetworkInfo
	realityNetCachedAt time.Time
	realityFirewall    func(port int, open bool)
	realityFWPort      int
	realityFWSynced    bool
}

func New(dataDir string, onReload func()) *Service {
	s := &Service{
		dataDir:         dataDir,
		onReload:        onReload,
		realityFirewall: realityserver.SyncWANFirewall,
	}

	secStore, err := NewDiskSecretStore(filepath.Join(dataDir, "xray", "secrets"))
	if err == nil {
		s.secrets = secStore
	}

	s.cleanLegacySlotFilesLocked()
	s.loadConfig()
	s.loadRealitySettings()
	_ = s.RecoverPendingTransactions()
	return s
}

func (s *Service) Secrets() SecretStore {
	return s.secrets
}

func (s *Service) DataDir() string {
	return s.dataDir
}

// SetTestRuntimeHook registers an optional test-only runtime hook for coordinator/watcher unit tests
// that validate configuration transactions without spawning a Linux awgm-xhttpd binary.
func (s *Service) SetTestRuntimeHook(fn func(cfg xhttpserver.Config) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.testRuntimeHook = fn
}

func (s *Service) ClearRecoveryRequired() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recoveryRequired = false
	s.recoveryReason = ""
	s.recoveryFingerprint = ""
}

func (s *Service) cleanLegacySlotFilesLocked() {
	if s.dataDir == "" {
		return
	}
	cfgDir := filepath.Join(s.dataDir, "singbox", "config.d")
	rels := []string{
		"19-server-ingress.json",
		filepath.Join("disabled", "19-server-ingress.json"),
		filepath.Join("pending", "19-server-ingress.json"),
		"22-server-ingress.json",
		filepath.Join("disabled", "22-server-ingress.json"),
		filepath.Join("pending", "22-server-ingress.json"),
	}
	for _, rel := range rels {
		_ = os.Remove(filepath.Join(cfgDir, rel))
	}
}

func (s *Service) disableOrchestratorIngressSlotLocked() {
	if s.orch == nil {
		return
	}
	_, _, wasActive := s.orch.EffectiveStat(orchestrator.SlotServerIngress)
	_ = s.orch.SetEnabledSilent(orchestrator.SlotServerIngress, false)
	if wasActive {
		_ = s.orch.ReloadNow()
	}
}

// SetOrchestrator ensures Sing-box never owns inbound VLESS server slots (awgm-xhttpd is mandatory).
func (s *Service) SetOrchestrator(orch *orchestrator.Orchestrator) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orch = orch
	s.cleanLegacySlotFilesLocked()
	s.disableOrchestratorIngressSlotLocked()
}

func (s *Service) Orchestrator() *orchestrator.Orchestrator {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.orch
}

func (s *Service) SetFircRoutingOverride(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fircRoutingOverride = enabled
	if s.config.Enabled && ((s.daemon != nil && s.daemon.IsRunning()) || (s.realitySrv != nil && s.realitySrv.IsRunning())) {
		_ = s.syncDaemonLocked()
	}
}

// SyncFircServerRouting synchronizes the persisted VLESS OutboundMode with FIRC's
// vless_xhttp_server toggle ("router" <-> "direct") and updates live awgm-xhttpd egress.
func (s *Service) SyncFircServerRouting(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fircRoutingOverride = enabled
	changed := false
	if enabled {
		if s.config.OutboundMode != "router" {
			s.config.OutboundMode = "router"
			s.config.OutboundInterface = ""
			s.config.OutboundSocksPort = 0
			changed = true
		}
	} else {
		if s.config.OutboundMode == "router" {
			s.config.OutboundMode = "direct"
			changed = true
		}
	}
	if changed {
		_ = s.saveConfig()
	}
	if s.config.Enabled {
		_ = s.syncDaemonLocked()
	}
}

func (s *Service) FircRoutingOverride() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fircRoutingOverride
}

func buildXHTTPServerConfig(cfg Config) xhttpserver.Config {
	listenAddr := strings.TrimSpace(cfg.ListenAddress)
	if listenAddr == "" {
		listenAddr = "127.0.0.1"
	}
	port := cfg.ListenPort
	if port <= 0 {
		port = 9008
	}
	path := strings.TrimSpace(cfg.Path)
	if path == "" {
		path = "/cdn-bridge"
	}
	tuning := cfg.GetTuning()
	if tuning.StreamPathSuffix != "" && !strings.HasSuffix(strings.TrimRight(path, "/"), tuning.StreamPathSuffix) {
		path = strings.TrimRight(path, "/") + "/" + tuning.StreamPathSuffix
	}
	var addPrefixes []string
	for _, ep := range cfg.Endpoints {
		if !ep.Enabled {
			continue
		}
		epPath := strings.TrimSpace(ep.Path)
		if epPath == "" {
			epPath = path
		}
		if ep.XHTTPTuning != nil && ep.XHTTPTuning.StreamPathSuffix != "" && !strings.HasSuffix(strings.TrimRight(epPath, "/"), ep.XHTTPTuning.StreamPathSuffix) {
			epPath = strings.TrimRight(epPath, "/") + "/" + ep.XHTTPTuning.StreamPathSuffix
		}
		if epPath != "" && epPath != path {
			addPrefixes = append(addPrefixes, epPath)
		}
	}
	var clients []string
	finiteCreds := make(map[string][]byte)
	for _, c := range cfg.Clients {
		if c.Enabled && strings.TrimSpace(c.ID) != "" {
			clients = append(clients, strings.TrimSpace(c.ID))
			if hasValidFPCredentials(c) {
				if sec, err := hex.DecodeString(strings.TrimSpace(c.FPSecretHex)); err == nil && len(sec) == xhttpserver.FPKeySize {
					finiteCreds[strings.TrimSpace(c.FPClientID)] = sec
				}
			}
		}
	}
	socksPort := cfg.OutboundSocksPort
	if socksPort <= 0 {
		socksPort = 1099
	}
	mode := strings.TrimSpace(cfg.OutboundMode)
	if mode == "" {
		if cfg.OutboundSocksPort > 0 {
			mode = "socks"
		} else if cfg.OutboundInterface != "" {
			mode = "interface"
		} else {
			mode = "direct"
		}
	}
	return xhttpserver.Config{
		ListenAddr:              fmt.Sprintf("%s:%d", listenAddr, port),
		PathPrefix:              path,
		PathPrefixes:            addPrefixes,
		Clients:                 clients,
		FiniteCredentials:       finiteCreds,
		TrustedProxies:          []string{"127.0.0.1/32", "::1/128"},
		EstablishedIdleTimeout:  120 * time.Second,
		MaxTotalSessions:        2048,
		MaxSessionsPerIP:        512,
		MaxPreAuthSessions:      512,
		MaxPreAuthSessionsPerIP: 256,
		MaxPreAuthBytesPerIP:    4 * 1024 * 1024,
		MaxPreAuthBytesGlobal:   16 * 1024 * 1024,
		Egress: xhttpserver.EgressConfig{
			Mode:                     mode,
			InterfaceName:            strings.TrimSpace(cfg.OutboundInterface),
			SocksAddr:                fmt.Sprintf("127.0.0.1:%d", socksPort),
			DNSProxyAddr:             "127.0.0.1:3553",
			DomainStrategy:           strings.TrimSpace(cfg.DomainStrategy),
			AllowPrivateDestinations: cfg.AllowLAN,
		},
	}
}

func (s *Service) syncDaemonLocked() error {
	s.disableOrchestratorIngressSlotLocked()
	s.cleanLegacySlotFilesLocked()
	if !s.config.Enabled {
		s.lastRuntimeErr = ""
		if s.daemon != nil {
			_ = s.daemon.Stop()
			s.daemon = nil
		}
		s.syncRealityLocked()
		return nil
	}
	if err := validateConfigTransports(s.config); err != nil {
		s.lastRuntimeErr = err.Error()
		return err
	}
	defer s.syncRealityLocked()
	xCfg := buildXHTTPServerConfig(s.config)
	if s.fircRoutingOverride {
		xCfg.Egress.Mode = "router"
	}
	if s.testRuntimeHook != nil {
		if err := s.testRuntimeHook(xCfg); err != nil {
			s.lastRuntimeErr = err.Error()
			return err
		}
		s.lastRuntimeErr = ""
		return nil
	}
	if _, err := xhttpserver.InspectBinary(""); err != nil {
		s.lastRuntimeErr = err.Error()
		if s.daemon != nil {
			_ = s.daemon.Stop()
			s.daemon = nil
		}
		return err
	}
	if len(xCfg.Clients) == 0 {
		s.lastRuntimeErr = ""
		if s.daemon != nil {
			_ = s.daemon.Stop()
			s.daemon = nil
		}
		return nil
	}
	if s.daemon != nil && s.daemon.IsRunning() && s.daemon.Addr() == xCfg.ListenAddr {
		if err := s.daemon.UpdateConfig(xCfg); err != nil {
			s.lastRuntimeErr = err.Error()
			return err
		}
		s.lastRuntimeErr = ""
		return nil
	}
	if s.daemon != nil {
		_ = s.daemon.Stop()
		s.daemon = nil
	}
	srv := xhttpserver.New(xCfg)
	if err := srv.Start(); err != nil {
		s.lastRuntimeErr = err.Error()
		return err
	}
	srv.SetOnUnexpectedExit(func() { s.restartCrashedDaemon(srv) })
	s.daemon = srv
	s.lastRuntimeErr = ""
	return nil
}

// daemonRestartDelay — пауза перед подъёмом упавшего awgm-xhttpd.
var daemonRestartDelay = 10 * time.Second

// restartCrashedDaemon поднимает awgm-xhttpd после падения (OOM-kill и т.п.),
// если srv всё ещё текущий демон: Stop/Shutdown обнуляют s.daemon, и тогда
// перезапуск не нужен. ponytail: одна попытка на падение — неудачный подъём
// оставляет причину в lastRuntimeErr; при повторяющихся падениях не чаще раза
// в daemonRestartDelay.
func (s *Service) restartCrashedDaemon(srv *xhttpserver.Server) {
	time.AfterFunc(daemonRestartDelay, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.daemon != srv || srv.IsRunning() {
			return
		}
		_ = s.syncDaemonLocked()
	})
}

func (s *Service) IsRecoveryRequired() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recoveryRequired
}

func (s *Service) RecoveryInfo() (required bool, reason string, fingerprint string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recoveryRequired, s.recoveryReason, s.recoveryFingerprint
}

func (s *Service) cfgPath() string {
	return filepath.Join(s.dataDir, "xray", "xray-server-settings.json")
}

func (s *Service) xrayDir() string {
	return filepath.Join(s.dataDir, "xray")
}

// tryAutoImportLegacyOnMissingSettings attempts non-destructive import from existing dataDir/xray/config.json
// or dataDir/xray/profiles/*/active.json ONLY when xray-server-settings.json does not exist yet.
func (s *Service) tryAutoImportLegacyOnMissingSettings() {
	candidates := []string{
		filepath.Join(s.xrayDir(), "config.json"),
	}
	if matches, err := filepath.Glob(filepath.Join(s.xrayDir(), "profiles", "*", "active.json")); err == nil {
		candidates = append(candidates, matches...)
	}
	for _, src := range candidates {
		if !fileExists(src) {
			continue
		}
		disc, err := DiscoverLegacy(src, "")
		if err != nil {
			s.recoveryRequired = true
			s.recoveryReason = fmt.Sprintf("legacy configuration read error (%s): %v", src, err)
			return
		}
		if !disc.Found {
			continue
		}
		if disc.Topology == TopologyC {
			_ = BackupLegacyConfigFile(src, s.dataDir)
			s.recoveryRequired = true
			s.recoveryReason = fmt.Sprintf("unsupported legacy configuration in %s: %s", filepath.Base(src), disc.ConflictReason)
			return
		}
		migrated, err := MigrateLegacy(context.Background(), disc, s.dataDir, 9008)
		if err != nil && !errors.Is(err, ErrAlreadyMigrated) {
			s.recoveryRequired = true
			s.recoveryReason = fmt.Sprintf("legacy configuration import failed (%s): %v", filepath.Base(src), err)
			return
		}
		if migrated != nil {
			s.config = *migrated
		}
		return
	}
}

func defaultConfig() Config {
	return Config{
		Enabled:            false,
		ListenAddress:      "127.0.0.1",
		ListenPort:         9008,
		DispatcherPort:     9009,
		PublicDomain:       "",
		PublicPort:         443,
		Path:               "",
		Transport:          "xhttp",
		Mode:               "packet-up",
		UplinkMethod:       "GET",
		XmuxMaxConnections: 2,
		OutboundSocksPort:  1099,
		Clients:            []Client{},
	}
}

func (s *Service) loadConfig() {
	s.config = defaultConfig()

	data, err := os.ReadFile(s.cfgPath())
	if err != nil {
		if os.IsNotExist(err) {
			s.recoveryRequired = false
			s.tryAutoImportLegacyOnMissingSettings()
			return
		}
		s.recoveryRequired = true
		s.recoveryReason = fmt.Sprintf("read config: %v", err)
		return
	}

	if len(data) == 0 {
		return
	}

	var loaded Config
	if err := json.Unmarshal(data, &loaded); err != nil {
		sum := sha256.Sum256(data)
		s.recoveryRequired = true
		s.recoveryReason = fmt.Sprintf("malformed JSON: %v", err)
		s.recoveryFingerprint = hex.EncodeToString(sum[:])
		return
	}

	if err := validateConfigTransports(loaded); err != nil {
		sum := sha256.Sum256(data)
		s.recoveryRequired = true
		s.recoveryReason = err.Error()
		s.recoveryFingerprint = hex.EncodeToString(sum[:])
		s.config = loaded
		return
	}

	s.config = loaded
	s.recoveryRequired = false
	s.recoveryReason = ""
	s.recoveryFingerprint = ""

	if s.config.ListenAddress == "" {
		s.config.ListenAddress = "127.0.0.1"
	}
	if s.config.ListenPort == 0 {
		s.config.ListenPort = 9008
	}
	if s.config.DispatcherPort == 0 {
		s.config.DispatcherPort = 9009
	}
	if s.config.PublicPort == 0 {
		s.config.PublicPort = 443
	}
	if s.config.Transport == "" {
		s.config.Transport = "xhttp"
	}
	if s.config.Mode == "" {
		s.config.Mode = "packet-up"
	}
	if s.config.UplinkMethod == "" {
		s.config.UplinkMethod = "GET"
	}
	if s.config.XmuxMaxConnections <= 0 {
		s.config.XmuxMaxConnections = 2
	}
	if s.config.OutboundMode == "" {
		if s.config.OutboundSocksPort > 0 {
			s.config.OutboundMode = "socks"
		} else if s.config.OutboundInterface != "" {
			s.config.OutboundMode = "interface"
		} else {
			s.config.OutboundMode = "direct"
		}
	}
	s.fircRoutingOverride = s.config.OutboundMode == "router"
	if s.config.Clients == nil {
		s.config.Clients = []Client{}
	}
	t := s.config.GetTuning()
	s.config.XHTTPTuning = &t
	s.config.UplinkMethod = t.UplinkMethod
}

func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	tmpFile, err := os.CreateTemp(dir, filepath.Base(path)+".tmp.*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmpFile.Name()
	defer func() {
		if tmpFile != nil {
			_ = tmpFile.Close()
			_ = os.Remove(tmpName)
		}
	}()

	if err := tmpFile.Chmod(perm); err != nil {
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if _, err := tmpFile.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", tmpName, err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	tmpFile = nil

	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename to %s: %w", path, err)
	}
	if dirF, err := os.Open(dir); err == nil {
		_ = dirF.Sync()
		_ = dirF.Close()
	}
	return nil
}

func (s *Service) writeSettingsBytes(data []byte) error {
	if s.saveSettingsHook != nil {
		if err := s.saveSettingsHook(s.cfgPath(), data); err != nil {
			return err
		}
	}
	return atomicWriteFile(s.cfgPath(), data, 0600)
}

func (s *Service) saveConfig() error {
	data, err := json.MarshalIndent(s.config, "", "  ")
	if err != nil {
		return err
	}
	return s.writeSettingsBytes(data)
}

func (s *Service) GetConfig() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cloneConfigLocked()
}

func (s *Service) UpdateConfig(cfg Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.recoveryRequired {
		return ErrRecoveryRequired
	}
	if err := validateConfigTransports(cfg); err != nil {
		return err
	}

	s.config.Enabled = cfg.Enabled
	if cfg.ListenAddress != "" {
		s.config.ListenAddress = strings.TrimSpace(cfg.ListenAddress)
	}
	if cfg.ListenPort > 0 {
		s.config.ListenPort = cfg.ListenPort
	}
	if cfg.DispatcherPort > 0 {
		s.config.DispatcherPort = cfg.DispatcherPort
	}
	s.config.PublicDomain = strings.TrimSpace(cfg.PublicDomain)
	if cfg.PublicPort > 0 {
		s.config.PublicPort = cfg.PublicPort
	}
	s.config.Path = strings.TrimSpace(cfg.Path)
	if cfg.Transport != "" {
		s.config.Transport = cfg.Transport
	}
	if cfg.Mode != "" {
		s.config.Mode = cfg.Mode
	}
	if cfg.UplinkMethod != "" {
		s.config.UplinkMethod = cfg.UplinkMethod
	}
	if cfg.XmuxMaxConnections > 0 {
		s.config.XmuxMaxConnections = cfg.XmuxMaxConnections
	}
	if cfg.DomainStrategy != "" {
		s.config.DomainStrategy = strings.TrimSpace(cfg.DomainStrategy)
	}
	if cfg.OutboundMode != "" {
		s.config.OutboundMode = strings.TrimSpace(cfg.OutboundMode)
		switch s.config.OutboundMode {
		case "direct", "router":
			s.config.OutboundInterface = ""
			s.config.OutboundSocksPort = 0
		case "socks":
			s.config.OutboundInterface = ""
			if cfg.OutboundSocksPort > 0 {
				s.config.OutboundSocksPort = cfg.OutboundSocksPort
			} else if s.config.OutboundSocksPort <= 0 {
				s.config.OutboundSocksPort = 1099
			}
		case "interface", "outbound":
			s.config.OutboundInterface = strings.TrimSpace(cfg.OutboundInterface)
			s.config.OutboundSocksPort = 0
		}
	} else {
		if cfg.OutboundInterface != "" {
			s.config.OutboundInterface = strings.TrimSpace(cfg.OutboundInterface)
			s.config.OutboundMode = "interface"
		} else if cfg.OutboundSocksPort > 0 {
			s.config.OutboundSocksPort = cfg.OutboundSocksPort
			s.config.OutboundMode = "socks"
		} else {
			s.config.OutboundSocksPort = 0
			s.config.OutboundInterface = ""
			s.config.OutboundMode = "direct"
		}
	}
	s.config.AllowLAN = cfg.AllowLAN
	if cfg.XHTTPTuning != nil {
		t := *cfg.XHTTPTuning
		if err := ValidateAndNormalizeXHTTPTuning(&t); err == nil {
			s.config.XHTTPTuning = &t
			s.config.UplinkMethod = t.UplinkMethod
		}
	}
	if cfg.Clients != nil {
		existingByID := make(map[string]Client, len(s.config.Clients))
		for _, c := range s.config.Clients {
			existingByID[c.ID] = c
		}
		s.config.Clients = make([]Client, len(cfg.Clients))
		copy(s.config.Clients, cfg.Clients)
		for i := range s.config.Clients {
			if !hasValidFPCredentials(s.config.Clients[i]) {
				if prev, ok := existingByID[s.config.Clients[i].ID]; ok && hasValidFPCredentials(prev) {
					s.config.Clients[i].FPClientID = prev.FPClientID
					s.config.Clients[i].FPSecretHex = prev.FPSecretHex
				}
			}
		}
	}
	if len(cfg.Endpoints) > 0 {
		s.config.Endpoints = make([]EndpointConfig, len(cfg.Endpoints))
		copy(s.config.Endpoints, cfg.Endpoints)
		NormalizeEndpoints(&s.config)
	}

	if err := s.saveConfig(); err != nil {
		return err
	}
	s.configRev++
	s.fircRoutingOverride = s.config.OutboundMode == "router"

	var err error
	if s.config.Enabled {
		err = s.syncDaemonLocked()
	} else {
		err = s.stopLocked()
		s.syncRealityLocked()
	}
	s.notifyReload()
	return err
}

func (s *Service) GetStatus() Status {
	s.mu.Lock()
	defer s.mu.Unlock()

	binInfo, _ := xhttpserver.InspectBinary("")
	daemonRunning := s.daemon != nil && s.daemon.IsRunning()
	realityRunning := s.realitySrv != nil && s.realitySrv.IsRunning()
	running := daemonRunning || realityRunning
	pid := 0
	if daemonRunning {
		pid = s.daemon.CDaemonPID()
	}
	version := ""
	if binInfo.Version != "" {
		version = "awgm-xhttpd " + binInfo.Version
	}
	if daemonRunning {
		if dVer := s.daemon.CDaemonVersion(); dVer != "" {
			version = "awgm-xhttpd " + dVer
		}
	}
	unavailableReason := ""
	if !binInfo.Available {
		if !realityRunning {
			unavailableReason = binInfo.Error
		}
	} else if s.config.Enabled && !running && s.lastRuntimeErr != "" {
		unavailableReason = s.lastRuntimeErr
	}

	configured := s.config.PublicDomain != "" && len(s.config.Clients) > 0
	clients := make([]Client, len(s.config.Clients))
	copy(clients, s.config.Clients)

	return Status{
		Backend:             "awgm-xhttpd",
		BinaryPath:          binInfo.Path,
		Installed:           binInfo.Available,
		Running:             running,
		PID:                 pid,
		ListenAddress:       s.config.ListenAddress,
		Port:                s.config.ListenPort,
		Version:             version,
		UnavailableReason:   unavailableReason,
		Domain:              s.config.PublicDomain,
		Configured:          configured,
		ClientsCount:        len(clients),
		Clients:             clients,
		RecoveryRequired:    s.recoveryRequired,
		RecoveryReason:      s.recoveryReason,
		RecoveryFingerprint: s.recoveryFingerprint,
	}
}

func (s *Service) AddClient(remark string) (*Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.recoveryRequired {
		return nil, ErrRecoveryRequired
	}

	id, err := generateUUID()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(remark) == "" {
		remark = fmt.Sprintf("Клиент %d", len(s.config.Clients)+1)
	}
	fpID, fpSecHex, _, err := xhttpserver.GenerateFPCredentials()
	if err != nil {
		return nil, err
	}

	c := Client{
		ID:          id,
		Remark:      strings.TrimSpace(remark),
		Enabled:     true,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		FPClientID:  fpID,
		FPSecretHex: fpSecHex,
	}

	candidate := s.cloneConfigLocked()
	candidate.Clients = append(candidate.Clients, c)

	txID, err := s.prepareCandidateLocked("", candidate)
	if err != nil {
		return nil, fmt.Errorf("prepare client candidate: %w", err)
	}
	if err := s.commitPreparedLocked(txID); err != nil {
		return nil, fmt.Errorf("commit client candidate: %w", err)
	}
	_ = s.finalizePreparedLocked(txID)
	return &c, nil
}

func (s *Service) RotateClientFPCredentials(id string) (*Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.recoveryRequired {
		return nil, ErrRecoveryRequired
	}
	fpID, fpSecHex, _, err := xhttpserver.GenerateFPCredentials()
	if err != nil {
		return nil, err
	}

	var updated *Client
	candidate := s.cloneConfigLocked()
	for i := range candidate.Clients {
		if candidate.Clients[i].ID == id {
			candidate.Clients[i].FPClientID = fpID
			candidate.Clients[i].FPSecretHex = fpSecHex
			cp := candidate.Clients[i]
			updated = &cp
			break
		}
	}
	if updated == nil {
		return nil, errors.New("client not found")
	}

	txID, err := s.prepareCandidateLocked("", candidate)
	if err != nil {
		return nil, fmt.Errorf("prepare rotate fp credentials candidate: %w", err)
	}
	if err := s.commitPreparedLocked(txID); err != nil {
		return nil, err
	}
	if err := s.finalizePreparedLocked(txID); err != nil {
		return nil, err
	}
	return updated, nil
}

func (s *Service) DeleteClient(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.recoveryRequired {
		return ErrRecoveryRequired
	}

	idx := -1
	for i, c := range s.config.Clients {
		if c.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return errors.New("client not found")
	}

	candidate := s.cloneConfigLocked()
	candidate.Clients = append(candidate.Clients[:idx], candidate.Clients[idx+1:]...)

	txID, err := s.prepareCandidateLocked("", candidate)
	if err != nil {
		return fmt.Errorf("prepare delete candidate: %w", err)
	}
	if err := s.commitPreparedLocked(txID); err != nil {
		return err
	}
	return s.finalizePreparedLocked(txID)
}

func (s *Service) ToggleClient(id string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.recoveryRequired {
		return ErrRecoveryRequired
	}

	found := false
	candidate := s.cloneConfigLocked()
	for i := range candidate.Clients {
		if candidate.Clients[i].ID == id {
			candidate.Clients[i].Enabled = enabled
			found = true
			break
		}
	}
	if !found {
		return errors.New("client not found")
	}

	txID, err := s.prepareCandidateLocked("", candidate)
	if err != nil {
		return fmt.Errorf("prepare toggle candidate: %w", err)
	}
	if err := s.commitPreparedLocked(txID); err != nil {
		return err
	}
	return s.finalizePreparedLocked(txID)
}

func (s *Service) notifyReload() {
	if s.onReload != nil {
		go s.onReload()
	}
}

func (s *Service) GetTuning() XHTTPTuning {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.config.GetTuning()
}

func (s *Service) UpdateTuning(tuning XHTTPTuning) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := ValidateAndNormalizeXHTTPTuning(&tuning); err != nil {
		return err
	}
	s.config.XHTTPTuning = &tuning
	s.config.UplinkMethod = tuning.UplinkMethod
	for i := range s.config.Endpoints {
		t := tuning
		s.config.Endpoints[i].XHTTPTuning = &t
		s.config.Endpoints[i].UplinkMethod = tuning.UplinkMethod
	}
	if err := s.saveConfig(); err != nil {
		return err
	}
	s.configRev++
	var err error
	if s.config.Enabled {
		err = s.syncDaemonLocked()
	}
	s.notifyReload()
	return err
}

func (s *Service) UpdatePublicDomain(domain string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config.PublicDomain = domain
	if len(s.config.Endpoints) > 0 && domain != "" {
		activeMap := make(map[string]bool)
		var ordered []string
		for _, d := range strings.Split(domain, ",") {
			d = strings.ToLower(strings.TrimSpace(d))
			if d != "" && !activeMap[d] {
				activeMap[d] = true
				ordered = append(ordered, d)
			}
		}
		knownMap := make(map[string]bool)
		originMap := make(map[string]bool)
		for i := range s.config.Endpoints {
			epDom := strings.ToLower(strings.TrimSpace(s.config.Endpoints[i].PublicDomain))
			if epDom != "" {
				knownMap[epDom] = true
			}
			orig := strings.ToLower(strings.TrimSpace(s.config.Endpoints[i].OriginHost))
			if orig != "" {
				originMap[orig] = true
			}
		}
		for _, d := range ordered {
			if !knownMap[d] && !originMap[d] && !strings.HasPrefix(d, "tg.") {
				cw := "external_https"
				ot := "public_address"
				if IsKeenDNSHost(d) {
					cw = "direct"
				}
				s.config.Endpoints = append(s.config.Endpoints, EndpointConfig{
					ID:            "ep-" + d,
					Name:          DefaultEndpointName(d, cw, ot),
					Enabled:       true,
					ConnectionWay: cw,
					OriginType:    ot,
					PublicDomain:  d,
					PublicPort:    s.config.PublicPort,
					Path:          s.config.Path,
					Transport:     s.config.Transport,
					Mode:          s.config.Mode,
					UplinkMethod:  s.config.UplinkMethod,
				})
			}
		}
		NormalizeEndpoints(&s.config)
	}
	s.configRev++
	return s.saveConfig()
}

func (s *Service) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.recoveryRequired {
		return ErrRecoveryRequired
	}
	s.config.Enabled = true
	_ = s.saveConfig()
	s.configRev++
	s.notifyReload()
	return s.syncDaemonLocked()
}

func (s *Service) StartConfigured() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.recoveryRequired {
		return ErrRecoveryRequired
	}
	if !s.config.Enabled {
		return nil
	}
	return s.syncDaemonLocked()
}

func (s *Service) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.config.Enabled = false
	_ = s.saveConfig()
	s.configRev++
	s.notifyReload()
	err := s.stopLocked()
	s.syncRealityLocked()
	return err
}

func (s *Service) Shutdown(ctx context.Context) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopLocked()
}

func (s *Service) ShutdownRuntime(ctx context.Context) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopLocked()
}

func (s *Service) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return (s.daemon != nil && s.daemon.IsRunning()) || (s.realitySrv != nil && s.realitySrv.IsRunning())
}

func (s *Service) Restart() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.recoveryRequired {
		return ErrRecoveryRequired
	}
	if s.daemon != nil {
		_ = s.daemon.Stop()
		s.daemon = nil
	}
	s.notifyReload()
	return s.syncDaemonLocked()
}

func (s *Service) stopLocked() error {
	s.lastRuntimeErr = ""
	if s.daemon != nil {
		_ = s.daemon.Stop()
		s.daemon = nil
	}
	s.stopRealityRuntimeLocked()
	s.disableOrchestratorIngressSlotLocked()
	s.cleanLegacySlotFilesLocked()
	return nil
}

// FactoryReset stops VLESS/REALITY runtimes, removes firewall rules and persisted
// VLESS files (preserving coordinator lock and journal files), and restores clean
// factory defaults in memory without writing settings files back to disk.
func (s *Service) FactoryReset(ctx context.Context) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.daemon != nil {
		_ = s.daemon.Stop()
		s.daemon = nil
	}
	s.stopRealityRuntimeLocked()
	s.realityFWSynced = false
	s.syncRealityFirewallLocked(0)

	s.disableOrchestratorIngressSlotLocked()
	s.cleanLegacySlotFilesLocked()

	if s.dataDir == "/opt/etc/awg-manager" {
		_ = os.Remove("/tmp/awgm-xhttpd.conf")
	}

	if s.dataDir != "" {
		xrayDir := s.xrayDir()
		if entries, err := os.ReadDir(xrayDir); err == nil {
			for _, entry := range entries {
				name := entry.Name()
				if name == "server-ingress.lock" || name == "server-ingress-transaction.json" {
					continue
				}
				_ = os.RemoveAll(filepath.Join(xrayDir, name))
			}
		}
		if secStore, err := NewDiskSecretStore(filepath.Join(xrayDir, "secrets")); err == nil {
			s.secrets = secStore
		}
	}

	s.config = defaultConfig()
	s.reality = defaultRealitySettings()
	s.realityLastErr = ""
	s.realityNetCache = nil
	s.realityNetCachedAt = time.Time{}
	s.lastRuntimeErr = ""
	s.fircRoutingOverride = false
	s.recoveryRequired = false
	s.recoveryReason = ""
	s.recoveryFingerprint = ""
	s.configRev++

	s.notifyReload()
	return nil
}

func generateUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("crypto/rand read: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
