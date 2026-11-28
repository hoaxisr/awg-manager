package firc

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
)

// ErrRecoveryRequired is returned when a previous engine switch or runtime config rollback
// failed to restore a consistent state, blocking further mutations until recovery is resolved.
var ErrRecoveryRequired = errors.New("firc: recovery_required (rollback failed; resolve recovery before further mutations)")

// ErrRecoveryPersistFailed is returned when persisting or removing recovery-state.json fails on disk.
var ErrRecoveryPersistFailed = errors.New("firc: recovery_state_persistence_failed")

// ErrCandidateDomainRequired is returned when an autolist candidate mutation is invoked without a domain.
var ErrCandidateDomainRequired = errors.New("firc: candidate domain is required")

// ErrCandidateGroupDNSOnly is returned when the target group of a candidate answers DNS only: there is no outbound to route into.
var ErrCandidateGroupDNSOnly = errors.New("firc: the group only answers DNS: nothing to route its names into")

// ErrCandidateGroupRequired is returned when adding a confirmed autolist candidate without an explicit target group.
var ErrCandidateGroupRequired = errors.New("firc: target groupId is required for confirmed candidate add")

// ErrCandidateGroupNotFound is returned when the requested target group does not exist in the current group set.
var ErrCandidateGroupNotFound = errors.New("firc: target group not found")

// BreakGlassOptions supplies audit and verification metadata for emergency administrative recovery clear.
type BreakGlassOptions struct {
	TransitionID string
	Actor        string
	Source       string
}

// Options configures the firc Operator.
type Options struct {
	Logger        *slog.Logger
	AppLog        logging.AppLogger
	ConfigDir     string
	SocketPath    string
	FircdBinary   string
	TunvlessBin   string
	InitScript    string
	DNSProxyAddr  string
	DNSProxyPort  int
	InboundPort   uint16
	StartTimeout  time.Duration
	ConntrackPath string
	// Foreign — поиск стороннего firc; nil — на каталоге вне системы его нет.
	Foreign *ForeignDetector
	// RCI — чтения NDMS; nil — подсети VPN-серверов берутся по умолчанию.
	RCI RCIReader
}

// RCIReader — чтение RCI через общий NDMS-клиент (семафор, токен, User-Agent).
type RCIReader interface {
	GetRaw(ctx context.Context, path string) ([]byte, error)
}

// EngineSwitchHooks defines the callbacks used by SwitchEngine to coordinate
// transactional engine switching across settings stores and sing-box/firc services.
type EngineSwitchHooks struct {
	AcquireTransition    func(ctx context.Context) (EngineTransitionGuard, error)
	GetCurrentEngine     func(ctx context.Context) (string, error)
	ValidateMutation     func(ctx context.Context, prevEngine, targetEngine string) error
	CaptureState         func(ctx context.Context) (restore func(ctx context.Context) error, err error)
	StartTarget          func(ctx context.Context, targetEngine string) error
	StopEngine           func(ctx context.Context, engine string) error
	ParkEngineForStandby func(ctx context.Context, engine string) error
	ActivateTarget       func(ctx context.Context, targetEngine string) error
	CommitSettings       func(ctx context.Context, targetEngine string) error
	OnSwitched           func(fircSelected bool)
}

// EngineTransitionGuard is acquired before mutationMu: the uplink controller
// takes the same lock order when it activates FIRC. Implementations must release
// on every return path.
type EngineTransitionGuard interface {
	CheckTarget(string) error
	Release()
}

// EngineSwitchOptions parameterizes non-default engine transition behavior.
type EngineSwitchOptions struct {
	// SingboxStandby keeps sing-box alive in standby when non-router work exists
	// instead of stopping the daemon and setting SingboxStoppedByEngine=true.
	SingboxStandby bool
	// AuthorizeDisabledStart allows an explicit user/wizard engine switch to "firc"
	// to clear .awgm-disabled after transaction prechecks succeed, restoring the
	// disabled intent automatically if any step in the switch fails. Internal
	// autostart paths leave this false so manual stop remains sticky.
	AuthorizeDisabledStart bool
}

type turnOffCtxKey struct{}

// WithTurnOff помечает явный уход пользователя с движка: если целевой движок не
// установлен, маршрутизация панели просто выключается. Без метки переход на
// неустановленный движок отказывает — миграция и снимки не должны терять её молча.
func WithTurnOff(ctx context.Context) context.Context {
	return context.WithValue(ctx, turnOffCtxKey{}, true)
}

// TurnOffRequested — есть ли в ctx метка WithTurnOff.
func TurnOffRequested(ctx context.Context) bool {
	v, _ := ctx.Value(turnOffCtxKey{}).(bool)
	return v
}

type switchPrevEngineCtxKey struct{}

// WithSwitchPreviousEngine attaches the authoritative pre-switch engine name
// to ctx so CaptureState hooks know the pre-switch owner even when settings on
// disk were already replaced (for example during snapshot restore).
func WithSwitchPreviousEngine(ctx context.Context, engine string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, switchPrevEngineCtxKey{}, strings.TrimSpace(engine))
}

// SwitchPreviousEngineFromContext retrieves the pre-switch engine name
// attached by WithSwitchPreviousEngine, if present and non-empty.
func SwitchPreviousEngineFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	v, ok := ctx.Value(switchPrevEngineCtxKey{}).(string)
	if !ok || strings.TrimSpace(v) == "" {
		return "", false
	}
	return strings.TrimSpace(v), true
}

func mergeEngineSwitchHooks(base, override EngineSwitchHooks) EngineSwitchHooks {
	out := base
	if override.AcquireTransition != nil {
		out.AcquireTransition = override.AcquireTransition
	}
	if override.GetCurrentEngine != nil {
		out.GetCurrentEngine = override.GetCurrentEngine
	}
	if override.ValidateMutation != nil {
		out.ValidateMutation = override.ValidateMutation
	}
	if override.CaptureState != nil {
		out.CaptureState = override.CaptureState
	}
	if override.StartTarget != nil {
		out.StartTarget = override.StartTarget
	}
	if override.StopEngine != nil {
		out.StopEngine = override.StopEngine
	}
	if override.ParkEngineForStandby != nil {
		out.ParkEngineForStandby = override.ParkEngineForStandby
	}
	if override.ActivateTarget != nil {
		out.ActivateTarget = override.ActivateTarget
	}
	if override.CommitSettings != nil {
		out.CommitSettings = override.CommitSettings
	}
	if override.OnSwitched != nil {
		out.OnSwitched = override.OnSwitched
	}
	return out
}

// RecoveryClearFaultHooks provides deterministic, phase-specific fault injection hooks
// for the two-phase ClearRecovery protocol and emergency tombstone rematerialization (P0.1).
type RecoveryClearFaultHooks struct {
	Phase1DirSync           func(dir string) error
	Phase2FinalDirSync      func(dir string) error
	RestoreTombstoneWrite   func(tmpPath string) error
	RestoreTombstoneSync    func(tmpPath string) error
	RestoreTombstoneDirSync func(dir string) error
}

// MigrationJournalCommitFaultHooks provides deterministic fault/crash injection hooks
// across the two-phase commit boundary of completeMigrationJournal (R1 / C1 commit boundary).
type MigrationJournalCommitFaultHooks struct {
	BeforeCommitMarkerWrite func(journal *MigrationJournal) error
	CommitMarkerDirSync     func(dir string) error
	CommitMarkerReadback    func(journalPath string, expected []byte) ([]byte, error)
	AfterCommitMarkerWrite  func(journal *MigrationJournal) error
	BeforeJournalRemove     func(journalPath string) error
	AfterJournalRemove      func(journalPath string) error
	PostRemoveDirSync       func(dir string) error
}

// Operator manages the lifecycle, configuration files, tunnels, and routing groups of fircd.
//
// Lock ordering invariant across internal/firc:
//
//	mutationMu -> lifecycleMu -> cloudSyncMu -> netfilterMu -> mirrorNetfilterMu -> cloudCommitMu -> pkgCloudIpsetMu
//
// Leaf locks (extConfigMu, cloudStateMu, cloudCancelMu, mu, extIpsetMu, extIptablesMu, extCloudDNSMu,
// extCloudClockMu) must never acquire any lock listed above.
type Operator struct {
	foreign                  *ForeignDetector
	rci                      RCIReader
	hasComponent             func(name string) bool
	rciAbsent                sync.Map // путь раздела → time.Time последнего 404
	foreignTicks             atomic.Int32
	recoveryDeferred         atomic.Bool
	runtimeObs               runtimeObserver
	mu                       sync.Mutex
	mutationMu               sync.Mutex
	lifecycleMu              sync.Mutex
	netfilterMu              sync.Mutex
	extConfigMu              sync.RWMutex
	extConfigGen             atomic.Uint64
	extConfigSaved           atomic.Bool
	extLoopStarted           bool
	extLoopCtx               context.Context
	extLoopCancel            context.CancelFunc
	extLoopDone              chan struct{}
	keenCloudProvider        KeenCloudRelayProvider
	cloudSyncMu              sync.Mutex
	cloudCommitMu            sync.Mutex
	cloudCancelMu            sync.Mutex
	cloudEpoch               uint64
	cloudActiveID            uint64
	cloudActiveCancel        context.CancelFunc
	cloudStateMu             sync.RWMutex
	cloudSetReady            bool
	cloudLastAttemptAt       time.Time
	cloudLastSyncAt          time.Time
	cloudLastSyncErr         string
	cloudLastEntries         []string
	cloudSourceCache         map[string]*cloudSourceEntry
	cloudNegBackoff          map[string]cloudNegBackoffEntry
	keenDNSSyncedActiveIface string
	keenDNSLastAgentResetAt  time.Time
	resetCloudAgentFn        func(context.Context) (int, error)
	log                      *slog.Logger
	configDir                string
	socketPath               string
	fircdBinary              string
	tunvlessBin              string
	initScript               string
	lastOutPath              string
	dnsProxyAddr             string
	dnsProxyPort             int
	inboundPort              uint16
	startTimeout             time.Duration
	conntrackPath            string
	client                   *Client
	cmd                      *exec.Cmd
	startHook                func(ctx context.Context) error
	stopHook                 func(ctx context.Context) error
	dirSyncHook              func(dir string) error
	clearFaultHooks          RecoveryClearFaultHooks
	srTxFaultHooks           ServerRoutingTxFaultHooks
	defaultHooks             EngineSwitchHooks
	managedUplinkConfig      func(context.Context, []Tunnel) ([]Tunnel, error)
	xhttpStatusFn            func() bool
	openfluxStatusFn         func() (bool, string)
	wgServersProvider        func(ctx context.Context) []DynamicWGServer
	lastWGServers            []DynamicWGServer
	routerPoliciesProvider   func(ctx context.Context) (RouterPolicies, bool)
	lastKeptDenies           []string
	appLog                   *logging.ScopedLogger
	singboxBinary            string
	singboxCheckFn           func(ctx context.Context, stagedConfigDir string) error
	singboxStageCopyHook     func(relPath string) error
	varLibDir                string
	migrationStepHook        func(operation, stage string, stepIndex int, targetPath string) error
	journalCommitHooks       MigrationJournalCommitFaultHooks
	groupsSaveFaultHook      func(groups []Group) error
	netfilterStrictHook      func(ctx context.Context, ext ExtConfig, groups []Group) error
	forceKernelNetfilter     atomic.Bool
	recoveryRequired         bool
	recoveryReason           string
	recoveryMarker           *RecoveryMarker
}

// NewOperator creates a new firc Operator with sensible defaults for Keenetic Entware.
// It immediately loads any durable on-disk recovery marker before auto-start or migration.
func NewOperator(opts Options) *Operator {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	if opts.ConfigDir == "" {
		opts.ConfigDir = DefaultConfigDir
	}
	if opts.SocketPath == "" {
		if filepath.Clean(opts.ConfigDir) != filepath.Clean(DefaultConfigDir) {
			opts.SocketPath = filepath.Join(opts.ConfigDir, "firc.sock")
		} else {
			opts.SocketPath = DefaultSocketPath
		}
	}
	if opts.FircdBinary == "" {
		opts.FircdBinary = DefaultFircdBinary
	}
	if opts.TunvlessBin == "" {
		opts.TunvlessBin = DefaultTunvlessBinary
	}
	if opts.InitScript == "" {
		opts.InitScript = "/opt/etc/init.d/S99firc"
	}
	if opts.DNSProxyAddr == "" {
		opts.DNSProxyAddr = DefaultDNSProxyAddr
	}
	if opts.DNSProxyPort <= 0 {
		opts.DNSProxyPort = DefaultDNSProxyPort
	}
	if opts.InboundPort == 0 {
		opts.InboundPort = DefaultMixedInboundPort
	}
	if opts.StartTimeout <= 0 {
		opts.StartTimeout = 30 * time.Second
	}
	if opts.Foreign == nil && filepath.Clean(opts.ConfigDir) == filepath.Clean(DefaultConfigDir) {
		opts.Foreign = NewForeignDetector("")
	}
	if opts.ConntrackPath == "" {
		opts.ConntrackPath = "/proc/net/nf_conntrack"
	}
	op := &Operator{
		log:           log,
		appLog:        logging.NewScopedLogger(opts.AppLog, logging.GroupRouting, logging.SubAccessPolicy),
		configDir:     opts.ConfigDir,
		socketPath:    opts.SocketPath,
		fircdBinary:   opts.FircdBinary,
		tunvlessBin:   opts.TunvlessBin,
		initScript:    opts.InitScript,
		dnsProxyAddr:  opts.DNSProxyAddr,
		dnsProxyPort:  opts.DNSProxyPort,
		inboundPort:   opts.InboundPort,
		startTimeout:  opts.StartTimeout,
		conntrackPath: opts.ConntrackPath,
		foreign:       opts.Foreign,
		rci:           opts.RCI,
		hasComponent:  ndmsinfo.HasComponent,
		client:        NewClient(opts.SocketPath),
		singboxBinary: "/opt/bin/sing-box",
		cloudEpoch:    1,
	}
	if op.isSystemConfigDir() {
		op.lastOutPath = "/tmp/firc-init/fircd.last"
	}
	op.client.gate = op.CheckNotForeign
	op.extConfigGen.Store(1)
	if _, err := os.Stat(op.extConfigPath()); err == nil {
		op.extConfigSaved.Store(true)
	}
	op.loadRecoveryMarkerFromDisk()
	op.recoverMigrationJournalOnStartup()
	return op
}

// ConfigDir returns the directory where firc configuration files are stored.
func (o *Operator) ConfigDir() string {
	return o.configDir
}

// InboundPort returns the local mixed proxy port kept active by FIRC.
func (o *Operator) InboundPort() uint16 {
	if o == nil {
		return 0
	}
	return o.inboundPort
}

func (o *Operator) isSystemConfigDir() bool {
	return o != nil && filepath.Clean(o.configDir) == filepath.Clean(DefaultConfigDir)
}

// SetForceKernelNetfilter enables kernel iptables netfilter management on Linux even when
// using a non-default configDir (used by root network namespace integration tests).
func (o *Operator) SetForceKernelNetfilter(enabled bool) {
	if o == nil {
		return
	}
	o.forceKernelNetfilter.Store(enabled)
}

// SetVarLibDir overrides the FIRC runtime state directory (defaults to /opt/var/lib/firc
// when using the default system configDir). Primarily used by tests.
func (o *Operator) SetVarLibDir(dir string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.varLibDir = strings.TrimSpace(dir)
	o.mu.Unlock()
}

func (o *Operator) effectiveVarLibDir() string {
	o.mu.Lock()
	dir := o.varLibDir
	o.mu.Unlock()
	if dir != "" {
		return dir
	}
	if o.isSystemConfigDir() {
		return "/opt/var/lib/firc"
	}
	return filepath.Join(filepath.Dir(o.configDir), "firc-var-lib")
}

func (o *Operator) shouldManageKernelNetfilter() bool {
	if o == nil || o.foreignPresent() {
		return false
	}
	return o.managesKernelNetfilter()
}

// managesKernelNetfilter — то же без оглядки на сторонний firc: так снимаются
// собственные правила панели, оставшиеся от движка FIRC.
func (o *Operator) managesKernelNetfilter() bool {
	if runtime.GOOS == "linux" && o.isSystemConfigDir() {
		return true
	}
	if hasCustomExtIptablesRunner() {
		return true
	}
	return runtime.GOOS == "linux" && o.forceKernelNetfilter.Load()
}

// MigrationJournalPath returns the path to the durable file-operation journal in ConfigDir.
func (o *Operator) MigrationJournalPath() string {
	if o.configDir == "" {
		return ""
	}
	return filepath.Join(o.configDir, "migration-journal.json")
}

// RecoveryMarkerPath returns the path to the durable recovery state file in ConfigDir.
func (o *Operator) RecoveryMarkerPath() string {
	return filepath.Join(o.configDir, "recovery-state.json")
}

// RecoveryTombstonePath returns the path to the two-phase recovery clear tombstone file in ConfigDir.
func (o *Operator) RecoveryTombstonePath() string {
	return filepath.Join(o.configDir, "recovery-state.json.clearing")
}

func (o *Operator) loadRecoveryMarkerFromDisk() {
	if o.configDir == "" {
		return
	}
	markerPath := o.RecoveryMarkerPath()
	tombstonePath := o.RecoveryTombstonePath()

	activePath := markerPath
	isTombstone := false
	data, err := os.ReadFile(markerPath)
	if err != nil {
		if os.IsNotExist(err) {
			tData, tErr := os.ReadFile(tombstonePath)
			if tErr != nil {
				if !os.IsNotExist(tErr) {
					o.recoveryRequired = true
					o.recoveryReason = fmt.Sprintf("unreadable recovery-state.json.clearing: %v", tErr)
					o.recoveryMarker = &RecoveryMarker{
						Required:        true,
						Reason:          o.recoveryReason,
						TimestampUnixMs: time.Now().UnixMilli(),
					}
				}
				return
			}
			data = tData
			activePath = tombstonePath
			isTombstone = true
		} else {
			o.recoveryRequired = true
			o.recoveryReason = fmt.Sprintf("unreadable recovery-state.json: %v", err)
			o.recoveryMarker = &RecoveryMarker{
				Required:        true,
				Reason:          o.recoveryReason,
				TimestampUnixMs: time.Now().UnixMilli(),
			}
			return
		}
	}

	var marker RecoveryMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		o.recoveryRequired = true
		o.recoveryReason = fmt.Sprintf("corrupt %s: %v", filepath.Base(activePath), err)
		o.recoveryMarker = &RecoveryMarker{
			Required:        true,
			Reason:          o.recoveryReason,
			TimestampUnixMs: time.Now().UnixMilli(),
		}
		return
	}
	if isTombstone {
		marker.Required = true
		if strings.TrimSpace(marker.Reason) == "" {
			marker.Reason = "incomplete recovery clear tombstone"
		}
	}
	if marker.Required {
		o.recoveryRequired = true
		o.recoveryReason = marker.Reason
		cp := marker
		o.recoveryMarker = &cp
	}
}

// SetDefaultEngineHooks registers the authoritative production EngineSwitchHooks on the Operator
// so that all API, UI, and AI entry points share a single lifecycle coordinator.
func (o *Operator) SetDefaultEngineHooks(hooks EngineSwitchHooks) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.defaultHooks = hooks
}

// DefaultEngineHooks returns the registered default EngineSwitchHooks.
func (o *Operator) DefaultEngineHooks() EngineSwitchHooks {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.defaultHooks
}

func (o *Operator) SetManagedUplinkConfig(fn func(context.Context, []Tunnel) ([]Tunnel, error)) {
	o.mu.Lock()
	o.managedUplinkConfig = fn
	o.mu.Unlock()
}

// HasProductionEngineHooks reports whether all required production engine-switch hooks
// (GetCurrentEngine, StartTarget, StopEngine, CommitSettings) are wired.
func (o *Operator) HasProductionEngineHooks() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.defaultHooks.GetCurrentEngine != nil &&
		o.defaultHooks.StartTarget != nil &&
		o.defaultHooks.StopEngine != nil &&
		o.defaultHooks.CommitSettings != nil
}

// RecoveryInfo returns whether the operator is in recovery_required state and why.
func (o *Operator) RecoveryInfo() (bool, string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.recoveryRequired, o.recoveryReason
}

// IsRecoveryRequired reports whether the operator is currently in recovery_required state.
func (o *Operator) IsRecoveryRequired() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.recoveryRequired
}

// RecoveryReason returns the current recovery_required diagnostic reason.
func (o *Operator) RecoveryReason() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.recoveryReason
}

// RecoveryMarkerInfo returns a copy of the durable RecoveryMarker if recovery is required.
func (o *Operator) RecoveryMarkerInfo() *RecoveryMarker {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.recoveryRequired || o.recoveryMarker == nil {
		return nil
	}
	cp := *o.recoveryMarker
	return &cp
}

// SetRecoveryRequiredWithMeta marks the operator as requiring recovery with structured transition
// diagnostics and atomically persists recovery-state.json (including parent directory fsync) to disk.
// Even if disk persistence fails, in-memory recovery_required remains set so the running process
// refuses further mutations, while logging RECOVERY_MARKER_PERSIST_FAILED and returning ErrRecoveryPersistFailed.
// If an existing RecoveryMarker was already active in memory and persisting the updated marker fails,
// the previous durable marker's identity is retained in memory (with LastPersistError recorded) so
// in-memory state and post-restart state do not diverge (P1.2).
func (o *Operator) SetRecoveryRequiredWithMeta(marker RecoveryMarker) error {
	// Метка описывает наш рантайм и лежит в каталоге firc: при стороннем её
	// некуда и незачем писать.
	if err := o.CheckNotForeign(); err != nil {
		o.log.Warn("firc recovery marker skipped", "reason", marker.Reason, "error", err)
		return err
	}
	marker.Required = true
	if marker.TimestampUnixMs == 0 {
		marker.TimestampUnixMs = time.Now().UnixMilli()
	}

	o.mu.Lock()
	wasAlreadyInRecovery := o.recoveryRequired && o.recoveryMarker != nil
	var prevMarkerCopy *RecoveryMarker
	if wasAlreadyInRecovery {
		cpPrev := *o.recoveryMarker
		prevMarkerCopy = &cpPrev
		if marker.ParentTransitionID == "" && cpPrev.TransitionID != "" && cpPrev.TransitionID != marker.TransitionID {
			marker.ParentTransitionID = cpPrev.TransitionID
		}
		if marker.PreviousReason == "" && cpPrev.Reason != "" && cpPrev.Reason != marker.Reason {
			marker.PreviousReason = cpPrev.Reason
		}
		if marker.PreviousEngine == "" && cpPrev.PreviousEngine != "" {
			marker.PreviousEngine = cpPrev.PreviousEngine
		}
	}
	o.recoveryRequired = true
	if !wasAlreadyInRecovery {
		o.recoveryReason = marker.Reason
		cp := marker
		o.recoveryMarker = &cp
	}
	cfgDir := o.configDir
	markerPath := o.RecoveryMarkerPath()
	dirSyncHook := o.dirSyncHook
	o.mu.Unlock()

	handlePersistFail := func(pErr error) error {
		o.mu.Lock()
		o.recoveryRequired = true
		if wasAlreadyInRecovery && prevMarkerCopy != nil {
			restored := *prevMarkerCopy
			restored.LastPersistError = fmt.Sprintf("failed to persist updated marker (transition=%s, step=%s): %v",
				marker.TransitionID, marker.FailedStep, pErr)
			o.recoveryMarker = &restored
			o.recoveryReason = restored.Reason
		} else if o.recoveryMarker != nil {
			o.recoveryMarker.LastPersistError = pErr.Error()
		}
		o.mu.Unlock()
		o.log.Error("RECOVERY_MARKER_PERSIST_FAILED",
			"path", markerPath,
			"error", pErr,
			"marker", marker,
			"preservedPreviousMarker", prevMarkerCopy,
		)
		return pErr
	}

	if cfgDir == "" {
		return handlePersistFail(fmt.Errorf("%w: configDir is empty", ErrRecoveryPersistFailed))
	}
	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return handlePersistFail(fmt.Errorf("%w: marshal %s: %v", ErrRecoveryPersistFailed, markerPath, err))
	}
	if err := atomicWriteYAMLFileWithSyncHook(markerPath, data, 0600, dirSyncHook); err != nil {
		return handlePersistFail(fmt.Errorf("%w: write %s: %v", ErrRecoveryPersistFailed, markerPath, err))
	}
	_ = os.Remove(o.RecoveryTombstonePath())

	o.mu.Lock()
	o.recoveryRequired = true
	o.recoveryReason = marker.Reason
	cp := marker
	o.recoveryMarker = &cp
	o.mu.Unlock()
	return nil
}

// SetRecoveryRequired marks the operator as requiring recovery with a diagnostic reason
// and persists the marker to disk.
func (o *Operator) SetRecoveryRequired(reason string) error {
	return o.SetRecoveryRequiredWithMeta(RecoveryMarker{
		Required:        true,
		Reason:          reason,
		TimestampUnixMs: time.Now().UnixMilli(),
	})
}

// restoreRecoveryTombstone atomically creates the recovery tombstone with mode 0600,
// executes file.Sync(), Close(), os.Rename(), and syncParentDir() so that emergency
// tombstone rematerialization is checked and durably synced (P0.1).
func restoreRecoveryTombstone(tombstonePath string, data []byte, hooks RecoveryClearFaultHooks, fallbackDirSyncHook func(string) error) error {
	dir := filepath.Dir(tombstonePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(tombstonePath)+".restore.*")
	if err != nil {
		return fmt.Errorf("create temp tombstone for %s: %w", tombstonePath, err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0600); err != nil {
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if hooks.RestoreTombstoneWrite != nil {
		if err := hooks.RestoreTombstoneWrite(tmpName); err != nil {
			return fmt.Errorf("write restored tombstone %s: %w", tmpName, err)
		}
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write restored tombstone %s: %w", tmpName, err)
	}
	if hooks.RestoreTombstoneSync != nil {
		if err := hooks.RestoreTombstoneSync(tmpName); err != nil {
			return fmt.Errorf("fsync restored tombstone %s: %w", tmpName, err)
		}
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("fsync restored tombstone %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close restored tombstone %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, tombstonePath); err != nil {
		return fmt.Errorf("rename restored tombstone %s -> %s: %w", tmpName, tombstonePath, err)
	}
	cleanup = false
	dirHook := fallbackDirSyncHook
	if hooks.RestoreTombstoneDirSync != nil {
		dirHook = hooks.RestoreTombstoneDirSync
	}
	if err := syncParentDir(tombstonePath, dirHook); err != nil {
		return fmt.Errorf("sync parent dir after restoring tombstone %s: %w", tombstonePath, err)
	}
	return nil
}

// ClearRecovery executes a two-phase durable recovery clear protocol (P0.1):
//  1. Phase 1 (Tombstone Transition): Atomically renames recovery-state.json to the
//     fail-closed tombstone recovery-state.json.clearing (or uses an existing tombstone
//     from a previously interrupted clear) and fsyncs the parent directory.
//     If any step or the Phase 1 directory fsync fails, recovery-state.json or
//     recovery-state.json.clearing remains on disk so a restarted Operator stays fail-closed.
//  2. Phase 2 (Tombstone Unlink & Final Directory Fsync): Removes recovery-state.json.clearing,
//     verifies its removal, and fsyncs the parent directory a second time.
//     If the Phase 2 directory fsync fails in a live OS kernel, restoreRecoveryTombstone
//     atomically and durably rematerializes the tombstone (0600 + file.Sync + Close + Rename +
//     syncParentDir), logging the Phase 2 error and any rematerialization error separately and
//     returning both via errors.Join while keeping recoveryRequired=true in memory.
//  3. Clears in-memory recovery_required state ONLY after both durable phases succeed.
func (o *Operator) ClearRecovery() error {
	if err := o.CheckNotForeign(); err != nil {
		return err
	}
	o.mu.Lock()
	cfgDir := o.configDir
	markerPath := o.RecoveryMarkerPath()
	tombstonePath := o.RecoveryTombstonePath()
	dirSyncHook := o.dirSyncHook
	clearHooks := o.clearFaultHooks
	inMemReq := o.recoveryRequired
	var inMemMarker *RecoveryMarker
	if o.recoveryMarker != nil {
		cp := *o.recoveryMarker
		inMemMarker = &cp
	}
	inMemReason := o.recoveryReason
	o.mu.Unlock()

	ensureInMemoryFailClosed := func() {
		o.mu.Lock()
		o.recoveryRequired = true
		if o.recoveryReason == "" {
			if inMemReason != "" {
				o.recoveryReason = inMemReason
			} else {
				o.recoveryReason = "recovery clear failed before durable completion"
			}
		}
		if o.recoveryMarker == nil && inMemMarker != nil {
			cp := *inMemMarker
			o.recoveryMarker = &cp
		}
		o.mu.Unlock()
	}

	if cfgDir != "" && markerPath != "" {
		var tombstoneData []byte
		if data, rerr := os.ReadFile(markerPath); rerr == nil && len(data) > 0 {
			tombstoneData = data
		} else if data, rerr := os.ReadFile(tombstonePath); rerr == nil && len(data) > 0 {
			tombstoneData = data
		} else {
			fallbackMarker := RecoveryMarker{
				Required:        true,
				Reason:          inMemReason,
				TimestampUnixMs: time.Now().UnixMilli(),
			}
			if inMemMarker != nil {
				fallbackMarker = *inMemMarker
				fallbackMarker.Required = true
			}
			if fallbackMarker.Reason == "" {
				fallbackMarker.Reason = "incomplete recovery clear tombstone"
			}
			tombstoneData, _ = json.MarshalIndent(fallbackMarker, "", "  ")
		}

		// Phase 1: Transition active marker to durable tombstone (recovery-state.json -> recovery-state.json.clearing)
		markerInfo, markerStatErr := os.Stat(markerPath)
		if markerStatErr == nil {
			if markerInfo.IsDir() {
				ensureInMemoryFailClosed()
				cErr := fmt.Errorf("%w: marker path %s is a directory", ErrRecoveryPersistFailed, markerPath)
				o.log.Error("RECOVERY_MARKER_CLEAR_FAILED",
					"phase", "tombstone_rename",
					"path", markerPath,
					"error", cErr,
				)
				return cErr
			}
			if err := os.Rename(markerPath, tombstonePath); err != nil {
				ensureInMemoryFailClosed()
				cErr := fmt.Errorf("%w: rename %s -> %s: %v", ErrRecoveryPersistFailed, markerPath, tombstonePath, err)
				o.log.Error("RECOVERY_MARKER_CLEAR_FAILED",
					"phase", "tombstone_rename",
					"path", markerPath,
					"tombstone", tombstonePath,
					"error", cErr,
				)
				return cErr
			}
		} else if !os.IsNotExist(markerStatErr) {
			ensureInMemoryFailClosed()
			cErr := fmt.Errorf("%w: stat %s: %v", ErrRecoveryPersistFailed, markerPath, markerStatErr)
			o.log.Error("RECOVERY_MARKER_CLEAR_FAILED",
				"phase", "tombstone_stat",
				"path", markerPath,
				"error", cErr,
			)
			return cErr
		} else {
			tombInfo, tombStatErr := os.Stat(tombstonePath)
			if tombStatErr == nil && tombInfo.IsDir() {
				ensureInMemoryFailClosed()
				cErr := fmt.Errorf("%w: tombstone path %s is a directory", ErrRecoveryPersistFailed, tombstonePath)
				o.log.Error("RECOVERY_MARKER_CLEAR_FAILED",
					"phase", "tombstone_stat",
					"tombstone", tombstonePath,
					"error", cErr,
				)
				return cErr
			} else if tombStatErr != nil && !os.IsNotExist(tombStatErr) {
				ensureInMemoryFailClosed()
				cErr := fmt.Errorf("%w: stat tombstone %s: %v", ErrRecoveryPersistFailed, tombstonePath, tombStatErr)
				o.log.Error("RECOVERY_MARKER_CLEAR_FAILED",
					"phase", "tombstone_stat",
					"tombstone", tombstonePath,
					"error", cErr,
				)
				return cErr
			} else if os.IsNotExist(tombStatErr) && inMemReq {
				phase1InitHook := dirSyncHook
				if clearHooks.Phase1DirSync != nil {
					phase1InitHook = clearHooks.Phase1DirSync
				}
				if werr := restoreRecoveryTombstone(tombstonePath, tombstoneData, RecoveryClearFaultHooks{}, phase1InitHook); werr != nil {
					ensureInMemoryFailClosed()
					cErr := fmt.Errorf("%w: write tombstone %s: %v", ErrRecoveryPersistFailed, tombstonePath, werr)
					o.log.Error("RECOVERY_MARKER_CLEAR_FAILED",
						"phase", "tombstone_write",
						"tombstone", tombstonePath,
						"error", cErr,
					)
					return cErr
				}
			}
		}

		// Phase 1 Directory Fsync: durably commit the tombstone transition before unlinking
		phase1Hook := dirSyncHook
		if clearHooks.Phase1DirSync != nil {
			phase1Hook = clearHooks.Phase1DirSync
		}
		if err := syncParentDir(tombstonePath, phase1Hook); err != nil {
			ensureInMemoryFailClosed()
			cErr := fmt.Errorf("%w: phase 1 tombstone dir sync of %s: %v", ErrRecoveryPersistFailed, tombstonePath, err)
			o.log.Error("RECOVERY_MARKER_CLEAR_FAILED",
				"phase", "tombstone_dir_sync",
				"tombstone", tombstonePath,
				"error", cErr,
			)
			return cErr
		}

		// Phase 2: Remove tombstone and perform final directory fsync
		if err := os.Remove(tombstonePath); err != nil && !os.IsNotExist(err) {
			ensureInMemoryFailClosed()
			cErr := fmt.Errorf("%w: remove tombstone %s: %v", ErrRecoveryPersistFailed, tombstonePath, err)
			o.log.Error("RECOVERY_MARKER_CLEAR_FAILED",
				"phase", "tombstone_remove",
				"tombstone", tombstonePath,
				"error", cErr,
			)
			return cErr
		}
		if _, statErr := os.Stat(tombstonePath); statErr == nil || !os.IsNotExist(statErr) {
			ensureInMemoryFailClosed()
			cErr := fmt.Errorf("%w: verify removal of tombstone %s: %v", ErrRecoveryPersistFailed, tombstonePath, statErr)
			o.log.Error("RECOVERY_MARKER_CLEAR_FAILED",
				"phase", "tombstone_verify_remove",
				"tombstone", tombstonePath,
				"error", cErr,
			)
			return cErr
		}
		phase2Hook := dirSyncHook
		if clearHooks.Phase2FinalDirSync != nil {
			phase2Hook = clearHooks.Phase2FinalDirSync
		}
		if err := syncParentDir(tombstonePath, phase2Hook); err != nil {
			ensureInMemoryFailClosed()
			cErr := fmt.Errorf("%w: phase 2 final dir sync of %s: %v", ErrRecoveryPersistFailed, tombstonePath, err)
			o.log.Error("RECOVERY_MARKER_CLEAR_FAILED",
				"phase", "final_dir_sync",
				"tombstone", tombstonePath,
				"error", cErr,
			)
			// Checked, durable rematerialization of the tombstone (0600 + file.Sync + Close + Rename + syncParentDir)
			if rErr := restoreRecoveryTombstone(tombstonePath, tombstoneData, clearHooks, dirSyncHook); rErr != nil {
				rematErr := fmt.Errorf("%w: rematerialize tombstone %s after phase 2 dir sync failure: %v", ErrRecoveryPersistFailed, tombstonePath, rErr)
				o.log.Error("RECOVERY_TOMBSTONE_REMATERIALIZE_FAILED",
					"tombstone", tombstonePath,
					"phase2Error", cErr,
					"restoreError", rematErr,
				)
				return errors.Join(cErr, rematErr)
			}
			o.log.Warn("RECOVERY_TOMBSTONE_REMATERIALIZED",
				"tombstone", tombstonePath,
				"phase2Error", cErr,
			)
			return cErr
		}
	}

	if jPath := o.MigrationJournalPath(); jPath != "" {
		if _, statErr := os.Stat(jPath); statErr == nil {
			if err := o.cleanupMigrationJournalOnClear(); err != nil {
				ensureInMemoryFailClosed()
				return fmt.Errorf("%w: clear migration journal %s: %v", ErrRecoveryPersistFailed, jPath, err)
			}
		}
	}

	o.mu.Lock()
	o.recoveryRequired = false
	o.recoveryReason = ""
	o.recoveryMarker = nil
	o.mu.Unlock()
	return nil
}

// ForceClearRecoveryAdmin performs an emergency break-glass administrative clear of recovery_required
// only when explicit confirmation token "FORCE_CLEAR_UNVERIFIED", a non-empty actor identity, and
// (when a TransitionID is recorded on the active marker) the matching incident-bound TransitionID are supplied.
func (o *Operator) ForceClearRecoveryAdmin(confirmToken string, opts ...BreakGlassOptions) error {
	if confirmToken != "FORCE_CLEAR_UNVERIFIED" {
		return errors.New("firc: administrative recovery clear requires confirm token FORCE_CLEAR_UNVERIFIED; use ReconcileRecovery instead")
	}
	var opt BreakGlassOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	actor := strings.TrimSpace(opt.Actor)
	if actor == "" {
		return errors.New("firc: administrative break-glass clear requires non-empty actor identity")
	}
	prevMarker := o.RecoveryMarkerInfo()
	if prevMarker != nil && strings.TrimSpace(prevMarker.TransitionID) != "" {
		if strings.TrimSpace(opt.TransitionID) != strings.TrimSpace(prevMarker.TransitionID) {
			return fmt.Errorf("firc: administrative break-glass clear requires matching incident-bound transitionId %q", prevMarker.TransitionID)
		}
	}
	if err := o.ClearRecovery(); err != nil {
		return err
	}
	_ = o.ReleaseCutoverBarrier(context.Background())
	o.log.Warn("BREAK_GLASS_RECOVERY_FORCE_CLEAR",
		"actor", actor,
		"source", opt.Source,
		"transitionId", opt.TransitionID,
		"previousMarker", prevMarker,
	)
	return nil
}

func (o *Operator) checkMutationAllowed() error {
	if err := o.CheckNotForeign(); err != nil {
		return err
	}
	return o.checkRecoveryClear()
}

// checkRecoveryClear — незавершённое восстановление нашего рантайма.
func (o *Operator) checkRecoveryClear() error {
	if o.recoveryDeferred.Load() {
		return ErrRestartRequired
	}
	req, reason := o.RecoveryInfo()
	if req {
		return fmt.Errorf("%w: %s", ErrRecoveryRequired, reason)
	}
	if err := o.checkNoUncommittedMigrationJournal(); err != nil {
		return err
	}
	return nil
}

// SetSingboxValidator overrides the Sing-box candidate configuration validator (`sing-box check -C <dir>`)
// used during preflight validation before replacing working Sing-box configuration files.
func (o *Operator) SetSingboxValidator(fn func(ctx context.Context, stagedConfigDir string) error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.singboxCheckFn = fn
}

// SetSingboxBinary configures the path to the Sing-box executable used by default candidate validation.
func (o *Operator) SetSingboxBinary(path string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.singboxBinary = path
}

// SetMigrationStepHook installs a step-level fault/crash injection callback invoked after each individual
// file mutation while the durable migration journal is in an uncommitted stage on disk.
func (o *Operator) SetMigrationStepHook(fn func(operation, stage string, stepIndex int, targetPath string) error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.migrationStepHook = fn
}

// SetMigrationJournalCommitFaultHooks installs fault/crash injection callbacks around the
// two-phase commit boundary of completeMigrationJournal.
func (o *Operator) SetMigrationJournalCommitFaultHooks(hooks MigrationJournalCommitFaultHooks) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.journalCommitHooks = hooks
}

// SetSingboxStageCopyHook installs a fault-injection callback invoked when staging preserved
// Sing-box config.d files during preflight verification (V2).
func (o *Operator) SetSingboxStageCopyHook(fn func(relPath string) error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.singboxStageCopyHook = fn
}

// Client returns the IPC client connected to fircd.
func (o *Operator) Client() *Client {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.client
}

// SetClient overrides the IPC client (used in unit tests).
func (o *Operator) SetClient(c *Client) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if c != nil {
		c.gate = o.CheckNotForeign
	}
	o.client = c
}

// SetLifecycleHooks overrides Start/Stop execution hooks for fault-injection tests.
func (o *Operator) SetLifecycleHooks(startFn, stopFn func(ctx context.Context) error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.startHook = startFn
	o.stopHook = stopFn
}

// SetDirSyncHook overrides parent directory fsync for durability fault-injection tests (P0.1).
func (o *Operator) SetDirSyncHook(fn func(dir string) error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.dirSyncHook = fn
}

// SetRecoveryClearFaultHooks configures phase-specific fault injection hooks for ClearRecovery (P0.1).
func (o *Operator) SetRecoveryClearFaultHooks(hooks RecoveryClearFaultHooks) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.clearFaultHooks = hooks
}

func syncParentDir(path string, hook func(dir string) error) error {
	dir := filepath.Dir(path)
	if hook != nil {
		return hook(dir)
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	dirFile, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open parent dir %s for fsync: %w", dir, err)
	}
	if err := dirFile.Sync(); err != nil {
		_ = dirFile.Close()
		return fmt.Errorf("fsync parent dir %s: %w", dir, err)
	}
	if err := dirFile.Close(); err != nil {
		return fmt.Errorf("close parent dir %s after fsync: %w", dir, err)
	}
	return nil
}

// DNSProxyEndpoint returns the host:port of fircd's local FakeIP DNS proxy (e.g., "127.0.0.1:3553").
func (o *Operator) DNSProxyEndpoint() string {
	return fmt.Sprintf("%s:%d", o.dnsProxyAddr, o.dnsProxyPort)
}

// ConfigPath returns the path to firc.conf.
func (o *Operator) ConfigPath() string {
	return filepath.Join(o.configDir, "firc.conf")
}

// GroupsPath returns the path to groups.yaml.
func (o *Operator) GroupsPath() string {
	return filepath.Join(o.configDir, "groups.yaml")
}

// TunnelsPath returns the path to tunnels.yaml.
func (o *Operator) TunnelsPath() string {
	return filepath.Join(o.configDir, "tunnels.yaml")
}

func atomicWriteYAMLFile(path string, data []byte, perm os.FileMode) error {
	return atomicWriteYAMLFileWithSyncHook(path, data, perm, nil)
}

// atomicWriteYAMLFileWithSyncHook пишет через storage.AtomicWritePerm, а затем
// ещё раз синкает каталог строго: у storage fsync каталога best-effort, а здесь
// его отказ — ошибка записи, и через dirSyncHook её подставляют тесты.
func atomicWriteYAMLFileWithSyncHook(path string, data []byte, perm os.FileMode, dirSyncHook func(dir string) error) error {
	if err := storage.AtomicWritePerm(path, data, perm); err != nil {
		return err
	}
	if err := syncParentDir(path, dirSyncHook); err != nil {
		return fmt.Errorf("sync parent dir of %s: %w", path, err)
	}
	return nil
}

type fileSnapshot struct {
	path   string
	exists bool
	data   []byte
	perm   os.FileMode
}

func captureFileSnapshot(path string, defaultPerm os.FileMode) (fileSnapshot, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fileSnapshot{path: path, exists: false, perm: defaultPerm}, nil
		}
		return fileSnapshot{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fileSnapshot{}, err
	}
	return fileSnapshot{
		path:   path,
		exists: true,
		data:   data,
		perm:   info.Mode().Perm(),
	}, nil
}

func restoreFileSnapshot(snap fileSnapshot) error {
	if !snap.exists {
		if err := os.Remove(snap.path); err != nil && !os.IsNotExist(err) {
			return err
		}
		if _, statErr := os.Stat(snap.path); statErr == nil || !os.IsNotExist(statErr) {
			return fmt.Errorf("verify removal of %s failed: %v", snap.path, statErr)
		}
		return nil
	}
	if err := atomicWriteYAMLFile(snap.path, snap.data, snap.perm); err != nil {
		return err
	}
	readBack, err := os.ReadFile(snap.path)
	if err != nil {
		return fmt.Errorf("read back restored %s: %w", snap.path, err)
	}
	if !bytes.Equal(readBack, snap.data) {
		return fmt.Errorf("restored %s content mismatch", snap.path)
	}
	return nil
}

// EnsureConfigFiles creates the config directory and writes a default firc.conf if none exists.
func (o *Operator) EnsureConfigFiles() error {
	if err := o.CheckNotForeign(); err != nil {
		return err
	}
	if err := os.MkdirAll(o.configDir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", o.configDir, err)
	}
	confPath := o.ConfigPath()
	if _, err := os.Stat(confPath); err == nil {
		return nil
	}
	content := fmt.Sprintf(`version: "1.0"
app:
  logLevel: info
  httpWeb:
    enabled: false
    host:
      address: 0.0.0.0
      port: 8880
  dnsProxy:
    host:
      address: %s
      port: %d
    disableRemap53: false
  fakeip:
    v4Pool: 198.18.0.0/15
`, o.dnsProxyAddr, o.dnsProxyPort)
	return atomicWriteYAMLFile(confPath, []byte(content), 0644)
}

// ValidateBinaries checks that both fircd and tunvless binaries exist, are regular
// non-empty executable files, and verifies SHA-256 checksums when a SHA256SUMS manifest
// is present in configDir.
func (o *Operator) ValidateBinaries() error {
	if err := o.CheckNotForeign(); err != nil {
		return err
	}
	fircdPath, tunvlessPath := o.fircdBinary, o.tunvlessBin
	for _, bin := range []struct {
		name string
		path string
	}{
		{"fircd", fircdPath},
		{"tunvless", tunvlessPath},
	} {
		info, err := os.Stat(bin.path)
		if err != nil {
			return fmt.Errorf("%s binary missing at %s: %w", bin.name, bin.path, err)
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			return fmt.Errorf("%s binary at %s is invalid", bin.name, bin.path)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0 {
			return fmt.Errorf("%s binary at %s is not executable (mode %04o)", bin.name, bin.path, info.Mode().Perm())
		}
	}
	manifestPath := filepath.Join(o.configDir, "SHA256SUMS")
	if _, err := os.Stat(manifestPath); err == nil {
		if err := VerifyChecksumManifest(manifestPath, filepath.Dir(fircdPath)); err != nil {
			return err
		}
	}
	return nil
}

// CheckRuntimeInstalledForSwitch fails closed before migration writes disk configs or
// creates snapshots when switching to FIRC is requested in production without fircd/tunvless binaries.
func (o *Operator) CheckRuntimeInstalledForSwitch() error {
	o.mu.Lock()
	hasStartHook := o.startHook != nil
	o.mu.Unlock()
	if hasStartHook {
		return nil
	}
	if _, err := o.validateProductionPreflight(); err != nil {
		return fmt.Errorf("AWGM Route runtime is not installed or invalid (install awgm-route): %w", err)
	}
	return nil
}

// validateProductionPreflight validates fircd/tunvless binary existence, executable permissions,
// SHA-256 checksums (when manifest is present), ELF headers, and optional initScript validity.
// Unlike the previous implementation, binary + ELF validation ALWAYS runs even when initScript
// (/opt/etc/init.d/S99firc) is present on disk (P0.2).
func (o *Operator) validateProductionPreflight() (string, error) {
	if err := o.ValidateBinaries(); err != nil {
		return "preflight_validate_binaries", err
	}
	for _, binPath := range []string{o.fircdBinary, o.tunvlessBin} {
		ef, err := elf.Open(binPath)
		if err != nil {
			return "preflight_validate_elf", fmt.Errorf("invalid ELF binary %s: %w", binPath, err)
		}
		_ = ef.Close()
	}
	info, statErr := os.Stat(o.initScript)
	if statErr != nil && !os.IsNotExist(statErr) {
		return "preflight_stat_init_script", fmt.Errorf("stat init script %s: %w", o.initScript, statErr)
	}
	if statErr == nil {
		if !info.Mode().IsRegular() || info.Size() == 0 {
			return "preflight_validate_init_script", fmt.Errorf("init script at %s is not a valid non-empty regular file", o.initScript)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0 {
			return "preflight_validate_init_script", fmt.Errorf("init script at %s is not executable (mode %04o)", o.initScript, info.Mode().Perm())
		}
	}
	return "", nil
}

// IsRunning returns true if fircd responds on its Unix domain socket.
func (o *Operator) IsRunning(ctx context.Context) bool {
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err := o.Client().Ping(pingCtx)
	return err == nil
}

// GetStatus returns the current installation and runtime status of fircd and tunvless.
func (o *Operator) GetStatus(ctx context.Context, selected bool) Status {
	return o.getStatus(ctx, selected, true)
}

// GetOfflineStatus returns disk-backed FIRC status without probing fircd IPC
// or starting any daemon. Used when FIRC is explicitly not selected.
func (o *Operator) GetOfflineStatus(selected bool) Status {
	return o.getStatus(context.Background(), selected, false)
}

func (o *Operator) getStatus(ctx context.Context, selected, allowLive bool) Status {
	if found := o.ForeignFirc(); found != nil {
		return Status{
			Selected:         selected,
			Foreign:          found,
			SocketPath:       o.socketPath,
			DNSProxyEndpoint: o.DNSProxyEndpoint(),
			InboundPort:      o.inboundPort,
		}
	}
	_, errFircd := os.Stat(o.fircdBinary)
	_, errTun := os.Stat(o.tunvlessBin)
	running := allowLive && o.IsRunning(ctx)
	recReq, recReason := o.RecoveryInfo()

	st := Status{
		FircdInstalled:    errFircd == nil,
		TunvlessInstalled: errTun == nil,
		Running:           running,
		Selected:          selected,
		RecoveryRequired:  recReq,
		RecoveryReason:    recReason,
		RecoveryMarker:    o.RecoveryMarkerInfo(),
		RestartRequired:   o.recoveryDeferred.Load(),
		SocketPath:        o.socketPath,
		DNSProxyEndpoint:  o.DNSProxyEndpoint(),
		InboundPort:       o.inboundPort,
	}

	if running {
		client := o.Client()
		qCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		if tuns, err := client.GetTunnels(qCtx); err == nil {
			st.TunnelsCount = len(tuns)
		}
		if grps, err := client.GetGroups(qCtx, false); err == nil {
			st.GroupsCount = len(grps)
		}
		if nf, err := client.Ping(qCtx); err == nil {
			st.Netfilter = nf
		}
		cancel()
	} else {
		if tuns, err := o.LoadTunnelsFile(); err == nil {
			st.TunnelsCount = len(tuns)
		}
		if grps, err := o.LoadGroupsFile(); err == nil {
			st.GroupsCount = len(grps)
		}
	}
	return st
}

// Start ensures fircd is running and waits until the Unix socket API is ready.
// Internal/background Start respects an explicit .awgm-disabled marker.
func (o *Operator) Start(ctx context.Context) error {
	_, err := o.startRuntimeAuthorized(ctx, false)
	return err
}

var ansiEscapeRE = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")

func sanitizeStartDiagnostic(raw string) string {
	clean := ansiEscapeRE.ReplaceAllString(raw, "")
	clean = strings.ReplaceAll(clean, "\r", "\n")
	lines := strings.Split(clean, "\n")
	parts := make([]string, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		parts = append(parts, strings.Join(fields, " "))
	}
	joined := strings.Join(parts, "; ")
	const maxDiagBytes = 400
	if len(joined) > maxDiagBytes {
		return strings.TrimSpace(joined[:maxDiagBytes-3]) + "..."
	}
	return joined
}

func (o *Operator) fircStartDiagnostic(initOut []byte) string {
	outStr := sanitizeStartDiagnostic(string(initOut))
	lastStr := ""
	if o.lastOutPath != "" {
		if b, err := os.ReadFile(o.lastOutPath); err == nil {
			lastStr = sanitizeStartDiagnostic(string(b))
		}
	}
	combined := outStr
	switch {
	case outStr == "":
		combined = lastStr
	case lastStr != "" && !strings.Contains(outStr, lastStr):
		combined = outStr + "; last: " + lastStr
	}
	const maxDiagBytes = 400
	if len(combined) > maxDiagBytes {
		return strings.TrimSpace(combined[:maxDiagBytes-3]) + "..."
	}
	return combined
}

func (o *Operator) startRuntime(ctx context.Context) error {
	o.mu.Lock()
	startHook := o.startHook
	o.mu.Unlock()
	if err := o.CheckNotForeign(); err != nil {
		return err
	}
	InvalidateOutputRoutingCache()
	if startHook != nil {
		return startHook(ctx)
	}

	if o.IsRunning(ctx) {
		o.ensureExtWatchdog()
		rCtx, cancel := context.WithTimeout(o.extWatchdogContext(), 15*time.Second)
		err := o.reconcileExtOnStartLocked(rCtx)
		cancel()
		if err != nil {
			return err
		}
		return nil
	}
	if err := o.EnsureConfigFiles(); err != nil {
		return err
	}
	if err := o.ensureRemapListenAddr(); err != nil {
		o.log.Warn("firc: remap53 listen address check failed", "error", err)
	}

	var initOut []byte
	o.mu.Lock()
	if err := o.ValidateBinaries(); err != nil {
		o.mu.Unlock()
		return err
	}
	initScript := o.initScript
	if info, err := os.Stat(initScript); err == nil {
		if !info.Mode().IsRegular() || info.Size() == 0 || (runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0) {
			o.mu.Unlock()
			return fmt.Errorf("init script at %s is not a valid executable file", initScript)
		}
		o.mu.Unlock()
		cmd := exec.CommandContext(ctx, initScript, "start")
		cmd.Env = append(os.Environ(), "STEER_TUN_SPARES=0")
		cmd.WaitDelay = 5 * time.Second
		out, runErr := cmd.CombinedOutput()
		initOut = out
		if runErr != nil && !errors.Is(runErr, exec.ErrWaitDelay) {
			if detail := o.fircStartDiagnostic(initOut); detail != "" {
				return fmt.Errorf("run %s start: %w (%s)", initScript, runErr, detail)
			}
			return fmt.Errorf("run %s start: %w", initScript, runErr)
		}
	} else if !os.IsNotExist(err) {
		o.mu.Unlock()
		return fmt.Errorf("stat init script %s: %w", initScript, err)
	} else {
		_ = os.MkdirAll(filepath.Dir(o.socketPath), 0755)
		cmd := exec.Command(o.fircdBinary, "--config", o.ConfigPath())
		cmd.Env = append(os.Environ(), "STEER_TUN_SPARES=0")
		if err := cmd.Start(); err != nil {
			o.mu.Unlock()
			return fmt.Errorf("start fircd: %w", err)
		}
		o.cmd = cmd
		o.mu.Unlock()
	}

	deadline := time.Now().Add(o.startTimeout)
	for time.Now().Before(deadline) {
		if o.IsRunning(ctx) {
			o.ensureExtWatchdog()
			rCtx, cancel := context.WithTimeout(o.extWatchdogContext(), 15*time.Second)
			err := o.reconcileExtOnStartLocked(rCtx)
			cancel()
			if err != nil {
				return err
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	if detail := o.fircStartDiagnostic(initOut); detail != "" {
		return fmt.Errorf("fircd did not become ready on %s within %s (%s)", o.socketPath, o.startTimeout, detail)
	}
	return fmt.Errorf("fircd did not become ready on %s within %s", o.socketPath, o.startTimeout)
}

// Stop stops the managed fircd daemon.
func (o *Operator) Stop(ctx context.Context) error {
	o.lifecycleMu.Lock()
	defer o.lifecycleMu.Unlock()
	return o.stopRuntime(ctx)
}

func (o *Operator) stopRuntime(ctx context.Context) error {
	extNetfilterGen.Add(1)
	o.cancelExtWatchdog()
	o.abortKeenDNSCloudSync()
	InvalidateOutputRoutingCache()
	runCleanup := func() {
		if o.isSystemConfigDir() {
			cleanupExtNetfilter(ctx, "")
		} else if o.managesKernelNetfilter() || hasCustomExtIpsetRunner() {
			cleanupExtNetfilterRules(ctx)
		}
	}
	if o.foreignPresent() {
		// Сторонний демон не наш: снимаем только свои цепочки FEXT_* и хук,
		// чтобы панель могла уйти с движка FIRC.
		runCleanup()
		return nil
	}
	o.mu.Lock()
	stopHook := o.stopHook
	initScript := o.initScript
	o.mu.Unlock()
	// Remove FEXT_* chains before stopping fircd so daemon ipset cleanup does not hit EBUSY
	// on FIRC_*S4/S6 sets referenced by FEXT_*, then sweep again after the daemon stops.
	runCleanup()
	if stopHook != nil {
		if err := stopHook(ctx); err != nil {
			runCleanup()
			return err
		}
		runCleanup()
		cleanupOrphanFircIpsets(ctx)
		return nil
	}

	var initErr error
	o.mu.Lock()
	if _, err := os.Stat(initScript); err == nil {
		cmd := exec.CommandContext(ctx, initScript, "stop")
		if err := cmd.Run(); err != nil {
			initErr = fmt.Errorf("run %s stop: %w", initScript, err)
		}
	}
	if initErr == nil && o.cmd != nil && o.cmd.Process != nil {
		_ = o.cmd.Process.Signal(syscall.SIGTERM)
		_, _ = o.cmd.Process.Wait()
		o.cmd = nil
	}
	o.mu.Unlock()
	runCleanup()
	if initErr != nil {
		return initErr
	}
	cleanupOrphanFircIpsets(ctx)
	return nil
}

// StopDirectChild terminates only a directly spawned child fircd process (used when
// S99firc init script is absent, e.g. in tests), leaving an independent S99firc
// daemon and its tunnels untouched across awg-manager restarts and upgrades.
func (o *Operator) StopDirectChild() {
	extNetfilterGen.Add(1)
	o.cancelExtWatchdog()
	o.abortKeenDNSCloudSync()
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.cmd != nil && o.cmd.Process != nil {
		_ = o.cmd.Process.Signal(syscall.SIGTERM)
		_, _ = o.cmd.Process.Wait()
		o.cmd = nil
	}
}

// Reload sends SIGHUP to fircd or triggers config reload.
func (o *Operator) Reload() error {
	InvalidateOutputRoutingCache()
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.cmd != nil && o.cmd.Process != nil {
		return o.cmd.Process.Signal(syscall.SIGHUP)
	}
	return nil
}

// EnsureInboundPort1099 guarantees that at least one enabled tunnel exposes the local
// SOCKS5 inbound on port 1099 (unless already present on one of the tunnels).
func EnsureInboundPort1099(tunnels []Tunnel, port uint16) []Tunnel {
	if port == 0 {
		port = DefaultMixedInboundPort
	}
	if len(tunnels) == 0 {
		return tunnels
	}
	for i := range tunnels {
		NormalizeTunnelNodeRefs(&tunnels[i])
		for j := range tunnels[i].Inbounds {
			// Пустой и 127.0.0.1 fircd отвергает. "lo" допустим и остаётся как есть:
			// на нём живёт служебный вход без пароля (PrepareUplink), в LAN ему нельзя.
			if tunnels[i].Inbounds[j].Listen == "" || tunnels[i].Inbounds[j].Listen == "127.0.0.1" {
				tunnels[i].Inbounds[j].Listen = "br0"
			}
			if strings.TrimSpace(tunnels[i].Inbounds[j].Node) == "" {
				tunnels[i].Inbounds[j].Node = defaultTunnelNodeKey(tunnels[i])
			} else {
				tunnels[i].Inbounds[j].Node = NormalizeFircNodeRef(tunnels[i].Inbounds[j].Node, tunnels[i])
			}
		}
	}
	for _, t := range tunnels {
		for _, inb := range t.Inbounds {
			if inb.Port == port {
				return tunnels
			}
		}
	}
	out := make([]Tunnel, len(tunnels))
	copy(out, tunnels)
	targetIdx := 0
	for i := range out {
		if out[i].Enable {
			targetIdx = i
			break
		}
	}
	inbs := make([]TunnelInbound, len(out[targetIdx].Inbounds), len(out[targetIdx].Inbounds)+1)
	copy(inbs, out[targetIdx].Inbounds)
	inbs = append(inbs, TunnelInbound{
		Port:   port,
		Listen: "br0",
		Node:   defaultTunnelNodeKey(out[targetIdx]),
	})
	out[targetIdx].Inbounds = inbs
	return out
}

func stripInboundPort(tunnels []Tunnel, port uint16) []Tunnel {
	if port == 0 {
		port = DefaultMixedInboundPort
	}
	out := make([]Tunnel, len(tunnels))
	copy(out, tunnels)
	for i := range out {
		if len(out[i].Inbounds) == 0 {
			continue
		}
		filtered := make([]TunnelInbound, 0, len(out[i].Inbounds))
		for _, inb := range out[i].Inbounds {
			if inb.Port != port {
				filtered = append(filtered, inb)
			}
		}
		out[i].Inbounds = filtered
	}
	return out
}

// SyncTunnels applies the desired tunnel set to fircd, automatically preserving
// the local port 1099 inbound when ensureInbound1099 is true.
func (o *Operator) SyncTunnels(ctx context.Context, tunnels []Tunnel, ensureInbound1099 bool) (*PutTunnelsResponse, error) {
	InvalidateOutputRoutingCache()
	synced := make([]Tunnel, len(tunnels))
	copy(synced, tunnels)
	for i := range synced {
		NormalizeTunnelNodeRefs(&synced[i])
	}
	if ensureInbound1099 {
		synced = EnsureInboundPort1099(synced, o.inboundPort)
	}
	prepared, err := o.materializeManagedUplinks(ctx, synced)
	if err != nil {
		return nil, err
	}
	return o.syncTunnelsPrepared(ctx, prepared, ensureInbound1099)
}

func (o *Operator) materializeManagedUplinks(ctx context.Context, tunnels []Tunnel) ([]Tunnel, error) {
	o.mu.Lock()
	materialize := o.managedUplinkConfig
	o.mu.Unlock()
	if materialize != nil {
		prepared, err := materialize(ctx, tunnels)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return prepared, nil
	}
	return tunnels, nil
}

func (o *Operator) syncTunnelsPrepared(ctx context.Context, synced []Tunnel, ensureInbound1099 bool) (*PutTunnelsResponse, error) {
	resp, err := o.Client().PutTunnels(ctx, synced)
	if err != nil && ensureInbound1099 && strings.Contains(err.Error(), "is busy") {
		stripped := stripInboundPort(synced, o.inboundPort)
		_ = o.SaveTunnelsFile(stripped)
		return o.Client().PutTunnels(ctx, stripped)
	}
	if err != nil {
		return resp, err
	}
	if vErr := o.VerifyLiveSyncedTunnels(ctx, synced); vErr != nil {
		return nil, vErr
	}
	return resp, nil
}

// SyncGroups applies the desired routing groups to fircd and persists them to groups.yaml.
func (o *Operator) SyncGroups(ctx context.Context, groups []Group) ([]Group, error) {
	InvalidateOutputRoutingCache()
	return o.Client().PutGroups(ctx, groups, true)
}

// SyncGroupsRuntimeOnly applies the desired routing groups to fircd in-memory runtime
// without overwriting groups.yaml on disk (save=false).
func (o *Operator) SyncGroupsRuntimeOnly(ctx context.Context, groups []Group) ([]Group, error) {
	InvalidateOutputRoutingCache()
	return o.Client().PutGroups(ctx, groups, false)
}

// TunnelValidationError indicates invalid tunnel source/config input during staging or apply.
type TunnelValidationError struct {
	Err error
}

func (e *TunnelValidationError) Error() string {
	if e == nil || e.Err == nil {
		return "invalid tunnel configuration"
	}
	return e.Err.Error()
}

func (e *TunnelValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (o *Operator) normalizeTunnelsForSave(tunnels []Tunnel, ensureInbound1099 bool) ([]Tunnel, error) {
	out := make([]Tunnel, len(tunnels))
	copy(out, tunnels)
	happKeysPath := ResolveHappKeysPath("", o.configDir)
	for i := range out {
		if err := NormalizeTunnelSources(&out[i], happKeysPath); err != nil {
			return nil, &TunnelValidationError{Err: err}
		}
		if err := ValidateTunnelFilters(&out[i]); err != nil {
			return nil, &TunnelValidationError{Err: err}
		}
		if !out[i].Advanced.KernelTCP && !out[i].Advanced.KernelTCPSet {
			out[i].Advanced.KernelTCP = true
			out[i].Advanced.KernelTCPSet = true
		}
	}
	if ensureInbound1099 {
		out = EnsureInboundPort1099(out, o.inboundPort)
	}
	return out, nil
}

// ApplyTunnels transactionally persists tunnels to tunnels.yaml and synchronizes them
// with fircd when running. If runtime synchronization fails, both tunnels.yaml and the
// live fircd runtime are rolled back to the previous tunnel set; if rollback fails,
// recovery_required is set.
func (o *Operator) ApplyTunnels(ctx context.Context, tunnels []Tunnel, ensureInbound1099 bool) (*PutTunnelsResponse, error) {
	o.mutationMu.Lock()
	defer o.mutationMu.Unlock()
	return o.applyTunnelsLocked(ctx, tunnels, ensureInbound1099)
}

// applyTunnelsLocked allows an atomic read-modify-apply of a single uplink.
// Caller holds mutationMu, including while capturing the fresh live snapshot.
func (o *Operator) applyTunnelsLocked(ctx context.Context, tunnels []Tunnel, ensureInbound1099 bool) (*PutTunnelsResponse, error) {
	if err := o.checkMutationAllowed(); err != nil {
		return nil, err
	}
	normalized, err := o.normalizeTunnelsForSave(tunnels, ensureInbound1099)
	if err != nil {
		return nil, err
	}
	tunnels = normalized

	running := o.IsRunning(ctx)
	var prevTunnels []Tunnel
	if running {
		live, err := o.Client().GetTunnels(ctx)
		if err != nil {
			return nil, fmt.Errorf("capture live fircd tunnels snapshot before mutation: %w", err)
		}
		prevTunnels = live
	} else {
		prevTunnels, _ = o.LoadTunnelsFile()
	}
	// Older editors do not round-trip newly introduced DNS metadata. Omission
	// preserves an existing explicit binding; [] explicitly clears it. Auto is
	// an intentional exit from strict uplink ownership.
	for i := range tunnels {
		if tunnels[i].EndpointDNS != nil {
			continue
		}
		for _, old := range prevTunnels {
			if old.ID != tunnels[i].ID || len(old.EndpointDNS) == 0 {
				continue
			}
			if tunnels[i].Uplink.Kind == "iface" || tunnels[i].Uplink.Kind == "tunnel" {
				tunnels[i].EndpointDNS = append([]string(nil), old.EndpointDNS...)
			} else {
				tunnels[i].EndpointDNS = []string{}
			}
			break
		}
	}

	prepared, err := o.materializeManagedUplinks(ctx, tunnels)
	if err != nil {
		return nil, err
	}
	tunnels = prepared

	snap, err := captureFileSnapshot(o.TunnelsPath(), 0600)
	if err != nil {
		return nil, fmt.Errorf("snapshot tunnels.yaml: %w", err)
	}
	if err := o.SaveTunnelsFile(tunnels); err != nil {
		return nil, fmt.Errorf("save tunnels.yaml: %w", err)
	}
	if running {
		putResp, syncErr := o.syncTunnelsPrepared(ctx, tunnels, ensureInbound1099)
		if syncErr != nil {
			_, rtErr := o.syncTunnelsPrepared(ctx, prevTunnels, false)
			fileErr := restoreFileSnapshot(snap)
			if fileErr != nil || rtErr != nil {
				reason := fmt.Sprintf("ApplyTunnels rollback failed (file=%v, runtime=%v)", fileErr, rtErr)
				persistErr := o.SetRecoveryRequiredWithMeta(RecoveryMarker{
					Reason:         reason,
					TransitionID:   fmt.Sprintf("apply-tunnels-%d", time.Now().UnixNano()),
					TargetEngine:   "firc",
					PreviousEngine: "firc",
					FailedStep:     "sync_tunnels_rollback",
					LastCompleted:  "save_tunnels_yaml",
				})
				return nil, errors.Join(
					fmt.Errorf("sync tunnels with fircd: %w", syncErr),
					fmt.Errorf("%w: %s", ErrRecoveryRequired, reason),
					fileErr,
					rtErr,
					persistErr,
				)
			}
			return nil, wrapSyncTunnelError(syncErr)
		}
		if putResp != nil && len(putResp.Tunnels) > 0 {
			_ = o.SaveTunnelsFile(putResp.Tunnels)
		}
		return putResp, nil
	}
	return &PutTunnelsResponse{Tunnels: tunnels}, nil
}

// StageTunnelsOffline normalizes and saves tunnels to tunnels.yaml under mutationMu
// without probing fircd IPC, invoking iptables, or starting any runtime process.
func (o *Operator) StageTunnelsOffline(tunnels []Tunnel, ensureInbound1099 bool) (*PutTunnelsResponse, error) {
	o.mutationMu.Lock()
	defer o.mutationMu.Unlock()

	if err := o.checkMutationAllowed(); err != nil {
		return nil, err
	}
	normalized, err := o.normalizeTunnelsForSave(tunnels, ensureInbound1099)
	if err != nil {
		return nil, err
	}
	if err := o.SaveTunnelsFile(normalized); err != nil {
		return nil, fmt.Errorf("save tunnels.yaml: %w", err)
	}
	if normalized == nil {
		normalized = []Tunnel{}
	}
	return &PutTunnelsResponse{Tunnels: normalized}, nil
}

func wrapSyncTunnelError(syncErr error) error {
	if syncErr == nil {
		return nil
	}
	msg := strings.ToLower(syncErr.Error())
	if strings.Contains(msg, "filterexclude") && strings.Contains(msg, "unknown key") {
		return &TunnelValidationError{
			Err: fmt.Errorf("установленная версия fircd не поддерживает filterExclude (обновите awgm-route или очистите поле «Исключать»): %w", syncErr),
		}
	}
	return fmt.Errorf("sync tunnels with fircd: %w", syncErr)
}

func cloneGroups(groups []Group) []Group {
	if groups == nil {
		return nil
	}
	out := make([]Group, len(groups))
	for i, g := range groups {
		cp := g
		if g.Interface != nil {
			cp.Interface = make(GroupInterface, len(g.Interface))
			copy(cp.Interface, g.Interface)
		}
		if g.Interfaces != nil {
			cp.Interfaces = make([]GroupIfaceState, len(g.Interfaces))
			copy(cp.Interfaces, g.Interfaces)
		}
		if g.Devices.Allow != nil {
			cp.Devices.Allow = make([]string, len(g.Devices.Allow))
			copy(cp.Devices.Allow, g.Devices.Allow)
		}
		if g.Devices.Deny != nil {
			cp.Devices.Deny = make([]string, len(g.Devices.Deny))
			copy(cp.Devices.Deny, g.Devices.Deny)
		}
		if g.List != nil {
			listCp := *g.List
			cp.List = &listCp
		}
		if g.Rules != nil {
			cp.Rules = make([]Rule, len(g.Rules))
			copy(cp.Rules, g.Rules)
		}
		out[i] = cp
	}
	return out
}

// ApplyGroups transactionally persists routing groups to groups.yaml and synchronizes them
// with fircd and netfilter when running. If runtime synchronization or strict netfilter
// reconciliation fails, both groups.yaml and the live fircd/netfilter runtime are rolled back
// to the previous group set; if rollback fails, recovery_required is set.
func (o *Operator) ApplyGroups(ctx context.Context, groups []Group) ([]Group, error) {
	return o.ApplyGroupsWithLinks(ctx, groups, nil)
}

// ApplyGroupsWithLinks — ApplyGroups вместе со связями групп (см. nextGroupLinks).
func (o *Operator) ApplyGroupsWithLinks(ctx context.Context, groups []Group, links map[string]string) ([]Group, error) {
	o.mutationMu.Lock()
	defer o.mutationMu.Unlock()
	return o.applyGroupsLocked(ctx, groups, links)
}

func (o *Operator) applyGroupsLocked(ctx context.Context, groups []Group, links map[string]string) ([]Group, error) {
	if err := o.checkMutationAllowed(); err != nil {
		return nil, err
	}

	running := o.IsRunning(ctx)
	var prevGroups []Group
	if running {
		live, err := o.Client().GetGroups(ctx, true)
		if err != nil {
			return nil, fmt.Errorf("capture live fircd groups snapshot before mutation: %w", err)
		}
		prevGroups = live
	} else {
		diskGroups, err := o.LoadGroupsFile()
		if err != nil {
			return nil, fmt.Errorf("load groups.yaml before mutation: %w", err)
		}
		prevGroups = diskGroups
	}

	return o.applyGroupsWithPrevLocked(ctx, groups, prevGroups, running, links)
}

func (o *Operator) applyGroupsWithPrevLocked(ctx context.Context, groups []Group, prevGroups []Group, running bool, links map[string]string) ([]Group, error) {
	o.migrateGroupLinks()
	ext := o.LoadExtConfig()
	ext = withNextGroupLinks(ext, groups, prevGroups, links)
	groups = keepOmittedFallback(groups, prevGroups)
	groups, _ = o.syncGroupsWithExt(ctx, groups, ext)

	snap, err := captureFileSnapshot(o.GroupsPath(), 0644)
	if err != nil {
		return nil, fmt.Errorf("snapshot groups.yaml: %w", err)
	}
	if err := o.SaveGroupsFile(groups); err != nil {
		return nil, fmt.Errorf("save groups.yaml: %w", err)
	}
	if running {
		updated, syncErr := o.SyncGroups(ctx, groups)
		var nfErr error
		if syncErr == nil {
			nfErr = o.ReconcileExtNetfilterStrict(ctx, ext, updated)
		}
		if syncErr != nil || nfErr != nil {
			causeErr := syncErr
			failedStep := "sync_groups_rollback"
			if causeErr == nil {
				causeErr = fmt.Errorf("reconcile netfilter for groups: %w", nfErr)
				failedStep = "netfilter_groups_rollback"
			} else {
				causeErr = fmt.Errorf("sync groups with fircd: %w", syncErr)
			}
			_, rtErr := o.SyncGroupsRuntimeOnly(ctx, prevGroups)
			nfRbErr := o.ReconcileExtNetfilterStrict(ctx, ext, prevGroups)
			fileErr := restoreFileSnapshot(snap)
			if fileErr != nil || rtErr != nil || nfRbErr != nil {
				reason := fmt.Sprintf("ApplyGroups rollback failed (file=%v, runtime=%v, netfilter=%v)", fileErr, rtErr, nfRbErr)
				persistErr := o.SetRecoveryRequiredWithMeta(RecoveryMarker{
					Reason:         reason,
					TransitionID:   fmt.Sprintf("apply-groups-%d", time.Now().UnixNano()),
					TargetEngine:   "firc",
					PreviousEngine: "firc",
					FailedStep:     failedStep,
					LastCompleted:  "save_groups_yaml",
				})
				return nil, errors.Join(
					causeErr,
					fmt.Errorf("%w: %s", ErrRecoveryRequired, reason),
					fileErr,
					rtErr,
					nfRbErr,
					persistErr,
				)
			}
			return nil, causeErr
		}
		if len(updated) > 0 {
			if saveErr := o.SaveGroupsFile(updated); saveErr != nil {
				_, rtErr := o.SyncGroupsRuntimeOnly(ctx, prevGroups)
				nfRbErr := o.ReconcileExtNetfilterStrict(ctx, ext, prevGroups)
				fileErr := restoreFileSnapshot(snap)
				if fileErr != nil || rtErr != nil || nfRbErr != nil {
					reason := fmt.Sprintf("ApplyGroups post-sync save rollback failed (file=%v, runtime=%v, netfilter=%v)", fileErr, rtErr, nfRbErr)
					persistErr := o.SetRecoveryRequiredWithMeta(RecoveryMarker{
						Reason:         reason,
						TransitionID:   fmt.Sprintf("apply-groups-%d", time.Now().UnixNano()),
						TargetEngine:   "firc",
						PreviousEngine: "firc",
						FailedStep:     "post_sync_save_groups_rollback",
						LastCompleted:  "sync_groups_runtime",
					})
					return nil, errors.Join(
						fmt.Errorf("save canonical groups.yaml after sync: %w", saveErr),
						fmt.Errorf("%w: %s", ErrRecoveryRequired, reason),
						fileErr,
						rtErr,
						nfRbErr,
						persistErr,
					)
				}
				return nil, fmt.Errorf("save canonical groups.yaml after sync: %w", saveErr)
			}
		}
		return updated, o.settleGroupLinks(ext)
	}
	return groups, o.settleGroupLinks(ext)
}

// StageGroupsOffline synchronizes ExtConfig derived groups and saves groups.yaml
// under mutationMu without probing fircd IPC, invoking iptables, or starting any runtime process.
func (o *Operator) StageGroupsOffline(groups []Group) ([]Group, error) {
	return o.StageGroupsOfflineWithLinks(groups, nil)
}

// StageGroupsOfflineWithLinks — StageGroupsOffline вместе со связями групп (см. nextGroupLinks).
func (o *Operator) StageGroupsOfflineWithLinks(groups []Group, links map[string]string) ([]Group, error) {
	o.mutationMu.Lock()
	defer o.mutationMu.Unlock()

	if err := o.checkMutationAllowed(); err != nil {
		return nil, err
	}
	prev, _ := o.LoadGroupsFile() // нечитаемый файл сохранению не помеха: без прежних групп связи снимутся
	o.migrateGroupLinks()
	ext := withNextGroupLinks(o.LoadExtConfig(), groups, prev, links)
	groups = keepOmittedFallback(groups, prev)
	groups, _ = o.syncGroupsWithExt(context.Background(), groups, ext)
	if groups == nil {
		groups = []Group{}
	}
	if err := o.SaveGroupsFile(groups); err != nil {
		return nil, fmt.Errorf("save groups.yaml: %w", err)
	}
	return groups, o.settleGroupLinks(ext)
}

// AddConfirmedCandidate transactionally adds a manually confirmed autolist candidate domain
// to the specified routing group under Operator.mutationMu:
//  1. Validates non-empty domain and explicit groupID.
//  2. Acquires mutationMu and verifies recovery_required / migration journal guards.
//  3. Reads authoritative groups under mutationMu (from live fircd when running without silent
//     fallback to stale disk, or from groups.yaml when fircd is stopped).
//  4. Locates the target group by ID or case-insensitive Name, adds or enables the domain rule,
//     and executes transactional applyGroupsWithPrevLocked (disk + fircd runtime + strict netfilter
//     with full rollback on any error).
//  5. Dismisses the candidate in fircd runtime on success.
func (o *Operator) AddConfirmedCandidate(ctx context.Context, domain, groupID string) ([]Group, string, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return nil, "", ErrCandidateDomainRequired
	}
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return nil, "", ErrCandidateGroupRequired
	}

	o.mutationMu.Lock()
	defer o.mutationMu.Unlock()

	if err := o.checkMutationAllowed(); err != nil {
		return nil, "", err
	}

	running := o.IsRunning(ctx)
	var prevGroups []Group
	if running {
		live, err := o.Client().GetGroups(ctx, true)
		if err != nil {
			return nil, "", fmt.Errorf("read live fircd groups before candidate add: %w", err)
		}
		prevGroups = live
	} else {
		diskGroups, err := o.LoadGroupsFile()
		if err != nil {
			return nil, "", fmt.Errorf("load groups.yaml before candidate add: %w", err)
		}
		prevGroups = diskGroups
	}

	groups := cloneGroups(prevGroups)
	targetIdx := -1
	for i := range groups {
		if groups[i].ID == groupID || strings.EqualFold(groups[i].Name, groupID) {
			targetIdx = i
			break
		}
	}
	if targetIdx < 0 {
		return nil, "", fmt.Errorf("%w: %s", ErrCandidateGroupNotFound, groupID)
	}

	if groups[targetIdx].IsDNSOnly() {
		return nil, "", fmt.Errorf("%w: %s", ErrCandidateGroupDNSOnly, groupID)
	}

	resolvedGroupID := groups[targetIdx].ID
	if strings.TrimSpace(resolvedGroupID) == "" {
		resolvedGroupID = groups[targetIdx].Name
	}

	ruleType := "namespace"
	ruleVal := domain
	autolistHost := domain
	autolistByDomain := true
	rawIP := domain
	if strings.Contains(domain, "/") {
		if ip, ipNet, cidrErr := net.ParseCIDR(domain); cidrErr == nil && ip != nil && ipNet != nil {
			rawIP = ip.String()
			if ip.To4() != nil {
				ruleType = "subnet"
			} else {
				ruleType = "subnet6"
			}
			autolistHost = ""
			autolistByDomain = false
		}
	} else if ip := net.ParseIP(domain); ip != nil {
		rawIP = ip.String()
		if ip4 := ip.To4(); ip4 != nil {
			ruleType = "subnet"
			ruleVal = ip4.String() + "/32"
		} else {
			ruleType = "subnet6"
			ruleVal = ip.String() + "/128"
		}
		autolistHost = ""
		autolistByDomain = false
	}

	exists := false
	for j := range groups[targetIdx].Rules {
		rl := &groups[targetIdx].Rules[j]
		existingRule := strings.TrimSpace(rl.Rule)
		if ruleType == "subnet" || ruleType == "subnet6" {
			if (strings.EqualFold(existingRule, ruleVal) || strings.EqualFold(existingRule, rawIP)) &&
				(rl.Type == "subnet" || rl.Type == "subnet6") {
				exists = true
				rl.Type = ruleType
				rl.Rule = ruleVal
				rl.Enable = true
				break
			}
			continue
		}
		if strings.EqualFold(existingRule, ruleVal) &&
			(rl.Type == "" || rl.Type == "domain" || rl.Type == "namespace" || rl.Type == "wild" || rl.Type == "DotWild" || rl.Type == "wildcard") {
			exists = true
			if rl.Type == "" || rl.Type == "domain" {
				rl.Type = "namespace"
			}
			rl.Enable = true
			if rl.Autolist == "" {
				rl.Autolist = autolistHost
			}
			rl.AutolistByDomain = true
			break
		}
	}
	if !exists {
		groups[targetIdx].Rules = append(groups[targetIdx].Rules, Rule{
			Type:             ruleType,
			Rule:             ruleVal,
			Enable:           true,
			Comment:          "autolist confirmed",
			Autolist:         autolistHost,
			AutolistByDomain: autolistByDomain,
		})
	}

	updated, err := o.applyGroupsWithPrevLocked(ctx, groups, prevGroups, running, nil)
	if err != nil {
		return nil, "", err
	}
	if running {
		if ruleType == "subnet" || ruleType == "subnet6" {
			dismissed := false
			if st, aErr := o.Client().GetAutolist(ctx); aErr == nil && st != nil {
				for _, addr := range st.Addresses {
					if strings.EqualFold(strings.TrimSpace(addr.Address), rawIP) {
						_ = o.Client().DismissAutolistAddress(ctx, addr.Address, addr.Proto, addr.Port)
						dismissed = true
					}
				}
			}
			if !dismissed {
				_ = o.Client().DismissAutolistAddress(ctx, rawIP, "tcp", 443)
			}
		} else {
			_ = o.Client().DismissAutolistCandidate(ctx, domain)
			if st, aErr := o.Client().GetAutolist(ctx); aErr == nil && st != nil {
				for _, addr := range st.Addresses {
					addrName := strings.ToLower(strings.TrimSpace(addr.Name))
					if addrName != "" && (addrName == domain || strings.HasSuffix(addrName, "."+domain)) {
						_ = o.Client().DismissAutolistAddress(ctx, addr.Address, addr.Proto, addr.Port)
					}
				}
			}
		}
	}
	return updated, resolvedGroupID, nil
}

// normalizeTargetEngineName validates active target routing engines ("firc" or "sing-box").
// Legacy "mihomo" is rejected as a target engine because Mihomo runtime has been removed.
func normalizeTargetEngineName(engine string) (string, error) {
	norm := strings.ToLower(strings.TrimSpace(engine))
	if norm == "singbox" {
		norm = "sing-box"
	}
	if norm != "firc" && norm != "sing-box" {
		return "", fmt.Errorf("routing engine %q is unsupported (must be sing-box or firc)", engine)
	}
	return norm, nil
}

// normalizeCurrentEngineName normalizes a persisted settings.json engine name so installations
// upgrading from legacy "mihomo" settings can still switch away to "firc" or "sing-box".
func normalizeCurrentEngineName(engine string) (string, error) {
	norm := strings.ToLower(strings.TrimSpace(engine))
	if norm == "singbox" {
		norm = "sing-box"
	}
	if norm != "firc" && norm != "sing-box" && norm != "mihomo" {
		return "", fmt.Errorf("routing engine %q is unsupported (must be sing-box or firc)", engine)
	}
	return norm, nil
}

// SwitchEngine executes a serialized, saga-compensated routing engine transition:
//  1. Acquires the unified mutation lock and verifies recovery_required is clear.
//  2. Fail-closed resolves the current engine and validates target engine + required lifecycle hooks.
//  3. Starts the candidate engine and verifies its readiness probe (and syncs tunnels/groups for firc).
//  4. Atomically commits the new engine selection to persistent settings.
//  5. Stops the previous engine and toggles the built-in VLESS XHTTP server.
//  6. On any failure, executes a deterministic safe rollback sequence:
//     Phase R1 (stop_candidate) -> Phase R2 (restore_settings) -> Phase R3 (restart_previous).
//     If any rollback step fails, persists recovery-state.json and sets recovery_required.
func (o *Operator) SwitchEngine(ctx context.Context, targetEngine string, overrideHooks ...EngineSwitchHooks) error {
	return o.SwitchEngineWithOptions(ctx, targetEngine, EngineSwitchOptions{}, overrideHooks...)
}

// SwitchEngineWithOptions executes a transactional routing engine switch with explicit
// stop-vs-standby options and full state capture/rollback support.
func (o *Operator) SwitchEngineWithOptions(ctx context.Context, targetEngine string, opts EngineSwitchOptions, overrideHooks ...EngineSwitchHooks) error {
	hooks := o.DefaultEngineHooks()
	for _, ov := range overrideHooks {
		hooks = mergeEngineSwitchHooks(hooks, ov)
	}
	var guard EngineTransitionGuard
	if hooks.AcquireTransition != nil {
		var err error
		guard, err = hooks.AcquireTransition(ctx)
		if err != nil {
			return err
		}
		if guard == nil {
			return errors.New("engine transition guard unavailable")
		}
		defer guard.Release()
	}

	o.mutationMu.Lock()
	defer o.mutationMu.Unlock()

	normTarget, err := normalizeTargetEngineName(targetEngine)
	if err != nil {
		return err
	}
	// Уйти с FIRC при стороннем firc можно всегда: метка восстановления и
	// журнал относились к нашему рантайму, а файлов firc уход не пишет.
	if normTarget == "firc" {
		if err := o.checkMutationAllowed(); err != nil {
			return err
		}
	} else if !o.foreignPresent() {
		if err := o.checkRecoveryClear(); err != nil {
			return err
		}
	}
	if guard != nil {
		if err := guard.CheckTarget(normTarget); err != nil {
			return err
		}
	}

	if hooks.GetCurrentEngine == nil {
		return errors.New("engine switch failed: GetCurrentEngine hook is not configured")
	}
	rawCur, err := hooks.GetCurrentEngine(ctx)
	if err != nil {
		return fmt.Errorf("engine switch failed: determine current routing engine: %w", err)
	}
	prevEngine, err := normalizeCurrentEngineName(rawCur)
	if err != nil {
		return fmt.Errorf("engine switch failed: current routing engine invalid: %w", err)
	}

	if hooks.CommitSettings == nil {
		return errors.New("engine switch failed: CommitSettings hook is not configured")
	}
	if normTarget != "firc" && hooks.StartTarget == nil {
		return fmt.Errorf("engine switch failed: StartTarget hook is not configured for %s", normTarget)
	}
	if prevEngine != normTarget {
		if prevEngine != "firc" && prevEngine != "mihomo" && (hooks.StopEngine == nil || hooks.StartTarget == nil) {
			return fmt.Errorf("engine switch failed: StopEngine/StartTarget hooks are not configured for previous engine %s", prevEngine)
		}
		if normTarget != "firc" && hooks.StopEngine == nil {
			return fmt.Errorf("engine switch failed: StopEngine hook is not configured for candidate engine %s", normTarget)
		}
	}

	if hooks.ValidateMutation != nil {
		if err := hooks.ValidateMutation(ctx, prevEngine, normTarget); err != nil {
			return err
		}
	}

	var restoreCapturedState func(ctx context.Context) error
	if hooks.CaptureState != nil {
		fn, cerr := hooks.CaptureState(WithSwitchPreviousEngine(ctx, prevEngine))
		if cerr != nil {
			return fmt.Errorf("engine switch failed: capture state: %w", cerr)
		}
		restoreCapturedState = fn
	}

	transitionID := fmt.Sprintf("tx-%d", time.Now().UnixNano())
	lastCompleted := "precheck"
	candidateStartAttempted := false
	disabledClearedBySwitch := false
	settingsCommitAttempted := false
	settingsCommitted := false
	previousStopAttempted := false
	onSwitchedCalled := false

	cutoverBarrierEngaged := false
	if prevEngine != normTarget {
		if err := o.EngageCutoverBarrier(ctx); err != nil {
			return fmt.Errorf("engine switch failed: engage cutover barrier: %w", err)
		}
		cutoverBarrierEngaged = true
	}
	releaseCutoverBarrierBestEffort := func() {
		if !cutoverBarrierEngaged {
			return
		}
		relCtx, relCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer relCancel()
		_ = o.ReleaseCutoverBarrier(relCtx)
	}

	startAndSyncFirc := func(allowClearDisabled bool) error {
		cleared, err := o.startRuntimeAuthorized(ctx, allowClearDisabled)
		if cleared {
			disabledClearedBySwitch = true
		}
		if err != nil {
			return err
		}
		tunnels, err := o.LoadTunnelsFile()
		if err != nil {
			return fmt.Errorf("load firc tunnels.yaml: %w", err)
		}
		if len(tunnels) > 0 {
			if _, err := o.SyncTunnels(ctx, tunnels, true); err != nil {
				return fmt.Errorf("firc tunnel sync failed: %w", err)
			}
		}
		groups, err := o.LoadGroupsFile()
		if err != nil {
			return fmt.Errorf("load firc groups.yaml: %w", err)
		}
		if len(groups) > 0 {
			if _, err := o.SyncGroups(ctx, groups); err != nil {
				return fmt.Errorf("firc group sync failed: %w", err)
			}
		}
		if err := o.ReconcileExtNetfilterStrict(ctx, o.LoadExtConfig(), groups); err != nil {
			return fmt.Errorf("firc ext netfilter reconciliation failed: %w", err)
		}
		return nil
	}

	// Deterministic safe rollback sequence (P0.2, P0.3, P1.3):
	// Phase R1: Always isolate/stop the candidate engine first if its start was attempted.
	// Phase R2: Restore persistent settings (and OnSwitched state) to prevEngine if committed or partially mutated.
	// Phase R3: Restart and verify prevEngine only after candidate is stopped and settings are restored.
	runSafeRollback := func(failedStep string, primaryErr error) error {
		var rbErrs []error

		// Phase R1: stop_candidate
		if candidateStartAttempted && (prevEngine != normTarget || disabledClearedBySwitch) {
			if normTarget == "firc" {
				if cerr := o.Stop(ctx); cerr != nil {
					rbErrs = append(rbErrs, fmt.Errorf("rollback step \"stop_candidate_firc\": %w", cerr))
				}
				o.CleanupPoolRejectBarrier(ctx)
				if disabledClearedBySwitch {
					if rerr := o.restoreDisabledMarker(); rerr != nil {
						rbErrs = append(rbErrs, fmt.Errorf("rollback step \"restore_disabled_firc\": %w", rerr))
					}
				}
			} else if restoreCapturedState == nil {
				if cerr := hooks.StopEngine(ctx, normTarget); cerr != nil {
					rbErrs = append(rbErrs, fmt.Errorf("rollback step \"stop_candidate_%s\": %w", normTarget, cerr))
				}
			}
		}

		// Phase R2: restore_settings (P0.3: also compensates partially successful CommitSettings)
		if (settingsCommitted || settingsCommitAttempted) && prevEngine != normTarget {
			needRestore := settingsCommitted
			if !needRestore {
				curRaw, gerr := hooks.GetCurrentEngine(ctx)
				if gerr != nil {
					needRestore = true
				} else if normCur, nerr := normalizeCurrentEngineName(curRaw); nerr != nil || normCur != prevEngine {
					needRestore = true
				}
			}
			if needRestore {
				if cerr := hooks.CommitSettings(ctx, prevEngine); cerr != nil {
					rbErrs = append(rbErrs, fmt.Errorf("rollback step \"restore_settings_%s\": %w", prevEngine, cerr))
				}
			}
			if (onSwitchedCalled || needRestore) && hooks.OnSwitched != nil {
				hooks.OnSwitched(prevEngine == "firc")
			}
		}

		// When rolling back to firc, restore captured sing-box/router parked state BEFORE restarting firc.
		if restoreCapturedState != nil && prevEngine == "firc" {
			if rerr := restoreCapturedState(ctx); rerr != nil {
				rbErrs = append(rbErrs, fmt.Errorf("rollback step \"restore_state_%s\": %w", prevEngine, rerr))
			}
		}

		// Phase R3: restart_previous
		if previousStopAttempted && prevEngine != normTarget && prevEngine != "mihomo" &&
			(prevEngine != "firc" || !o.foreignPresent()) {
			var cerr error
			if prevEngine == "firc" {
				cerr = startAndSyncFirc(false)
			} else {
				cerr = hooks.StartTarget(ctx, prevEngine)
			}
			if cerr != nil {
				rbErrs = append(rbErrs, fmt.Errorf("rollback step \"restart_previous_%s\": %w", prevEngine, cerr))
			}
		}

		// When rolling back to a non-firc engine, restore exact captured state (SingboxStoppedByEngine + router)
		// after settings and previous target start are restored.
		if restoreCapturedState != nil && prevEngine != "firc" {
			if rerr := restoreCapturedState(ctx); rerr != nil {
				rbErrs = append(rbErrs, fmt.Errorf("rollback step \"restore_state_%s\": %w", prevEngine, rerr))
			}
		} else if restoreCapturedState == nil && previousStopAttempted && prevEngine != normTarget && prevEngine != "firc" && prevEngine != "mihomo" && hooks.ActivateTarget != nil {
			if aerr := hooks.ActivateTarget(ctx, prevEngine); aerr != nil {
				rbErrs = append(rbErrs, fmt.Errorf("rollback step \"activate_previous_%s\": %w", prevEngine, aerr))
			}
		}

		if len(rbErrs) > 0 {
			reason := fmt.Sprintf("switch %s -> %s failed at %s and rollback failed: %v", prevEngine, normTarget, failedStep, errors.Join(rbErrs...))
			persistErr := o.SetRecoveryRequiredWithMeta(RecoveryMarker{
				Reason:         reason,
				TransitionID:   transitionID,
				TargetEngine:   normTarget,
				PreviousEngine: prevEngine,
				FailedStep:     failedStep,
				LastCompleted:  lastCompleted,
			})
			return errors.Join(
				primaryErr,
				fmt.Errorf("%w: %s", ErrRecoveryRequired, reason),
				errors.Join(rbErrs...),
				persistErr,
			)
		}
		releaseCutoverBarrierBestEffort()
		return primaryErr
	}

	// Step 1: Start candidate engine and verify readiness BEFORE committing settings.
	// Even if StartTarget fails partway through, candidateStartAttempted is true so
	// Phase R1 (stop_candidate) always runs to clean up partial candidate state (P0.2).
	candidateStartAttempted = true
	if normTarget == "firc" {
		if err := startAndSyncFirc(opts.AuthorizeDisabledStart); err != nil {
			return runSafeRollback("start_candidate_firc", fmt.Errorf("firc candidate start failed: %w", err))
		}
	} else {
		if err := hooks.StartTarget(ctx, normTarget); err != nil {
			return runSafeRollback("start_candidate_"+normTarget, fmt.Errorf("candidate engine %s start failed: %w", normTarget, err))
		}
	}
	lastCompleted = "start_candidate_" + normTarget
	if cutoverBarrierEngaged {
		if err := o.EngageCutoverBarrier(ctx); err != nil {
			return runSafeRollback("reanchor_cutover_guard_"+normTarget, fmt.Errorf("re-anchor cutover barrier failed: %w", err))
		}
	}

	// Step 2: Commit RoutingEngine setting atomically
	settingsCommitAttempted = true
	if err := hooks.CommitSettings(ctx, normTarget); err != nil {
		return runSafeRollback("commit_settings_"+normTarget, fmt.Errorf("commit routing engine %s failed: %w", normTarget, err))
	}
	settingsCommitted = true
	lastCompleted = "commit_settings_" + normTarget

	// Step 3: Stop or standby-park previous engine if different (legacy "mihomo" settings value has no managed runtime)
	if prevEngine != normTarget && prevEngine != "mihomo" {
		previousStopAttempted = true
		if prevEngine == "firc" {
			if err := o.Stop(ctx); err != nil {
				return runSafeRollback("stop_previous_firc", fmt.Errorf("stop previous firc engine failed: %w", err))
			}
			o.CleanupPoolRejectBarrier(ctx)
		} else if opts.SingboxStandby && hooks.ParkEngineForStandby != nil {
			if err := hooks.ParkEngineForStandby(ctx, prevEngine); err != nil {
				return runSafeRollback("park_standby_"+prevEngine, fmt.Errorf("park previous engine %s for standby failed: %w", prevEngine, err))
			}
		} else {
			if err := hooks.StopEngine(ctx, prevEngine); err != nil {
				return runSafeRollback("stop_previous_"+prevEngine, fmt.Errorf("stop previous engine %s failed: %w", prevEngine, err))
			}
		}
		lastCompleted = "stop_previous_" + prevEngine
	} else if normTarget == "firc" {
		// Same-engine convergence (already firc): apply requested stop vs standby policy consistently.
		if opts.SingboxStandby && hooks.ParkEngineForStandby != nil {
			if err := hooks.ParkEngineForStandby(ctx, "sing-box"); err != nil {
				return runSafeRollback("park_standby_sing-box", fmt.Errorf("park sing-box for standby failed: %w", err))
			}
		} else if !opts.SingboxStandby && hooks.StopEngine != nil {
			if err := hooks.StopEngine(ctx, "sing-box"); err != nil {
				return runSafeRollback("stop_sing-box", fmt.Errorf("stop sing-box failed: %w", err))
			}
		}
	}

	// Step 4: Activate target routing path after previous engine released resources
	if hooks.ActivateTarget != nil {
		if err := hooks.ActivateTarget(ctx, normTarget); err != nil {
			return runSafeRollback("activate_target_"+normTarget, fmt.Errorf("activate target engine %s failed: %w", normTarget, err))
		}
		lastCompleted = "activate_target_" + normTarget
	}
	if normTarget == "firc" {
		groups, gerr := o.LoadGroupsFile()
		if gerr != nil {
			return runSafeRollback("reconcile_netfilter_firc", fmt.Errorf("load firc groups.yaml failed: %w", gerr))
		}
		if err := o.ReconcileExtNetfilterStrict(ctx, o.LoadExtConfig(), groups); err != nil {
			return runSafeRollback("reconcile_netfilter_firc", fmt.Errorf("reconcile firc ext netfilter failed: %w", err))
		}
		if cutoverBarrierEngaged {
			if err := o.EngageCutoverBarrier(ctx); err != nil {
				return runSafeRollback("reanchor_cutover_guard_post_"+normTarget, fmt.Errorf("re-anchor cutover barrier failed: %w", err))
			}
		}
	}

	// Step 5: Toggle built-in VLESS XHTTP server / post-switch notifications
	if hooks.OnSwitched != nil {
		onSwitchedCalled = true
		hooks.OnSwitched(normTarget == "firc")
	}
	releaseCutoverBarrierBestEffort()
	lastCompleted = "completed"
	return nil
}

// ReconcileRecovery performs a verified recovery reconciliation workflow (P0.1, P0.2, P1.1):
//  1. Acquires the unified mutation lock.
//  2. Determines the authoritative target engine with explicit priority (P1.1):
//     preferredEngine ("explicit_request") -> RecoveryMarker.PreviousEngine ("recovery_marker_previous") ->
//     GetCurrentEngine ("current_settings") -> RecoveryMarker.TargetEngine ("recovery_marker_target").
//  3. Executes non-destructive preflight BEFORE stopping any engine (P0.2):
//     validates hooks, captures before-image, parses/validates tunnels.yaml & groups.yaml, and verifies
//     fircd/tunvless binaries + ELF headers.
//  4. Executes the destructive phase with a compensation sequence (P0.2):
//     stops non-target engines, starts and verifies the target engine, and commits settings.json.
//     If any destructive step fails, stops the failed target, restores settings, and restarts the previously
//     working fallback engine before persisting an updated RecoveryMarker.
//  5. Clears recovery-state.json on disk first and clears in-memory state ONLY after disk removal succeeds (P0.1).
func (o *Operator) ReconcileRecovery(ctx context.Context, preferredEngine string, overrideHooks ...EngineSwitchHooks) (*ReconcileReport, error) {
	o.mutationMu.Lock()
	defer o.mutationMu.Unlock()

	if err := o.CheckNotForeign(); err != nil {
		return nil, err
	}
	if o.recoveryDeferred.Load() {
		return nil, ErrRestartRequired
	}
	hooks := o.DefaultEngineHooks()
	for _, ov := range overrideHooks {
		hooks = mergeEngineSwitchHooks(hooks, ov)
	}

	prevMarker := o.RecoveryMarkerInfo()

	// P1.1: Authoritative target resolution priority:
	// 1. Explicit administrator request (preferredEngine)
	// 2. Last known good engine from RecoveryMarker.PreviousEngine (protects against partially mutated settings)
	// 3. Current persisted setting from GetCurrentEngine
	// 4. RecoveryMarker.TargetEngine as last fallback
	targetRaw := strings.TrimSpace(preferredEngine)
	targetSource := ""
	if targetRaw != "" {
		targetSource = "explicit_request"
	} else if prevMarker != nil && strings.TrimSpace(prevMarker.PreviousEngine) != "" {
		targetRaw = strings.TrimSpace(prevMarker.PreviousEngine)
		targetSource = "recovery_marker_previous"
	} else if hooks.GetCurrentEngine != nil {
		if cur, err := hooks.GetCurrentEngine(ctx); err == nil && strings.TrimSpace(cur) != "" {
			targetRaw = strings.TrimSpace(cur)
			targetSource = "current_settings"
		}
	}
	if targetRaw == "" && prevMarker != nil && strings.TrimSpace(prevMarker.TargetEngine) != "" {
		targetRaw = strings.TrimSpace(prevMarker.TargetEngine)
		targetSource = "recovery_marker_target"
	}

	targetEngine, err := normalizeTargetEngineName(targetRaw)
	if err != nil {
		return nil, fmt.Errorf("reconcile recovery: invalid target engine %q: %w", targetRaw, err)
	}

	if hooks.CommitSettings == nil {
		return nil, errors.New("reconcile recovery: CommitSettings hook is not configured")
	}
	if hooks.StopEngine == nil {
		return nil, errors.New("reconcile recovery: StopEngine hook is not configured")
	}
	if targetEngine != "firc" && hooks.StartTarget == nil {
		return nil, fmt.Errorf("reconcile recovery: StartTarget hook is not configured for %s", targetEngine)
	}

	// Capture before-image prior to any destructive operation (P0.2)
	prevSettingsEngine := ""
	if hooks.GetCurrentEngine != nil {
		rawCur, gerr := hooks.GetCurrentEngine(ctx)
		if gerr != nil {
			return nil, fmt.Errorf("reconcile recovery preflight: determine current engine: %w", gerr)
		}
		if strings.TrimSpace(rawCur) != "" {
			normCur, nerr := normalizeCurrentEngineName(rawCur)
			if nerr != nil {
				return nil, fmt.Errorf("reconcile recovery preflight: current engine invalid: %w", nerr)
			}
			prevSettingsEngine = normCur
		}
	}

	fallbackEngine := ""
	if prevMarker != nil && strings.TrimSpace(prevMarker.PreviousEngine) != "" {
		if normPrev, nerr := normalizeTargetEngineName(prevMarker.PreviousEngine); nerr == nil {
			fallbackEngine = normPrev
		}
	}
	if fallbackEngine == "" {
		if normPrev, nerr := normalizeTargetEngineName(prevSettingsEngine); nerr == nil {
			fallbackEngine = normPrev
		}
	}
	wasFircRunning := o.IsRunning(ctx)

	recordPreflightFailure := func(step string, cause error) (*ReconcileReport, error) {
		reason := fmt.Sprintf("recovery reconciliation preflight to %s failed at %s (no engines stopped): %v", targetEngine, step, cause)
		persistErr := o.SetRecoveryRequiredWithMeta(RecoveryMarker{
			Reason:         reason,
			TransitionID:   fmt.Sprintf("reconcile-preflight-%d", time.Now().UnixNano()),
			TargetEngine:   targetEngine,
			PreviousEngine: fallbackEngine,
			FailedStep:     step,
			LastCompleted:  "preflight",
		})
		return nil, errors.Join(
			fmt.Errorf("reconcile recovery preflight failed at %s: %w", step, cause),
			persistErr,
		)
	}

	// Phase 1: Non-Destructive Preflight (P0.2)
	// Parse/validate YAML and verify binary/ELF readiness BEFORE stopping any running engine.
	var (
		preflightTunnels []Tunnel
		preflightGroups  []Group
	)
	if targetEngine == "firc" {
		tunnels, lerr := o.LoadTunnelsFile()
		if lerr != nil {
			return recordPreflightFailure("preflight_load_tunnels_yaml", lerr)
		}
		groups, gerr := o.LoadGroupsFile()
		if gerr != nil {
			return recordPreflightFailure("preflight_load_groups_yaml", gerr)
		}
		if cerr := o.EnsureConfigFiles(); cerr != nil {
			return recordPreflightFailure("preflight_ensure_config", cerr)
		}
		o.mu.Lock()
		hasStartHook := o.startHook != nil
		o.mu.Unlock()
		if !hasStartHook {
			if step, perr := o.validateProductionPreflight(); perr != nil {
				return recordPreflightFailure(step, perr)
			}
		}
		preflightTunnels = tunnels
		preflightGroups = groups
	}

	report := &ReconcileReport{
		TargetEngine:           targetEngine,
		TargetResolutionSource: targetSource,
	}

	var (
		stoppedNonTargets       []string
		targetStartAttempted    bool
		settingsCommitAttempted bool
		settingsCommitted       bool
		onSwitchedCalled        bool
	)

	startAndVerifyFirc := func() error {
		if err := o.Start(ctx); err != nil {
			return fmt.Errorf("start_firc: %w", err)
		}
		if len(preflightTunnels) > 0 {
			for i := range preflightTunnels {
				NormalizeTunnelNodeRefs(&preflightTunnels[i])
			}
			preflightTunnels = EnsureInboundPort1099(preflightTunnels, o.inboundPort)
			if _, err := o.SyncTunnels(ctx, preflightTunnels, true); err != nil {
				return fmt.Errorf("sync_tunnels: %w", err)
			}
		} else {
			if _, err := o.Client().GetTunnels(ctx); err != nil {
				return fmt.Errorf("verify_firc_tunnels_api: %w", err)
			}
		}
		if len(preflightGroups) > 0 {
			if _, err := o.SyncGroups(ctx, preflightGroups); err != nil {
				return fmt.Errorf("sync_groups: %w", err)
			}
		} else {
			if _, err := o.Client().GetGroups(ctx, true); err != nil {
				return fmt.Errorf("verify_firc_groups_api: %w", err)
			}
		}
		ext := o.LoadExtConfig()
		if err := o.ReconcileExtNetfilterStrict(ctx, ext, preflightGroups); err != nil {
			return fmt.Errorf("reconcile_netfilter: %w", err)
		}
		return nil
	}

	// Phase 2 Compensation Sequence (P0.2, P0.3):
	// If any destructive step fails, isolate/stop the failed target, restore settings to fallbackEngine,
	// and restart fallbackEngine if it was stopped during reconciliation.
	compensateAndFail := func(step string, cause error) (*ReconcileReport, error) {
		var compErrs []error
		restoredEngine := ""

		// 1. Stop failed target engine if it differs from fallbackEngine
		if targetStartAttempted && fallbackEngine != "" && targetEngine != fallbackEngine {
			if targetEngine == "firc" {
				if cerr := o.Stop(ctx); cerr != nil {
					compErrs = append(compErrs, fmt.Errorf("compensate stop_target_firc: %w", cerr))
				}
				o.CleanupPoolRejectBarrier(ctx)
			} else if hooks.StopEngine != nil {
				if cerr := hooks.StopEngine(ctx, targetEngine); cerr != nil {
					compErrs = append(compErrs, fmt.Errorf("compensate stop_target_%s: %w", targetEngine, cerr))
				}
			}
		}

		// 2. Restore settings to fallbackEngine if settings were committed, partially mutated during a failed CommitSettings attempt (P0.3), or out of sync
		if fallbackEngine != "" {
			needRestore := settingsCommitted || (prevSettingsEngine != "" && prevSettingsEngine != fallbackEngine)
			if !needRestore && settingsCommitAttempted {
				if hooks.GetCurrentEngine != nil {
					curRaw, gerr := hooks.GetCurrentEngine(ctx)
					if gerr != nil {
						needRestore = true
					} else if normCur, nerr := normalizeCurrentEngineName(curRaw); nerr != nil || normCur != fallbackEngine {
						needRestore = true
					}
				} else {
					needRestore = true
				}
			}
			if needRestore {
				if hooks.CommitSettings != nil {
					if cerr := hooks.CommitSettings(ctx, fallbackEngine); cerr != nil {
						compErrs = append(compErrs, fmt.Errorf("compensate restore_settings_%s: %w", fallbackEngine, cerr))
					}
				}
				if (onSwitchedCalled || settingsCommitted || settingsCommitAttempted) && hooks.OnSwitched != nil {
					hooks.OnSwitched(fallbackEngine == "firc")
				}
			}
		}

		// 3. Restart fallbackEngine if it was stopped during stop_non_target_engines
		if fallbackEngine != "" && fallbackEngine != targetEngine && slices.Contains(stoppedNonTargets, fallbackEngine) {
			if fallbackEngine == "firc" {
				if wasFircRunning || prevSettingsEngine == "firc" || (prevMarker != nil && prevMarker.PreviousEngine == "firc") {
					if cerr := o.Start(ctx); cerr != nil {
						compErrs = append(compErrs, fmt.Errorf("compensate restart_previous_firc: %w", cerr))
					} else {
						restoredEngine = "firc"
					}
				}
			} else if hooks.StartTarget != nil {
				if cerr := hooks.StartTarget(ctx, fallbackEngine); cerr != nil {
					compErrs = append(compErrs, fmt.Errorf("compensate restart_previous_%s: %w", fallbackEngine, cerr))
				} else {
					if hooks.ActivateTarget != nil {
						if aerr := hooks.ActivateTarget(ctx, fallbackEngine); aerr != nil {
							compErrs = append(compErrs, fmt.Errorf("compensate activate_previous_%s: %w", fallbackEngine, aerr))
						}
					}
					restoredEngine = fallbackEngine
				}
			}
		}

		reason := fmt.Sprintf("recovery reconciliation to %s failed at %s (restored=%s, compErrs=%v): %v",
			targetEngine, step, restoredEngine, errors.Join(compErrs...), cause)
		persistErr := o.SetRecoveryRequiredWithMeta(RecoveryMarker{
			Reason:         reason,
			TransitionID:   fmt.Sprintf("reconcile-%d", time.Now().UnixNano()),
			TargetEngine:   targetEngine,
			PreviousEngine: fallbackEngine,
			FailedStep:     step,
			LastCompleted:  "destructive_reconcile",
		})
		return nil, errors.Join(
			fmt.Errorf("reconcile recovery failed at %s: %w", step, cause),
			errors.Join(compErrs...),
			persistErr,
		)
	}

	if err := o.EngageCutoverBarrier(ctx); err != nil {
		return nil, fmt.Errorf("reconcile recovery failed at engage_cutover_barrier: %w", err)
	}

	// Step 1: Stop all non-target engines
	allEngines := []string{"firc", "sing-box"}
	for _, eng := range allEngines {
		if eng == targetEngine {
			continue
		}
		if eng == "firc" {
			if err := o.Stop(ctx); err != nil {
				return compensateAndFail("stop_non_target_firc", err)
			}
			o.CleanupPoolRejectBarrier(ctx)
			stoppedNonTargets = append(stoppedNonTargets, "firc")
			report.StoppedEngines = append(report.StoppedEngines, "firc")
		} else if hooks.StopEngine != nil {
			if err := hooks.StopEngine(ctx, eng); err != nil {
				return compensateAndFail("stop_non_target_"+eng, err)
			}
			stoppedNonTargets = append(stoppedNonTargets, eng)
			report.StoppedEngines = append(report.StoppedEngines, eng)
		}
	}

	// Step 2: Start and verify target engine + runtime/disk consistency
	targetStartAttempted = true
	if targetEngine == "firc" {
		if err := startAndVerifyFirc(); err != nil {
			return compensateAndFail("verify_target_firc", err)
		}
		report.TunnelsVerified = len(preflightTunnels)
		report.GroupsVerified = len(preflightGroups)
	} else {
		if err := hooks.StartTarget(ctx, targetEngine); err != nil {
			return compensateAndFail("start_target_"+targetEngine, err)
		}
	}

	// Step 3: Commit authoritative engine to settings.json
	settingsCommitAttempted = true
	if err := hooks.CommitSettings(ctx, targetEngine); err != nil {
		return compensateAndFail("commit_settings_"+targetEngine, err)
	}
	settingsCommitted = true
	if hooks.ActivateTarget != nil {
		if err := hooks.ActivateTarget(ctx, targetEngine); err != nil {
			return compensateAndFail("activate_target_"+targetEngine, err)
		}
	}
	if hooks.OnSwitched != nil {
		onSwitchedCalled = true
		hooks.OnSwitched(targetEngine == "firc")
	}

	// Step 4: Clear durable recovery marker on disk FIRST and in memory ONLY after disk removal succeeds (P0.1)
	if err := o.ClearRecovery(); err != nil {
		return nil, fmt.Errorf("reconcile recovery failed at clear_recovery: %w", err)
	}
	relCtx, relCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer relCancel()
	_ = o.ReleaseCutoverBarrier(relCtx)
	report.StepsCompleted = []string{"preflight", "stop_non_target_engines", "verify_target_engine", "commit_settings", "clear_recovery"}
	report.Verified = true
	report.ClearedRecovery = true
	report.RecoveryCleared = true
	report.TimestampUnixMs = time.Now().UnixMilli()
	return report, nil
}

type tunnelsYAMLFile struct {
	ConfigVersion string   `yaml:"configVersion,omitempty"`
	Tunnels       []Tunnel `yaml:"tunnels"`
}

type groupsYAMLFile struct {
	ConfigVersion string  `yaml:"configVersion,omitempty"`
	Groups        []Group `yaml:"groups"`
}

// SaveTunnelsFile writes tunnels atomically to tunnels.yaml in ConfigDir.
func (o *Operator) SaveTunnelsFile(tunnels []Tunnel) error {
	if err := o.CheckNotForeign(); err != nil {
		return err
	}
	if o.configDir == "" {
		return errors.New("firc config directory is not configured")
	}
	data, err := yaml.Marshal(tunnelsYAMLFile{ConfigVersion: "0.7.0", Tunnels: tunnels})
	if err != nil {
		return err
	}
	return atomicWriteYAMLFile(o.TunnelsPath(), data, 0600)
}

// LoadTunnelsFile reads tunnels from tunnels.yaml in ConfigDir.
func (o *Operator) LoadTunnelsFile() ([]Tunnel, error) {
	if o.CheckNotForeign() != nil {
		return nil, nil // туннели стороннего firc панель не показывает
	}
	data, err := os.ReadFile(o.TunnelsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	if len(root.Content) == 0 || root.Content[0].Kind == yaml.ScalarNode {
		return nil, nil
	}
	if root.Content[0].Kind == yaml.MappingNode {
		var doc tunnelsYAMLFile
		if err := root.Content[0].Decode(&doc); err != nil {
			return nil, err
		}
		return doc.Tunnels, nil
	}
	var list []Tunnel
	if err := root.Content[0].Decode(&list); err != nil {
		return nil, err
	}
	return list, nil
}

// SetGroupsSaveFaultHook installs a deterministic fault-injection callback for SaveGroupsFile (used in tests).
func (o *Operator) SetGroupsSaveFaultHook(fn func(groups []Group) error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.groupsSaveFaultHook = fn
}

// SetNetfilterStrictHook installs a deterministic fault/inspection callback for ReconcileExtNetfilterStrict (used in tests).
func (o *Operator) SetNetfilterStrictHook(fn func(ctx context.Context, ext ExtConfig, groups []Group) error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.netfilterStrictHook = fn
}

// SaveGroupsFile writes groups atomically to groups.yaml in ConfigDir.
func (o *Operator) SaveGroupsFile(groups []Group) error {
	if err := o.CheckNotForeign(); err != nil {
		return err
	}
	o.mu.Lock()
	hook := o.groupsSaveFaultHook
	o.mu.Unlock()
	if hook != nil {
		if err := hook(groups); err != nil {
			return err
		}
	}
	if o.configDir == "" {
		return errors.New("firc config directory is not configured")
	}
	if err := ValidateGroups(groups, o.addressPool()); err != nil {
		return err
	}
	data, err := yaml.Marshal(groupsYAMLFile{ConfigVersion: "0.7.0", Groups: normalizeGroupResolve(groups)})
	if err != nil {
		return err
	}
	return atomicWriteYAMLFile(o.GroupsPath(), data, 0644)
}

// LoadGroupsFile reads groups from groups.yaml in ConfigDir.
func (o *Operator) LoadGroupsFile() ([]Group, error) {
	if o.CheckNotForeign() != nil {
		return nil, nil // группы стороннего firc панель не показывает
	}
	data, err := os.ReadFile(o.GroupsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	if len(root.Content) == 0 || root.Content[0].Kind == yaml.ScalarNode {
		return nil, nil
	}
	if root.Content[0].Kind == yaml.MappingNode {
		var doc groupsYAMLFile
		if err := root.Content[0].Decode(&doc); err != nil {
			return nil, err
		}
		return normalizeGroupResolve(doc.Groups), nil
	}
	var list []Group
	if err := root.Content[0].Decode(&list); err != nil {
		return nil, err
	}
	return normalizeGroupResolve(list), nil
}

// VerifyLiveSyncedTunnels inspects GET /api/v1/tunnels/state after SyncTunnels to verify
// no enabled tunnel's inbound is stuck in `no-node` and explicit node selectors match resolved nodes.
func (o *Operator) VerifyLiveSyncedTunnels(ctx context.Context, synced []Tunnel) error {
	if len(synced) == 0 {
		return nil
	}
	states, err := o.Client().GetTunnelsState(ctx)
	if err != nil {
		// Fake/stopped or unmocked callers handle GET state unavailability elsewhere.
		return nil
	}
	stateByID := make(map[string]TunnelState, len(states))
	for _, st := range states {
		stateByID[st.ID] = st
	}
	for _, t := range synced {
		if !t.Enable || len(t.Inbounds) == 0 {
			continue
		}
		st, ok := stateByID[t.ID]
		if !ok {
			continue
		}
		for _, inbSt := range st.Inbounds {
			if strings.EqualFold(inbSt.State, "no-node") {
				return fmt.Errorf("tunnel %q inbound :%d failed to resolve node (state=no-node, why=%q)", t.ID, inbSt.Port, inbSt.Why)
			}
		}
		if len(st.Nodes) > 0 {
			nodeSet := make(map[string]bool, len(st.Nodes))
			for _, n := range st.Nodes {
				nodeSet[n.Key] = true
			}
			for _, inb := range t.Inbounds {
				if inb.Node == "" || inb.Node == TunnelNodePool {
					continue
				}
				if !nodeSet[inb.Node] {
					return fmt.Errorf("tunnel %q inbound :%d references unresolved node %q", t.ID, inb.Port, inb.Node)
				}
			}
		}
	}
	return nil
}
