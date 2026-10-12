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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/childproc"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/netif"
	"github.com/hoaxisr/awg-manager/internal/sys/routerinfo"
)

var (
	ErrArchNotSupported  = errors.New("telemt is only supported on aarch64 routers")
	ErrNotInstalled      = errors.New("telemt binary is not installed")
	ErrChecksumMismatch  = errors.New("downloaded archive checksum does not match pinned SHA256")
	ErrDiskSpace         = errors.New("insufficient disk space for telemt installation")
	ErrAlreadyInstalling = errors.New("telemt installation is already in progress")
	ErrInvalidPort       = errors.New("invalid port")
	ErrPortReserved      = errors.New("port is reserved")
	ErrInterfaceRequired = errors.New("network interface is required")
)

// Downloader defines the narrow contract required for downloading binaries.
type Downloader interface {
	DownloadFile(ctx context.Context, url, destPath string, maxBytes int64) error
}

// Service coordinates telemt installation, configuration, and process management.
type Service struct {
	mu            sync.Mutex
	isInstalling  bool
	cancelInstall context.CancelFunc
	dataDir       string
	telemtDir     string
	binPath       string
	cfgPath       string
	settingsPath  string
	pidPath       string
	arch          string
	spec          BinarySpec
	downloader    Downloader
	freeDisk      func(path string) (int64, bool)
	config        Config
	cachedVersion string
	openedPort    int
	openedIface   string
}

// New creates a new telemt service instance without performing filesystem side effects.
func New(dataDir string, arch string) *Service {
	telemtDir := filepath.Join(dataDir, "telemt")
	if dataDir == "" {
		telemtDir = ManagedTelemtDir
	}

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
		freeDisk:     routerinfo.FreeBytes,
	}

	s.loadSettings()
	return s
}

// SetDownloader injects the shared downloader implementation.
func (s *Service) SetDownloader(d Downloader) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.downloader = d
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
			cfg.Interface = strings.TrimSpace(cfg.Interface)
			s.config = cfg
			return
		}
	}
	s.config = DefaultConfig()
}

func (s *Service) saveSettingsLocked() error {
	if err := os.MkdirAll(s.telemtDir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.settingsPath, data, 0600)
}

// IsInstalled reports whether the managed telemt binary is in place and executable.
func (s *Service) IsInstalled() bool {
	fi, err := os.Stat(s.binPath)
	if err != nil || fi.IsDir() {
		return false
	}
	return fi.Mode()&0111 != 0
}

// GetRunningState reports whether telemt process is running and its PID.
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

func (s *Service) resolveHost() string {
	if s.config.Interface != "" {
		if ip := netif.FirstIPv4(s.config.Interface); ip != "" {
			return ip
		}
	}
	if lanIP := netif.RouterLANIP(storage.DefaultInterface); lanIP != "" {
		return lanIP
	}
	return "192.168.1.1"
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
	// Mask secret in public status polling to prevent accidental leaks
	cfgCopy.Secret = ""

	var link string
	if installed && running && s.config.Secret != "" {
		host := s.resolveHost()
		link = GenerateTgLink(host, s.config.Port, s.config.Secret, s.config.TLSDomain)
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
		errMsg = fmt.Sprintf("architecture %s is not supported (telemt is only available for aarch64)", s.arch)
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

// GetConfig returns the full current configuration including secret for management.
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
		return 0, fmt.Errorf("%w: must be between 1 and 65535, got %d", ErrInvalidPort, port)
	}
	switch port {
	case 22, 2222, 1099, 51820:
		return 0, fmt.Errorf("%w: %d (reserved by system services)", ErrPortReserved, port)
	}
	return port, nil
}

// NormalizeTLSDomain strips whitespace, URL scheme, userinfo, path, query, and port.
func NormalizeTLSDomain(raw string) string {
	d := strings.TrimSpace(raw)
	if d == "" {
		return DefaultTLSDomain
	}
	if idx := strings.Index(d, "://"); idx != -1 {
		d = d[idx+3:]
	}
	if strings.HasPrefix(d, "//") {
		d = strings.TrimPrefix(d, "//")
	}
	if idx := strings.IndexAny(d, "/?#"); idx != -1 {
		d = d[:idx]
	}
	if at := strings.LastIndex(d, "@"); at != -1 {
		d = d[at+1:]
	}
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

// SaveConfig updates the telemt configuration, writes config.toml with 0600 permissions,
// and starts/stops/restarts the process accordingly.
func (s *Service) SaveConfig(ctx context.Context, cfg Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	port, err := ValidatePort(cfg.Port)
	if err != nil {
		return err
	}
	cfg.Port = port
	cfg.Interface = strings.TrimSpace(cfg.Interface)
	if cfg.Enabled && cfg.Interface == "" {
		return ErrInterfaceRequired
	}

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
	if err := os.WriteFile(s.cfgPath, []byte(tomlData), 0600); err != nil {
		return err
	}

	running, _ := s.GetRunningState()
	if running {
		if !s.config.Enabled {
			return s.stopProcess(ctx)
		}
		return s.restartProcess(ctx)
	} else if s.config.Enabled && s.IsInstalled() {
		return s.startProcess(ctx)
	}

	return nil
}

// Install downloads, verifies, and installs the pinned telemt binary.
// It explicitly leaves the service disabled until configured by the user.
func (s *Service) Install(ctx context.Context) error {
	s.mu.Lock()
	if s.isInstalling {
		s.mu.Unlock()
		return ErrAlreadyInstalling
	}
	s.isInstalling = true
	installCtx, cancel := context.WithCancel(ctx)
	s.cancelInstall = cancel
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.isInstalling = false
		s.cancelInstall = nil
		s.mu.Unlock()
	}()

	if !IsArchSupported(s.arch) {
		return ErrArchNotSupported
	}

	spec := s.spec
	if spec.URL == "" {
		return fmt.Errorf("no download URL for architecture %s", s.arch)
	}

	if err := os.MkdirAll(s.telemtDir, 0700); err != nil {
		return err
	}

	if s.freeDisk != nil {
		if free, ok := s.freeDisk(s.telemtDir); ok && free < 25*1024*1024 {
			return ErrDiskSpace
		}
	}

	tmpArchive := filepath.Join(s.telemtDir, fmt.Sprintf("telemt-dl-%d.tar.gz.tmp", time.Now().UnixNano()))
	defer func() { _ = os.Remove(tmpArchive) }()

	maxArchiveSize := spec.ArchiveSize + 1024*1024
	if s.downloader != nil {
		if err := s.downloader.DownloadFile(installCtx, spec.URL, tmpArchive, maxArchiveSize); err != nil {
			return fmt.Errorf("download %s: %w", spec.URL, err)
		}
	} else {
		// Fallback for tests when downloader not set
		if err := downloadHTTPFallback(installCtx, spec.URL, tmpArchive, maxArchiveSize); err != nil {
			return err
		}
	}

	fi, err := os.Stat(tmpArchive)
	if err != nil {
		return err
	}
	if fi.Size() == 0 || (spec.ArchiveSize > 0 && fi.Size() > maxArchiveSize) {
		return fmt.Errorf("invalid downloaded archive size (%d bytes)", fi.Size())
	}

	computedSHA, err := sha256File(tmpArchive)
	if err != nil {
		return err
	}
	if !strings.EqualFold(computedSHA, spec.SHA256) {
		return fmt.Errorf("%w: expected %s, got %s", ErrChecksumMismatch, spec.SHA256, computedSHA)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if installCtx.Err() != nil {
		return installCtx.Err()
	}

	tmpBin := filepath.Join(s.telemtDir, "telemt.tmp")
	defer func() { _ = os.Remove(tmpBin) }()

	archFile, err := os.Open(tmpArchive)
	if err != nil {
		return err
	}
	defer archFile.Close()

	maxBinSize := spec.BinarySize + 1024*1024
	if err := extractTarGzBinary(archFile, "telemt", tmpBin, maxBinSize); err != nil {
		return fmt.Errorf("failed to extract binary: %w", err)
	}

	binFi, err := os.Stat(tmpBin)
	if err != nil {
		return err
	}
	if binFi.Size() == 0 || (spec.BinarySize > 0 && binFi.Size() > maxBinSize) {
		return fmt.Errorf("invalid extracted binary size (%d bytes)", binFi.Size())
	}

	if err := os.Chmod(tmpBin, 0755); err != nil {
		return err
	}

	_ = s.stopProcess(ctx)

	if err := os.Rename(tmpBin, s.binPath); err != nil {
		return fmt.Errorf("failed to install binary: %w", err)
	}

	s.cachedVersion = ""
	s.ensureSymlink()

	if _, err := os.Stat(s.cfgPath); os.IsNotExist(err) {
		tomlData := GenerateTOML(s.config)
		_ = os.WriteFile(s.cfgPath, []byte(tomlData), 0600)
	}

	// Keep service disabled after install until user selects interface and explicitly enables it
	s.config.Enabled = false
	_ = s.saveSettingsLocked()

	return nil
}

func (s *Service) ensureSymlink() {
	if s.binPath != ManagedTelemtBinaryPath {
		return
	}
	if fi, err := os.Lstat(LegacyTelemtBinaryPath); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			if target, err := os.Readlink(LegacyTelemtBinaryPath); err == nil {
				if target == s.binPath || strings.Contains(target, "awg-manager") {
					_ = os.Remove(LegacyTelemtBinaryPath)
					_ = os.Symlink(s.binPath, LegacyTelemtBinaryPath)
				}
			}
		}
		// If regular file or foreign symlink exists, do NOT touch it!
	} else if os.IsNotExist(err) {
		_ = os.Symlink(s.binPath, LegacyTelemtBinaryPath)
	}
}

// Update updates telemt to the latest pinned version.
func (s *Service) Update(ctx context.Context) error {
	return s.Install(ctx)
}

// Uninstall cleanly stops and removes the managed telemt installation.
func (s *Service) Uninstall(ctx context.Context) error {
	s.mu.Lock()
	if s.isInstalling && s.cancelInstall != nil {
		s.cancelInstall()
	}
	defer s.mu.Unlock()

	_ = s.stopProcess(ctx)

	_ = os.Remove(s.binPath)
	s.cachedVersion = ""

	if s.binPath == ManagedTelemtBinaryPath {
		if fi, err := os.Lstat(LegacyTelemtBinaryPath); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			if target, err := os.Readlink(LegacyTelemtBinaryPath); err == nil {
				if target == s.binPath || strings.Contains(target, "awg-manager") {
					_ = os.Remove(LegacyTelemtBinaryPath)
				}
			}
		}
	}

	s.config.Enabled = false
	s.config.Interface = ""
	_ = s.saveSettingsLocked()
	_ = os.Remove(s.pidPath)

	return nil
}

// Start launches the telemt daemon process if enabled and interface is configured.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(s.config.Interface) == "" {
		return ErrInterfaceRequired
	}
	if err := s.startProcess(ctx); err != nil {
		return err
	}
	s.config.Enabled = true
	return s.saveSettingsLocked()
}

// StartIfEnabled launches the telemt daemon if installed, enabled, and interface configured.
func (s *Service) StartIfEnabled(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.config.Enabled && strings.TrimSpace(s.config.Interface) != "" && s.IsInstalled() {
		return s.startProcess(ctx)
	}
	return nil
}

func (s *Service) startProcess(ctx context.Context) error {
	if !s.IsInstalled() {
		return ErrNotInstalled
	}
	if strings.TrimSpace(s.config.Interface) == "" {
		return ErrInterfaceRequired
	}

	port := s.config.Port
	if port <= 0 {
		port = DefaultPort
	}

	running, _ := s.GetRunningState()
	if running {
		if err := applyFirewall(ctx, s.config.Interface, port); err != nil {
			return err
		}
		s.openedPort = port
		s.openedIface = s.config.Interface
		return nil
	}

	_ = os.Remove(s.pidPath)

	tomlData := GenerateTOML(s.config)
	if err := os.WriteFile(s.cfgPath, []byte(tomlData), 0600); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(s.pidPath), 0755); err != nil {
		return err
	}

	cmd := exec.Command(s.binPath, "run", s.cfgPath)
	cmd.Dir = s.telemtDir

	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devnull.Close()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, devnull, devnull

	childproc.SetProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("telemt start failed: %w", err)
	}

	pid := cmd.Process.Pid
	if err := os.WriteFile(s.pidPath, []byte(strconv.Itoa(pid)), 0600); err != nil {
		_ = childproc.KillGroup(pid)
		return err
	}

	go func() {
		_, _ = cmd.Process.Wait()
	}()

	started := false
	for i := 0; i < 5; i++ {
		time.Sleep(100 * time.Millisecond)
		if childproc.MatchesBinary(pid, "telemt") {
			started = true
			break
		}
	}
	if !started {
		_ = os.Remove(s.pidPath)
		return errors.New("telemt failed to start: process exited immediately")
	}

	if err := applyFirewall(ctx, s.config.Interface, port); err != nil {
		_ = childproc.TerminateGroup(pid)
		_ = os.Remove(s.pidPath)
		return fmt.Errorf("telemt firewall setup failed: %w", err)
	}
	s.openedPort = port
	s.openedIface = s.config.Interface

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
	removeFirewall(ctx, s.openedIface, s.openedPort)
	if s.openedPort > 0 && (s.openedPort != port || s.openedIface != s.config.Interface) {
		removeFirewall(ctx, s.config.Interface, port)
	}
	s.openedPort = 0
	s.openedIface = ""

	running, pid := s.GetRunningState()
	if !running {
		_ = os.Remove(s.pidPath)
		return nil
	}

	if pid > 0 && childproc.MatchesBinary(pid, "telemt") {
		_ = childproc.TerminateGroup(pid)
		for i := 0; i < 5; i++ {
			time.Sleep(100 * time.Millisecond)
			if !childproc.MatchesBinary(pid, "telemt") {
				break
			}
		}
		if childproc.MatchesBinary(pid, "telemt") {
			_ = childproc.KillGroup(pid)
		}
	}

	_ = os.Remove(s.pidPath)
	return nil
}

// Restart stops and restarts telemt.
func (s *Service) Restart(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(s.config.Interface) == "" {
		return ErrInterfaceRequired
	}
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
	case <-time.After(200 * time.Millisecond):
	}
	return s.startProcess(ctx)
}

// Close gracefully shuts down the service without modifying saved configuration.
func (s *Service) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopProcess(context.Background())
}

// Cleanup removes telemt processes, firewall rules, and files during opkg remove.
func Cleanup(ctx context.Context, dataDir string) {
	telemtDir := filepath.Join(dataDir, "telemt")
	pidPath := filepath.Join(telemtDir, "telemt.pid")

	for _, p := range []string{pidPath, ManagedTelemtPIDPath} {
		if data, err := os.ReadFile(p); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 {
				if childproc.MatchesBinary(pid, "telemt") {
					_ = childproc.TerminateGroup(pid)
					_ = childproc.KillGroup(pid)
				}
			}
			_ = os.Remove(p)
		}
	}

	// Remove firewall rules and hook
	removeFirewall(ctx, "", DefaultPort)
	removeTelemtNetfilterHook()

	// Remove symlink if it points to telemt
	if fi, err := os.Lstat(LegacyTelemtBinaryPath); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		if target, err := os.Readlink(LegacyTelemtBinaryPath); err == nil {
			if strings.Contains(target, "awg-manager") || strings.Contains(target, "telemt") {
				_ = os.Remove(LegacyTelemtBinaryPath)
			}
		}
	}

	_ = os.RemoveAll(telemtDir)
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func extractTarGzBinary(r io.Reader, targetName, destPath string, maxBytes int64) error {
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
			written, copyErr := io.Copy(out, io.LimitReader(tr, maxBytes+1))
			_ = out.Close()
			if copyErr != nil {
				return copyErr
			}
			if written > maxBytes {
				return fmt.Errorf("binary exceeds maximum limit (%d bytes)", maxBytes)
			}
			return nil
		}
	}
	return fmt.Errorf("binary %q not found in archive", targetName)
}

func downloadHTTPFallback(ctx context.Context, url, destPath string, maxBytes int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "awg-manager-telemt-installer")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download HTTP status %d", resp.StatusCode)
	}

	out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, io.LimitReader(resp.Body, maxBytes+1))
	return err
}
