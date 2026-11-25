package firc

import (
	"context"
	"errors"
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
	// InstallStateForeign — на роутере сторонний firc, рантайм панели не предлагается.
	InstallStateForeign InstallState = "foreign"
)

// InstallStatus is the JSON payload for /api/firc/install-status.
type InstallStatus struct {
	State             InstallState `json:"state"`
	InstalledVersion  string       `json:"installedVersion,omitempty"`
	AvailableVersion  string       `json:"availableVersion,omitempty"`
	UpdateAvailable   bool         `json:"updateAvailable"`
	Available         bool         `json:"available"`
	UnavailableReason string       `json:"unavailableReason,omitempty"`
	FircdPresent      bool         `json:"fircdPresent"`
	TunvlessPresent   bool         `json:"tunvlessPresent"`
	InitScriptPresent bool         `json:"initScriptPresent"`
	NDMHookPresent    bool         `json:"ndmHookPresent"`
	ConfigPresent     bool         `json:"configPresent"`
	FreeDiskBytes     int64        `json:"freeDiskBytes,omitempty"`
	RequiredDiskBytes int64        `json:"requiredDiskBytes,omitempty"`
	Busy              bool         `json:"busy"`
	BusyAction        string       `json:"busyAction,omitempty"`
	Progress          float64      `json:"progress,omitempty"` // 0.0–1.0
	Error             string       `json:"error,omitempty"`
	ActiveEngine      bool         `json:"activeEngine"`
	DependentServices []string     `json:"dependentServices,omitempty"`
	Foreign           *ForeignFirc `json:"foreign,omitempty"`
}

// OnDemandInstaller manages on-demand FIRC binary download, verification, and install/uninstall.
type OnDemandInstaller struct {
	// onFinished — подписчик на конец операции (успех или ошибка); зовётся
	// из finish вне i.mu. Задаётся до первой операции.
	onFinished func()
	mu         sync.Mutex
	busy       bool
	busyAction string
	progress   float64
	lastError  string

	arch   string
	spec   FircBinarySpec
	layout PackageLayout

	downloader Downloader
	freeDisk   func(string) (int64, bool)

	// Lifecycle checkers connected from production wiring
	engineActiveFn      func() bool
	fircRunningFn       func(ctx context.Context) bool
	tunvlessRunningFn   func(ctx context.Context) bool
	checkDependenciesFn func(ctx context.Context) ([]string, error)
	lockRuntime         func() func()
	packages            RuntimePackageManager
	foreign             *ForeignDetector
}

func (odi *OnDemandInstaller) SetRuntimeLocker(lock func() func()) { odi.lockRuntime = lock }

// checkNotForeign — отказ пакетных операций при стороннем firc.
func (odi *OnDemandInstaller) checkNotForeign() error {
	found, err := odi.foreign.Detect()
	if err != nil {
		return err
	}
	if found != nil {
		return ErrForeignFirc
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
		foreign:  NewForeignDetector(""),
	}
}

// SetDownloader sets the HTTP downloader.
func (odi *OnDemandInstaller) SetDownloader(dl Downloader) {
	odi.downloader = dl
}

// SetFreeDiskFn overrides the free-disk probe (tests only).
func (odi *OnDemandInstaller) SetFreeDiskFn(fn func(string) (int64, bool)) {
	odi.freeDisk = fn
}

// SetPackageLayout overrides the filesystem layout (tests only).
func (odi *OnDemandInstaller) SetPackageLayout(layout PackageLayout) {
	odi.layout = layout
	odi.foreign = NewForeignDetector(layout.RootDir)
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
	ErrFircActive            = errors.New("firc: cannot uninstall while AWGM Route is the active routing engine")
	ErrFircRunning           = errors.New("firc: cannot uninstall while AWGM Route is running; stop it first")
	ErrTunvlessRunning       = errors.New("firc: cannot uninstall while tunvless is running")
	ErrFircDependencies      = errors.New("firc: cannot uninstall: active services depend on AWGM Route")
	ErrInsufficientDisk      = errors.New("firc: insufficient disk space for installation")
	ErrNoDownloader          = errors.New("firc: downloader not configured")
	ErrSHA256Mismatch        = errors.New("firc: SHA256 checksum verification failed")
	ErrReleaseUnavailable    = errors.New("firc: release is not yet published or configured for this architecture")
	ErrInstallActiveCore     = errors.New("firc: cannot install or upgrade while AWGM Route is active; switch routing engine first")
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

	foreign, ownerErr := odi.foreign.Detect()
	if foreign != nil {
		active := isSelected
		if odi.engineActiveFn != nil {
			active = odi.engineActiveFn()
		}
		return InstallStatus{
			State:        InstallStateForeign,
			Busy:         busy,
			BusyAction:   busyAction,
			ActiveEngine: active,
			Foreign:      odi.foreign.describe(foreign),
		}
	}

	// Probe files
	_, errFircd := os.Stat(odi.layout.FircdBin)
	_, errTun := os.Stat(odi.layout.TunvlessBin)
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

	installedVer := ""
	var packageErr error
	if odi.packages != nil {
		installedVer, packageErr = odi.packages.Version(ctx, RuntimePackageName)
	}
	journal, journalErr := odi.readOperation()
	if ownerErr != nil {
		lastErr = ownerErr.Error()
	} else if packageErr != nil {
		lastErr = packageErr.Error()
	} else if journalErr != nil {
		lastErr = journalErr.Error()
	} else if journal.Error != "" {
		lastErr = journal.Error
	}

	available := odi.spec.IsAvailable()
	unavailableReason := ""
	if !available {
		if strings.TrimSpace(odi.spec.Version) != "" {
			unavailableReason = "AWGM Route release v" + odi.spec.Version + " is not yet published on GitHub for " + odi.arch
		} else {
			unavailableReason = "AWGM Route release is not yet published on GitHub for " + odi.arch
		}
	}

	var state InstallState
	switch {
	case busy && (busyAction == "install" || busyAction == "upgrade"):
		state = InstallStateInstalling
	case busy && busyAction == "uninstall":
		state = InstallStateUninstalling
	case journalErr != nil || journal.Pending || ownerErr != nil || packageErr != nil || dependencyErr != nil:
		state = InstallStateRecoveryNeeded
	case !fircdPresent && !tunvlessPresent && !initPresent && installedVer == "":
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
		(state == InstallStateInstalled || state == InstallStateRunning) &&
		installedVer != "" &&
		odi.spec.Version != "" &&
		installedVer != odi.spec.Version

	status := InstallStatus{
		State:             state,
		InstalledVersion:  installedVer,
		AvailableVersion:  odi.spec.Version,
		UpdateAvailable:   updateAvailable,
		Available:         available,
		UnavailableReason: unavailableReason,
		FircdPresent:      fircdPresent,
		TunvlessPresent:   tunvlessPresent,
		InitScriptPresent: initPresent,
		NDMHookPresent:    ndmHookPresent,
		ConfigPresent:     cfgPresent,
		Busy:              busy,
		BusyAction:        busyAction,
		Progress:          progress,
		Error:             lastErr,
		ActiveEngine:      active,
		DependentServices: dependentServices,
	}

	required := odi.spec.PackageSize*4 + diskReserveBytes
	status.RequiredDiskBytes = required
	if odi.freeDisk != nil {
		if free, ok := odi.freeDisk(filepath.Dir(odi.layout.FircdBin)); ok {
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
