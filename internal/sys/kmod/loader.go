// Package kmod provides kernel module loading functionality.
package kmod

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

const (
	// sysfsWaitTimeout is the max time to wait for sysfs entry after insmod.
	sysfsWaitTimeout = 10 * time.Second
	// sysfsWaitInterval is the polling interval for sysfs entry.
	sysfsWaitInterval = 100 * time.Millisecond
	// SysfsPath is the sysfs path for the kernel module.
	SysfsPath = "/sys/module/" + ModuleName
)

// ModulesDir is the directory containing kernel modules.
// It is a var (not const) to allow overriding in tests.
var ModulesDir = "/opt/etc/awg-manager/modules"

// BundledDir is the directory containing bundled per-model .ko files from IPK.
// After selecting the correct module, this directory is removed.
var BundledDir = filepath.Join(ModulesDir, "bundled")

const (
	// ModuleName is the name of the kernel module.
	ModuleName = "amneziawg"
)

// Loader handles kernel module loading operations.
type Loader struct {
	model         string // e.g. "KN-1010"
	soc           SoC    // kept for backward compatibility
	modulePath    string
	targetVersion string // user-selected kmod version ("", "latest", "1.0.2", etc.)

	mu             sync.RWMutex
	downloadStatus DownloadStatus
	downloadError  string
}

// New creates a new kernel module loader.
// Reads model and SoC from cached NDMS info (ndmsinfo.Init must be called first).
func New() *Loader {
	model := DetectModel()
	// NC-xxxx is equivalent to KN-xxxx — normalize so .ko file lookup
	// and download URLs match (files are named amneziawg-KN-xxxx.ko).
	model = strings.Replace(model, "NC-", "KN-", 1)
	soc := DetectSoC()

	l := &Loader{
		model:          model,
		soc:            soc,
		downloadStatus: StatusNotNeeded,
	}

	// Resolve module path:
	// 1. New flat path first (downloaded per-model modules)
	// 2. Old SoC-based path as fallback (upgrading from bundled IPK)
	newPath := filepath.Join(ModulesDir, "amneziawg.ko")
	if _, err := os.Stat(newPath); err == nil {
		l.modulePath = newPath
		return l
	}
	if soc != SoCUnknown {
		oldPath := soc.ModulePath()
		if _, err := os.Stat(oldPath); err == nil {
			l.modulePath = oldPath
		}
	}

	return l
}

// knownSoCNames is the set of SoC directory names used by old bundled IPKs.
var knownSoCNames = map[string]bool{
	string(SoCMT7621): true,
	string(SoCMT7628): true,
	string(SoCEN7512): true,
	string(SoCEN7516): true,
	string(SoCEN7528): true,
	string(SoCMT7622): true,
	string(SoCMT7981): true,
	string(SoCMT7988): true,
}

// CleanupLegacyModules removes old SoC-based module directories that don't
// match this router's SoC. After upgrade from bundled IPK to per-model
// downloads, stale directories for other architectures are left behind.
// Returns the number of directories removed.
func (l *Loader) CleanupLegacyModules() int {
	entries, err := os.ReadDir(ModulesDir)
	if err != nil {
		return 0
	}

	removed := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !knownSoCNames[name] {
			continue // not a SoC directory, leave it alone
		}
		if l.soc != SoCUnknown && name == string(l.soc) {
			continue // matches this router's SoC, keep as fallback
		}
		if err := os.RemoveAll(filepath.Join(ModulesDir, name)); err == nil {
			removed++
		}
	}
	return removed
}

// Model returns the detected router model string (e.g. "KN-1010").
func (l *Loader) Model() string {
	return l.model
}

// SoC returns the detected System-on-Chip type.
func (l *Loader) SoC() SoC {
	return l.soc
}

// ModulePath returns the path to the kernel module for this router.
func (l *Loader) ModulePath() string {
	return l.modulePath
}

// ModuleExists checks if the kernel module file exists on disk.
func (l *Loader) ModuleExists() bool {
	if l.modulePath == "" {
		return false
	}
	_, err := os.Stat(l.modulePath)
	return err == nil
}

// IsLoaded checks if the kernel module is currently loaded.
func (l *Loader) IsLoaded() bool {
	result, err := exec.Run(context.Background(), "lsmod")
	if err != nil {
		return false
	}
	// Check each line for module name
	for _, line := range strings.Split(result.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == ModuleName {
			return true
		}
	}
	return false
}

// Load loads the kernel module using insmod and waits for sysfs registration.
func (l *Loader) Load(ctx context.Context) error {
	if l.modulePath == "" {
		return fmt.Errorf("unknown SoC, cannot determine module path")
	}
	if !l.ModuleExists() {
		return fmt.Errorf("module not found: %s", l.modulePath)
	}
	if _, err := exec.Run(ctx, "insmod", l.modulePath); err != nil {
		return err
	}
	// Wait for sysfs entry to appear — insmod returns before sysfs is registered
	return l.waitForSysfs(ctx)
}

// waitForSysfs polls for the sysfs module entry after insmod.
func (l *Loader) waitForSysfs(ctx context.Context) error {
	deadline := time.After(sysfsWaitTimeout)
	ticker := time.NewTicker(sysfsWaitInterval)
	defer ticker.Stop()

	for {
		if _, err := os.Stat(SysfsPath); err == nil {
			return nil
		}
		select {
		case <-deadline:
			return fmt.Errorf("timeout waiting for %s after insmod", SysfsPath)
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// GetLoadError retrieves kernel messages related to module loading.
// Useful for debugging when Load() fails.
func (l *Loader) GetLoadError() string {
	result, err := exec.Shell(context.Background(), "dmesg | grep -i amneziawg | tail -5")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(result.Stdout)
}

// SetTargetVersion sets the target kernel module version.
// If a specific version is set, EnsureModule downloads that version instead of latest.
// Empty string or "latest" means use ExpectedKmodVersion (default behavior).
func (l *Loader) SetTargetVersion(version string) {
	l.targetVersion = version
}

// TargetVersion returns the effective target version for downloads.
func (l *Loader) TargetVersion() string {
	if l.targetVersion == "" || l.targetVersion == "latest" {
		return ExpectedKmodVersion
	}
	return l.targetVersion
}

// OnDiskVersion returns the version string stored on disk, or "" if unknown.
func (l *Loader) OnDiskVersion() string {
	return readVersion()
}

// Unload removes the kernel module using rmmod.
func (l *Loader) Unload(ctx context.Context) error {
	_, err := exec.Run(ctx, "rmmod", ModuleName)
	return err
}

// SwapModule downloads the specified version, then unloads and reloads the module.
// The caller is responsible for stopping all tunnels BEFORE calling SwapModule.
func (l *Loader) SwapModule(ctx context.Context, version string) error {
	if l.model == "" || l.soc == SoCUnknown {
		return fmt.Errorf("unknown router model, cannot swap kernel module")
	}

	// Download the new version
	l.setStatus(StatusDownloading, "")
	if err := downloadVersion(ctx, l.model, version); err != nil {
		l.setStatus(StatusFailed, err.Error())
		return fmt.Errorf("download version %s: %w", version, err)
	}
	l.modulePath = filepath.Join(ModulesDir, "amneziawg.ko")
	l.setStatus(StatusDownloaded, "")

	// If module is loaded, unload it first then reload
	if l.IsLoaded() {
		if err := l.Unload(ctx); err != nil {
			return fmt.Errorf("unload module: %w", err)
		}
		if err := l.Load(ctx); err != nil {
			return fmt.Errorf("load new module: %w", err)
		}
	}

	// Update target version
	l.targetVersion = version

	return nil
}

// EnsureModule selects a bundled module (if available from IPK install/upgrade),
// then loads the module via insmod if not already loaded.
// If a target version is set and differs from on-disk version, downloads the target version
// and reloads the module.
func (l *Loader) EnsureModule(ctx context.Context) error {
	// Select from bundled (fresh install or upgrade)
	l.selectBundledModule()

	// Check if user requested a specific version that differs from on-disk
	if l.targetVersion != "" && l.model != "" && needsUpdate(l.targetVersion) {
		if err := l.swapToTargetVersion(ctx); err == nil {
			return nil
		}
		// Download failed — fall through to use whatever is on disk
	}

	// Already loaded — done
	if l.IsLoaded() {
		return nil
	}

	// Module on disk — load it
	if l.ModuleExists() {
		return l.Load(ctx)
	}

	// No module available
	if l.model == "" {
		l.setStatus(StatusUnsupported, "unknown router model")
		return fmt.Errorf("unknown router model")
	}
	l.setStatus(StatusUnsupported, "no bundled module for "+l.model)
	return fmt.Errorf("no kernel module for model %s", l.model)
}

// swapToTargetVersion downloads the target version and reloads the module if needed.
func (l *Loader) swapToTargetVersion(ctx context.Context) error {
	l.setStatus(StatusDownloading, "")
	if err := downloadVersion(ctx, l.model, l.targetVersion); err != nil {
		l.setStatus(StatusFailed, err.Error())
		return err
	}
	l.modulePath = filepath.Join(ModulesDir, "amneziawg.ko")
	l.setStatus(StatusDownloaded, "")

	// Unload old version if loaded, then load new
	if l.IsLoaded() {
		if err := l.Unload(ctx); err != nil {
			return fmt.Errorf("unload old module: %w", err)
		}
	}
	return l.Load(ctx)
}

// selectBundledModule checks BundledDir for a per-model .ko matching this router,
// copies it to ModulesDir/amneziawg.ko, writes the version file, and removes BundledDir.
// This is a one-shot operation after IPK install/upgrade.
func (l *Loader) selectBundledModule() {
	entries, err := os.ReadDir(BundledDir)
	if err != nil {
		return // no bundled dir — normal restart
	}

	if l.model == "" {
		// Can't select without knowing the model; leave bundled for next attempt
		return
	}

	// Find amneziawg-{model}.ko (e.g. amneziawg-KN-1010.ko)
	koName := fmt.Sprintf("amneziawg-%s.ko", l.model)
	var found string
	for _, e := range entries {
		if e.Name() == koName {
			found = filepath.Join(BundledDir, koName)
			break
		}
	}

	if found == "" {
		// No match for this model — clean up bundled dir anyway
		os.RemoveAll(BundledDir)
		return
	}

	// Copy bundled .ko → active module
	targetPath := filepath.Join(ModulesDir, "amneziawg.ko")
	if err := copyFile(found, targetPath); err != nil {
		return
	}

	// Write version from bundled/version file
	versionPath := filepath.Join(BundledDir, "version")
	if data, err := os.ReadFile(versionPath); err == nil {
		_ = writeVersion(strings.TrimSpace(string(data)))
	}

	// Update module path
	l.modulePath = targetPath

	// Clean up — bundled dir no longer needed
	os.RemoveAll(BundledDir)
}

// copyFile copies src to dst atomically (write to .tmp, then rename).
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tmpPath := dst + ".tmp"
	out, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	_, err = io.Copy(out, in)
	closeErr := out.Close()
	if err != nil {
		os.Remove(tmpPath)
		return err
	}
	if closeErr != nil {
		os.Remove(tmpPath)
		return closeErr
	}

	if err := os.Rename(tmpPath, dst); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

// TriggerDownload manually triggers a kernel module download.
// If download succeeds, also attempts to load the module.
func (l *Loader) TriggerDownload(ctx context.Context) error {
	if l.model == "" {
		return fmt.Errorf("unknown router model")
	}
	if l.soc == SoCUnknown {
		return fmt.Errorf("unsupported model: %s", l.model)
	}

	l.setStatus(StatusDownloading, "")
	if err := download(ctx, l.model); err != nil {
		l.setStatus(StatusFailed, err.Error())
		return err
	}

	l.modulePath = filepath.Join(ModulesDir, "amneziawg.ko")
	l.setStatus(StatusDownloaded, "")

	// Try to load after download; ignore errors (module might need reboot)
	if !l.IsLoaded() {
		if err := l.Load(ctx); err != nil {
			return fmt.Errorf("downloaded but failed to load: %w", err)
		}
	}

	return nil
}

// DownloadStatus returns the current download status.
func (l *Loader) DownloadStatus() DownloadStatus {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.downloadStatus
}

// DownloadError returns the last download error message.
func (l *Loader) DownloadError() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.downloadError
}

func (l *Loader) setStatus(status DownloadStatus, errMsg string) {
	l.mu.Lock()
	l.downloadStatus = status
	l.downloadError = errMsg
	l.mu.Unlock()
}
