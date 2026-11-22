package firc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/httpdownload"
	"github.com/hoaxisr/awg-manager/internal/sys/routerinfo"
)

// FircBinarySpec describes one architecture's FIRC release download metadata.
// Embedded in the build via embedded_firc.go so a compromised server cannot serve
// a tampered binary that awg-manager would still trust.
type FircBinarySpec struct {
	PackageURL    string `json:"packageUrl"`
	MirrorURL     string `json:"mirrorUrl,omitempty"`
	PackageSHA256 string `json:"packageSha256"`
	PackageSize   int64  `json:"packageSize"`
	Version       string `json:"version"`
}

// InstallState represents the FIRC installation lifecycle state.
type InstallState string

const (
	InstallStateNotInstalled   InstallState = "not_installed"
	InstallStateInstalled      InstallState = "installed"
	InstallStateRunning        InstallState = "running"
	InstallStateRecoveryNeeded InstallState = "recovery_required"
	InstallStateInstalling     InstallState = "installing"
	InstallStateUninstalling   InstallState = "uninstalling"
	InstallStateUnavailable    InstallState = "unavailable"
)

// InstallStatus is the JSON payload for /api/firc/install-status.
type InstallStatus struct {
	State                  InstallState `json:"state"`
	InstalledVersion       string       `json:"installedVersion,omitempty"`
	AvailableVersion       string       `json:"availableVersion,omitempty"`
	UpdateAvailable        bool         `json:"updateAvailable"`
	Available              bool         `json:"available"`
	UnavailableReason      string       `json:"unavailableReason,omitempty"`
	FircdPresent           bool         `json:"fircdPresent"`
	TunvlessPresent        bool         `json:"tunvlessPresent"`
	InitScriptPresent      bool         `json:"initScriptPresent"`
	NDMHookPresent         bool         `json:"ndmHookPresent"`
	ConfigPresent          bool         `json:"configPresent"`
	FreeDiskBytes          int64        `json:"freeDiskBytes,omitempty"`
	RequiredDiskBytes      int64        `json:"requiredDiskBytes,omitempty"`
	Busy                   bool         `json:"busy"`
	BusyAction             string       `json:"busyAction,omitempty"`
	Progress               float64      `json:"progress,omitempty"` // 0.0вЂ“1.0
	Error                  string       `json:"error,omitempty"`
	ActiveEngine           bool         `json:"activeEngine"`
	LegacyBundledOwnership bool         `json:"legacyBundledOwnership,omitempty"`
	ExternalPackage        string       `json:"externalPackage,omitempty"`
	ExternalMigratable     bool         `json:"externalMigratable,omitempty"`
	DependentServices      []string     `json:"dependentServices,omitempty"`
}

// OnDemandInstaller manages on-demand FIRC binary download, verification, and install/uninstall.
type OnDemandInstaller struct {
	mu         sync.Mutex
	busy       bool
	busyAction string
	progress   float64
	lastError  string

	arch         string
	spec         FircBinarySpec
	fallbackSpec FircBinarySpec
	layout       PackageLayout

	downloader Downloader
	freeDisk   func(string) (int64, bool)

	// Lifecycle checkers connected from production wiring
	engineActiveFn      func() bool
	fircRunningFn       func(ctx context.Context) bool
	tunvlessRunningFn   func(ctx context.Context) bool
	checkDependenciesFn func(ctx context.Context) ([]string, error)
	lockRuntime         func() func()
	packages            RuntimePackageManager
	legacyFaultHooks    LegacyMigrationFaultHooks
}

func (odi *OnDemandInstaller) SetRuntimeLocker(lock func() func()) { odi.lockRuntime = lock }

// ExternalRuntimePackage reports the Entware opkg package name (such as "firc" or
// "tunvless") that owns FIRC runtime binaries or hooks outside awg-manager and
// awg-firc-runtime, or "" when none is registered.
func (odi *OnDemandInstaller) ExternalRuntimePackage() (string, error) {
	infoDir := odi.rootPath("/opt/lib/opkg/info")
	entries, err := os.ReadDir(infoDir)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	protected := map[string]bool{
		"/opt/sbin/fircd": true, "/opt/bin/fircd": true,
		"/opt/sbin/tunvless": true, "/opt/bin/tunvless": true,
		"/opt/etc/init.d/S99firc":                true,
		"/opt/etc/ndm/netfilter.d/100-firc":      true,
		"/opt/etc/ndm/iflayerchanged.d/100-firc": true,
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".list") || entry.Name() == RuntimePackageName+".list" || entry.Name() == "awg-manager.list" {
			continue
		}
		list := filepath.Join(infoDir, entry.Name())
		data, err := os.ReadFile(list)
		if err != nil {
			return "", fmt.Errorf("read package ownership: %w", err)
		}
		for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
			if protected[strings.TrimSpace(line)] {
				return strings.TrimSuffix(entry.Name(), ".list"), nil
			}
		}
	}
	return "", nil
}

// Refuse direct mutation of files owned by a third-party IPK (such as upstream firc or tunvless).
// Legacy bundled awg-manager ownership is handled exclusively via transactional migration.
func (odi *OnDemandInstaller) checkExternalPackageOwnership() error {
	pkg, err := odi.ExternalRuntimePackage()
	if err != nil {
		return err
	}
	if pkg != "" {
		return fmt.Errorf("firc runtime belongs to external package %s.list; refusing to overwrite", pkg)
	}
	return nil
}

func (odi *OnDemandInstaller) checkPackageOwnership() error {
	if err := odi.checkExternalPackageOwnership(); err != nil {
		return err
	}
	legacy, err := odi.HasLegacyBundledOwnership()
	if err != nil {
		return err
	}
	if legacy {
		return errors.New("firc runtime belongs to package awg-manager.list; use legacy ownership migration before modifying it")
	}
	return nil
}

// Downloader downloads files from URLs.
type Downloader interface {
	DownloadFile(ctx context.Context, req DownloadFileRequest) (DownloadFileResult, error)
}

// DownloadFileRequest mirrors installer.DownloadFileRequest for FIRC downloads.
type DownloadFileRequest struct {
	URL          string
	DestPath     string
	Timeout      time.Duration
	MaxFileBytes int64
	Mode         os.FileMode
	Progress     httpdownload.ProgressFn
}

// DownloadFileResult mirrors installer.DownloadFileResult.
type DownloadFileResult struct {
	Path string
	Size int64
}

// NewOnDemandInstaller creates a new FIRC installer for the given architecture.
func NewOnDemandInstaller(arch string, spec FircBinarySpec) *OnDemandInstaller {
	return &OnDemandInstaller{
		arch:     arch,
		spec:     spec,
		layout:   DefaultPackageLayout(""),
		freeDisk: routerinfo.FreeBytes,
		packages: OpkgRuntimeManager{},
	}
}

// SetDownloader sets the HTTP downloader.
func (odi *OnDemandInstaller) SetDownloader(dl Downloader) {
	odi.downloader = dl
}

// SetFallbackPackageSpec configures an alternate verified Entware `firc` IPK
// specification used only when the primary runtime release is unavailable.
func (odi *OnDemandInstaller) SetFallbackPackageSpec(spec FircBinarySpec) {
	odi.mu.Lock()
	odi.fallbackSpec = spec
	odi.mu.Unlock()
}

func (odi *OnDemandInstaller) installSpec() (spec FircBinarySpec, fromFallback bool) {
	if odi.spec.IsAvailable() {
		return odi.spec, false
	}
	if odi.fallbackSpec.IsAvailable() {
		return odi.fallbackSpec, true
	}
	return odi.spec, false
}

// SetFreeDiskFn overrides the free-disk probe (tests only).
func (odi *OnDemandInstaller) SetFreeDiskFn(fn func(string) (int64, bool)) {
	odi.freeDisk = fn
}

// SetPackageLayout overrides the filesystem layout (tests only).
func (odi *OnDemandInstaller) SetPackageLayout(layout PackageLayout) {
	odi.layout = layout
}

// SetEngineActiveChecker wires the active routing engine check.
func (odi *OnDemandInstaller) SetEngineActiveChecker(fn func() bool) {
	odi.engineActiveFn = fn
}

// SetFircRunningChecker wires the fircd running check.
func (odi *OnDemandInstaller) SetFircRunningChecker(fn func(ctx context.Context) bool) {
	odi.fircRunningFn = fn
}

// SetTunvlessRunningChecker wires the tunvless running check.
func (odi *OnDemandInstaller) SetTunvlessRunningChecker(fn func(ctx context.Context) bool) {
	odi.tunvlessRunningFn = fn
}

// SetDependencyChecker wires the dependent services check.
func (odi *OnDemandInstaller) SetDependencyChecker(fn func(ctx context.Context) ([]string, error)) {
	odi.checkDependenciesFn = fn
}

var (
	ErrInstallBusy           = errors.New("firc: install/uninstall operation already in progress")
	ErrFircActive            = errors.New("firc: cannot uninstall while FIRC is the active routing engine")
	ErrFircRunning           = errors.New("firc: cannot uninstall while FIRC is running; stop it first")
	ErrTunvlessRunning       = errors.New("firc: cannot uninstall while tunvless is running")
	ErrFircDependencies      = errors.New("firc: cannot uninstall: active services depend on FIRC")
	ErrInsufficientDisk      = errors.New("firc: insufficient disk space for installation")
	ErrNoDownloader          = errors.New("firc: downloader not configured")
	ErrSHA256Mismatch        = errors.New("firc: SHA256 checksum verification failed")
	ErrReleaseUnavailable    = errors.New("firc: release is not yet published or configured for this architecture")
	ErrInstallActiveCore     = errors.New("firc: cannot install or upgrade while FIRC is active; switch routing engine first")
	ErrLayoutIncomplete      = errors.New("firc: package layout verification failed after install")
	ErrRuntimeNotInstalled   = errors.New("firc: runtime package is not installed; install it first")
	ErrAlreadyUpToDate       = errors.New("firc: runtime is already at the latest available version")
	ErrUpgradeRequiresRepair = errors.New("firc: runtime requires recovery before upgrading; run repair first")
	ErrUpgradeBackupMissing  = errors.New("firc: verified backup of installed runtime is required before upgrading")
)

const (
	diskReserveBytes int64 = 16 << 20
	downloadMaxBytes int64 = 32 << 20 // 32 MiB per binary max
)

// GetInstallStatus returns the current FIRC installation status.
func (odi *OnDemandInstaller) GetInstallStatus(ctx context.Context, op *Operator, isSelected bool) InstallStatus {
	odi.mu.Lock()
	busy := odi.busy
	busyAction := odi.busyAction
	progress := odi.progress
	lastErr := odi.lastError
	odi.mu.Unlock()

	// Probe files
	_, errFircd := os.Stat(odi.layout.FircdBin)
	if errFircd != nil {
		// Standalone Entware firc installs binaries into /opt/bin instead of /opt/sbin.
		_, errFircd = os.Stat(odi.rootPath("/opt/bin/fircd"))
	}
	_, errTun := os.Stat(odi.layout.TunvlessBin)
	if errTun != nil {
		_, errTun = os.Stat(odi.rootPath("/opt/bin/tunvless"))
	}
	_, errInit := os.Stat(odi.layout.InitScript)
	_, errNetfilter := os.Stat(odi.layout.NDMNetfilterHook)
	_, errIfLayer := os.Lstat(odi.layout.NDMIfLayerHook)
	_, errCfg := os.Stat(odi.layout.ConfigFile)

	fircdPresent := errFircd == nil
	tunvlessPresent := errTun == nil
	initPresent := errInit == nil
	ndmHookPresent := errNetfilter == nil
	cfgPresent := errCfg == nil

	active := isSelected
	if odi.engineActiveFn != nil {
		active = odi.engineActiveFn()
	}

	var dependentServices []string
	var dependencyErr error
	if odi.checkDependenciesFn != nil {
		if deps, err := odi.checkDependenciesFn(ctx); err == nil {
			dependentServices = deps
		} else {
			dependencyErr = err
			lastErr = err.Error()
		}
	}

	isRunning := false
	if odi.fircRunningFn != nil {
		isRunning = odi.fircRunningFn(ctx)
	} else if op != nil {
		isRunning = op.IsRunning(ctx)
	}

	extPkg, extPkgErr := odi.ExternalRuntimePackage()
	installedVer := ""
	var packageErr error
	if odi.packages != nil {
		installedVer, packageErr = odi.packages.Version(ctx, RuntimePackageName)
		if packageErr == nil && installedVer == "" && extPkg != "" {
			installedVer, packageErr = odi.packages.Version(ctx, extPkg)
		}
	}
	legacyOwned, legacyErr := odi.HasLegacyBundledOwnership()
	journal, journalErr := odi.readOperation()
	if extPkgErr != nil {
		lastErr = extPkgErr.Error()
	} else if packageErr != nil {
		lastErr = packageErr.Error()
	} else if legacyErr != nil {
		lastErr = legacyErr.Error()
	} else if journalErr != nil {
		lastErr = journalErr.Error()
	} else if journal.Error != "" {
		lastErr = journal.Error
	}

	effectiveSpec, _ := odi.installSpec()
	available := effectiveSpec.IsAvailable()
	unavailableReason := ""
	if !available {
		if strings.TrimSpace(odi.spec.Version) != "" {
			unavailableReason = "FIRC release v" + odi.spec.Version + " is not yet published on GitHub for " + odi.arch
		} else {
			unavailableReason = "FIRC release is not yet published on GitHub for " + odi.arch
		}
	}

	var state InstallState
	switch {
	case busy && (busyAction == "install" || busyAction == "upgrade" || busyAction == "migrate_legacy" || busyAction == "migrate_external"):
		state = InstallStateInstalling
	case busy && busyAction == "uninstall":
		state = InstallStateUninstalling
	case journalErr != nil || journal.Pending || extPkgErr != nil || packageErr != nil || dependencyErr != nil || legacyErr != nil:
		state = InstallStateRecoveryNeeded
	case !fircdPresent && !tunvlessPresent && !initPresent && installedVer == "" && !legacyOwned && extPkg == "":
		if !available {
			state = InstallStateUnavailable
		} else {
			state = InstallStateNotInstalled
		}
	case !fircdPresent || !tunvlessPresent || !initPresent || !ndmHookPresent || !cfgPresent || errIfLayer != nil:
		// Partial installation: some runtime files are missing
		state = InstallStateRecoveryNeeded
	case op != nil && op.IsRecoveryRequired():
		state = InstallStateRecoveryNeeded
	case isRunning:
		state = InstallStateRunning
	default:
		state = InstallStateInstalled
	}

	updateAvailable := available &&
		!legacyOwned &&
		extPkg == "" &&
		(state == InstallStateInstalled || state == InstallStateRunning) &&
		installedVer != "" &&
		effectiveSpec.Version != "" &&
		installedVer != effectiveSpec.Version

	status := InstallStatus{
		State:                  state,
		InstalledVersion:       installedVer,
		AvailableVersion:       effectiveSpec.Version,
		UpdateAvailable:        updateAvailable,
		Available:              available,
		UnavailableReason:      unavailableReason,
		FircdPresent:           fircdPresent,
		TunvlessPresent:        tunvlessPresent,
		InitScriptPresent:      initPresent,
		NDMHookPresent:         ndmHookPresent,
		ConfigPresent:          cfgPresent,
		Busy:                   busy,
		BusyAction:             busyAction,
		Progress:               progress,
		Error:                  lastErr,
		ActiveEngine:           active,
		LegacyBundledOwnership: legacyOwned,
		ExternalPackage:        extPkg,
		ExternalMigratable:     extPkg != "" && isAllowedExternalFircPackage(extPkg),
		DependentServices:      dependentServices,
	}

	required := effectiveSpec.PackageSize*4 + diskReserveBytes
	status.RequiredDiskBytes = required
	if odi.freeDisk != nil {
		diskProbeDir := filepath.Dir(odi.layout.FircdBin)
		if _, err := os.Stat(diskProbeDir); err != nil {
			diskProbeDir = filepath.Dir(odi.rootPath("/opt/bin/fircd"))
		}
		if free, ok := odi.freeDisk(diskProbeDir); ok {
			status.FreeDiskBytes = free
		}
	}

	return status
}

// PendingOperationAction reports the pending operation action recorded in
// firc-runtime-operation.json if recovery is pending, or "".
func (odi *OnDemandInstaller) PendingOperationAction() string {
	op, err := odi.readOperation()
	if err != nil || !op.Pending {
		return ""
	}
	return op.Action
}

// Install runs a verified package operation synchronously (also used by tests).
func (odi *OnDemandInstaller) Install(ctx context.Context) error {
	if err := odi.reserve("install"); err != nil {
		return err
	}
	err := odi.packageOperation(ctx, "install", nil, false, false)
	odi.finish(err)
	return err
}

// Uninstall removes only the runtime package, preserving user configuration.
func (odi *OnDemandInstaller) Uninstall(ctx context.Context, op *Operator, isSelected bool, purgeConfigs bool) error {
	if err := odi.reserve("uninstall"); err != nil {
		return err
	}
	err := odi.packageOperation(ctx, "uninstall", op, isSelected, purgeConfigs)
	odi.finish(err)
	return err
}

func (odi *OnDemandInstaller) setProgress(p float64) {
	odi.mu.Lock()
	odi.progress = p
	odi.mu.Unlock()
}

func (odi *OnDemandInstaller) setError(msg string) {
	odi.mu.Lock()
	odi.lastError = msg
	odi.mu.Unlock()
}
