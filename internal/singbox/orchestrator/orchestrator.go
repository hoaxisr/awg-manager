package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DraftValidator is the slim contract ApplyDraft uses to run
// `sing-box check` over the tmpdir snapshot. The real implementation
// lives in internal/singbox; tests pass a stub.
type DraftValidator interface {
	Validate(ctx context.Context, configDir string) error
}

// ProcessController is the subset of sing-box.Process the orchestrator
// uses. The real *singbox.Process satisfies it.
type ProcessController interface {
	IsRunning() (bool, int) // (running, pid)
	Start() error
	Stop() error
	Reload() error
}

// Orchestrator is the single writer for sing-box config.d. See package
// doc. Safe for concurrent use by registered producers.
type Orchestrator struct {
	configDir string
	proc      ProcessController

	// appliedPath is where Reload persists the applied-state breadcrumb
	// ({hash, hasTun} of the config last applied to sing-box). Captured
	// from the package-level appliedStatePath seam at construction and
	// immutable afterwards, so Reload (including late debounce-timer
	// fires) never races a test redirecting the seam.
	appliedPath string

	mu      sync.Mutex
	slots   map[Slot]SlotMeta
	enabled map[Slot]bool

	// validator runs `sing-box check` on a directory. nil = skip
	// check (used by tests that don't need it).
	validator DraftValidator

	// logf, if non-nil, receives short human-readable messages about
	// reload outcomes (validation errors, lifecycle transitions). Set
	// by SetLogger; nil = silent.
	logf func(level string, msg string)

	// For T4 reload coalescing.
	reloadTimer *time.Timer
	reloading   bool

	// holds > 0 подавляет debounce-reload: продюсер, записавший слот во время
	// перехода режима, не должен дёргать движок посреди чужой транзакции (при
	// живом tun каждый такой reload — полный Stop+Start). Подавленная запись
	// помечается в pendingReload и применяется одним reload'ом на release.
	// ReloadNow под hold НЕ подавляется: он явный и сам применяет всё
	// накопленное, поэтому сбрасывает pendingReload.
	holds         int
	pendingReload bool

	// prevHasTun records whether the LAST applied config had a tun
	// inbound. Reload compares it against the new config's tun presence:
	// a toggle (added or removed) forces a restart because sing-box
	// cannot add/remove a tun inbound via SIGHUP. Guarded by o.mu.
	prevHasTun bool

	// lastReloadValidation stores the ValidationResult of the most
	// recent Reload that was SKIPPED because validateLocked failed
	// (engine keeps running on the old config). Cleared on the next
	// successful validation. Surfaced to the UI via
	// LastReloadValidation — primarily so a dangling reference inside
	// the user slot (90-user.json), which prune deliberately does not
	// self-heal, is visible instead of silently freezing applies.
	// Guarded by o.mu.
	lastReloadValidation *ValidationResult

	// shouldRun, when non-nil and returning false, suppresses cold-start
	// of sing-box during Reload. Used by Operator to enforce the
	// user-pressed-Stop sticky intent so config-change-triggered reloads
	// don't resurrect the daemon. SIGHUP (already running) and stop
	// transitions remain unaffected.
	shouldRun func() bool
}

// SetLogger registers a sink for orchestrator-level log lines.
// level is one of "info", "warn", "error".
func (o *Orchestrator) SetLogger(fn func(level string, msg string)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.logf = fn
}

// SetShouldRun registers a predicate consulted before Reload starts a
// stopped sing-box. fn returning false suppresses the cold-start branch;
// fn returning true (or nil predicate) preserves the legacy "always
// start when needed" behaviour. Used to plumb the manual-stop intent
// from Operator into orchestrator-triggered reloads.
func (o *Orchestrator) SetShouldRun(fn func() bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.shouldRun = fn
}

// LastReloadValidation returns a copy of the validation result that made
// the most recent Reload skip applying the merged config, or nil when the
// last validation passed (or no reload happened yet). Safe for concurrent
// callers; the copy shares no mutable state with the orchestrator.
func (o *Orchestrator) LastReloadValidation() *ValidationResult {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.lastReloadValidation == nil {
		return nil
	}
	cp := *o.lastReloadValidation
	cp.Errors = append([]ValidationError(nil), o.lastReloadValidation.Errors...)
	return &cp
}

// CurrentHasTun reports whether the LAST applied config had a tun inbound.
// Consumers (the Process reload path) use it to choose restart-over-SIGHUP:
// sing-box cannot hot-reload a tun inbound. Safe for concurrent callers.
func (o *Orchestrator) CurrentHasTun() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.prevHasTun
}

// log emits via logf if set. Caller may or may not hold the lock.
func (o *Orchestrator) log(level, msg string) {
	o.mu.Lock()
	fn := o.logf
	o.mu.Unlock()
	if fn != nil {
		fn(level, msg)
	}
}

// New constructs an orchestrator rooted at configDir (typically
// /opt/etc/sing-box/config.d). It does NOT touch disk — call Bootstrap
// after construction to scan/migrate existing files.
func New(configDir string, proc ProcessController) *Orchestrator {
	o := &Orchestrator{
		configDir:   configDir,
		proc:        proc,
		appliedPath: appliedStatePath,
		slots:       make(map[Slot]SlotMeta),
		enabled:     make(map[Slot]bool),
	}
	// Seed prevHasTun from the last applied state so a daemon restart
	// doesn't start from the in-memory zero value (false) and mistake an
	// already-running tun config for a toggle — the skip gate in Reload
	// is the primary defense, this seed covers the fallback path where
	// the skip does not fire for some other reason (e.g. hash mismatch).
	if st, ok := loadAppliedState(o.appliedPath); ok {
		o.prevHasTun = st.HasTun
	}
	return o
}

// ConfigDir returns the absolute path the orchestrator is rooted at —
// the directory sing-box reads via `-C`. Read-only access for handlers
// that need to enumerate active slot files (e.g. config-preview).
func (o *Orchestrator) ConfigDir() string {
	return o.configDir
}

// Register adds a slot to the registry. Returns ErrSlotAlreadyRegistered
// if called twice for the same slot.
func (o *Orchestrator) Register(meta SlotMeta) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.slots[meta.Slot]; ok {
		return ErrSlotAlreadyRegistered
	}
	o.slots[meta.Slot] = meta
	if meta.AlwaysOn {
		o.enabled[meta.Slot] = true
	}
	return nil
}

// Bootstrap ensures the on-disk layout (configDir + disabled subdir)
// exists and populates the in-memory enabled map for any registered
// slot whose file is found. Call once after all Register calls and
// before any Save/SetEnabled. Idempotent.
func (o *Orchestrator) Bootstrap() error {
	if err := o.ensureDirs(); err != nil {
		return err
	}
	if err := o.sweepStaleCheckDirs(); err != nil {
		// Sweep failure is non-fatal — log and continue. Stale dirs
		// are harmless cosmetic noise.
		o.log("warn", fmt.Sprintf("orchestrator: sweep check dirs: %v", err))
	}
	if err := o.sweepStaleTempFiles(); err != nil {
		// Same best-effort treatment: a crash between AtomicWrite's temp write
		// and rename leaves a `*.tmp.<pid>.<nanotime>` file behind. sing-box's
		// `*.json` glob ignores it, so it is cosmetic flash accumulation.
		o.log("warn", fmt.Sprintf("orchestrator: sweep .tmp: %v", err))
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for slot, meta := range o.slots {
		a, d := o.scanDirForSlot(meta)
		switch {
		case a && d:
			// Pathological: same slot in both places. Prefer active as
			// truth, drop the stale disabled copy.
			if err := o.removeDisabledCopy(meta); err != nil {
				return fmt.Errorf("bootstrap %s: %w", slot, err)
			}
			o.enabled[slot] = true
		case a:
			o.enabled[slot] = true
		case d:
			if meta.AlwaysOn {
				// AlwaysOn slot found only in disabled/ — possible after a
				// downgrade-and-disable cycle on a previous build. Promote
				// it back to active/ so sing-box's -C (non-recursive) sees
				// it and our enabled-map reflects the AlwaysOn invariant.
				if err := os.Rename(o.disabledPath(meta), o.activePath(meta)); err != nil {
					return fmt.Errorf("bootstrap %s: promote from disabled: %w", slot, err)
				}
				o.enabled[slot] = true
			} else {
				// Non-AlwaysOn: file in disabled/ means user-disabled.
				o.enabled[slot] = false
			}
		default:
			// No file. AlwaysOn stays true (the producer must Save
			// its initial content); regular slots default to false.
			if !meta.AlwaysOn {
				o.enabled[slot] = false
			}
		}
	}
	return nil
}

// removeDisabledCopy deletes the disabled-side file for a slot. Used
// only to resolve a both-locations conflict during Bootstrap.
func (o *Orchestrator) removeDisabledCopy(meta SlotMeta) error {
	return removeIfExists(o.disabledPath(meta))
}

// checkDirPrefixes are the MkdirTemp prefixes of every validation tmpdir
// the orchestrator creates inside configDir (ApplyDraft, CheckMerged /
// SaveAndValidate, CheckSlotAlone). The sweep must know them all: a crash
// between MkdirTemp and the deferred RemoveAll strands the dir on flash
// storage forever otherwise.
var checkDirPrefixes = []string{".apply-check-", ".save-check-", ".alone-check-"}

// sweepStaleCheckDirs removes leftover validation tmpdirs from crashed
// check runs. Tmpdir creation uses MkdirTemp with a well-known prefix;
// cleanup is best-effort.
func (o *Orchestrator) sweepStaleCheckDirs() error {
	entries, err := os.ReadDir(o.configDir)
	if err != nil {
		return err
	}
	var firstErr error
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		stale := false
		for _, p := range checkDirPrefixes {
			if strings.HasPrefix(e.Name(), p) {
				stale = true
				break
			}
		}
		if !stale {
			continue
		}
		if err := os.RemoveAll(filepath.Join(o.configDir, e.Name())); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// tempFileMarker is the infix AtomicWritePerm gives its temp files
// (`<name>.tmp.<pid>.<nanotime>`) before the rename into place.
const tempFileMarker = ".tmp."

// sweepStaleTempFiles removes leftover AtomicWrite temp files (`*.tmp.<pid>.<n>`)
// from a crash between the temp write and the rename. It scans the active dir
// plus disabled/ and pending/, since slot writes land in all three. Best-effort:
// the first removal error is returned but the sweep continues.
func (o *Orchestrator) sweepStaleTempFiles() error {
	dirs := []string{
		o.configDir,
		filepath.Join(o.configDir, disabledSubdir),
		o.pendingDir(),
	}
	var firstErr error
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue // disabled/ or pending/ may not exist yet
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.Contains(e.Name(), tempFileMarker) {
				continue
			}
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// Save writes the slot's JSON atomically to whichever location matches
// the slot's CURRENT enabled state, then schedules a debounced reload.
func (o *Orchestrator) Save(slot Slot, jsonBytes []byte) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.saveLocked(slot, jsonBytes); err != nil {
		return err
	}
	o.scheduleReload()
	return nil
}

// SaveSilent is Save without the SIGHUP debounce. The slot file is
// written but no reload is scheduled. Used by intentional "update on
// disk only" paths (e.g. selector.default change that must not disturb
// the live selector.now). Note: a CONCURRENT Save by another producer
// will still trigger the next debounced reload — silence is best-effort
// and only meaningful when this writer is the sole change source for
// the window.
func (o *Orchestrator) SaveSilent(slot Slot, jsonBytes []byte) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.saveLocked(slot, jsonBytes)
}

// saveLocked is the shared body. Caller MUST hold o.mu. It does not arm
// the reload timer — that is the caller's responsibility (Save does,
// SaveSilent does not).
func (o *Orchestrator) saveLocked(slot Slot, jsonBytes []byte) error {
	meta, ok := o.slots[slot]
	if !ok {
		return ErrUnknownSlot
	}
	var path string
	if o.enabled[slot] {
		path = o.activePath(meta)
	} else {
		path = o.disabledPath(meta)
	}
	if err := writeAtomic(path, jsonBytes); err != nil {
		return fmt.Errorf("save %s: %w", slot, err)
	}
	return nil
}

// HoldReloads подавляет debounce-reload'ы до вызова возвращённой функции.
// Нужен на время перехода режима: teardown и провижининг пишут слоты по
// нескольку раз и дольше окна debounce, и без hold чужой reload прилетает
// посреди транзакции. Возвращённый release идемпотентен (sync.Once); когда
// снят последний hold, накопленная запись применяется одним отложенным
// reload'ом.
//
// Взведённый таймер здесь НЕ отменяется: гасит себя он сам, увидев hold в
// своём теле (см. scheduleReload). Отмена снаружи не закрывала окно между
// срабатыванием таймера и взятием mu — Stop() в нём возвращает false, а
// callback всё равно доходил до Reload уже под hold'ом.
func (o *Orchestrator) HoldReloads() func() {
	o.mu.Lock()
	o.holds++
	o.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			o.mu.Lock()
			o.holds--
			resume := o.holds == 0 && o.pendingReload
			if resume {
				o.pendingReload = false
				o.scheduleReload()
			}
			o.mu.Unlock()
		})
	}
}

// ScheduleReload планирует debounce-reload извне — для продюсеров, которые
// пишут свой слот сами (легаси-путь туннелей пишет 10-tunnels.json напрямую,
// со своей валидацией и откатом), но применять обязаны через оркестратор: он
// один знает про hold, skip-gate по хешу и applied-state.
func (o *Orchestrator) ScheduleReload() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.scheduleReload()
}

// SetEnabled toggles slot activity by renaming the file between
// active and disabled locations. AlwaysOn slots reject disable.
// Schedules a debounced reload.
func (o *Orchestrator) SetEnabled(slot Slot, enabled bool) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.setEnabledLocked(slot, enabled, true)
}

// SetEnabledSilent toggles slot activity without scheduling a debounced reload.
// Caller is responsible for calling Reload() when it needs the runtime updated.
func (o *Orchestrator) SetEnabledSilent(slot Slot, enabled bool) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.setEnabledLocked(slot, enabled, false)
}

// setEnabledLocked is the shared body. Caller MUST hold o.mu. The short-circuit
// reconciles against the ACTUAL on-disk layout, not just the in-memory map: a
// map↔disk drift (e.g. saveLocked wrote the active file while the map said
// disabled) must not let a no-op leave a stray active file that MergeDir would
// still pick up. renameForToggle already heals the both-locations case.
func (o *Orchestrator) setEnabledLocked(slot Slot, enabled, scheduleReload bool) error {
	meta, ok := o.slots[slot]
	if !ok {
		return ErrUnknownSlot
	}
	if !enabled && meta.AlwaysOn {
		return ErrSlotAlwaysOn
	}
	a, d := o.scanDirForSlot(meta)
	// Disk already in the target shape? enabled → active present, no stray
	// disabled copy; disabled → no active file (a parked copy may or may not
	// exist). Only then is a no-op safe.
	diskMatches := !a
	if enabled {
		diskMatches = a && !d
	}
	if o.enabled[slot] == enabled && diskMatches {
		return nil
	}
	if err := o.renameForToggle(meta, enabled); err != nil {
		return fmt.Errorf("toggle %s: %w", slot, err)
	}
	o.enabled[slot] = enabled
	if scheduleReload {
		o.scheduleReload()
	}
	return nil
}

// SetValidator wires a DraftValidator used by ApplyDraft. Pass nil to
// skip the external check (the default). Production wiring lives in
// main.go alongside SetLogger.
func (o *Orchestrator) SetValidator(v DraftValidator) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.validator = v
}

// Snapshot returns the current state of all registered slots in
// KnownSlots() order, filtering to only those that are registered.
func (o *Orchestrator) Snapshot() []SlotState {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []SlotState
	for _, meta := range KnownSlots() {
		if _, ok := o.slots[meta.Slot]; !ok {
			continue
		}
		en := o.enabled[meta.Slot]
		var path string
		if en {
			path = o.activePath(meta)
		} else {
			path = o.disabledPath(meta)
		}
		out = append(out, SlotState{
			Slot:     meta.Slot,
			Filename: meta.Filename,
			Enabled:  en,
			Present:  fileExists(path),
			Bytes:    fileSize(path),
		})
	}
	return out
}
