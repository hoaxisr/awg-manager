package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hoaxisr/awg-manager/internal/mihomo"
	"github.com/hoaxisr/awg-manager/internal/mihomonative"
	"github.com/hoaxisr/awg-manager/internal/proxyengine"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// DynamicEngine wraps both Singbox and Mihomo engines and routes calls
// based on the current active RoutingEngine setting.
type DynamicEngine struct {
	singboxEngine proxyengine.Engine
	mihomoEngine  proxyengine.Engine
	settingsStore *storage.SettingsStore
	coordinator   *mihomo.ApplyCoordinator
	nativeStore   *mihomonative.Store
	compileFn     func(context.Context) (*mihomo.CompileResult, error)

	OnMihomoReload func() error
	// MihomoSidecarNeeded keeps native loopback exports alive while sing-box
	// owns transparent routing.
	MihomoSidecarNeeded func() bool
	OnMihomoPrepare     func() error
	// OnMihomoReady publishes native ProxyN exports only after the process and
	// its bridge listeners are ready. OnMihomoUnavailable withdraws them on
	// failed transitions and runtime shutdown.
	OnMihomoReady       func() error
	OnMihomoUnavailable func() error
	OnMihomoShutdown    func(context.Context) error
	OnMihomoPostApply   func()
	OnReady             func()

	onReadyOnce        sync.Once
	transitionMu       sync.Mutex
	currentMihomoMode  mihomoRuntimeMode
	shuttingDown       atomic.Bool
	suppressReloadHook atomic.Int32
	expectedReloadHook atomic.Int32
	reloadHookRequests atomic.Uint64
	reloadHookHandled  atomic.Uint64
}

var errDynamicEngineShuttingDown = errors.New("routing engine is shutting down")

type mihomoRuntimeMode uint8

const (
	mihomoRuntimeOff mihomoRuntimeMode = iota
	mihomoRuntimeExports
	mihomoRuntimePrimary
)

func (d *DynamicEngine) withTransition(fn func() error) error {
	if d.shuttingDown.Load() {
		return errDynamicEngineShuttingDown
	}
	d.transitionMu.Lock()
	defer d.transitionMu.Unlock()
	if d.shuttingDown.Load() {
		return errDynamicEngineShuttingDown
	}
	return fn()
}

// nativeMutationTransition is passed to MihomoHandler so store mutation,
// bridge preparation, config apply and rollback share the same lifecycle lock.
func (d *DynamicEngine) nativeMutationTransition(fn func() error) error {
	return d.withTransition(fn)
}

// reloadWithinTransition is valid only from a callback already wrapped by
// nativeMutationTransition.
func (d *DynamicEngine) reloadWithinTransition() error {
	if d.shuttingDown.Load() {
		return errDynamicEngineShuttingDown
	}
	return d.reloadLocked()
}

// syncMihomoRuntimeWithinTransition is valid only from a callback already wrapped
// by nativeMutationTransition.
func (d *DynamicEngine) syncMihomoRuntimeWithinTransition() error {
	if d.shuttingDown.Load() {
		return errDynamicEngineShuttingDown
	}
	return d.syncMihomoRuntimeLocked()
}

func NewDynamicEngine(sb, mh proxyengine.Engine, store *storage.SettingsStore) *DynamicEngine {
	return &DynamicEngine{
		singboxEngine: sb,
		mihomoEngine:  mh,
		settingsStore: store,
	}
}

type StartupStatus string

const (
	StatusReady    StartupStatus = "ready"
	StatusDegraded StartupStatus = "degraded"
)

type StartupResult struct {
	Status StartupStatus
	Error  error
}

func (d *DynamicEngine) SetCoordinator(c *mihomo.ApplyCoordinator) {
	d.coordinator = c
}

func (d *DynamicEngine) SetNativeStore(s *mihomonative.Store) {
	d.nativeStore = s
}

func (d *DynamicEngine) SetCompileFunc(fn func(context.Context) (*mihomo.CompileResult, error)) {
	d.compileFn = fn
}

func (d *DynamicEngine) Coordinator() *mihomo.ApplyCoordinator {
	return d.coordinator
}

func (d *DynamicEngine) triggerReady() {
	if d.OnReady != nil {
		d.onReadyOnce.Do(d.OnReady)
	}
}

func (d *DynamicEngine) Startup(ctx context.Context) StartupResult {
	if d.coordinator == nil {
		d.triggerReady()
		return StartupResult{Status: StatusReady}
	}
	if err := d.coordinator.RecoverOnStartup(ctx); err != nil {
		return StartupResult{Status: StatusDegraded, Error: err}
	}
	d.triggerReady()
	return StartupResult{Status: StatusReady}
}

func (d *DynamicEngine) ApplyNativeMutation(ctx context.Context, mutateFn func() error) error {
	_, err := d.ApplyNativeMutationWithOutcome(ctx, mutateFn)
	return err
}

func (d *DynamicEngine) ApplyNativeMutationWithOutcome(ctx context.Context, mutateFn func() error) (*mihomo.MutationOutcome, error) {
	if d.coordinator == nil {
		if mutateFn != nil {
			if err := mutateFn(); err != nil {
				return nil, err
			}
		}
		return &mihomo.MutationOutcome{
			ApplyPath: mihomo.ApplyPathDraftOnly,
		}, nil
	}
	outcome, err := d.coordinator.ApplyMutationWithOutcome(ctx, mutateFn, func(c context.Context) (*mihomo.CompileResult, error) {
		if d.compileFn != nil {
			return d.compileFn(c)
		}
		return nil, errors.New("compile function not configured")
	})
	if err != nil {
		return outcome, err
	}
	if readyErr := d.markMihomoReady(); readyErr != nil {
		return outcome, d.failMihomo(readyErr)
	}
	return outcome, nil
}

func (d *DynamicEngine) ApplyDraftOnly(ctx context.Context, mutateFn func() error) error {
	if d.nativeStore == nil {
		return mutateFn()
	}
	baseDigest, _ := d.nativeStore.CurrentDigest()
	txid := mihomo.GenerateTxID()
	snapFile, err := d.nativeStore.CreateSnapshotFile(txid)
	if err != nil {
		return err
	}
	if err := mutateFn(); err != nil {
		_ = d.nativeStore.RestoreSnapshotFile(snapFile)
		_ = d.nativeStore.RemoveSnapshotFile(snapFile)
		return err
	}
	targetDigest, _ := d.nativeStore.CurrentDigest()
	dj := mihomo.DraftJournal{
		Version:                  1,
		TxID:                     txid,
		State:                    mihomo.DraftPending,
		DraftSnapshotFile:        snapFile,
		BaseDesiredStoreDigest:   baseDigest,
		TargetDesiredStoreDigest: targetDigest,
		CreatedAt:                time.Now(),
		UpdatedAt:                time.Now(),
	}
	return d.nativeStore.SaveDraftJournal(dj)
}

func (d *DynamicEngine) ApplyPendingDraft(ctx context.Context) error {
	if d.coordinator == nil {
		return nil
	}
	return d.coordinator.MutateAndApply(ctx, nil, func(c context.Context) (*mihomo.CompileResult, error) {
		if d.compileFn != nil {
			return d.compileFn(c)
		}
		return nil, errors.New("compile function not configured")
	})
}

func (d *DynamicEngine) IsDegraded() bool {
	if d.coordinator == nil {
		return false
	}
	return d.coordinator.State() == mihomo.StateRecoveryRequired
}

func (d *DynamicEngine) CheckMutationAllowed() error {
	if d.coordinator == nil {
		return nil
	}
	return d.coordinator.CheckMutationAllowed()
}

func (d *DynamicEngine) ExportEvidence(ctx context.Context) (*mihomo.RecoveryEvidenceDTO, error) {
	if d.coordinator == nil {
		return nil, errors.New("coordinator unavailable")
	}
	return d.coordinator.ExportSafeEvidence(ctx)
}

func (d *DynamicEngine) Reconcile(ctx context.Context, action string, force bool) error {
	if d.coordinator == nil {
		return errors.New("coordinator unavailable")
	}
	if err := d.coordinator.Reconcile(ctx, action, force); err != nil {
		return err
	}
	d.triggerReady()
	return nil
}

func (d *DynamicEngine) HasUnallocatedBridges() bool {
	if d.nativeStore == nil {
		return false
	}
	for _, p := range d.nativeStore.ListProxies() {
		if p.SourceID == "" && p.Bridge == nil && p.Enabled && p.SelectedEngine == mihomonative.EngineMihomo {
			return true
		}
	}
	for _, s := range d.nativeStore.ListSubscriptions() {
		if s.Bridge == nil && d.nativeStore.IsSubscriptionExportable(s.ID) {
			return true
		}
	}
	return false
}

func (d *DynamicEngine) activeRoutingEngine() string {
	settings, err := d.settingsStore.Load()
	if err == nil && settings.SingboxRouter.RoutingEngine == "mihomo" {
		return "mihomo"
	}
	return "sing-box"
}

func (d *DynamicEngine) getActiveEngine() proxyengine.Engine {
	if d.activeRoutingEngine() == "mihomo" {
		return d.mihomoEngine
	}
	return d.singboxEngine
}

func (d *DynamicEngine) desiredMihomoMode() mihomoRuntimeMode {
	settings, err := d.settingsStore.Load()
	if err == nil && settings.SingboxRouter.Enabled && settings.SingboxRouter.RoutingEngine == "mihomo" {
		return mihomoRuntimePrimary
	}
	if d.MihomoSidecarNeeded != nil && d.MihomoSidecarNeeded() {
		return mihomoRuntimeExports
	}
	return mihomoRuntimeOff
}

func (d *DynamicEngine) prepareMihomoConfig(mode mihomoRuntimeMode) error {
	if d.OnMihomoReload != nil {
		if err := d.OnMihomoReload(); err != nil {
			return fmt.Errorf("prepare Mihomo config: %w", err)
		}
	}
	running, _ := d.mihomoEngine.IsRunning()
	if !running {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := d.mihomoEngine.ValidateConfigDir(ctx); err != nil {
			if mode == mihomoRuntimeExports {
				return fmt.Errorf("validate Mihomo exports config: %w", err)
			}
			return fmt.Errorf("validate Mihomo config: %w", err)
		}
	}
	return nil
}

func (d *DynamicEngine) markMihomoUnavailable() error {
	if d.OnMihomoUnavailable != nil {
		return d.OnMihomoUnavailable()
	}
	return nil
}

func (d *DynamicEngine) markMihomoReady() error {
	if d.OnMihomoReady != nil {
		return d.OnMihomoReady()
	}
	return nil
}

func (d *DynamicEngine) failMihomo(original error) error {
	withdrawErr := d.markMihomoUnavailable()
	if withdrawErr == nil {
		return original
	}
	return errors.Join(original, fmt.Errorf("withdraw Mihomo exports: %w", withdrawErr))
}

func (d *DynamicEngine) stopMihomoAfterWithdraw(reason string) error {
	d.currentMihomoMode = mihomoRuntimeOff
	if err := d.markMihomoUnavailable(); err != nil {
		return fmt.Errorf("withdraw Mihomo exports before %s: %w", reason, err)
	}
	if err := d.mihomoEngine.Stop(); err != nil {
		return fmt.Errorf("%s: %w", reason, err)
	}
	return nil
}

func (d *DynamicEngine) runMihomo(mode mihomoRuntimeMode, start bool) error {
	if d.coordinator != nil && d.compileFn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		var mutateFn func() error
		hasBridges := d.HasUnallocatedBridges()
		if hasBridges && d.OnMihomoPrepare != nil {
			mutateFn = func() error {
				return d.OnMihomoPrepare()
			}
		}

		// Optimization: if process is already running, no unallocated bridges exist,
		// and the compiled configuration is unchanged, skip MutateAndApply to avoid
		// unnecessary process restarts (which kill active streaming sessions).
		if !start && !hasBridges && d.mihomoEngine != nil {
			if running, pid := d.mihomoEngine.IsRunning(); running && pid > 0 {
				if res, err := d.compileFn(ctx); err == nil && res != nil {
					rec := d.coordinator.AppliedRecord()
					if rec != nil && rec.AppliedConfigDigest == res.ConfigDigest && rec.RuntimeMode == res.Mode {
						d.currentMihomoMode = mode
						if err := d.markMihomoReady(); err != nil {
							return d.failMihomo(err)
						}
						if d.OnMihomoPostApply != nil {
							d.OnMihomoPostApply()
						}
						return nil
					}
				}
			}
		}

		if err := d.coordinator.MutateAndApply(ctx, mutateFn, d.compileFn); err != nil {
			if d.activeRoutingEngine() != "mihomo" && d.mihomoEngine != nil {
				_ = d.mihomoEngine.Stop()
			}
			return d.failMihomo(fmt.Errorf("mihomo coordinator apply: %w", err))
		}
		d.currentMihomoMode = mode
		if err := d.markMihomoReady(); err != nil {
			return d.failMihomo(err)
		}
		if d.OnMihomoPostApply != nil {
			d.OnMihomoPostApply()
		}
		return nil
	}
	if mode == mihomoRuntimeOff {
		return d.stopMihomoAfterWithdraw("stop stale Mihomo engine")
	}
	if d.OnMihomoPrepare != nil {
		if err := d.OnMihomoPrepare(); err != nil {
			return d.failMihomo(fmt.Errorf("prepare Mihomo bridge allocations: %w", err))
		}
	}
	if err := d.prepareMihomoConfig(mode); err != nil {
		return d.failMihomo(err)
	}

	running, _ := d.mihomoEngine.IsRunning()
	var err error
	if start || !running {
		err = d.mihomoEngine.Start()
	} else {
		err = d.mihomoEngine.Reload()
	}
	if err != nil {
		if mode == mihomoRuntimeExports {
			return d.failMihomo(fmt.Errorf("start Mihomo exports runtime: %w", err))
		}
		return d.failMihomo(fmt.Errorf("start Mihomo routing runtime: %w", err))
	}
	d.currentMihomoMode = mode
	if err := d.markMihomoReady(); err != nil {
		return d.failMihomo(fmt.Errorf("publish Mihomo exports: %w", err))
	}
	return nil
}

func (d *DynamicEngine) Reload() error {
	return d.withTransition(d.reloadLocked)
}

func (d *DynamicEngine) reloadLocked() error {
	mode := d.desiredMihomoMode()
	if mode == mihomoRuntimePrimary {
		return d.runMihomo(mode, false)
	}
	if err := d.runMihomo(mode, false); err != nil {
		return err
	}
	// With routing disabled and Mihomo merely selected in settings there is no
	// transparent primary to reload; the exports runtime above is the complete
	// desired state.
	if d.activeRoutingEngine() == "mihomo" {
		return nil
	}

	// Process.OnReload is synchronous. Consume exactly the callback caused by
	// this call before it can attempt transitionMu; unrelated concurrent hooks
	// record a request and are drained after the sing-box call returns.
	d.suppressReloadHook.Add(1)
	d.expectedReloadHook.Store(1)
	reloadErr := d.singboxEngine.Reload()
	d.expectedReloadHook.Store(0)
	d.suppressReloadHook.Add(-1)
	drainErr := d.drainReloadHookRequestsLocked()
	return errors.Join(reloadErr, drainErr)
}

// SyncMihomoRuntime converges only the Mihomo half of the state machine. It is
// used after NDMS toggle changes and sing-box slot reloads, where recursively
// reloading the sing-box primary would be incorrect.
func (d *DynamicEngine) SyncMihomoRuntime() error {
	return d.withTransition(d.syncMihomoRuntimeLocked)
}

func (d *DynamicEngine) syncMihomoRuntimeLocked() error {
	mode := d.desiredMihomoMode()
	err := d.runMihomo(mode, false)
	if err != nil && d.activeRoutingEngine() != "mihomo" && d.mihomoEngine != nil {
		_ = d.mihomoEngine.Stop()
	}
	return err
}

func (d *DynamicEngine) ForceStopMihomo() error {
	d.transitionMu.Lock()
	defer d.transitionMu.Unlock()
	if d.mihomoEngine != nil {
		return d.mihomoEngine.Stop()
	}
	return nil
}

func (d *DynamicEngine) drainReloadHookRequestsLocked() error {
	target := d.reloadHookRequests.Load()
	if target <= d.reloadHookHandled.Load() {
		return nil
	}
	if err := d.syncMihomoRuntimeLocked(); err != nil {
		return err
	}
	d.reloadHookHandled.Store(target)
	return nil
}

// HandleSingboxReload mirrors out-of-band sing-box reloads into Mihomo. A
// reload initiated by DynamicEngine already converged Mihomo first, so the
// synchronous Process.OnReload callback is suppressed to avoid a second
// generate/reload/activate cycle.
func (d *DynamicEngine) HandleSingboxReload() error {
	if d.shuttingDown.Load() {
		return nil
	}
	if d.expectedReloadHook.CompareAndSwap(1, 0) {
		return nil
	}
	d.reloadHookRequests.Add(1)
	if d.suppressReloadHook.Load() > 0 {
		return nil
	}
	d.transitionMu.Lock()
	defer d.transitionMu.Unlock()
	if d.shuttingDown.Load() {
		return nil
	}
	return d.drainReloadHookRequestsLocked()
}

func (d *DynamicEngine) IsRunning() (bool, int) {
	return d.getActiveEngine().IsRunning()
}

func (d *DynamicEngine) Start() error {
	return d.withTransition(func() error {
		mode := d.desiredMihomoMode()
		if mode == mihomoRuntimePrimary {
			return d.runMihomo(mode, true)
		}
		if err := d.runMihomo(mode, false); err != nil {
			return err
		}
		if d.activeRoutingEngine() == "mihomo" {
			return nil
		}
		return d.singboxEngine.Start()
	})
}

func (d *DynamicEngine) ClearManualStop() error {
	return d.getActiveEngine().ClearManualStop()
}

func (d *DynamicEngine) ValidateConfigDir(ctx context.Context) error {
	return d.getActiveEngine().ValidateConfigDir(ctx)
}

func (d *DynamicEngine) ConfigDir() string {
	return d.getActiveEngine().ConfigDir()
}

func (d *DynamicEngine) Binary() string {
	return d.getActiveEngine().Binary()
}

func (d *DynamicEngine) LastError() string {
	return d.getActiveEngine().LastError()
}

func (d *DynamicEngine) CrashStats() (recentCrashes int, lastCrashReason string, restartSuppressedUntil time.Time) {
	return d.getActiveEngine().CrashStats()
}

func (d *DynamicEngine) Stop() error {
	return d.withTransition(func() error {
		if d.activeRoutingEngine() != "mihomo" {
			// A live exports runtime is independent of sing-box's transparent-routing
			// lifecycle. Without exports, however, also converge/withdraw any stale
			// Mihomo process before honouring the sing-box stop.
			if d.desiredMihomoMode() == mihomoRuntimeOff {
				if err := d.runMihomo(mihomoRuntimeOff, false); err != nil {
					return err
				}
			}
			return d.singboxEngine.Stop()
		}
		mode := d.desiredMihomoMode()
		if mode == mihomoRuntimeExports {
			return d.runMihomo(mode, false)
		}
		return d.stopMihomoAfterWithdraw("stop Mihomo engine")
	})
}

// ShutdownMihomo is the terminal transition used by both syscall.Exec restart
// and normal process exit. Setting the flag before waiting for transitionMu
// prevents queued/new API and boot syncs from publishing bridges afterward.
func (d *DynamicEngine) ShutdownMihomo(ctx context.Context) error {
	d.shuttingDown.Store(true)
	d.transitionMu.Lock()
	defer d.transitionMu.Unlock()
	var withdrawErr error
	if d.OnMihomoShutdown != nil {
		withdrawErr = d.OnMihomoShutdown(ctx)
	} else {
		withdrawErr = d.markMihomoUnavailable()
	}
	if withdrawErr != nil {
		return fmt.Errorf("withdraw Mihomo exports before shutdown: %w", withdrawErr)
	}
	if err := d.mihomoEngine.Stop(); err != nil {
		return fmt.Errorf("stop Mihomo engine during shutdown: %w", err)
	}
	return nil
}
