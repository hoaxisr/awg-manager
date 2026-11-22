package openflux

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/childproc"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/routerinfo"
)

var (
	// ErrNotInstalled indicates the openflux binary is missing.
	ErrNotInstalled = errors.New("бинарный файл OpenFlux не установлен")
	// ErrArchUnsupported indicates no prebuilt binary exists for the device architecture.
	ErrArchUnsupported = errors.New("архитектура устройства не поддерживается OpenFlux")
)

// TunnelStore mirrors the active OpenFlux client OpkgTun interface into the manager's tunnel list.
type TunnelStore interface {
	Get(id string) (*storage.AWGTunnel, error)
	List() ([]storage.AWGTunnel, error)
	Exists(id string) bool
	Create(t *storage.AWGTunnel) error
	Update(id string, mut func(*storage.AWGTunnel) error) error
	Delete(id string) error
}

// RouterCommands provides NDMS interface and policy lifecycle methods.
type RouterCommands interface {
	AllocateTunInterface(ctx context.Context, preferred string) (ndmsName string, kernelName string, err error)
	CreateInterface(ctx context.Context, ndmsName, desc string, mtu int, ipWithMask string, global bool, defaultRoute bool) error
	DownInterface(ctx context.Context, ndmsName string) error
	DeleteInterface(ctx context.Context, ndmsName string) error
	PermitPolicy(ctx context.Context, policy, iface string) error
	DenyPolicy(ctx context.Context, policy, iface string) error
}

// PolicyResolver resolves an access policy name to its primary outbound IPv4 address.
type PolicyResolver func(ctx context.Context, policyName string) (string, error)

// ClientStatus describes runtime state of the outbound OpenFlux client.
type ClientStatus struct {
	Installed   bool                `json:"installed"`
	Running     bool                `json:"running"`
	PID         int                 `json:"pid,omitempty"`
	UptimeS     int64               `json:"uptimeS,omitempty"`
	InboundMode string              `json:"inboundMode,omitempty"`
	NdmsIface   string              `json:"ndmsIface,omitempty"`
	TunIface    string              `json:"tunIface,omitempty"`
	TunAddr     string              `json:"tunAddr,omitempty"`
	Policy      string              `json:"policy,omitempty"`
	Socks5Port  int                 `json:"socks5Port"`
	Socks5URI   string              `json:"socks5Uri,omitempty"`
	HttpURI     string              `json:"httpUri,omitempty"`
	Error       string              `json:"error,omitempty"`
	Config      *PublicClientConfig `json:"config,omitempty"`
	LogTail     string              `json:"logTail,omitempty"`
}

// Status describes overall OpenFlux binary, exit-server, and client state.
type Status struct {
	Installed     bool          `json:"installed"`
	Running       bool          `json:"running"`
	PID           int           `json:"pid,omitempty"`
	UptimeS       int64         `json:"uptimeS,omitempty"`
	Version       string        `json:"version,omitempty"`
	LatestVersion string        `json:"latestVersion,omitempty"`
	ArchSupported bool          `json:"archSupported"`
	Binary        string        `json:"binary"`
	Arch          string        `json:"arch"`
	LinkAvailable bool          `json:"linkAvailable"`
	Error         string        `json:"error,omitempty"`
	Config        *PublicConfig `json:"config,omitempty"`
	LogTail       string        `json:"logTail,omitempty"`
	Client        *ClientStatus `json:"client,omitempty"`
}

// Service manages the OpenFlux exit server and client processes.
type Service struct {
	mu                      sync.Mutex
	dataDir                 string
	openfluxDir             string
	binPath                 string
	binExplicit             bool
	cfgPath                 string
	keyPath                 string
	mailruCookiesPath       string
	avitoCookiesPath        string
	cookieStorePath         string
	pidPath                 string
	logPath                 string
	clientCfgPath           string
	clientKeyPath           string
	clientMailruCookiesPath string
	clientAvitoCookiesPath  string
	clientCookieStorePath   string
	clientPidPath           string
	clientLogPath           string
	arch                    string
	spec                    BinarySpec
	httpClient              *http.Client
	freeDisk                func(string) (int64, bool)
	config                  Config
	clientConfig            ClientConfig
	startedAt               time.Time
	clientStartedAt         time.Time
	openedPort              int
	lastErr                 string
	clientLastErr           string
	clientStopCh            chan struct{}
	getPublicHost           func(context.Context) string
	routerCmds              RouterCommands
	policyResolver          PolicyResolver
	tunnelStore             TunnelStore

	cachedVerPath string
	cachedVer     string
}

// SetRouterCommands attaches an NDMS interface/policy controller.
func (s *Service) SetRouterCommands(rc RouterCommands) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routerCmds = rc
}

// SetPolicyResolver attaches a policy egress IP resolver for OpenFlux exit node mode.
func (s *Service) SetPolicyResolver(pr PolicyResolver) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policyResolver = pr
}

// SetTunnelStore wires the tunnel store for mirroring the OpenFlux client tunnel into /tunnels.
func (s *Service) SetTunnelStore(st TunnelStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tunnelStore = st
	if s.hasClientConfigLocked() {
		s.syncClientMirrorLocked()
	}
}

// New creates a new OpenFlux service.
func New(dataDir string, arch string, getPublicHost func(context.Context) string) *Service {
	if dataDir == "" {
		dataDir = "/opt/etc/awg-manager"
	}
	ofDir := filepath.Join(dataDir, "openflux")
	_ = os.MkdirAll(ofDir, 0700)
	_ = os.Chmod(ofDir, 0700)

	normArch := NormalizeArch(arch)
	spec := EmbeddedBinaries[normArch]
	if spec.Version == "" {
		spec.Version = PinnedVersion
	}

	binExplicit := false
	binPath := "/opt/bin/openflux"
	if envBin := strings.TrimSpace(os.Getenv("AWGM_OPENFLUX_BIN")); envBin != "" {
		binPath = envBin
		binExplicit = true
	} else if dataDir != "/opt/etc/awg-manager" {
		binPath = filepath.Join(ofDir, "openflux")
	}

	s := &Service{
		dataDir:                 dataDir,
		openfluxDir:             ofDir,
		binPath:                 binPath,
		binExplicit:             binExplicit,
		cfgPath:                 filepath.Join(ofDir, "config.json"),
		keyPath:                 filepath.Join(ofDir, "secret.key"),
		mailruCookiesPath:       filepath.Join(ofDir, "mailru_cookies.txt"),
		avitoCookiesPath:        filepath.Join(ofDir, "avito_cookies.txt"),
		cookieStorePath:         filepath.Join(ofDir, "cookies-server.json"),
		pidPath:                 filepath.Join(ofDir, "openflux.pid"),
		logPath:                 filepath.Join(ofDir, "openflux.log"),
		clientCfgPath:           filepath.Join(ofDir, "client_config.json"),
		clientKeyPath:           filepath.Join(ofDir, "client_secret.key"),
		clientMailruCookiesPath: filepath.Join(ofDir, "client_mailru_cookies.txt"),
		clientAvitoCookiesPath:  filepath.Join(ofDir, "client_avito_cookies.txt"),
		clientCookieStorePath:   filepath.Join(ofDir, "cookies-client.json"),
		clientPidPath:           filepath.Join(ofDir, "openflux_client.pid"),
		clientLogPath:           filepath.Join(ofDir, "openflux_client.log"),
		arch:                    normArch,
		spec:                    spec,
		httpClient:              &http.Client{Timeout: 3 * time.Minute},
		freeDisk:                routerinfo.FreeBytes,
		config:                  DefaultConfig(),
		clientConfig:            ClientConfig{},
		getPublicHost:           getPublicHost,
	}

	s.loadConfig()
	s.loadClientConfig()
	s.tightenPermissionsLocked()
	return s
}

func (s *Service) tightenPermissionsLocked() {
	if s.openfluxDir != "" {
		_ = os.Chmod(s.openfluxDir, 0700)
	}
	for _, p := range []string{
		s.cfgPath,
		s.clientCfgPath,
		s.keyPath,
		s.clientKeyPath,
		s.mailruCookiesPath,
		s.clientMailruCookiesPath,
		s.avitoCookiesPath,
		s.clientAvitoCookiesPath,
		s.cookieStorePath,
		s.clientCookieStorePath,
		s.logPath,
		s.clientLogPath,
		s.pidPath,
		s.clientPidPath,
	} {
		if p == "" {
			continue
		}
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			_ = os.Chmod(p, 0600)
		}
	}
}

func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	return os.Chmod(path, perm)
}

func (s *Service) loadConfig() {
	data, err := os.ReadFile(s.cfgPath)
	if err == nil {
		var cfg Config
		if err := json.Unmarshal(data, &cfg); err == nil {
			if strings.TrimSpace(cfg.Secret) == "" {
				cfg.Secret, _ = GenerateSecret()
			}
			s.config = cfg
		}
	} else {
		_ = s.saveConfigLocked()
	}
}

func (s *Service) saveConfigLocked() error {
	if s.openfluxDir != "" {
		_ = os.Chmod(s.openfluxDir, 0700)
	}
	data, err := json.MarshalIndent(s.config, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(s.cfgPath, data, 0600)
}

func opkgTunNum(name string) (int, bool) {
	for _, prefix := range []string{"OpkgTun", "opkgtun"} {
		if strings.HasPrefix(name, prefix) {
			if n, err := strconv.Atoi(strings.TrimPrefix(name, prefix)); err == nil {
				return n, true
			}
		}
	}
	return -1, false
}

func (s *Service) hasClientConfigLocked() bool {
	return strings.TrimSpace(s.clientConfig.Name) != "" ||
		strings.TrimSpace(s.clientConfig.DirectDial) != "" ||
		strings.TrimSpace(s.clientConfig.Secret) != "" ||
		len(s.clientConfig.Transports) > 0 ||
		s.clientConfig.Enabled
}

func (s *Service) loadClientConfig() {
	data, err := os.ReadFile(s.clientCfgPath)
	if err == nil {
		var cfg ClientConfig
		if err := json.Unmarshal(data, &cfg); err == nil {
			if strings.TrimSpace(cfg.Socks5Listen) == "" {
				cfg.Socks5Listen = fmt.Sprintf("127.0.0.1:%d", DefaultClientSocks5Port)
			}
			if strings.TrimSpace(cfg.Name) == "" {
				cfg.Name = "OpenFlux Выход"
			}
			// Sanitize any invalid legacy OpkgTun0/OpkgTun1 pins
			if idx, ok := opkgTunNum(cfg.NdmsIface); ok && idx < 2 {
				cfg.NdmsIface = ""
				cfg.TunIface = ""
			}
			s.clientConfig = cfg
			return
		}
	}
	s.clientConfig = ClientConfig{}
}

func (s *Service) saveClientConfigLocked() error {
	if s.openfluxDir != "" {
		_ = os.Chmod(s.openfluxDir, 0700)
	}
	data, err := json.MarshalIndent(s.clientConfig, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(s.clientCfgPath, data, 0600)
}

// IsInstalled checks if the binary exists and is a regular file.
func (s *Service) IsInstalled() bool {
	if envBin := strings.TrimSpace(os.Getenv("AWGM_OPENFLUX_BIN")); envBin != "" && !s.binExplicit {
		if fi, err := os.Stat(envBin); err == nil && !fi.IsDir() {
			s.binPath = envBin
			return true
		}
	}
	info, err := os.Stat(s.binPath)
	if err != nil {
		if !s.binExplicit {
			fallback := filepath.Join(s.openfluxDir, "openflux")
			if fi, fe := os.Stat(fallback); fe == nil && !fi.IsDir() {
				s.binPath = fallback
				return true
			}
		}
		return false
	}
	return !info.IsDir()
}

func (s *Service) detectInstalledVersionLocked(installed bool) string {
	if !installed {
		return ""
	}
	if s.cachedVer != "" && s.cachedVerPath == s.binPath {
		return s.cachedVer
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, s.binPath, "--version").Output()
	if err == nil {
		ver := strings.TrimSpace(string(out))
		if ver != "" && !strings.Contains(ver, "\n") && len(ver) <= 64 {
			s.cachedVerPath = s.binPath
			s.cachedVer = ver
			return ver
		}
	}
	return s.spec.Version
}

// GetRunningState inspects the PID file and process table.
func (s *Service) GetRunningState() (bool, int) {
	return s.checkRunningState(s.pidPath)
}

// GetClientRunningState inspects the client PID file and process table.
func (s *Service) GetClientRunningState() (bool, int) {
	return s.checkRunningState(s.clientPidPath)
}

func (s *Service) checkRunningState(pidPath string) (bool, int) {
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return false, 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return false, 0
	}

	if !childproc.IsAlive(pid) {
		_ = os.Remove(pidPath)
		return false, 0
	}

	if !childproc.MatchesBinary(pid, s.binPath) && !childproc.MatchesBinary(pid, "openflux") {
		_ = os.Remove(pidPath)
		return false, 0
	}

	return true, pid
}

// AutoDiscoverFreeTun finds the first free OpkgTunN and opkgtunN by scanning system interfaces.
// Indices 0 and 1 are reserved exclusively for router mode (FakeIP / PolicyTun).
func AutoDiscoverFreeTun(preferred string) (string, string) {
	existing := make(map[int]bool)
	if ifaces, err := net.Interfaces(); err == nil {
		for _, iface := range ifaces {
			for _, prefix := range []string{"OpkgTun", "opkgtun"} {
				if strings.HasPrefix(iface.Name, prefix) {
					numStr := strings.TrimPrefix(iface.Name, prefix)
					if n, err := strconv.Atoi(numStr); err == nil && n >= 0 {
						existing[n] = true
					}
				}
			}
		}
	}
	if preferred != "" {
		for _, prefix := range []string{"OpkgTun", "opkgtun"} {
			if strings.HasPrefix(preferred, prefix) {
				numStr := strings.TrimPrefix(preferred, prefix)
				if n, err := strconv.Atoi(numStr); err == nil && n >= 2 {
					if !existing[n] {
						return fmt.Sprintf("OpkgTun%d", n), fmt.Sprintf("opkgtun%d", n)
					}
				}
			}
		}
	}
	for i := 10; i <= 64; i++ {
		if !existing[i] {
			return fmt.Sprintf("OpkgTun%d", i), fmt.Sprintf("opkgtun%d", i)
		}
	}
	for i := 9; i >= 2; i-- {
		if !existing[i] {
			return fmt.Sprintf("OpkgTun%d", i), fmt.Sprintf("opkgtun%d", i)
		}
	}
	return "OpkgTun10", "opkgtun10"
}

func (s *Service) allocateTunLocked(ctx context.Context, preferred string) (string, string) {
	if s.routerCmds != nil {
		ndms, tun, err := s.routerCmds.AllocateTunInterface(ctx, preferred)
		if err == nil && ndms != "" && tun != "" {
			return ndms, tun
		}
	}
	return AutoDiscoverFreeTun(preferred)
}

func (s *Service) ensureClientTunAllocatedLocked(ctx context.Context) {
	inbound := strings.ToLower(strings.TrimSpace(s.clientConfig.InboundMode))
	if inbound != "" && inbound != "tun" {
		return
	}
	preferred := strings.TrimSpace(s.clientConfig.NdmsIface)
	if idx, ok := opkgTunNum(preferred); ok && idx < 2 {
		preferred = ""
	}
	ndms, tun := s.allocateTunLocked(ctx, preferred)
	if ndms != "" && tun != "" {
		s.clientConfig.NdmsIface = ndms
		s.clientConfig.TunIface = tun
	}
}

func normalizedInboundMode(raw string) string {
	m := strings.ToLower(strings.TrimSpace(raw))
	if m == "" {
		return "tun"
	}
	return m
}

func (s *Service) isOwnedClientInterfaceLocked(iface, expectedPrevIface string) bool {
	iface = strings.TrimSpace(iface)
	if iface == "" {
		return false
	}
	idx, ok := opkgTunNum(iface)
	if !ok || idx < 2 {
		return false
	}
	if srvIface := strings.TrimSpace(s.config.NdmsIface); srvIface != "" && strings.EqualFold(iface, srvIface) {
		return false
	}
	if strings.TrimSpace(expectedPrevIface) == "" || !strings.EqualFold(iface, strings.TrimSpace(expectedPrevIface)) {
		return false
	}
	if s.tunnelStore != nil {
		if rec, err := s.tunnelStore.Get("openflux-client"); err == nil && rec != nil {
			if recIface := strings.TrimSpace(rec.RawNdmsIface); recIface != "" && !strings.EqualFold(recIface, iface) {
				return false
			}
		}
		if tunnels, err := s.tunnelStore.List(); err == nil {
			for _, t := range tunnels {
				if t.ID != "openflux-client" && strings.EqualFold(strings.TrimSpace(t.RawNdmsIface), iface) {
					return false
				}
			}
		}
	}
	return true
}

func (s *Service) getClientStatusLocked() ClientStatus {
	installed := s.IsInstalled()
	if !s.hasClientConfigLocked() {
		return ClientStatus{
			Installed:   installed,
			Running:     false,
			InboundMode: "",
			NdmsIface:   "",
			TunIface:    "",
			TunAddr:     "",
			Policy:      "",
			Socks5Port:  DefaultClientSocks5Port,
			Socks5URI:   "",
			HttpURI:     "",
			Error:       "",
			Config:      nil,
			LogTail:     "",
		}
	}
	running, pid := s.GetClientRunningState()
	var uptimeS int64
	if running {
		startedAt := s.clientStartedAt
		if startedAt.IsZero() {
			if fi, err := os.Stat(s.clientPidPath); err == nil {
				startedAt = fi.ModTime()
			}
		}
		if !startedAt.IsZero() {
			uptimeS = int64(time.Since(startedAt).Seconds())
		}
		if uptimeS < 1 {
			uptimeS = 1
		}
	}

	inboundMode := strings.ToLower(strings.TrimSpace(s.clientConfig.InboundMode))
	if inboundMode == "" {
		inboundMode = "tun"
	}
	ndmsIface := strings.TrimSpace(s.clientConfig.NdmsIface)
	tunIface := strings.TrimSpace(s.clientConfig.TunIface)
	if idx, ok := opkgTunNum(ndmsIface); ok && idx < 2 {
		ndmsIface = ""
		tunIface = ""
	}
	if (inboundMode == "tun" || inboundMode == "") && (ndmsIface == "" || tunIface == "") {
		nd, tu := AutoDiscoverFreeTun("")
		if ndmsIface == "" {
			ndmsIface = nd
		}
		if tunIface == "" {
			tunIface = tu
		}
	}
	tunAddr := strings.TrimSpace(s.clientConfig.TunAddr)
	if tunAddr == "" {
		tunAddr = "10.10.10.2/24"
	}
	policy := strings.TrimSpace(s.clientConfig.Policy)

	var clientCfg *PublicClientConfig
	if s.hasClientConfigLocked() {
		pub := s.clientConfig.Public()
		clientCfg = &pub
	}
	logTail := s.getFileLogTailLocked(s.clientLogPath, 100)
	errStr := s.clientLastErr
	if running {
		if inboundMode == "tun" {
			if strings.Contains(logTail, "Tunnel active") || strings.Contains(logTail, "utun interface:") {
				errStr = ""
			}
		} else if strings.Contains(logTail, "[SOCKS5] Listening on") {
			errStr = ""
		}
	}

	return ClientStatus{
		Installed:   installed,
		Running:     running,
		PID:         pid,
		UptimeS:     uptimeS,
		InboundMode: inboundMode,
		NdmsIface:   ndmsIface,
		TunIface:    tunIface,
		TunAddr:     tunAddr,
		Policy:      policy,
		Socks5Port:  s.clientConfig.Socks5Port(),
		Socks5URI:   s.clientConfig.Socks5URI(),
		HttpURI:     s.clientConfig.HttpURI(),
		Error:       errStr,
		Config:      clientCfg,
		LogTail:     logTail,
	}
}

// GetClientStatus returns the current OpenFlux client status.
func (s *Service) GetClientStatus() ClientStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getClientStatusLocked()
}

// GetStatus returns the current service status (both server and client).
func (s *Service) GetStatus(ctx context.Context) Status {
	s.mu.Lock()
	defer s.mu.Unlock()

	installed := s.IsInstalled()
	running, pid := s.GetRunningState()

	var uptimeS int64
	if running {
		startedAt := s.startedAt
		if startedAt.IsZero() {
			if fi, err := os.Stat(s.pidPath); err == nil {
				startedAt = fi.ModTime()
			}
		}
		if !startedAt.IsZero() {
			uptimeS = int64(time.Since(startedAt).Seconds())
		}
		if uptimeS < 1 {
			uptimeS = 1
		}
	}

	host := ""
	if s.getPublicHost != nil {
		host = s.getPublicHost(ctx)
	}

	link, linkErr := GenerateLink(s.config, host)

	supported := IsArchSupported(s.arch)
	pubCfg := s.config.Public()
	clientSt := s.getClientStatusLocked()
	ver := s.detectInstalledVersionLocked(installed)

	return Status{
		Installed:     installed,
		Running:       running,
		PID:           pid,
		UptimeS:       uptimeS,
		Version:       ver,
		LatestVersion: s.spec.Version,
		ArchSupported: supported,
		Binary:        s.binPath,
		Arch:          s.arch,
		LinkAvailable: linkErr == nil && strings.TrimSpace(link) != "",
		Error:         s.lastErr,
		Config:        &pubCfg,
		LogTail:       s.getLogTailLocked(100),
		Client:        &clientSt,
	}
}

// GetShareLink generates the current openflux://v1/... client profile link explicitly on request.
func (s *Service) GetShareLink(ctx context.Context) (string, error) {
	s.mu.Lock()
	cfg := s.config
	getHost := s.getPublicHost
	s.mu.Unlock()

	host := ""
	if getHost != nil {
		host = getHost(ctx)
	}
	return GenerateLink(cfg, host)
}

// SaveConfig validates and saves the configuration, applying changes transactionally with rollback on failure.
func (s *Service) SaveConfig(ctx context.Context, cfg Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	oldConfig := s.config
	wasRunning, _ := s.GetRunningState()

	cfg = mergeConfigSecrets(cfg, oldConfig)
	cfg.NormalizeURLs()

	if strings.EqualFold(strings.TrimSpace(cfg.Mode), "l3") {
		preferred := strings.TrimSpace(cfg.NdmsIface)
		if idx, ok := opkgTunNum(preferred); ok && idx < 2 {
			preferred = ""
		}
		ndms, tun := s.allocateTunLocked(ctx, preferred)
		if ndms != "" && tun != "" {
			cfg.NdmsIface = ndms
			cfg.TunIface = tun
		}
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	s.config = cfg
	if err := s.saveConfigLocked(); err != nil {
		s.config = oldConfig
		return err
	}

	var opErr error
	if wasRunning {
		if !s.config.Enabled {
			opErr = s.stopProcessForConfig(ctx, oldConfig)
		} else {
			if stopErr := s.stopProcessForConfig(ctx, oldConfig); stopErr != nil {
				opErr = stopErr
			} else {
				time.Sleep(200 * time.Millisecond)
				opErr = s.startProcess(ctx)
			}
		}
	} else if s.config.Enabled && s.IsInstalled() {
		opErr = s.startProcess(ctx)
	}

	if opErr != nil {
		s.config = oldConfig
		_ = s.saveConfigLocked()
		if wasRunning {
			_ = s.startProcess(ctx)
		} else if stillRunning, _ := s.GetRunningState(); stillRunning {
			_ = s.stopProcessForConfig(ctx, cfg)
		}
		return opErr
	}

	if strings.TrimSpace(cfg.MailruCookies) != strings.TrimSpace(oldConfig.MailruCookies) {
		clearStaleCookieStores(s.cookieStorePath, filepath.Join(s.openfluxDir, "cookies-yandex.json"))
	}

	return nil
}

// Start launches the OpenFlux process and persists Enabled=true atomically.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	prevErr := s.lastErr
	if err := s.startProcess(ctx); err != nil {
		return err
	}
	s.config.Enabled = true
	if err := s.saveConfigLocked(); err != nil {
		_ = s.stopProcess(ctx)
		s.config.Enabled = false
		s.lastErr = prevErr
		return fmt.Errorf("не удалось сохранить конфигурацию: %w", err)
	}
	return nil
}

// Stop stops the OpenFlux process and persists Enabled=false atomically.
func (s *Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	prevEnabled := s.config.Enabled
	if err := s.stopProcess(ctx); err != nil {
		return err
	}
	s.config.Enabled = false
	if err := s.saveConfigLocked(); err != nil {
		s.config.Enabled = prevEnabled
		if prevEnabled && s.IsInstalled() {
			_ = s.startProcess(ctx)
		}
		return fmt.Errorf("не удалось сохранить конфигурацию: %w", err)
	}
	return nil
}

// Restart restarts the OpenFlux process.
func (s *Service) Restart(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.restartProcess(ctx)
}

func (s *Service) rollbackNewClientIfaceLocked(ctx context.Context, newCfg ClientConfig, oldIface string) {
	if s.routerCmds == nil {
		return
	}
	newIface := strings.TrimSpace(newCfg.NdmsIface)
	if newIface == "" || strings.EqualFold(newIface, strings.TrimSpace(oldIface)) {
		return
	}
	idx, ok := opkgTunNum(newIface)
	if !ok || idx < 2 || strings.EqualFold(newIface, strings.TrimSpace(s.config.NdmsIface)) {
		return
	}
	if s.tunnelStore != nil {
		if tunnels, err := s.tunnelStore.List(); err == nil {
			for _, t := range tunnels {
				if t.ID != "openflux-client" && strings.EqualFold(strings.TrimSpace(t.RawNdmsIface), newIface) {
					return
				}
			}
		}
	}
	if np := strings.TrimSpace(newCfg.Policy); np != "" && np != "none" {
		_ = s.routerCmds.DenyPolicy(ctx, np, newIface)
	}
	_ = s.routerCmds.DownInterface(ctx, newIface)
	_ = s.routerCmds.DeleteInterface(ctx, newIface)
}

// SaveClientConfig validates and saves the client configuration transactionally,
// applying router interface/policy cleanup only after persistence and runtime updates succeed.
func (s *Service) SaveClientConfig(ctx context.Context, cfg ClientConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	oldConfig := s.clientConfig
	wasRunning, _ := s.GetClientRunningState()
	oldIface := strings.TrimSpace(oldConfig.NdmsIface)
	oldPolicy := strings.TrimSpace(oldConfig.Policy)
	oldInbound := normalizedInboundMode(oldConfig.InboundMode)

	cfg = mergeClientConfigSecrets(cfg, oldConfig)
	cfg.NormalizeURLs()

	if strings.TrimSpace(cfg.Socks5Listen) == "" {
		cfg.Socks5Listen = fmt.Sprintf("127.0.0.1:%d", DefaultClientSocks5Port)
	}
	if strings.TrimSpace(cfg.Name) == "" {
		cfg.Name = "OpenFlux Выход"
	}

	inbound := normalizedInboundMode(cfg.InboundMode)
	if inbound == "tun" {
		preferred := strings.TrimSpace(cfg.NdmsIface)
		if idx, ok := opkgTunNum(preferred); ok && idx < 2 {
			preferred = ""
		}
		ndms, tun := s.allocateTunLocked(ctx, preferred)
		if ndms != "" && tun != "" {
			cfg.NdmsIface = ndms
			cfg.TunIface = tun
		}
		if strings.TrimSpace(cfg.TunAddr) == "" {
			cfg.TunAddr = "10.10.10.2/24"
		}
		if cfg.TunMTU <= 0 {
			cfg.TunMTU = 1280
		}
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	// Persist configuration atomically first before any router interface or policy mutations.
	s.clientConfig = cfg
	if err := s.saveClientConfigLocked(); err != nil {
		s.clientConfig = oldConfig
		return err
	}

	var opErr error
	if wasRunning {
		if !s.clientConfig.Enabled {
			opErr = s.stopClientProcessForIface(ctx, oldIface, oldIface)
		} else {
			if stopErr := s.stopClientProcessForIface(ctx, oldIface, oldIface); stopErr != nil {
				opErr = stopErr
			} else {
				time.Sleep(200 * time.Millisecond)
				opErr = s.startClientProcess(ctx, true)
			}
		}
	} else if s.clientConfig.Enabled && s.IsInstalled() {
		opErr = s.startClientProcess(ctx, true)
	}

	if opErr != nil {
		s.rollbackNewClientIfaceLocked(ctx, s.clientConfig, oldIface)
		s.clientConfig = oldConfig
		_ = s.saveClientConfigLocked()
		if wasRunning {
			_ = s.startClientProcess(ctx, true)
		}
		s.syncClientMirrorLocked()
		return opErr
	}

	newIface := strings.TrimSpace(s.clientConfig.NdmsIface)
	newPolicy := strings.TrimSpace(s.clientConfig.Policy)
	if s.routerCmds != nil && oldIface != "" && s.isOwnedClientInterfaceLocked(oldIface, oldIface) {
		ifaceChanged := !strings.EqualFold(oldIface, newIface)
		switchedToSocks := inbound == "socks5" && oldInbound != "socks5"
		if oldPolicy != "" && oldPolicy != "none" && (oldPolicy != newPolicy || ifaceChanged || switchedToSocks) {
			_ = s.routerCmds.DenyPolicy(ctx, oldPolicy, oldIface)
		}
		if switchedToSocks || ifaceChanged {
			_ = s.routerCmds.DownInterface(ctx, oldIface)
			_ = s.routerCmds.DeleteInterface(ctx, oldIface)
		}
	}

	if strings.TrimSpace(cfg.MailruCookies) != strings.TrimSpace(oldConfig.MailruCookies) {
		clearStaleCookieStores(s.clientCookieStorePath)
	}

	s.syncClientMirrorLocked()
	return nil
}

// StartClient launches the OpenFlux client process and persists Enabled=true atomically.
func (s *Service) StartClient(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.startClientProcess(ctx, true); err != nil {
		return err
	}
	s.clientConfig.Enabled = true
	if err := s.saveClientConfigLocked(); err != nil {
		_ = s.stopClientProcess(ctx)
		s.clientConfig.Enabled = false
		s.syncClientMirrorLocked()
		return fmt.Errorf("не удалось сохранить конфигурацию клиента: %w", err)
	}
	s.syncClientMirrorLocked()
	return nil
}

// StopClient stops the OpenFlux client process and persists Enabled=false atomically.
func (s *Service) StopClient(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	prevEnabled := s.clientConfig.Enabled
	if err := s.stopClientProcess(ctx); err != nil {
		return err
	}
	s.clientConfig.Enabled = false
	s.clientLastErr = ""
	if err := s.saveClientConfigLocked(); err != nil {
		s.clientConfig.Enabled = prevEnabled
		if prevEnabled && s.IsInstalled() {
			_ = s.startClientProcess(ctx, true)
		}
		s.syncClientMirrorLocked()
		return fmt.Errorf("не удалось сохранить конфигурацию клиента: %w", err)
	}
	s.syncClientMirrorLocked()
	return nil
}

// DeleteClient completely stops the client, cleans up owned OpkgTun/NDMS interface, and resets client config.
func (s *Service) DeleteClient(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_ = s.stopClientProcess(ctx)

	if s.routerCmds != nil {
		ndmsIface := strings.TrimSpace(s.clientConfig.NdmsIface)
		if ndmsIface != "" && s.isOwnedClientInterfaceLocked(ndmsIface, ndmsIface) {
			if s.clientConfig.Policy != "" {
				_ = s.routerCmds.DenyPolicy(ctx, s.clientConfig.Policy, ndmsIface)
			}
			_ = s.routerCmds.DownInterface(ctx, ndmsIface)
			_ = s.routerCmds.DeleteInterface(ctx, ndmsIface)
		}
	}

	if s.tunnelStore != nil {
		_ = s.tunnelStore.Delete("openflux-client")
	}

	_ = os.Remove(s.clientCfgPath)
	secureZeroAndRemoveFile(s.clientKeyPath)
	secureZeroAndRemoveFile(s.clientMailruCookiesPath)
	secureZeroAndRemoveFile(s.clientAvitoCookiesPath)
	_ = os.Remove(s.clientPidPath)
	s.clientConfig = ClientConfig{}
	s.clientStartedAt = time.Time{}
	s.clientLastErr = ""

	return nil
}

func (s *Service) syncClientMirrorLocked() {
	if s.tunnelStore == nil {
		return
	}
	inbound := strings.ToLower(strings.TrimSpace(s.clientConfig.InboundMode))
	if inbound == "" {
		inbound = "tun"
	}
	if inbound != "tun" {
		if s.tunnelStore.Exists("openflux-client") {
			_ = s.tunnelStore.Delete("openflux-client")
		}
		return
	}

	name := strings.TrimSpace(s.clientConfig.Name)
	if name == "" {
		name = "OpenFlux Выход"
	}

	tunAddr := strings.TrimSpace(s.clientConfig.TunAddr)
	if tunAddr == "" {
		tunAddr = "10.10.10.2/24"
	}
	mtu := s.clientConfig.TunMTU
	if mtu <= 0 {
		mtu = 1280
	}

	endpoint := "cloud:openflux"
	if strings.TrimSpace(s.clientConfig.DirectDial) != "" {
		endpoint = strings.TrimSpace(s.clientConfig.DirectDial)
	} else if len(s.clientConfig.Transports) > 0 {
		endpoint = "cloud:" + strings.Join(s.clientConfig.Transports, "+")
	}

	mutate := func(rec *storage.AWGTunnel) {
		rec.ID = "openflux-client"
		rec.Type = "awg"
		rec.Backend = "wdtt-raw"
		rec.OpenFluxClientID = "default"
		rec.Name = name
		rec.RawNdmsIface = strings.TrimSpace(s.clientConfig.NdmsIface)
		rec.RawKernelIface = strings.TrimSpace(s.clientConfig.TunIface)
		rec.Enabled = s.clientConfig.Enabled
		rec.DefaultRoute = s.clientConfig.DefaultRoute
		rec.Interface = storage.AWGInterface{
			Address: tunAddr,
			MTU:     mtu,
		}
		rec.Peer = storage.AWGPeer{
			Endpoint:   endpoint,
			AllowedIPs: []string{"0.0.0.0/0"},
		}
		if rec.ConnectivityCheck == nil || rec.ConnectivityCheck.Method == "" {
			rec.ConnectivityCheck = &storage.ConnectivityCheckConfig{Method: "http"}
		}
	}

	if !s.tunnelStore.Exists("openflux-client") {
		rec := storage.AWGTunnel{
			Type:      "awg",
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		}
		mutate(&rec)
		_ = s.tunnelStore.Create(&rec)
	} else {
		_ = s.tunnelStore.Update("openflux-client", func(rec *storage.AWGTunnel) error {
			mutate(rec)
			return nil
		})
	}
}

// RestartClient restarts the OpenFlux client process.
func (s *Service) RestartClient(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	err := s.restartClientProcess(ctx)
	if s.hasClientConfigLocked() {
		s.syncClientMirrorLocked()
	}
	return err
}

// ParseClientLink decodes an openflux://v1/... link and builds a ClientConfig preserving local listen settings.
func (s *Service) ParseClientLink(link string) (ShareConfig, ClientConfig, error) {
	sc, err := Decode(link)
	if err != nil {
		return ShareConfig{}, ClientConfig{}, err
	}
	s.mu.Lock()
	base := s.clientConfig
	s.mu.Unlock()

	cfg := ClientConfigFromShare(sc, base)
	return sc, cfg, nil
}

// Shutdown stops running server and client processes on daemon exit without clearing their Enabled flags.
func (s *Service) Shutdown(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()

	_ = s.stopProcess(ctx)
	_ = s.stopClientProcess(ctx)
}

// StartIfEnabled starts server and client processes if configured as enabled and installed.
func (s *Service) StartIfEnabled(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.IsInstalled() {
		return nil
	}
	var firstErr error
	if s.config.Enabled {
		if err := s.startProcess(ctx); err != nil {
			firstErr = err
		}
	}
	if s.clientConfig.Enabled {
		if err := s.startClientProcess(ctx, true); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if s.hasClientConfigLocked() {
		s.syncClientMirrorLocked()
	}
	return firstErr
}

func (s *Service) cleanupServerRuntimeFiles() {
	secureZeroAndRemoveFile(s.keyPath)
	secureZeroAndRemoveFile(s.mailruCookiesPath)
	secureZeroAndRemoveFile(s.avitoCookiesPath)
}

func (s *Service) cleanupClientRuntimeFiles() {
	secureZeroAndRemoveFile(s.clientKeyPath)
	secureZeroAndRemoveFile(s.clientMailruCookiesPath)
	secureZeroAndRemoveFile(s.clientAvitoCookiesPath)
}

func formatAvitoCookiesForFile(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	var netscapeLines []string
	for _, line := range strings.Split(strings.ReplaceAll(trimmed, "\r\n", "\n"), "\n") {
		l := strings.TrimSpace(line)
		if l == "" {
			continue
		}
		check := strings.TrimPrefix(l, "#HttpOnly_")
		if strings.Contains(check, "\t") && len(strings.Split(check, "\t")) >= 7 {
			netscapeLines = append(netscapeLines, l)
		}
	}
	if len(netscapeLines) > 0 {
		return strings.Join(netscapeLines, "\n") + "\n"
	}

	if strings.HasPrefix(trimmed, "[") {
		var items []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal([]byte(trimmed), &items); err == nil {
			var out strings.Builder
			for _, it := range items {
				name := strings.TrimSpace(it.Name)
				if name == "" {
					continue
				}
				fmt.Fprintf(&out, ".avito.ru\tTRUE\t/\tTRUE\t0\t%s\t%s\n", name, strings.TrimSpace(it.Value))
			}
			if out.Len() > 0 {
				return out.String()
			}
		}
	}

	header := strings.TrimSpace(trimmed)
	if len(header) >= len("Cookie:") && strings.EqualFold(header[:len("Cookie:")], "Cookie:") {
		header = strings.TrimSpace(header[len("Cookie:"):])
	}
	var out strings.Builder
	for _, seg := range strings.FieldsFunc(header, func(r rune) bool { return r == ';' || r == '\n' }) {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		name, val, ok := strings.Cut(seg, "=")
		name = strings.TrimSpace(name)
		val = strings.TrimSpace(val)
		if ok && name != "" {
			fmt.Fprintf(&out, ".avito.ru\tTRUE\t/\tTRUE\t0\t%s\t%s\n", name, val)
		}
	}
	if out.Len() > 0 {
		return out.String()
	}
	return trimmed + "\n"
}

func (s *Service) startProcess(ctx context.Context) error {
	if !s.IsInstalled() {
		return ErrNotInstalled
	}

	running, _ := s.GetRunningState()
	if running {
		s.applyFirewall(ctx)
		return nil
	}

	if err := s.config.Validate(); err != nil {
		return fmt.Errorf("ошибка валидации настроек: %w", err)
	}

	s.lastErr = ""
	_ = os.MkdirAll(s.openfluxDir, 0700)
	_ = os.Chmod(s.openfluxDir, 0700)
	if err := atomicWriteFile(s.keyPath, []byte(strings.TrimSpace(s.config.Secret)+"\n"), 0600); err != nil {
		return fmt.Errorf("не удалось записать ключ шифрования: %w", err)
	}

	_ = os.Remove(s.pidPath)

	if s.policyResolver != nil && s.config.Policy != "" && s.config.Policy != "none" {
		if ip, err := s.policyResolver(ctx, s.config.Policy); err == nil && ip != "" {
			s.config.PolicyIP = ip
		} else {
			s.config.PolicyIP = ""
		}
	} else {
		s.config.PolicyIP = ""
	}

	if strings.EqualFold(strings.TrimSpace(s.config.Mode), "l3") {
		ndmsIface := strings.TrimSpace(s.config.NdmsIface)
		idx, ok := opkgTunNum(ndmsIface)
		if ndmsIface == "" || (ok && idx < 2) {
			ndms, tun := s.allocateTunLocked(ctx, "")
			if ndms != "" && tun != "" {
				s.config.NdmsIface = ndms
				s.config.TunIface = tun
				_ = s.saveConfigLocked()
				ndmsIface = ndms
			}
		}
		if ndmsIface == "" || ndmsIface == "OpkgTun0" || ndmsIface == "OpkgTun1" {
			s.cleanupServerRuntimeFiles()
			return fmt.Errorf("недопустимый или невыделенный интерфейс OpkgTun для exit-сервера: %q", ndmsIface)
		}
		if s.routerCmds != nil {
			desc := "OpenFlux Server"
			if name := strings.TrimSpace(s.config.Name); name != "" {
				desc = "OpenFlux " + name
			}
			mtu := s.config.TunMTU
			if mtu <= 0 {
				mtu = 1300
			}
			if err := s.routerCmds.CreateInterface(ctx, ndmsIface, desc, mtu, "", s.config.ExposeToPolicies, false); err != nil {
				s.cleanupServerRuntimeFiles()
				return fmt.Errorf("ошибка создания интерфейса %s в NDMS: %w", ndmsIface, err)
			}
		}
	}

	buildOpts, err := prepareRuntimeCookieArgs(
		s.cookieStorePath,
		s.mailruCookiesPath, s.config.MailruCookies,
		s.avitoCookiesPath, s.config.AvitoCookies,
	)
	if err != nil {
		s.cleanupServerRuntimeFiles()
		return err
	}
	args := s.config.BuildArgsWithOptions(s.keyPath, buildOpts)

	logFile, err := os.OpenFile(s.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		s.cleanupServerRuntimeFiles()
		return fmt.Errorf("не удалось открыть файл журнала: %w", err)
	}
	_ = os.Chmod(s.logPath, 0600)

	var logOffset int64
	if fi, stErr := logFile.Stat(); stErr == nil {
		logOffset = fi.Size()
	}

	cmd := exec.Command(s.binPath, args...)
	cmd.Dir = s.openfluxDir
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		s.cleanupServerRuntimeFiles()
		if strings.EqualFold(strings.TrimSpace(s.config.Mode), "l3") && s.routerCmds != nil {
			ndmsIface := strings.TrimSpace(s.config.NdmsIface)
			if ndmsIface != "" && ndmsIface != "OpkgTun0" && ndmsIface != "OpkgTun1" {
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_ = s.routerCmds.DownInterface(cleanupCtx, ndmsIface)
				cancel()
			}
		}
		s.lastErr = fmt.Sprintf("не удалось запустить openflux: %v", err)
		return fmt.Errorf("не удалось запустить openflux: %w", err)
	}

	pid := cmd.Process.Pid
	_ = atomicWriteFile(s.pidPath, []byte(strconv.Itoa(pid)+"\n"), 0600)
	s.startedAt = time.Now()

	waitDone := make(chan error, 1)
	go func() {
		waitErr := cmd.Wait()
		_ = logFile.Close()
		waitDone <- waitErr
	}()

	select {
	case waitErr := <-waitDone:
		s.cleanupServerRuntimeFiles()
		_ = os.Remove(s.pidPath)
		if strings.EqualFold(strings.TrimSpace(s.config.Mode), "l3") && s.routerCmds != nil {
			ndmsIface := strings.TrimSpace(s.config.NdmsIface)
			if ndmsIface != "" && ndmsIface != "OpkgTun0" && ndmsIface != "OpkgTun1" {
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_ = s.routerCmds.DownInterface(cleanupCtx, ndmsIface)
				cancel()
			}
		}
		msg := extractLastErrorFromLog(readFileSliceFrom(s.logPath, logOffset))
		if msg == "" && waitErr != nil {
			msg = fmt.Sprintf("процесс завершился сразу после запуска: %v", waitErr)
		} else if msg == "" {
			msg = "процесс завершился сразу после запуска"
		}
		s.lastErr = msg
		return fmt.Errorf("%s", msg)
	case <-time.After(150 * time.Millisecond):
	}

	s.applyFirewall(ctx)
	return nil
}

func readFileSliceFrom(path string, offset int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return ""
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, 64*1024))
	if err != nil {
		return ""
	}
	return string(data)
}

func clearStaleCookieStores(paths ...string) {
	for _, p := range paths {
		if p == "" {
			continue
		}
		_ = os.Remove(p)
		_ = os.Remove(p + ".incoming")
	}
}

func prepareRuntimeCookieArgs(cookieStorePath, mailruCookiesPath, rawMailruCookies, avitoCookiesPath, rawAvitoCookies string) (BuildArgsOptions, error) {
	opts := BuildArgsOptions{
		CookieStoreFile: cookieStorePath,
	}
	if mc := strings.TrimSpace(rawMailruCookies); mc != "" {
		if err := atomicWriteFile(mailruCookiesPath, []byte(mc+"\n"), 0600); err != nil {
			return BuildArgsOptions{}, fmt.Errorf("не удалось записать cookies Mail.ru: %w", err)
		}
		opts.MailruCookiesFile = mailruCookiesPath
	} else {
		secureZeroAndRemoveFile(mailruCookiesPath)
	}
	if formatted := formatAvitoCookiesForFile(rawAvitoCookies); formatted != "" {
		if err := atomicWriteFile(avitoCookiesPath, []byte(formatted), 0600); err != nil {
			secureZeroAndRemoveFile(mailruCookiesPath)
			return BuildArgsOptions{}, fmt.Errorf("не удалось записать cookies Avito: %w", err)
		}
		opts.AvitoCookiesFile = avitoCookiesPath
	} else {
		secureZeroAndRemoveFile(avitoCookiesPath)
	}
	return opts, nil
}

func (s *Service) stopProcess(ctx context.Context) error {
	return s.stopProcessForConfig(ctx, s.config)
}

func (s *Service) stopProcessForConfig(ctx context.Context, cfg Config) error {
	s.cleanupServerRuntimeFiles()
	s.lastErr = ""
	running, pid := s.GetRunningState()
	if !running {
		_ = os.Remove(s.pidPath)
		s.removeFirewall(ctx)
		if strings.EqualFold(strings.TrimSpace(cfg.Mode), "l3") && s.routerCmds != nil {
			ndmsIface := strings.TrimSpace(cfg.NdmsIface)
			if ndmsIface != "" && ndmsIface != "OpkgTun0" && ndmsIface != "OpkgTun1" {
				_ = s.routerCmds.DownInterface(ctx, ndmsIface)
			}
		}
		return nil
	}

	s.removeFirewall(ctx)
	terminatePID(pid, s.pidPath)

	if strings.EqualFold(strings.TrimSpace(cfg.Mode), "l3") && s.routerCmds != nil {
		ndmsIface := strings.TrimSpace(cfg.NdmsIface)
		if ndmsIface != "" && ndmsIface != "OpkgTun0" && ndmsIface != "OpkgTun1" {
			_ = s.routerCmds.DownInterface(ctx, ndmsIface)
		}
	}
	return nil
}

func (s *Service) restartProcess(ctx context.Context) error {
	if err := s.stopProcess(ctx); err != nil {
		return err
	}
	time.Sleep(200 * time.Millisecond)
	return s.startProcess(ctx)
}

func isHelpOrBannerLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return true
	}
	if strings.HasPrefix(trimmed, "-") {
		return true
	}
	if strings.HasPrefix(trimmed, "written by ") || strings.HasPrefix(trimmed, "OpenFlux — ") {
		return true
	}
	switch trimmed {
	case "USAGE", "ROLE", "TRANSPORT", "TRANSPORTS", "INBOUND", "MODE", "TRANSPORT MODIFIERS", "BENCHMARK", "LOGGING", "DEPRECATED":
		return true
	}
	if strings.HasPrefix(trimmed, "openflux --") || strings.Contains(trimmed, "[OPTIONS]") {
		return true
	}
	return false
}

func extractLastErrorFromLog(logTail string) string {
	lines := strings.Split(strings.TrimSpace(logTail), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if isHelpOrBannerLine(line) {
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "flag provided but not defined:") ||
			strings.HasPrefix(lower, "flag:") ||
			strings.Contains(lower, "is only supported on") ||
			strings.Contains(lower, "bind: address already in use") ||
			strings.Contains(lower, "address already in use") ||
			strings.Contains(lower, "panic:") ||
			strings.Contains(lower, "fatal:") ||
			strings.Contains(lower, "[error]") ||
			strings.Contains(lower, "failed to") ||
			strings.Contains(lower, "error:") ||
			strings.Contains(lower, "handshake failed") ||
			strings.Contains(lower, "no such host") ||
			strings.Contains(lower, "i/o timeout") ||
			strings.Contains(lower, "server misbehaving") ||
			strings.Contains(lower, "tls:") ||
			strings.Contains(lower, "connection refused") ||
			strings.Contains(lower, "network is unreachable") ||
			strings.Contains(lower, "context deadline exceeded") {
			return line
		}
	}

	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if isHelpOrBannerLine(line) {
			continue
		}
		lower := strings.ToLower(line)
		if strings.Contains(lower, "error") || strings.Contains(lower, "failed") {
			return line
		}
	}

	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !isHelpOrBannerLine(line) {
			return line
		}
	}

	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "flag provided but not defined:") {
			return trimmed
		}
	}

	return ""
}

func (s *Service) startClientProcess(ctx context.Context, resetStopCh bool) error {
	if !s.IsInstalled() {
		return ErrNotInstalled
	}

	running, _ := s.GetClientRunningState()
	if running {
		return nil
	}

	if err := s.clientConfig.Validate(); err != nil {
		return fmt.Errorf("ошибка валидации настроек клиента: %w", err)
	}
	if err := s.clientConfig.ValidateForStart(); err != nil {
		return err
	}

	if resetStopCh {
		if s.clientStopCh != nil {
			close(s.clientStopCh)
		}
		s.clientStopCh = make(chan struct{})
		s.clientLastErr = ""
	}
	stopCh := s.clientStopCh
	if stopCh == nil {
		stopCh = make(chan struct{})
		s.clientStopCh = stopCh
	}

	_ = os.MkdirAll(s.openfluxDir, 0700)
	_ = os.Chmod(s.openfluxDir, 0700)
	if strings.TrimSpace(s.clientConfig.Secret) != "" {
		if err := atomicWriteFile(s.clientKeyPath, []byte(strings.TrimSpace(s.clientConfig.Secret)+"\n"), 0600); err != nil {
			return fmt.Errorf("не удалось записать ключ шифрования клиента: %w", err)
		}
	}

	_ = os.Remove(s.clientPidPath)

	inboundMode := strings.ToLower(strings.TrimSpace(s.clientConfig.InboundMode))
	if inboundMode == "" {
		inboundMode = "tun"
	}
	if inboundMode == "tun" {
		s.ensureClientTunAllocatedLocked(ctx)
		if err := s.saveClientConfigLocked(); err != nil {
			s.cleanupClientRuntimeFiles()
			return fmt.Errorf("не удалось сохранить конфигурацию клиента: %w", err)
		}
		s.syncClientMirrorLocked()
	}
	ndmsIface := strings.TrimSpace(s.clientConfig.NdmsIface)
	if inboundMode == "tun" && (ndmsIface == "" || ndmsIface == "OpkgTun0" || ndmsIface == "OpkgTun1") {
		s.cleanupClientRuntimeFiles()
		return fmt.Errorf("недопустимый или невыделенный интерфейс OpkgTun: %q", ndmsIface)
	}
	tunAddr := strings.TrimSpace(s.clientConfig.TunAddr)
	if tunAddr == "" {
		tunAddr = "10.10.10.2/24"
	}
	policy := strings.TrimSpace(s.clientConfig.Policy)

	if inboundMode == "tun" && s.routerCmds != nil {
		desc := "OpenFlux"
		if name := strings.TrimSpace(s.clientConfig.Name); name != "" {
			if strings.HasPrefix(name, "OpenFlux") {
				desc = name
			} else {
				desc = "OpenFlux " + name
			}
		}
		mtu := s.clientConfig.TunMTU
		if mtu <= 0 {
			mtu = 1280
		}
		if err := s.routerCmds.CreateInterface(ctx, ndmsIface, desc, mtu, tunAddr, s.clientConfig.ExposeToPolicies, s.clientConfig.DefaultRoute); err != nil {
			s.cleanupClientRuntimeFiles()
			return fmt.Errorf("не удалось создать интерфейс %s в NDMS: %w", ndmsIface, err)
		}
		if policy != "" && policy != "none" {
			if err := s.routerCmds.PermitPolicy(ctx, policy, ndmsIface); err != nil {
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_ = s.routerCmds.DownInterface(cleanupCtx, ndmsIface)
				cancel()
				s.cleanupClientRuntimeFiles()
				return fmt.Errorf("не удалось привязать интерфейс %s к политике %s: %w", ndmsIface, policy, err)
			}
		}
	}

	buildOpts, err := prepareRuntimeCookieArgs(
		s.clientCookieStorePath,
		s.clientMailruCookiesPath, s.clientConfig.MailruCookies,
		s.clientAvitoCookiesPath, s.clientConfig.AvitoCookies,
	)
	if err != nil {
		s.cleanupClientRuntimeFiles()
		return err
	}
	args := s.clientConfig.BuildArgsWithOptions(s.clientKeyPath, buildOpts)

	openFlags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if resetStopCh {
		openFlags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	} else if fi, stErr := os.Stat(s.clientLogPath); stErr == nil && fi.Size() > 128*1024 {
		openFlags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	}

	logFile, err := os.OpenFile(s.clientLogPath, openFlags, 0600)
	if err != nil {
		s.cleanupClientRuntimeFiles()
		return fmt.Errorf("не удалось открыть файл журнала клиента: %w", err)
	}
	_ = os.Chmod(s.clientLogPath, 0600)

	cmd := exec.Command(s.binPath, args...)
	cmd.Dir = s.openfluxDir
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		s.cleanupClientRuntimeFiles()
		if inboundMode == "tun" && s.routerCmds != nil && ndmsIface != "" && ndmsIface != "OpkgTun0" && ndmsIface != "OpkgTun1" {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = s.routerCmds.DownInterface(cleanupCtx, ndmsIface)
			cancel()
		}
		return fmt.Errorf("не удалось запустить клиент openflux: %w", err)
	}

	pid := cmd.Process.Pid
	_ = atomicWriteFile(s.clientPidPath, []byte(strconv.Itoa(pid)+"\n"), 0600)
	s.clientStartedAt = time.Now()

	waitDone := make(chan error, 1)
	go func(stop <-chan struct{}) {
		waitErr := cmd.Wait()
		_ = logFile.Close()
		waitDone <- waitErr

		s.mu.Lock()
		defer s.mu.Unlock()

		select {
		case <-stop:
			return
		default:
		}

		if waitErr != nil {
			if msg := extractLastErrorFromLog(s.getFileLogTailLocked(s.clientLogPath, 30)); msg != "" {
				s.clientLastErr = msg
			} else {
				s.clientLastErr = fmt.Sprintf("процесс завершился: %v", waitErr)
			}
		}

		if s.clientConfig.Enabled && s.clientConfig.AutoReconnect {
			go func() {
				select {
				case <-stop:
					return
				case <-time.After(5 * time.Second):
				}
				s.mu.Lock()
				defer s.mu.Unlock()
				select {
				case <-stop:
					return
				default:
				}
				if s.clientConfig.Enabled {
					_ = s.startClientProcess(context.Background(), false)
				}
			}()
		}
	}(stopCh)

	if resetStopCh {
		select {
		case waitErr := <-waitDone:
			if s.clientStopCh != nil {
				close(s.clientStopCh)
				s.clientStopCh = nil
			}
			s.cleanupClientRuntimeFiles()
			_ = os.Remove(s.clientPidPath)
			if inboundMode == "tun" && s.routerCmds != nil && ndmsIface != "" && ndmsIface != "OpkgTun0" && ndmsIface != "OpkgTun1" {
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_ = s.routerCmds.DownInterface(cleanupCtx, ndmsIface)
				cancel()
			}
			msg := extractLastErrorFromLog(s.getFileLogTailLocked(s.clientLogPath, 30))
			if msg == "" && waitErr != nil {
				msg = fmt.Sprintf("процесс завершился сразу после запуска: %v", waitErr)
			} else if msg == "" {
				msg = "процесс завершился сразу после запуска"
			}
			s.clientLastErr = msg
			return fmt.Errorf("%s", msg)
		case <-time.After(150 * time.Millisecond):
		}
	}

	return nil
}

func (s *Service) stopClientProcess(ctx context.Context) error {
	iface := strings.TrimSpace(s.clientConfig.NdmsIface)
	return s.stopClientProcessForIface(ctx, iface, iface)
}

func (s *Service) stopClientProcessForIface(ctx context.Context, ndmsIface, expectedPrevIface string) error {
	if s.clientStopCh != nil {
		close(s.clientStopCh)
		s.clientStopCh = nil
	}
	s.cleanupClientRuntimeFiles()

	running, pid := s.GetClientRunningState()
	if running {
		terminatePID(pid, s.clientPidPath)
	} else {
		_ = os.Remove(s.clientPidPath)
	}

	if s.routerCmds != nil {
		ndmsIface = strings.TrimSpace(ndmsIface)
		if ndmsIface != "" && s.isOwnedClientInterfaceLocked(ndmsIface, expectedPrevIface) {
			_ = s.routerCmds.DownInterface(ctx, ndmsIface)
		}
	}

	return nil
}

func (s *Service) restartClientProcess(ctx context.Context) error {
	if err := s.stopClientProcess(ctx); err != nil {
		return err
	}
	time.Sleep(200 * time.Millisecond)
	return s.startClientProcess(ctx, true)
}

func terminatePID(pid int, pidPath string) {
	_ = childproc.Terminate(pid)

	done := make(chan struct{})
	go func() {
		for i := 0; i < 20; i++ {
			if !childproc.IsAlive(pid) {
				close(done)
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		_ = childproc.Kill(pid)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = childproc.Kill(pid)
	}

	_ = os.Remove(pidPath)
}

func secureZeroAndRemoveFile(path string) {
	if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() {
		size := fi.Size()
		if size > 0 {
			zeroes := make([]byte, size)
			_ = os.WriteFile(path, zeroes, 0600)
		}
	}
	_ = os.Remove(path)
}

// ParseStaleNatDeleteArgs parses the output of `iptables -t nat -S POSTROUTING` and returns
// command argument slices for deleting rules tagged with "AWGM_OPENFLUX" whose outgoing interface
// is not in the keepWANs list.
func ParseStaleNatDeleteArgs(iptablesOutput string, keepWANs []string) [][]string {
	keepMap := make(map[string]bool)
	for _, w := range keepWANs {
		if trimmed := strings.TrimSpace(w); trimmed != "" {
			keepMap[trimmed] = true
		}
	}

	var results [][]string
	lines := strings.Split(iptablesOutput, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "AWGM_OPENFLUX") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) <= 2 || fields[0] != "-A" || fields[1] != "POSTROUTING" {
			continue
		}
		wan := ""
		for i, f := range fields {
			if (f == "-o" || f == "--out-interface") && i+1 < len(fields) {
				wan = strings.Trim(fields[i+1], "\"")
				break
			}
		}
		if wan != "" && keepMap[wan] {
			continue
		}
		var delRuleArgs []string
		for _, token := range fields[2:] {
			delRuleArgs = append(delRuleArgs, strings.Trim(token, "\""))
		}
		results = append(results, append([]string{"-t", "nat", "-D", "POSTROUTING"}, delRuleArgs...))
	}
	return results
}

func cleanStaleNatRules(ctx context.Context, keepWANs []string) {
	if runtime.GOOS != "linux" {
		return
	}
	cmd := exec.CommandContext(ctx, "iptables", "-t", "nat", "-S", "POSTROUTING")
	out, err := cmd.Output()
	if err != nil {
		return
	}
	delCmds := ParseStaleNatDeleteArgs(string(out), keepWANs)
	for _, args := range delCmds {
		_ = exec.CommandContext(ctx, "iptables", args...).Run()
	}
}

func (s *Service) applyFirewall(ctx context.Context) {
	if runtime.GOOS != "linux" {
		return
	}
	hasDirect := false
	for _, t := range s.config.Transports {
		if strings.EqualFold(strings.TrimSpace(t), "direct") {
			hasDirect = true
			break
		}
	}

	if s.openedPort > 0 && (!s.config.OpenFirewall || !hasDirect || s.openedPort != s.config.DirectPort()) {
		portStr := strconv.Itoa(s.openedPort)
		for {
			delCmd := exec.CommandContext(ctx, "iptables", "-D", "INPUT", "-p", "tcp", "--dport", portStr, "-j", "ACCEPT")
			if err := delCmd.Run(); err != nil {
				break
			}
		}
		s.openedPort = 0
	}
	if s.config.OpenFirewall && hasDirect {
		port := s.config.DirectPort()
		if port > 0 {
			portStr := strconv.Itoa(port)
			checkCmd := exec.CommandContext(ctx, "iptables", "-C", "INPUT", "-p", "tcp", "--dport", portStr, "-j", "ACCEPT")
			if err := checkCmd.Run(); err != nil {
				addCmd := exec.CommandContext(ctx, "iptables", "-I", "INPUT", "1", "-p", "tcp", "--dport", portStr, "-j", "ACCEPT")
				_ = addCmd.Run()
			}
			s.openedPort = port
		}
	}

	if s.config.NatMode == "internet-only" {
		wans := s.config.StaticNATList()
		cleanStaleNatRules(ctx, wans)
		for _, wan := range wans {
			wan = strings.TrimSpace(wan)
			if wan == "" {
				continue
			}
			checkCmd := exec.CommandContext(ctx, "iptables", "-t", "nat", "-C", "POSTROUTING", "-o", wan, "-m", "comment", "--comment", "AWGM_OPENFLUX", "-j", "MASQUERADE")
			if err := checkCmd.Run(); err != nil {
				addCmd := exec.CommandContext(ctx, "iptables", "-t", "nat", "-I", "POSTROUTING", "1", "-o", wan, "-m", "comment", "--comment", "AWGM_OPENFLUX", "-j", "MASQUERADE")
				_ = addCmd.Run()
			}
		}
	} else {
		cleanStaleNatRules(ctx, nil)
	}

	if s.routerCmds != nil && s.config.Policy != "" && s.config.Policy != "none" && s.config.NdmsIface != "" {
		_ = s.routerCmds.PermitPolicy(ctx, s.config.Policy, s.config.NdmsIface)
	}
}

func (s *Service) removeFirewall(ctx context.Context) {
	if runtime.GOOS != "linux" {
		return
	}
	if s.openedPort > 0 {
		portStr := strconv.Itoa(s.openedPort)
		for {
			delCmd := exec.CommandContext(ctx, "iptables", "-D", "INPUT", "-p", "tcp", "--dport", portStr, "-j", "ACCEPT")
			if err := delCmd.Run(); err != nil {
				break
			}
		}
		s.openedPort = 0
	}

	cleanStaleNatRules(ctx, nil)

	if s.routerCmds != nil && s.config.Policy != "" && s.config.NdmsIface != "" {
		_ = s.routerCmds.DenyPolicy(ctx, s.config.Policy, s.config.NdmsIface)
	}
}

// Install downloads (or copies from local cache), verifies SHA-256, and installs the OpenFlux binary.
func (s *Service) Install(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !IsArchSupported(s.arch) || s.spec.URL == "" {
		return fmt.Errorf("%w: %s", ErrArchUnsupported, s.arch)
	}

	targetPath := s.binPath
	targetDir := filepath.Dir(targetPath)
	dirPerm := os.FileMode(0755)
	if targetDir == s.openfluxDir {
		dirPerm = 0700
	}
	if err := os.MkdirAll(targetDir, dirPerm); err != nil {
		targetDir = s.openfluxDir
		targetPath = filepath.Join(s.openfluxDir, "openflux")
		if err2 := os.MkdirAll(targetDir, 0700); err2 != nil {
			return fmt.Errorf("не удалось создать каталог для openflux: %w", err2)
		}
		_ = os.Chmod(targetDir, 0700)
	}

	if s.freeDisk != nil {
		const minSpaceBytes int64 = 35 * 1024 * 1024
		if free, ok := s.freeDisk(targetDir); ok && free >= 0 && free < minSpaceBytes {
			// Fall back to openfluxDir if /opt/bin partition is tight
			altDir := s.openfluxDir
			if altDir != targetDir {
				if altFree, altOK := s.freeDisk(altDir); altOK && altFree >= minSpaceBytes {
					targetDir = altDir
					targetPath = filepath.Join(altDir, "openflux")
				} else {
					return fmt.Errorf("недостаточно свободного места в %s (%d МБ, требуется >= 35 МБ)", targetDir, free/(1024*1024))
				}
			} else {
				return fmt.Errorf("недостаточно свободного места в %s (%d МБ, требуется >= 35 МБ)", targetDir, free/(1024*1024))
			}
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.spec.URL, nil)
	if err != nil {
		return fmt.Errorf("ошибка подготовки запроса загрузки: %w", err)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("не удалось скачать OpenFlux: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("сервер вернул HTTP %d при скачивании %s", resp.StatusCode, s.spec.URL)
	}

	tmpPath := targetPath + ".tmp"
	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return fmt.Errorf("не удалось создать временный файл %s: %w", tmpPath, err)
	}

	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, hasher), resp.Body); err != nil {
		_ = out.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("ошибка сохранения OpenFlux: %w", err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("ошибка закрытия временного файла: %w", err)
	}

	if expected := strings.TrimSpace(s.spec.SHA256); expected != "" {
		got := hex.EncodeToString(hasher.Sum(nil))
		if !strings.EqualFold(got, expected) {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("несовпадение SHA-256 бинарника OpenFlux: получено %s, ожидалось %s", got, expected)
		}
	}

	_ = os.Chmod(tmpPath, 0755)
	if err := os.Rename(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("не удалось установить бинарник OpenFlux в %s: %w", targetPath, err)
	}

	s.binPath = targetPath
	s.cachedVerPath = ""
	s.cachedVer = ""
	return nil
}

// GetLogTail returns the last N lines from the server process log file.
func (s *Service) GetLogTail(maxLines int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getLogTailLocked(maxLines)
}

// GetClientLogTail returns the last N lines from the client process log file.
func (s *Service) GetClientLogTail(maxLines int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getFileLogTailLocked(s.clientLogPath, maxLines)
}

func (s *Service) getLogTailLocked(maxLines int) string {
	return s.getFileLogTailLocked(s.logPath, maxLines)
}

func (s *Service) getFileLogTailLocked(path string, maxLines int) string {
	if maxLines <= 0 {
		maxLines = 100
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}
