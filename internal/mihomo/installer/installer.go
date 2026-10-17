package installer

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/sys/routerinfo"
)

const DefaultBinaryPath = "/opt/etc/awg-manager/mihomo/mihomo"

type InstallState string

const (
	InstallStateInstalled       InstallState = "installed"
	InstallStateMissing         InstallState = "missing"
	InstallStateMissingNoSpace  InstallState = "missing_no_space"
	InstallStateOutdatedNoSpace InstallState = "outdated_no_space"
)

const safetyMargin = 5 << 20 // 5 MiB slack for disk-gate

type Installer struct {
	binaryPath string
	arch       string
	spec       BinarySpec
	appLog     *logging.ScopedLogger
	freeDisk   func(path string) (int64, bool)
	shaMu      sync.Mutex
}

func New(binaryPath, arch string, spec BinarySpec, appLog logging.AppLogger) *Installer {
	return &Installer{
		binaryPath: binaryPath,
		arch:       arch,
		spec:       spec,
		appLog:     logging.NewScopedLogger(appLog, logging.GroupMihomo, logging.SubSBProcess),
		freeDisk:   routerinfo.FreeBytes,
	}
}

// ResolveBinary returns the path of the existing executable binary, checking
// the primary managed path, Entware's /opt/bin/mihomo, and system PATH.
func (i *Installer) ResolveBinary() string {
	if isExecutable(i.binaryPath) {
		return i.binaryPath
	}
	if isExecutable("/opt/bin/mihomo") {
		return "/opt/bin/mihomo"
	}
	if lp, err := exec.LookPath("mihomo"); err == nil && isExecutable(lp) {
		return lp
	}
	return i.binaryPath
}

func (i *Installer) IsInstalled() bool {
	return isExecutable(i.ResolveBinary())
}

func (i *Installer) IsInstallAvailable() bool {
	if i.spec.URL != "" {
		return true
	}
	return canRunOpkg()
}

func (i *Installer) RequiredVersion() string {
	if i.spec.Version != "" {
		return i.spec.Version
	}
	return RequiredVersion
}

func (i *Installer) RequiredSize() int64 {
	return i.spec.Size
}

func (i *Installer) FreeBytes() (int64, bool) {
	if i.freeDisk == nil {
		return routerinfo.FreeBytes(filepath.Dir(i.binaryPath))
	}
	return i.freeDisk(filepath.Dir(i.binaryPath))
}

func (i *Installer) SetFreeDiskFn(fn func(string) (int64, bool)) {
	i.freeDisk = fn
}

// CurrentVersion runs `<binary> -v` and returns parsed semantic version (e.g. "1.19.29").
func (i *Installer) CurrentVersion(ctx context.Context) string {
	bin := i.ResolveBinary()
	if !isExecutable(bin) {
		return ""
	}
	cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, bin, "-v").Output()
	if err != nil {
		return ""
	}
	// Output is typically: "Mihomo Meta v1.19.29 linux arm64..." or "Mihomo v1.19.27..."
	re := regexp.MustCompile(`(?i)\bv?([0-9]+\.[0-9]+\.[0-9]+[^\s]*)`)
	match := re.FindStringSubmatch(string(out))
	if len(match) >= 2 {
		return strings.TrimPrefix(match[1], "v")
	}
	return ""
}

func (i *Installer) UpdateAvailable(ctx context.Context) bool {
	cur := i.CurrentVersion(ctx)
	if cur == "" {
		return false
	}
	req := i.RequiredVersion()
	return req != "" && cur != req
}

func (i *Installer) EvaluateInstallState(ctx context.Context) InstallState {
	installed := i.IsInstalled()
	free, freeOK := i.FreeBytes()
	reqSize := i.RequiredSize()

	if reqSize > 0 && freeOK && free < reqSize+safetyMargin {
		if installed {
			if i.UpdateAvailable(ctx) {
				return InstallStateOutdatedNoSpace
			}
			return InstallStateInstalled
		}
		return InstallStateMissingNoSpace
	}

	if installed {
		return InstallStateInstalled
	}
	return InstallStateMissing
}

func (i *Installer) Install(ctx context.Context) error {
	// 1. Try embedded binary download if available.
	if i.spec.URL != "" {
		if i.appLog != nil {
			i.appLog.Info("install", i.arch, fmt.Sprintf("Downloading Mihomo v%s from %s", i.spec.Version, i.spec.URL))
		}

		err := i.downloadAndExtract(ctx)
		if err == nil {
			if i.appLog != nil {
				i.appLog.Info("install", i.arch, "Mihomo binary installed successfully")
			}
			return nil
		}

		if i.appLog != nil {
			i.appLog.Warn("install", i.arch, fmt.Sprintf("Direct binary download failed: %v. Checking opkg fallback...", err))
		}
	}

	// 2. Fallback to opkg if available.
	if canRunOpkg() {
		if i.appLog != nil {
			i.appLog.Info("install", "opkg", "Installing mihomo via opkg...")
		}
		cmd := exec.CommandContext(ctx, "opkg", "update")
		_ = cmd.Run()

		installCmd := exec.CommandContext(ctx, "opkg", "install", "mihomo")
		out, err := installCmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("opkg install mihomo failed: %w: %s", err, string(out))
		}
		if i.appLog != nil {
			i.appLog.Info("install", "opkg", "Mihomo installed via opkg successfully")
		}
		return nil
	}

	return fmt.Errorf("no suitable installation method found for architecture %s", i.arch)
}

func (i *Installer) downloadAndExtract(ctx context.Context) error {
	free, freeOK := i.FreeBytes()
	reqSize := i.RequiredSize()
	if reqSize > 0 && freeOK && free < reqSize+safetyMargin {
		return fmt.Errorf("insufficient disk space for mihomo: %d bytes free, need at least %d bytes", free, reqSize+safetyMargin)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", i.spec.URL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status code: %d", resp.StatusCode)
	}

	hasher := sha256.New()
	rawStream := io.TeeReader(resp.Body, hasher)

	var reader io.Reader = rawStream
	if strings.HasSuffix(i.spec.URL, ".gz") {
		gz, err := gzip.NewReader(rawStream)
		if err != nil {
			return fmt.Errorf("gzip reader error: %w", err)
		}
		defer gz.Close()
		reader = gz
	}

	tmpPath := i.binaryPath + ".tmp"
	if err := os.MkdirAll(filepath.Dir(i.binaryPath), 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	_ = os.Remove(tmpPath)

	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, reader); err != nil {
		out.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("copy stream: %w", err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close tmp file: %w", err)
	}

	// Verify SHA-256 of downloaded archive
	if i.spec.SHA256 != "" {
		actualSha := hex.EncodeToString(hasher.Sum(nil))
		if !strings.EqualFold(actualSha, i.spec.SHA256) {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("checksum mismatch for mihomo: expected %s, got %s", i.spec.SHA256, actualSha)
		}
	}

	if err := os.Rename(tmpPath, i.binaryPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename to target: %w", err)
	}

	return os.Chmod(i.binaryPath, 0755)
}

func (i *Installer) Remove(ctx context.Context) error {
	var errs []error
	if err := os.Remove(i.binaryPath); err != nil && !os.IsNotExist(err) {
		errs = append(errs, err)
	}
	_ = os.Remove(i.binaryPath + ".tmp")

	if isExecutable("/opt/bin/mihomo") && canRunOpkg() {
		cmd := exec.CommandContext(ctx, "opkg", "remove", "mihomo")
		if err := cmd.Run(); err != nil {
			errs = append(errs, fmt.Errorf("opkg remove mihomo: %w", err))
		}
	}
	return errors.Join(errs...)
}

func isExecutable(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && (info.Mode()&0111 != 0)
}

func canRunOpkg() bool {
	if isExecutable("/opt/bin/opkg") {
		return true
	}
	_, err := exec.LookPath("opkg")
	return err == nil
}
