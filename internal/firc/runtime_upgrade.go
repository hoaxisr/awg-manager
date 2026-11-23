package firc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// StartUpgrade reserves an asynchronous transactional upgrade of awg-firc-runtime
// and runs it under the runtime mutation lock so active engines safely restart.
func (i *OnDemandInstaller) StartUpgrade(op *Operator) error {
	if err := i.reserve("upgrade"); err != nil {
		return err
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		i.runWithRuntimeLock(op, func() {
			i.finish(i.upgradeRuntimeLocked(ctx, op))
		})
	}()
	return nil
}

// Upgrade synchronously upgrades awg-firc-runtime to the configured release spec,
// downloading and verifying the new package before stopping an active daemon and
// rolling back to the cached previous package on failure.
func (i *OnDemandInstaller) Upgrade(ctx context.Context, op *Operator) error {
	if err := i.reserve("upgrade"); err != nil {
		return err
	}
	var err error
	i.runWithRuntimeLock(op, func() {
		err = i.upgradeRuntimeLocked(ctx, op)
	})
	i.finish(err)
	return err
}

func (i *OnDemandInstaller) upgradeRuntimeLocked(ctx context.Context, op *Operator) (result error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if i.packages == nil {
		return errors.New("firc: runtime package manager unavailable")
	}
	if checker, ok := i.packages.(interface{ CheckEnvironment() error }); ok {
		if err := checker.CheckEnvironment(); err != nil {
			return err
		}
	}
	if err := i.checkExternalPackageOwnership(); err != nil {
		return err
	}
	legacyOwned, err := i.HasLegacyBundledOwnership()
	if err != nil {
		return err
	}
	if legacyOwned {
		return errors.New("firc runtime belongs to package awg-manager.list; migrate ownership before upgrading")
	}

	priorOp, journalErr := i.readOperation()
	if journalErr != nil || priorOp.Pending {
		return ErrUpgradeRequiresRepair
	}
	if op != nil && op.IsRecoveryRequired() {
		return ErrUpgradeRequiresRepair
	}
	if i.checkDependenciesFn != nil {
		if _, depErr := i.checkDependenciesFn(ctx); depErr != nil {
			return fmt.Errorf("dependency check: %w", depErr)
		}
	}

	spec, fromFallback := i.installSpec()
	if !spec.IsAvailable() {
		return ErrReleaseUnavailable
	}

	previousVersion, verErr := i.packages.Version(ctx, RuntimePackageName)
	if verErr != nil {
		return fmt.Errorf("%w: %v", ErrUpgradeRequiresRepair, verErr)
	}
	if previousVersion == "" {
		return ErrRuntimeNotInstalled
	}
	if previousVersion == spec.Version {
		return ErrAlreadyUpToDate
	}
	if i.isRuntimeMissingOrBroken(ctx, op) {
		return ErrUpgradeRequiresRepair
	}

	currentlyRunning := i.isCurrentlyRunning(ctx, op)
	tunRunning := i.isTunvlessCurrentlyRunning(ctx)
	wasRunning := currentlyRunning || tunRunning
	if wasRunning && op == nil {
		if currentlyRunning {
			return ErrFircRunning
		}
		return ErrTunvlessRunning
	}

	var wasDisabled bool
	if op != nil {
		wasDisabled = op.ManuallyDisabled()
	} else {
		_, statErr := os.Lstat(i.rootPath("/opt/etc/firc/.awgm-disabled"))
		wasDisabled = !os.IsNotExist(statErr)
	}

	cache := i.rootPath("/opt/var/cache/awg-manager/firc-runtime")
	previousIPK, backupErr := i.verifyPreviousRuntimeBackup(cache, previousVersion)
	if backupErr != nil {
		return fmt.Errorf("%w: %v", ErrUpgradeBackupMissing, backupErr)
	}
	previousReceiptBytes, err := os.ReadFile(filepath.Join(cache, "installed.json"))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUpgradeBackupMissing, err)
	}

	required := spec.PackageSize*4 + (16 << 20)
	free, ok := i.probeFreeDiskForFirc()
	if !ok {
		return errors.New("firc: cannot verify free disk space")
	}
	if free < required {
		return ErrInsufficientDisk
	}

	if err := os.MkdirAll(cache, 0700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(cache, "upgrade-stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)

	ipk := filepath.Join(stage, "runtime.ipk")
	i.setProgress(0.1)
	activeSpec, err := i.prepareVerifiedRuntimeIPK(ctx, cache, stage, ipk, spec, fromFallback)
	if err != nil {
		return err
	}
	if activeSpec.Version == previousVersion {
		return ErrAlreadyUpToDate
	}

	payload, err := runtimePackageContents(ipk, i.arch, activeSpec.Version)
	if err != nil {
		return err
	}
	var unpacked int64
	for _, entry := range payload {
		unpacked += entry.header.Size
	}
	free, ok = i.freeDisk(cache)
	if !ok {
		return errors.New("cannot verify free space before package upgrade")
	}
	if free < unpacked+2*activeSpec.PackageSize+(16<<20) {
		return ErrInsufficientDisk
	}
	i.setProgress(0.35)

	ipkData, err := os.ReadFile(ipk)
	if err != nil {
		return err
	}
	if err := persistRuntimeIPKCache(cache, ipkData, spec, activeSpec, fromFallback); err != nil {
		return err
	}

	if err := i.saveOperationWithHook(runtimeOperation{
		Action:          "upgrade",
		Pending:         true,
		PreviousVersion: previousVersion,
		TargetVersion:   activeSpec.Version,
	}); err != nil {
		return err
	}

	stopped := false
	mutated := false
	completed := false
	defer func() {
		if completed {
			j := runtimeOperation{
				Action:          "upgrade",
				Pending:         false,
				PreviousVersion: previousVersion,
				TargetVersion:   activeSpec.Version,
			}
			if result != nil {
				j.Error = result.Error()
			}
			if saveErr := i.saveOperationWithHook(j); saveErr != nil {
				result = errors.Join(result, fmt.Errorf("persist runtime upgrade journal: %w", saveErr))
			}
			return
		}
		if result == nil {
			return
		}

		rollbackCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()

		rollbackOK := true
		if mutated {
			if op != nil && (i.isCurrentlyRunning(rollbackCtx, op) || i.isTunvlessCurrentlyRunning(rollbackCtx)) {
				if stopErr := op.stopRuntime(rollbackCtx); stopErr != nil {
					rollbackOK = false
					result = errors.Join(result, fmt.Errorf("stop failed upgraded runtime during rollback: %w", stopErr))
				} else if waitErr := i.waitRuntimeStopped(rollbackCtx, op, 10*time.Second); waitErr != nil {
					rollbackOK = false
					result = errors.Join(result, fmt.Errorf("wait stopped upgraded runtime during rollback: %w", waitErr))
				}
			}
			if rollbackOK {
				if rbErr := i.packages.Install(rollbackCtx, previousIPK); rbErr != nil {
					rollbackOK = false
					result = errors.Join(result, fmt.Errorf("runtime rollback failed: %w", rbErr))
				}
			}
			if rollbackOK {
				v, vErr := i.packages.Version(rollbackCtx, RuntimePackageName)
				if vErr != nil || v != previousVersion {
					rollbackOK = false
					result = errors.Join(result, fmt.Errorf("runtime rollback version verification failed: %q: %v", v, vErr))
				}
			}
			if rollbackOK {
				if layErr := verifyInstalledRuntime(previousIPK, i.arch, previousVersion, i.layout); layErr != nil {
					rollbackOK = false
					result = errors.Join(result, fmt.Errorf("runtime rollback layout verification failed: %w", layErr))
				}
			}
			if rollbackOK {
				if recErr := atomicWriteYAMLFile(filepath.Join(cache, "installed.json"), previousReceiptBytes, 0600); recErr != nil {
					rollbackOK = false
					result = errors.Join(result, fmt.Errorf("restore previous runtime receipt during rollback: %w", recErr))
				}
			}
		}

		if rollbackOK && stopped && wasRunning && !wasDisabled && op != nil {
			if startErr := op.startRuntime(rollbackCtx); startErr != nil {
				rollbackOK = false
				result = errors.Join(result, fmt.Errorf("restart previous runtime after rollback: %w", startErr))
			} else if !i.isCurrentlyRunning(rollbackCtx, op) {
				rollbackOK = false
				result = errors.Join(result, errors.New("previous runtime did not become ready after rollback"))
			}
		}

		if saveErr := i.saveOperationWithHook(runtimeOperation{
			Action:          "upgrade",
			Pending:         mutated && !rollbackOK,
			PreviousVersion: previousVersion,
			TargetVersion:   activeSpec.Version,
			Error:           result.Error(),
		}); saveErr != nil {
			result = errors.Join(result, fmt.Errorf("persist runtime upgrade rollback journal: %w", saveErr))
		}
	}()

	if wasRunning {
		i.setProgress(0.45)
		stopped = true
		if err := op.stopRuntime(ctx); err != nil {
			return fmt.Errorf("stop active FIRC before runtime upgrade: %w", err)
		}
		if err := i.waitRuntimeStopped(ctx, op, 10*time.Second); err != nil {
			return err
		}
	}

	mutated = true
	i.setProgress(0.6)
	if err := i.packages.Install(ctx, ipk); err != nil {
		return fmt.Errorf("opkg upgrade failed: %w", err)
	}

	i.setProgress(0.75)
	version, err := i.packages.Version(ctx, RuntimePackageName)
	if err != nil {
		return err
	}
	if version != activeSpec.Version {
		return fmt.Errorf("upgraded runtime version %q, expected %q", version, activeSpec.Version)
	}
	if err := verifyInstalledRuntime(ipk, i.arch, activeSpec.Version, i.layout); err != nil {
		return fmt.Errorf("%w: %v", ErrLayoutIncomplete, err)
	}

	i.setProgress(0.88)
	if wasRunning && !wasDisabled && op != nil {
		if err := op.startRuntime(ctx); err != nil {
			return fmt.Errorf("restart FIRC after runtime upgrade: %w", err)
		}
		if !i.isCurrentlyRunning(ctx, op) {
			return errors.New("FIRC did not become ready after runtime upgrade")
		}
	}

	receipt, err := json.Marshal(activeSpec)
	if err != nil {
		return err
	}
	if err := atomicWriteYAMLFile(filepath.Join(cache, "installed.json"), receipt, 0600); err != nil {
		return err
	}

	completed = true
	return nil
}
