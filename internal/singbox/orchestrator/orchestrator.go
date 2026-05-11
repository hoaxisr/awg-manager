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

	mu      sync.Mutex
	slots   map[Slot]SlotMeta
	enabled map[Slot]bool

	// dirty signals that on-disk state changed since the last successful
	// reload. Task 4 wires this into a debounced reloader. For now Save
	// / SetEnabled just flip it.
	dirty bool

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
}

// SetLogger registers a sink for orchestrator-level log lines.
// level is one of "info", "warn", "error".
func (o *Orchestrator) SetLogger(fn func(level string, msg string)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.logf = fn
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
	return &Orchestrator{
		configDir: configDir,
		proc:      proc,
		slots:     make(map[Slot]SlotMeta),
		enabled:   make(map[Slot]bool),
	}
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
	if err := o.sweepStaleApplyCheckDirs(); err != nil {
		// Sweep failure is non-fatal — log and continue. Stale dirs
		// are harmless cosmetic noise.
		o.log("warn", fmt.Sprintf("orchestrator: sweep .apply-check: %v", err))
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

// sweepStaleApplyCheckDirs removes leftover .apply-check-* directories
// from crashed Apply runs. Tmpdir creation uses MkdirTemp with a
// well-known prefix; cleanup is best-effort.
func (o *Orchestrator) sweepStaleApplyCheckDirs() error {
	entries, err := os.ReadDir(o.configDir)
	if err != nil {
		return err
	}
	var firstErr error
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !strings.HasPrefix(e.Name(), ".apply-check-") {
			continue
		}
		if err := os.RemoveAll(filepath.Join(o.configDir, e.Name())); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Save writes the slot's JSON atomically to whichever location matches
// the slot's CURRENT enabled state. Marks the orchestrator dirty so a
// later Reload (Task 4) will pick it up.
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

// saveLocked is the shared body. Caller MUST hold o.mu. Marks the
// orchestrator dirty but does not arm the reload timer — that is the
// caller's responsibility (Save does, SaveSilent does not).
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
	o.dirty = true
	return nil
}

// SetEnabled toggles slot activity by renaming the file between
// active and disabled locations. AlwaysOn slots reject disable.
// Marks the orchestrator dirty.
func (o *Orchestrator) SetEnabled(slot Slot, enabled bool) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	meta, ok := o.slots[slot]
	if !ok {
		return ErrUnknownSlot
	}
	if !enabled && meta.AlwaysOn {
		return ErrSlotAlwaysOn
	}
	if o.enabled[slot] == enabled {
		return nil // already in target state
	}
	if err := o.renameForToggle(meta, enabled); err != nil {
		return fmt.Errorf("toggle %s: %w", slot, err)
	}
	o.enabled[slot] = enabled
	o.dirty = true
	o.scheduleReload()
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

