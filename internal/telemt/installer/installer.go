package installer

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

	"github.com/hoaxisr/awg-manager/internal/sys/routerinfo"
)

const (
	ManagedTelemtDir        = "/opt/etc/awg-manager/telemt"
	ManagedTelemtBinaryPath = "/opt/etc/awg-manager/telemt/telemt"
	LegacyTelemtBinaryPath  = "/opt/bin/telemt"
	LegacyTelemtUsrPath     = "/opt/usr/bin/telemt"
	LegacyTelemtDir         = "/opt/etc/telemt"

	DefaultTelemtVersion = "3.5.8"
	PinnedTelemtVersion  = "3.5.8"
	GitHubReleasesURL    = "https://api.github.com/repos/telemt/telemt/releases/latest"
)

type BinarySpec struct {
	Version     string
	URL         string
	SHA256      string // SHA256 of the downloaded tar.gz archive
	ArchiveSize int64  // bytes
	BinarySize  int64  // uncompressed binary bytes
}

var EmbeddedBinaries = map[string]BinarySpec{
	"aarch64": {
		Version:     PinnedTelemtVersion,
		URL:         "https://github.com/telemt/telemt/releases/download/3.5.8/telemt-aarch64-linux-musl.tar.gz",
		SHA256:      "d003522a56ba71548808cfedffe2dc3cd55203255bd61325e43d5f86f8000006",
		ArchiveSize: 6135298,
		BinarySize:  13141032,
	},
	"x86_64": {
		Version:     PinnedTelemtVersion,
		URL:         "https://github.com/telemt/telemt/releases/download/3.5.8/telemt-x86_64-linux-musl.tar.gz",
		SHA256:      "2842a0ab1fef3f06125ce6818e4d48b41a6e8a850b151898486344365f8bb619",
		ArchiveSize: 6701352,
	},
	"mipsel": {
		Version:     PinnedTelemtVersion,
		URL:         "",
		SHA256:      "0748a44ebfc0006f8a61d82d98b04c28f14494ed63f17ab8306130acf73575a8",
		ArchiveSize: 8020518,
		BinarySize:  20638864,
	},
}

type Status struct {
	Installed       bool   `json:"installed"`
	Running         bool   `json:"running"`
	PID             int    `json:"pid,omitempty"`
	Version         string `json:"version,omitempty"`
	LatestVersion   string `json:"latestVersion,omitempty"`
	UpdateAvailable bool   `json:"updateAvailable"`
	Binary          string `json:"binary,omitempty"`
	Arch            string `json:"arch,omitempty"`
	Error           string `json:"error,omitempty"`
}

type gitHubRelease struct {
	TagName string        `json:"tag_name"`
	Assets  []gitHubAsset `json:"assets"`
}

type gitHubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

type TelemtInstaller struct {
	targetPath string
	arch       string
	spec       BinarySpec
	client     *http.Client
	freeDisk   func(path string) (int64, bool)
	onRestart  func(ctx context.Context) error
	mu         sync.Mutex
	cacheMu    sync.RWMutex

	cachedLatestVersion string
	cachedDownloadURL   string
	cachedCheckTime     time.Time
}

func New(arch string) *TelemtInstaller {
	normArch := normalizeArch(arch)
	spec := EmbeddedBinaries[normArch]
	return &TelemtInstaller{
		targetPath: ManagedTelemtBinaryPath,
		arch:       normArch,
		spec:       spec,
		client:     &http.Client{Timeout: 3 * time.Minute},
		freeDisk:   routerinfo.FreeBytes,
	}
}

func (i *TelemtInstaller) SetRestartHandler(fn func(ctx context.Context) error) {
	i.onRestart = fn
}

func normalizeArch(arch string) string {
	a := strings.ToLower(strings.TrimSpace(arch))
	switch {
	case strings.HasPrefix(a, "aarch64") || strings.HasPrefix(a, "arm64"):
		return "aarch64"
	case strings.HasPrefix(a, "mipsel") || strings.HasPrefix(a, "mipsle"):
		return "mipsel"
	case strings.HasPrefix(a, "mips"):
		return "mips"
	case strings.HasPrefix(a, "x86_64") || strings.HasPrefix(a, "amd64"):
		return "x86_64"
	case strings.HasPrefix(a, "armv7") || strings.HasPrefix(a, "armhf"):
		return "armv7"
	default:
		return a
	}
}

func isExecutable(fi os.FileInfo) bool {
	if fi.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return fi.Mode()&0111 != 0
}

func (i *TelemtInstaller) ResolveBinary() string {
	candidates := []string{
		i.targetPath,
		ManagedTelemtBinaryPath,
		LegacyTelemtBinaryPath,
		LegacyTelemtUsrPath,
	}
	for _, p := range candidates {
		if fi, err := os.Stat(p); err == nil && isExecutable(fi) {
			return p
		}
	}
	if lp, err := exec.LookPath("telemt"); err == nil {
		if fi, err := os.Stat(lp); err == nil && isExecutable(fi) {
			return lp
		}
	}
	return ""
}

func (i *TelemtInstaller) IsInstalled() bool {
	return i.ResolveBinary() != ""
}

func (i *TelemtInstaller) GetRunningState() (bool, int) {
	// 1. Check pid files
	for _, pidFile := range []string{"/opt/var/run/telemt.pid", "/opt/var/run/telemt-raw.pid", "/var/run/telemt.pid"} {
		data, err := os.ReadFile(pidFile)
		if err == nil {
			pidStr := strings.TrimSpace(string(data))
			if pid, err := strconv.Atoi(pidStr); err == nil && pid > 0 {
				if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); err == nil {
					return true, pid
				}
			}
		}
	}

	// 2. Check pidof / pgrep
	out, err := exec.Command("pidof", "telemt").Output()
	if err == nil {
		fields := strings.Fields(string(out))
		if len(fields) > 0 {
			if pid, err := strconv.Atoi(fields[0]); err == nil && pid > 0 {
				return true, pid
			}
		}
	}
	return false, 0
}

var versionRe = regexp.MustCompile(`(?:telemt|v)?\s*([0-9]+\.[0-9]+\.[0-9]+)`)

func (i *TelemtInstaller) GetInstalledVersion(ctx context.Context) string {
	bin := i.ResolveBinary()
	if bin == "" {
		return ""
	}
	cmd := exec.CommandContext(ctx, bin, "--version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		cmd = exec.CommandContext(ctx, bin, "-V")
		out, err = cmd.CombinedOutput()
		if err != nil {
			return ""
		}
	}
	matches := versionRe.FindStringSubmatch(string(out))
	if len(matches) > 1 {
		return matches[1]
	}
	return strings.TrimSpace(string(out))
}

func (i *TelemtInstaller) CheckLatestRelease(ctx context.Context) (latestVersion string, downloadURL string, err error) {
	if i.spec.Version != "" {
		return i.spec.Version, i.spec.URL, nil
	}

	i.cacheMu.RLock()
	if i.cachedLatestVersion != "" && time.Since(i.cachedCheckTime) < 15*time.Minute {
		latestVersion = i.cachedLatestVersion
		downloadURL = i.cachedDownloadURL
		i.cacheMu.RUnlock()
		return latestVersion, downloadURL, nil
	}
	i.cacheMu.RUnlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, GitHubReleasesURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "awg-manager-telemt-installer")
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := i.client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("github api request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("github api returned status %d", resp.StatusCode)
	}

	var rel gitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", "", fmt.Errorf("failed to decode release json: %w", err)
	}

	tag := strings.TrimPrefix(rel.TagName, "v")

	// Match asset for arch
	var bestURL string
	for _, asset := range rel.Assets {
		name := strings.ToLower(asset.Name)
		if !strings.HasSuffix(name, ".tar.gz") {
			continue
		}
		if i.arch == "aarch64" && (strings.Contains(name, "aarch64") || strings.Contains(name, "arm64")) && strings.Contains(name, "musl") {
			bestURL = asset.BrowserDownloadURL
			break
		}
		if i.arch == "x86_64" && (strings.Contains(name, "x86_64") || strings.Contains(name, "amd64")) && strings.Contains(name, "musl") {
			bestURL = asset.BrowserDownloadURL
			break
		}
		if i.arch == "mipsel" && (strings.Contains(name, "mipsel") || strings.Contains(name, "mipsle")) {
			bestURL = asset.BrowserDownloadURL
			break
		}
		if i.arch == "mips" && strings.Contains(name, "mips") && !strings.Contains(name, "mipsel") && !strings.Contains(name, "mipsle") {
			bestURL = asset.BrowserDownloadURL
			break
		}
	}

	i.cacheMu.Lock()
	i.cachedLatestVersion = tag
	i.cachedDownloadURL = bestURL
	i.cachedCheckTime = time.Now()
	i.cacheMu.Unlock()

	return tag, bestURL, nil
}

func compareVersions(current, latest string) bool {
	if current == "" || latest == "" {
		return false
	}
	current = strings.TrimPrefix(current, "v")
	latest = strings.TrimPrefix(latest, "v")
	cParts := strings.Split(current, ".")
	lParts := strings.Split(latest, ".")
	for idx := 0; idx < len(cParts) && idx < len(lParts); idx++ {
		cV, err1 := strconv.Atoi(cParts[idx])
		lV, err2 := strconv.Atoi(lParts[idx])
		if err1 == nil && err2 == nil {
			if lV > cV {
				return true
			}
			if lV < cV {
				return false
			}
		}
	}
	return len(lParts) > len(cParts)
}

func (i *TelemtInstaller) GetStatus(ctx context.Context) Status {
	bin := i.ResolveBinary()
	installed := bin != ""
	running, pid := i.GetRunningState()

	curVer := ""
	if installed {
		curVer = i.GetInstalledVersion(ctx)
	}

	latestVer, _, _ := i.CheckLatestRelease(ctx)
	updateAvail := false
	if installed && latestVer != "" && curVer != "" {
		updateAvail = compareVersions(curVer, latestVer)
	}

	return Status{
		Installed:       installed,
		Running:         running,
		PID:             pid,
		Version:         curVer,
		LatestVersion:   latestVer,
		UpdateAvailable: updateAvail,
		Binary:          bin,
		Arch:            i.arch,
	}
}

func (i *TelemtInstaller) Install(ctx context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	// If already installed and functional, return success
	if bin := i.ResolveBinary(); bin != "" {
		i.ensureSymlinks(bin)
		return nil
	}

	return i.downloadAndInstall(ctx)
}

func (i *TelemtInstaller) Update(ctx context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	wasRunning, _ := i.GetRunningState()

	if err := i.downloadAndInstall(ctx); err != nil {
		return err
	}

	if wasRunning {
		_ = i.restartService(ctx)
	}
	return nil
}

func (i *TelemtInstaller) downloadAndInstall(ctx context.Context) error {
	requiredSpace := int64(16 << 20)
	if i.spec.BinarySize > 0 {
		requiredSpace = i.spec.BinarySize + (5 << 20)
	}
	if i.freeDisk != nil {
		if free, ok := i.freeDisk(filepath.Dir(i.targetPath)); ok && free < requiredSpace {
			return fmt.Errorf("insufficient disk space for telemt: %d bytes free, need %d bytes", free, requiredSpace)
		}
	}

	if err := os.MkdirAll(ManagedTelemtDir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", ManagedTelemtDir, err)
	}

	latestVer, downloadURL, err := i.CheckLatestRelease(ctx)
	if err != nil || downloadURL == "" {
		// If download URL is empty (e.g. mipsel without GitHub asset, or offline), check if bundled prebuilt exists
		bundledCandidates := []string{
			filepath.Join("/opt/etc/awg-manager/prebuilt/telemt", i.arch, "telemt"),
			filepath.Join("/opt/share/awg-manager/prebuilt/telemt", i.arch, "telemt"),
			ManagedTelemtBinaryPath,
		}
		for _, bc := range bundledCandidates {
			if fi, err := os.Stat(bc); err == nil && !fi.IsDir() && (fi.Mode()&0111 != 0) {
				if bc != ManagedTelemtBinaryPath {
					if err := copyExecutable(bc, ManagedTelemtBinaryPath); err != nil {
						return fmt.Errorf("failed to copy bundled telemt: %w", err)
					}
				}
				i.ensureSymlinks(ManagedTelemtBinaryPath)
				return nil
			}
		}
		if downloadURL == "" {
			return fmt.Errorf("no download URL found for arch %q and no bundled binary found (latest version %s)", i.arch, latestVer)
		}
		return fmt.Errorf("download telemt failed: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "awg-manager-telemt-installer")

	resp, err := i.client.Do(req)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", downloadURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: HTTP %d", downloadURL, resp.StatusCode)
	}

	tmpTarget := ManagedTelemtBinaryPath + ".tmp"
	_ = os.Remove(tmpTarget)

	hasher := sha256.New()
	tee := io.TeeReader(resp.Body, hasher)

	if err := extractTarGzBinary(tee, "telemt", tmpTarget); err != nil {
		_ = os.Remove(tmpTarget)
		return fmt.Errorf("extract telemt: %w", err)
	}

	// Drain remaining archive bytes if any to complete hash calculation
	_, _ = io.Copy(io.Discard, tee)

	if i.spec.SHA256 != "" {
		actualSha := hex.EncodeToString(hasher.Sum(nil))
		if !strings.EqualFold(actualSha, i.spec.SHA256) {
			_ = os.Remove(tmpTarget)
			return fmt.Errorf("checksum mismatch for telemt: expected %s, got %s", i.spec.SHA256, actualSha)
		}
	}

	if i.spec.BinarySize > 0 {
		if fi, err := os.Stat(tmpTarget); err == nil && fi.Size() != i.spec.BinarySize {
			_ = os.Remove(tmpTarget)
			return fmt.Errorf("extracted telemt binary size mismatch: expected %d bytes, got %d bytes", i.spec.BinarySize, fi.Size())
		}
	}

	if err := os.Chmod(tmpTarget, 0755); err != nil {
		_ = os.Remove(tmpTarget)
		return err
	}

	if err := os.Rename(tmpTarget, ManagedTelemtBinaryPath); err != nil {
		_ = os.Remove(tmpTarget)
		return err
	}

	i.ensureSymlinks(ManagedTelemtBinaryPath)
	return nil
}

func extractTarGzBinary(r io.Reader, binaryName, targetPath string) error {
	gzr, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if filepath.Base(hdr.Name) == binaryName {
			f, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				_ = f.Close()
				return err
			}
			return f.Close()
		}
	}
	return fmt.Errorf("binary %q not found in archive", binaryName)
}

func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func (i *TelemtInstaller) ensureSymlinks(binPath string) {
	_ = os.MkdirAll("/opt/bin", 0755)
	_ = os.Remove("/opt/bin/telemt")
	_ = os.Symlink(binPath, "/opt/bin/telemt")

	// Ensure compatibility symlink /opt/etc/telemt -> ManagedTelemtDir
	if fi, err := os.Lstat(LegacyTelemtDir); os.IsNotExist(err) {
		_ = os.Symlink(ManagedTelemtDir, LegacyTelemtDir)
	} else if err == nil && fi.Mode()&os.ModeSymlink != 0 {
		_ = os.Remove(LegacyTelemtDir)
		_ = os.Symlink(ManagedTelemtDir, LegacyTelemtDir)
	}
}

func (i *TelemtInstaller) Restart(ctx context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.restartService(ctx)
}

func (i *TelemtInstaller) restartService(ctx context.Context) error {
	if i.onRestart != nil {
		return i.onRestart(ctx)
	}

	var errs []string
	if _, err := os.Stat("/opt/etc/init.d/S99telemt"); err == nil {
		cmd := exec.CommandContext(ctx, "/opt/etc/init.d/S99telemt", "restart")
		if out, err := cmd.CombinedOutput(); err != nil {
			errs = append(errs, fmt.Sprintf("S99telemt: %v (%s)", err, strings.TrimSpace(string(out))))
		}
	}
	if _, err := os.Stat("/opt/etc/init.d/S96telemt-raw"); err == nil {
		cmd := exec.CommandContext(ctx, "/opt/etc/init.d/S96telemt-raw", "restart")
		if out, err := cmd.CombinedOutput(); err != nil {
			errs = append(errs, fmt.Sprintf("S96telemt-raw: %v (%s)", err, strings.TrimSpace(string(out))))
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (i *TelemtInstaller) Uninstall(ctx context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	// Stop services
	if _, err := os.Stat("/opt/etc/init.d/S99telemt"); err == nil {
		_ = exec.CommandContext(ctx, "/opt/etc/init.d/S99telemt", "stop").Run()
	}
	if _, err := os.Stat("/opt/etc/init.d/S96telemt-raw"); err == nil {
		_ = exec.CommandContext(ctx, "/opt/etc/init.d/S96telemt-raw", "stop").Run()
	}
	_ = exec.CommandContext(ctx, "killall", "-9", "telemt").Run()
	time.Sleep(300 * time.Millisecond)

	bin := i.ResolveBinary()
	if bin != "" {
		_ = os.Remove(bin)
	}
	_ = os.Remove(ManagedTelemtBinaryPath)

	// Remove symlink if it was pointing to us
	if fi, err := os.Lstat(LegacyTelemtBinaryPath); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			target, _ := os.Readlink(LegacyTelemtBinaryPath)
			if strings.Contains(target, "awg-manager") {
				_ = os.Remove(LegacyTelemtBinaryPath)
			}
		}
	}
	return nil
}
