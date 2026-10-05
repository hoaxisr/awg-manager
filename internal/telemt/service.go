package telemt

import (
	"archive/tar"
	"compress/gzip"
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
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/childproc"
	"github.com/hoaxisr/awg-manager/internal/listenfirewall"
	"github.com/hoaxisr/awg-manager/internal/sys/routerinfo"
)

var (
	ErrArchNotSupported = errors.New("telemt is only supported on aarch64 and x86_64 routers")
	ErrNotInstalled     = errors.New("telemt binary is not installed")
	ErrChecksumMismatch = errors.New("downloaded archive checksum does not match pinned SHA256")
	ErrDiskSpace        = errors.New("insufficient disk space for telemt installation")
)

// Service coordinates telemt installation, configuration, and process management.
type Service struct {
	mu            sync.Mutex
	dataDir       string
	telemtDir     string
	binPath       string
	cfgPath       string
	settingsPath  string
	pidPath       string
	arch          string
	spec          BinarySpec
	httpClient    *http.Client
	freeDisk      func(path string) (int64, bool)
	config        Config
	cachedVersion string
	openedPort    int
}

// New creates a new telemt service instance.
func New(dataDir string, arch string) *Service {
	telemtDir := filepath.Join(dataDir, "telemt")
	if dataDir == "" {
		telemtDir = ManagedTelemtDir
	}
	_ = os.MkdirAll(telemtDir, 0755)

	normArch := NormalizeArch(arch)
	spec := EmbeddedBinaries[normArch]

	pidPath := ManagedTelemtPIDPath
	if dataDir != "/opt/etc/awg-manager" && dataDir != "" {
		pidPath = filepath.Join(telemtDir, "telemt.pid")
	}

	s := &Service{
		dataDir:      dataDir,
		telemtDir:    telemtDir,
		binPath:      filepath.Join(telemtDir, "telemt"),
		cfgPath:      filepath.Join(telemtDir, "config.toml"),
		settingsPath: filepath.Join(telemtDir, "settings.json"),
		pidPath:      pidPath,
		arch:         normArch,
		spec:         spec,
		httpClient:   &http.Client{Timeout: 3 * time.Minute},
		freeDisk:     routerinfo.FreeBytes,
	}

	s.loadSettings()
	return s
}

func (s *Service) loadSettings() {
	data, err := os.ReadFile(s.settingsPath)
	if err == nil {
		var cfg Config
		if err := json.Unmarshal(data, &cfg); err == nil {
			if strings.TrimSpace(cfg.Secret) == "" {
				cfg.Secret, _ = GenerateSecret()
			}
			if port, err := ValidatePort(cfg.Port); err == nil {
				cfg.Port = port
			} else {
				cfg.Port = DefaultPort
			}
			cfg.TLSDomain = NormalizeTLSDomain(cfg.TLSDomain)
			s.config = cfg
			return
		}
	}
	s.config = DefaultConfig()
}

func (s *Service) saveSettingsLocked() error {
	data, err := json.MarshalIndent(s.config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.settingsPath, data, 0644)
}

func (s *Service) IsInstalled() bool {
	fi, err := os.Stat(s.binPath)
	if err != nil || fi.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return fi.Mode()&0111 != 0
}

func (s *Service) GetRunningState() (bool, int) {
	data, err := os.ReadFile(s.pidPath)
	if err == nil {
		pidStr := strings.TrimSpace(string(data))
		if pid, err := strconv.Atoi(pidStr); err == nil && pid > 0 {
			if childproc.MatchesBinary(pid, "telemt") {
				return true, pid
			}
		}
	}
	return false, 0
}

var versionRe = regexp.MustCompile(`(?:telemt|v)?\s*([0-9]+\.[0-9]+\.[0-9]+)`)

func (s *Service) getInstalledVersionLocked(ctx context.Context) string {
	if !s.IsInstalled() {
		s.cachedVersion = ""
		return ""
	}
	if s.cachedVersion != "" {
		return s.cachedVersion
	}
	cmd := exec.CommandContext(ctx, s.binPath, "--version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	matches := versionRe.FindStringSubmatch(string(out))
	if len(matches) > 1 {
		s.cachedVersion = matches[1]
	} else {
		s.cachedVersion = strings.TrimSpace(string(out))
	}
	return s.cachedVersion
}

func (s *Service) GetInstalledVersion(ctx context.Context) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getInstalledVersionLocked(ctx)
}

// GetStatus returns the live status for the API and UI.
func (s *Service) GetStatus(ctx context.Context) Status {
	s.mu.Lock()
	defer s.mu.Unlock()

	installed := s.IsInstalled()
	running, pid := s.GetRunningState()
	supported := IsArchSupported(s.arch)
	installedVer := s.getInstalledVersionLocked(ctx)

	cfgCopy := s.config

	var link string
	if installed && s.config.Secret != "" {
		host := s.config.ListenIP
		if host == "" || host == "0.0.0.0" {
			host = "192.168.1.1" // UI or caller replaces with active router LAN/WAN IP
		}
		link = GenerateTgLink(s.config.Mode, host, s.config.Port, s.config.Secret, s.config.TLSDomain, s.config.WebHost)
	}

	source := "managed"
	if !installed {
		source = ""
	}

	updateAvailable := false
	if installed && installedVer != "" && s.spec.Version != "" && installedVer != s.spec.Version {
		updateAvailable = true
	}

	var errMsg string
	if !supported {
		errMsg = fmt.Sprintf("Архитектура %s не поддерживается (telemt доступен только для aarch64 и x86_64)", s.arch)
	}

	return Status{
		Installed:       installed,
		Running:         running,
		PID:             pid,
		Version:         installedVer,
		LatestVersion:   s.spec.Version,
		UpdateAvailable: updateAvailable,
		ArchSupported:   supported,
		Binary:          s.binPath,
		Arch:            s.arch,
		Source:          source,
		Link:            link,
		Error:           errMsg,
		Config:          &cfgCopy,
	}
}

// GetConfig returns the current configuration.
func (s *Service) GetConfig() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.config
}

// ValidatePort checks that the port is within the valid range (1-65535)
// and rejects ports reserved by system components.
// If port is 0, DefaultPort (8443) is returned.
func ValidatePort(port int) (int, error) {
	if port == 0 {
		port = DefaultPort
	}
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid port %d: must be between 1 and 65535", port)
	}
	switch port {
	case 22, 2222, 1099, 51820:
		return 0, fmt.Errorf("port %d is reserved by system", port)
	}
	return port, nil
}

// NormalizeTLSDomain strips whitespace, URL scheme, userinfo, path, query, and port.
func NormalizeTLSDomain(raw string) string {
	d := strings.TrimSpace(raw)
	if d == "" {
		return DefaultTLSDomain
	}
	// Strip scheme if present (e.g. https://)
	if idx := strings.Index(d, "://"); idx != -1 {
		d = d[idx+3:]
	}
	if strings.HasPrefix(d, "//") {
		d = strings.TrimPrefix(d, "//")
	}
	// Strip path, query, or fragment
	if idx := strings.IndexAny(d, "/?#"); idx != -1 {
		d = d[:idx]
	}
	// Strip userinfo if present (e.g. user:pass@)
	if at := strings.LastIndex(d, "@"); at != -1 {
		d = d[at+1:]
	}
	// Strip port if present
	if host, _, err := net.SplitHostPort(d); err == nil {
		d = host
	} else if strings.Contains(d, ":") && !strings.Contains(d, "]") {
		parts := strings.Split(d, ":")
		if len(parts) == 2 {
			d = parts[0]
		}
	}
	d = strings.TrimSuffix(strings.TrimSpace(d), ".")
	d = strings.ToLower(d)
	if d == "" {
		return DefaultTLSDomain
	}
	return d
}

// SaveConfig updates the telemt configuration and writes config.toml.
func (s *Service) SaveConfig(ctx context.Context, cfg Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	port, err := ValidatePort(cfg.Port)
	if err != nil {
		return err
	}
	cfg.Port = port

	if strings.TrimSpace(cfg.Secret) == "" {
		sec, err := GenerateSecret()
		if err != nil {
			return fmt.Errorf("failed to generate secret: %w", err)
		}
		cfg.Secret = sec
	}

	cfg.TLSDomain = NormalizeTLSDomain(cfg.TLSDomain)

	s.config = cfg
	if err := s.saveSettingsLocked(); err != nil {
		return err
	}

	tomlData := GenerateTOML(s.config)
	if err := os.WriteFile(s.cfgPath, []byte(tomlData), 0644); err != nil {
		return err
	}

	// If running, reload or restart, or stop if disabled
	running, _ := s.GetRunningState()
	if running {
		if !s.config.Enabled {
			_ = s.stopProcess(ctx)
		} else {
			_ = s.restartProcess(ctx)
		}
	} else if s.config.Enabled && s.IsInstalled() {
		_ = s.startProcess(ctx)
	}

	return nil
}

// Install downloads, verifies, and installs the pinned telemt binary.
func (s *Service) Install(ctx context.Context) error {
	if !IsArchSupported(s.arch) {
		return ErrArchNotSupported
	}

	spec := s.spec
	if spec.URL == "" {
		return fmt.Errorf("no download URL for architecture %s", s.arch)
	}

	_ = os.MkdirAll(s.telemtDir, 0755)

	// Check free disk space (at least 25 MB)
	if s.freeDisk != nil {
		if free, ok := s.freeDisk(s.telemtDir); ok && free < 25*1024*1024 {
			return ErrDiskSpace
		}
	}

	// Download archive to a temporary file OUTSIDE s.mu.Lock()
	tmpArchiveFile, err := os.CreateTemp(s.telemtDir, "telemt-download-*.tar.gz.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp archive: %w", err)
	}
	tmpArchive := tmpArchiveFile.Name()
	defer func() { _ = os.Remove(tmpArchive) }()
	defer tmpArchiveFile.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, spec.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "awg-manager-telemt-installer")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with HTTP %d", resp.StatusCode)
	}

	h := sha256.New()
	tee := io.TeeReader(resp.Body, h)
	if _, err := io.Copy(tmpArchiveFile, tee); err != nil {
		return fmt.Errorf("failed to write archive: %w", err)
	}
	_ = tmpArchiveFile.Close()

	computedSHA := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(computedSHA, spec.SHA256) {
		return fmt.Errorf("%w: expected %s, got %s", ErrChecksumMismatch, spec.SHA256, computedSHA)
	}

	// Lock only for unpack, swap, and start
	s.mu.Lock()
	defer s.mu.Unlock()

	tmpBin := filepath.Join(s.telemtDir, "telemt.tmp")
	defer func() { _ = os.Remove(tmpBin) }()

	archFile, err := os.Open(tmpArchive)
	if err != nil {
		return err
	}
	defer archFile.Close()

	if err := extractTarGzBinary(archFile, "telemt", tmpBin); err != nil {
		return fmt.Errorf("failed to extract binary: %w", err)
	}

	if err := os.Chmod(tmpBin, 0755); err != nil {
		return err
	}

	// Stop any existing process before replacing binary
	_ = s.stopProcess(ctx)

	// Atomically move binary to target path
	if err := os.Rename(tmpBin, s.binPath); err != nil {
		return fmt.Errorf("failed to install binary: %w", err)
	}

	s.cachedVersion = ""

	// Create symlink /opt/bin/telemt if safe
	s.ensureSymlink()

	// Ensure default config exists
	if _, err := os.Stat(s.cfgPath); os.IsNotExist(err) {
		tomlData := GenerateTOML(s.config)
		_ = os.WriteFile(s.cfgPath, []byte(tomlData), 0644)
	}

	// Start service
	s.config.Enabled = true
	_ = s.saveSettingsLocked()
	return s.startProcess(ctx)
}

func (s *Service) ensureSymlink() {
	if s.binPath == ManagedTelemtBinaryPath {
		if fi, err := os.Lstat(LegacyTelemtBinaryPath); err == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				_ = os.Remove(LegacyTelemtBinaryPath)
				_ = os.Symlink(s.binPath, LegacyTelemtBinaryPath)
			}
		} else if os.IsNotExist(err) {
			_ = os.Symlink(s.binPath, LegacyTelemtBinaryPath)
		}
	}
}

// Update updates telemt to the latest pinned version.
func (s *Service) Update(ctx context.Context) error {
	return s.Install(ctx)
}

// Uninstall cleanly stops and removes the managed telemt installation.
func (s *Service) Uninstall(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. Stop daemon process
	_ = s.stopProcess(ctx)

	// 2. Remove binary
	_ = os.Remove(s.binPath)
	s.cachedVersion = ""

	// 3. Remove symlink only if it points to our managed binary
	if s.binPath == ManagedTelemtBinaryPath {
		if fi, err := os.Lstat(LegacyTelemtBinaryPath); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			if target, err := os.Readlink(LegacyTelemtBinaryPath); err == nil {
				if strings.Contains(target, "awg-manager") {
					_ = os.Remove(LegacyTelemtBinaryPath)
				}
			}
		}
	}

	s.config.Enabled = false
	_ = s.saveSettingsLocked()
	return nil
}

// Start launches the telemt daemon process.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.startProcess(ctx); err != nil {
		return err
	}
	s.config.Enabled = true
	_ = s.saveSettingsLocked()
	return nil
}

// StartIfEnabled launches the telemt daemon if it is installed and configured as enabled.
func (s *Service) StartIfEnabled(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.config.Enabled && s.IsInstalled() {
		return s.startProcess(ctx)
	}
	return nil
}

func (s *Service) startProcess(ctx context.Context) error {
	if !s.IsInstalled() {
		return ErrNotInstalled
	}

	running, _ := s.GetRunningState()
	if running {
		port := s.config.Port
		if port <= 0 {
			port = DefaultPort
		}
		_ = listenfirewall.Apply(ctx, port, "tcp")
		s.openedPort = port
		return nil
	}

	// Clean up any stale PID file before starting
	_ = os.Remove(s.pidPath)

	// Ensure config.toml is written
	tomlData := GenerateTOML(s.config)
	_ = os.WriteFile(s.cfgPath, []byte(tomlData), 0644)

	_ = os.MkdirAll(filepath.Dir(s.pidPath), 0755)

	// telemt has native daemon support: telemt start --pid-file <path> <config>
	cmd := exec.CommandContext(ctx, s.binPath, "start", "--pid-file", s.pidPath, s.cfgPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		// Fallback: try foreground execution in background goroutine if daemon mode fails
		return fmt.Errorf("telemt start failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}

	// Verify startup
	time.Sleep(200 * time.Millisecond)
	if running, _ := s.GetRunningState(); !running {
		return errors.New("telemt failed to start: process exited immediately")
	}

	port := s.config.Port
	if port <= 0 {
		port = DefaultPort
	}
	_ = listenfirewall.Apply(ctx, port, "tcp")
	s.openedPort = port

	return nil
}

// Stop terminates the telemt daemon process.
func (s *Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config.Enabled = false
	_ = s.saveSettingsLocked()
	return s.stopProcess(ctx)
}

func (s *Service) stopProcess(ctx context.Context) error {
	port := s.config.Port
	if port <= 0 {
		port = DefaultPort
	}
	listenfirewall.Remove(ctx, port, "tcp")
	if s.openedPort > 0 && s.openedPort != port {
		listenfirewall.Remove(ctx, s.openedPort, "tcp")
	}
	s.openedPort = 0

	running, pid := s.GetRunningState()
	if !running {
		_ = os.Remove(s.pidPath)
		return nil
	}

	// Try graceful stop via telemt CLI
	stopCmd := exec.CommandContext(ctx, s.binPath, "stop", "--pid-file", s.pidPath)
	_ = stopCmd.Run()

	// Wait up to 1 second
stopWait:
	for i := 0; i < 5; i++ {
		select {
		case <-ctx.Done():
			break stopWait
		case <-time.After(200 * time.Millisecond):
		}
		if r, _ := s.GetRunningState(); !r {
			_ = os.Remove(s.pidPath)
			return nil
		}
	}

	// Terminate by PID
	if pid > 0 {
		if childproc.MatchesBinary(pid, "telemt") {
			_ = childproc.Terminate(pid)
			select {
			case <-ctx.Done():
			case <-time.After(200 * time.Millisecond):
			}
			if r, _ := s.GetRunningState(); r {
				if childproc.MatchesBinary(pid, "telemt") {
					_ = childproc.Kill(pid)
				}
			}
		}
	}

	_ = os.Remove(s.pidPath)
	return nil
}

// Restart stops and restarts telemt.
func (s *Service) Restart(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.restartProcess(ctx); err != nil {
		return err
	}
	s.config.Enabled = true
	_ = s.saveSettingsLocked()
	return nil
}

func (s *Service) restartProcess(ctx context.Context) error {
	_ = s.stopProcess(ctx)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(300 * time.Millisecond):
	}
	return s.startProcess(ctx)
}

// Close gracefully shuts down the service without modifying configuration.
func (s *Service) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopProcess(context.Background())
}

// extractTarGzBinary reads a .tar.gz archive stream and writes the file named targetName to destPath.
func extractTarGzBinary(r io.Reader, targetName, destPath string) error {
	gzr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip reader error: %w", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar reader error: %w", err)
		}

		baseName := filepath.Base(hdr.Name)
		if hdr.Typeflag == tar.TypeReg && baseName == targetName {
			out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, tr)
			_ = out.Close()
			return copyErr
		}
	}
	return fmt.Errorf("binary %q not found in archive", targetName)
}
