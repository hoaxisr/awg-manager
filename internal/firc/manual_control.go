package firc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

var ErrManuallyDisabled = errors.New("firc: disabled by user; explicitly enable FIRC first")

// LockRuntimeMutation serializes package operations with migration and process
// lifecycle. Callers must not call Start/Stop while holding this lock.
func (o *Operator) LockRuntimeMutation() func() {
	o.mutationMu.Lock()
	o.lifecycleMu.Lock()
	return func() { o.lifecycleMu.Unlock(); o.mutationMu.Unlock() }
}

// The init script uses the same durable marker. Internal shutdown/rollback Stop
// must not modify user intent, unlike the explicit UI action.
func (o *Operator) disabledPath() string { return filepath.Join(o.configDir, ".awgm-disabled") }

func (o *Operator) ManuallyDisabled() bool {
	_, err := os.Lstat(o.disabledPath())
	return !os.IsNotExist(err) // fail closed if intent cannot be read
}

func (o *Operator) StopByUser(ctx context.Context) error {
	o.mutationMu.Lock()
	defer o.mutationMu.Unlock()
	o.lifecycleMu.Lock()
	defer o.lifecycleMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := o.CheckNotForeign(); err != nil {
		return err
	}
	if err := os.MkdirAll(o.configDir, 0700); err != nil {
		return err
	}
	if err := atomicWriteYAMLFile(o.disabledPath(), []byte("disabled by user\n"), 0600); err != nil {
		return fmt.Errorf("persist disabled state: %w", err)
	}
	// Drain any pass that began before the disable marker. Later passes check
	// the marker under the same netfilter lock and cannot reinstall hooks.
	extNetfilterGen.Add(1)
	o.netfilterMu.Lock()
	defer o.netfilterMu.Unlock()
	// Keep disabled intent even if stop fails, so recovery cannot relaunch it.
	if err := o.stopRuntime(ctx); err != nil {
		return err
	}
	if o.IsRunning(ctx) {
		return errors.New("firc: daemon still responds after stop")
	}
	return nil
}

// startRuntimeAuthorized acquires lifecycleMu (never mutationMu) so callers that
// already hold mutationMu (such as SwitchEngineWithOptions) can clear an explicit
// .awgm-disabled marker only after preflight checks succeed, and roll back cleanly.
func (o *Operator) startRuntimeAuthorized(ctx context.Context, authorizeDisabled bool) (clearedDisabled bool, err error) {
	o.lifecycleMu.Lock()
	defer o.lifecycleMu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	wasDisabled := o.ManuallyDisabled()
	if !wasDisabled {
		return false, o.startRuntime(ctx)
	}
	if !authorizeDisabled {
		return false, ErrManuallyDisabled
	}

	if err := o.EnsureConfigFiles(); err != nil {
		return false, err
	}
	o.mu.Lock()
	hasStartHook := o.startHook != nil
	dirSyncHook := o.dirSyncHook
	o.mu.Unlock()
	if !hasStartHook {
		if _, perr := o.validateProductionPreflight(); perr != nil {
			return false, perr
		}
	}

	disabledPath := o.disabledPath()
	if err := os.Remove(disabledPath); err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("clear disabled state: %w", err)
	}
	if err := syncParentDir(disabledPath, dirSyncHook); err != nil {
		restoreErr := atomicWriteYAMLFileWithSyncHook(disabledPath, []byte("disabled by user\n"), 0600, dirSyncHook)
		return false, errors.Join(err, restoreErr)
	}
	if err := o.startRuntime(ctx); err != nil {
		restoreErr := atomicWriteYAMLFileWithSyncHook(disabledPath, []byte("disabled by user\n"), 0600, dirSyncHook)
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cleanupErr := o.stopRuntime(cleanupCtx)
		if cleanupErr != nil {
			cleanupErr = fmt.Errorf("cleanup failed enable: %w", cleanupErr)
		}
		return false, errors.Join(err, restoreErr, cleanupErr)
	}
	return true, nil
}

func (o *Operator) restoreDisabledMarker() error {
	o.lifecycleMu.Lock()
	defer o.lifecycleMu.Unlock()
	if err := o.CheckNotForeign(); err != nil {
		return err
	}
	if err := os.MkdirAll(o.configDir, 0700); err != nil {
		return err
	}
	o.mu.Lock()
	dirSyncHook := o.dirSyncHook
	o.mu.Unlock()
	return atomicWriteYAMLFileWithSyncHook(o.disabledPath(), []byte("disabled by user\n"), 0600, dirSyncHook)
}

func (o *Operator) StartByUser(ctx context.Context) error {
	o.mutationMu.Lock()
	defer o.mutationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := o.checkMutationAllowed(); err != nil {
		return err
	}
	_, err := o.startRuntimeAuthorized(ctx, true)
	return err
}
