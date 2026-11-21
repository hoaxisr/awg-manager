package firc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const externalBackupManifestVersion = 1

type externalBackupManifest struct {
	Version         int                 `json:"version"`
	ExternalPackage string              `json:"externalPackage"`
	WasRunning      bool                `json:"wasRunning"`
	WasDisabled     bool                `json:"wasDisabled"`
	TargetSpec      FircBinarySpec      `json:"targetSpec"`
	UserConfigFiles []legacyBackedEntry `json:"userConfigFiles"`
	VarStateFiles   []legacyBackedEntry `json:"varStateFiles"`
}

func (i *OnDemandInstaller) externalBackupDir() string {
	return i.rootPath("/opt/var/cache/awg-manager/firc-runtime/external-migration-backup")
}

func (i *OnDemandInstaller) runWithRuntimeLock(op *Operator, fn func()) {
	if i.lockRuntime != nil {
		unlock := i.lockRuntime()
		defer unlock()
	} else if op != nil {
		unlock := op.LockRuntimeMutation()
		defer unlock()
	}
	fn()
}

func (i *OnDemandInstaller) snapshotBackedEntry(filesDir, rel string, seq *int, syncHook func(string) error) (legacyBackedEntry, bool, error) {
	full := i.rootPath(rel)
	st, err := os.Lstat(full)
	if os.IsNotExist(err) {
		return legacyBackedEntry{}, false, nil
	}
	if err != nil {
		return legacyBackedEntry{}, false, err
	}
	if st.IsDir() {
		return legacyBackedEntry{}, false, nil
	}
	entry := legacyBackedEntry{
		RelPath: filepath.ToSlash(strings.TrimPrefix(rel, "/")),
		Mode:    st.Mode().Perm(),
	}
	if st.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(full)
		if err != nil {
			return legacyBackedEntry{}, false, err
		}
		entry.IsSymlink = true
		entry.LinkTarget = filepath.ToSlash(target)
		return entry, true, nil
	}
	if !st.Mode().IsRegular() {
		return legacyBackedEntry{}, false, fmt.Errorf("unsupported file type at %s", full)
	}
	*seq++
	blobName := fmt.Sprintf("blob-%04d", *seq)
	data, err := os.ReadFile(full)
	if err != nil {
		return legacyBackedEntry{}, false, err
	}
	sum, size, err := writeAndSyncBackupFile(
		filepath.Join(filesDir, blobName),
		"blobs/"+blobName,
		data,
		0600,
		syncHook,
	)
	if err != nil {
		return legacyBackedEntry{}, false, err
	}
	entry.BackupName = blobName
	entry.SHA256 = sum
	entry.Size = size
	return entry, true, nil
}

func (i *OnDemandInstaller) walkAndBackupDir(
	absDir, relPrefix, filesDir string,
	excludeRel func(string) bool,
	syncHook func(string) error,
	seq *int,
) ([]legacyBackedEntry, error) {
	dirPath := i.rootPath(absDir)
	st, err := os.Stat(dirPath)
	if os.IsNotExist(err) || (err == nil && !st.IsDir()) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []legacyBackedEntry
	walkErr := filepath.Walk(dirPath, func(p string, info os.FileInfo, wErr error) error {
		if wErr != nil {
			return wErr
		}
		relFromBase, err := filepath.Rel(dirPath, p)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(relFromBase)
		if relSlash == "." {
			return nil
		}
		if info.IsDir() {
			if excludeRel != nil && excludeRel(relSlash) {
				return filepath.SkipDir
			}
			return nil
		}
		if excludeRel != nil && excludeRel(relSlash) {
			return nil
		}
		entry, ok, err := i.snapshotBackedEntry(filesDir, relPrefix+relSlash, seq, syncHook)
		if err != nil {
			return err
		}
		if ok {
			entries = append(entries, entry)
		}
		return nil
	})
	return entries, walkErr
}

// StartExternalMigration reserves and runs an asynchronous transactional migration
// from an allowlisted external Entware FIRC package to awg-firc-runtime.
func (i *OnDemandInstaller) StartExternalMigration(op *Operator) error {
	if err := i.reserve("migrate_external"); err != nil {
		return err
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		i.runWithRuntimeLock(op, func() {
			i.finish(i.migrateExternalLocked(ctx, op))
		})
	}()
	return nil
}

// MigrateExternalRuntime synchronously replaces an external Entware FIRC package
// ("firc" or "tunvless") with awg-firc-runtime while preserving /opt/etc/firc
// configurations and /opt/var/lib/firc runtime state across opkg removal.
func (i *OnDemandInstaller) MigrateExternalRuntime(ctx context.Context, op *Operator) error {
	if err := i.reserve("migrate_external"); err != nil {
		return err
	}
	var err error
	i.runWithRuntimeLock(op, func() {
		err = i.migrateExternalLocked(ctx, op)
	})
	i.finish(err)
	return err
}

func (i *OnDemandInstaller) verifyExternalBackup(backupDir string) (externalBackupManifest, error) {
	var m externalBackupManifest
	manifestPath := filepath.Join(backupDir, "manifest.json")
	st, err := os.Lstat(manifestPath)
	if err != nil {
		return m, fmt.Errorf("read external migration backup manifest: %w", err)
	}
	if !st.Mode().IsRegular() {
		return m, errors.New("external migration backup manifest is not a regular file")
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return m, fmt.Errorf("read external migration backup manifest: %w", err)
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, fmt.Errorf("unmarshal external migration backup manifest: %w", err)
	}
	if m.Version != externalBackupManifestVersion {
		return m, fmt.Errorf("unsupported external migration backup manifest version %d", m.Version)
	}
	if !isAllowedExternalFircPackage(m.ExternalPackage) {
		return m, fmt.Errorf("invalid external package %q in migration backup", m.ExternalPackage)
	}
	if !m.TargetSpec.IsAvailable() {
		return m, errors.New("external migration backup missing valid target runtime spec")
	}
	filesDir := filepath.Join(backupDir, "blobs")
	if err := verifyBackedEntries(filesDir, "userConfig", m.UserConfigFiles); err != nil {
		return m, err
	}
	if err := verifyBackedEntries(filesDir, "varState", m.VarStateFiles); err != nil {
		return m, err
	}
	return m, nil
}

func (i *OnDemandInstaller) captureExternalBackup(
	backupDir, extPkg string,
	wasRunning bool,
	targetSpec FircBinarySpec,
	op *Operator,
) (externalBackupManifest, error) {
	hooks := i.getLegacyMigrationFaultHooks()
	var m externalBackupManifest
	m.Version = externalBackupManifestVersion
	m.ExternalPackage = extPkg
	m.WasRunning = wasRunning
	m.TargetSpec = targetSpec
	if op != nil {
		m.WasDisabled = op.ManuallyDisabled()
	} else {
		_, err := os.Lstat(i.rootPath("/opt/etc/firc/.awgm-disabled"))
		m.WasDisabled = !os.IsNotExist(err)
	}

	if err := os.MkdirAll(filepath.Dir(backupDir), 0700); err != nil {
		return m, err
	}
	tmpDir := backupDir + ".tmp"
	_ = os.RemoveAll(tmpDir)
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = os.RemoveAll(tmpDir)
		}
	}()

	filesDir := filepath.Join(tmpDir, "blobs")
	if err := os.MkdirAll(filesDir, 0700); err != nil {
		return m, err
	}

	seq := 0
	var err error
	m.UserConfigFiles, err = i.walkAndBackupDir("/opt/etc/firc", "opt/etc/firc/", filesDir, func(relSlash string) bool {
		return relSlash == "SHA256SUMS"
	}, hooks.FileSync, &seq)
	if err != nil {
		return m, err
	}
	m.VarStateFiles, err = i.walkAndBackupDir("/opt/var/lib/firc", "opt/var/lib/firc/", filesDir, func(relSlash string) bool {
		return relSlash == "update" || strings.HasPrefix(relSlash, "update/")
	}, hooks.FileSync, &seq)
	if err != nil {
		return m, err
	}

	if err := syncBackupDir(filesDir, hooks.DirSync); err != nil {
		return m, err
	}
	rawManifest, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return m, err
	}
	if _, _, err := writeAndSyncBackupFile(
		filepath.Join(tmpDir, "manifest.json"),
		"manifest.json",
		rawManifest,
		0600,
		hooks.FileSync,
	); err != nil {
		return m, err
	}
	if err := syncBackupDir(tmpDir, hooks.DirSync); err != nil {
		return m, err
	}

	_ = os.RemoveAll(backupDir)
	if hooks.BackupRename != nil {
		if err := hooks.BackupRename(tmpDir, backupDir); err != nil {
			return m, fmt.Errorf("rename external backup dir %s -> %s: %w", tmpDir, backupDir, err)
		}
	} else if err := os.Rename(tmpDir, backupDir); err != nil {
		return m, fmt.Errorf("rename external backup dir %s -> %s: %w", tmpDir, backupDir, err)
	}
	cleanupTmp = false

	if err := syncParentDir(backupDir, hooks.ParentDirSync); err != nil {
		return m, fmt.Errorf("sync external backup parent dir: %w", err)
	}
	return i.verifyExternalBackup(backupDir)
}

func (i *OnDemandInstaller) waitRuntimeStopped(ctx context.Context, op *Operator, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if !i.isCurrentlyRunning(ctx, op) && !i.isTunvlessCurrentlyRunning(ctx) {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("FIRC or tunvless remained running after stop before external migration")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (i *OnDemandInstaller) restoreExternalBackupState(backupDir string, m externalBackupManifest) error {
	if err := os.MkdirAll(i.rootPath("/opt/etc/firc"), 0700); err != nil {
		return err
	}
	if err := os.MkdirAll(i.rootPath("/opt/var/lib/firc/tunnels"), 0700); err != nil {
		return err
	}
	if err := i.restoreAndVerifyUserConfigs(backupDir, m.UserConfigFiles); err != nil {
		return fmt.Errorf("restore FIRC configuration after external migration: %w", err)
	}
	if err := i.restoreAndVerifyUserConfigs(backupDir, m.VarStateFiles); err != nil {
		return fmt.Errorf("restore FIRC var state after external migration: %w", err)
	}
	return nil
}

func (i *OnDemandInstaller) activateRuntimeFromExternalBackup(
	ctx context.Context,
	backupDir, cache, ipk string,
	m externalBackupManifest,
) error {
	version, err := i.packages.Version(ctx, RuntimePackageName)
	if err != nil || version != m.TargetSpec.Version || verifyInstalledRuntime(ipk, i.arch, m.TargetSpec.Version, i.layout) != nil {
		if instErr := i.packages.Install(ctx, ipk); instErr != nil {
			return fmt.Errorf("install awg-firc-runtime during external migration: %w", instErr)
		}
		version, err = i.packages.Version(ctx, RuntimePackageName)
		if err != nil {
			return err
		}
		if version != m.TargetSpec.Version {
			return fmt.Errorf("migrated runtime version %q, expected %q", version, m.TargetSpec.Version)
		}
	}

	if err := i.restoreExternalBackupState(backupDir, m); err != nil {
		return err
	}
	if err := verifyInstalledRuntime(ipk, i.arch, m.TargetSpec.Version, i.layout); err != nil {
		return fmt.Errorf("%w: %v", ErrLayoutIncomplete, err)
	}

	receipt, err := json.Marshal(m.TargetSpec)
	if err != nil {
		return err
	}
	if err := atomicWriteYAMLFile(filepath.Join(cache, "installed.json"), receipt, 0600); err != nil {
		return err
	}
	i.setProgress(0.85)
	return nil
}

func (i *OnDemandInstaller) probeFreeDiskForFirc() (int64, bool) {
	if i.freeDisk == nil {
		return 0, false
	}
	probeDir := filepath.Dir(i.layout.FircdBin)
	if _, err := os.Stat(probeDir); err != nil {
		probeDir = filepath.Dir(i.rootPath("/opt/bin/fircd"))
	}
	return i.freeDisk(probeDir)
}

func (i *OnDemandInstaller) migrateExternalLocked(ctx context.Context, op *Operator) (result error) {
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
	remover, ok := i.packages.(ExternalPackageRemover)
	if !ok {
		return errors.New("firc: external package removal is not supported by package manager")
	}

	priorOp, journalErr := i.readOperation()
	if journalErr != nil {
		return fmt.Errorf("cannot read runtime recovery journal: %w", journalErr)
	}
	if priorOp.Pending {
		if priorOp.Action != "migrate_external" {
			return fmt.Errorf("cannot migrate external FIRC runtime while %q operation is pending recovery", priorOp.Action)
		}
		return i.recoverExternalMigrationLocked(ctx, op, true)
	}

	extPkg, err := i.ExternalRuntimePackage()
	if err != nil {
		return err
	}
	spec, fromFallback := i.installSpec()
	if extPkg == "" {
		if spec.IsAvailable() {
			if installedVer, verErr := i.packages.Version(ctx, RuntimePackageName); verErr == nil && installedVer == spec.Version {
				return nil
			}
		}
		return errors.New("no external Entware FIRC package to migrate")
	}
	if !isAllowedExternalFircPackage(extPkg) {
		return fmt.Errorf("external Entware package %q is not eligible for automatic replacement", extPkg)
	}
	if !spec.IsAvailable() {
		return ErrReleaseUnavailable
	}
	if i.checkDependenciesFn != nil {
		deps, depErr := i.checkDependenciesFn(ctx)
		if depErr != nil {
			return fmt.Errorf("dependency check: %w", depErr)
		}
		if len(deps) > 0 {
			return fmt.Errorf("%w: %s", ErrFircDependencies, strings.Join(deps, ", "))
		}
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

	required := spec.PackageSize*4 + (24 << 20)
	free, ok := i.probeFreeDiskForFirc()
	if !ok {
		return errors.New("firc: cannot verify free disk space")
	}
	if free < required {
		return ErrInsufficientDisk
	}

	cache := i.rootPath("/opt/var/cache/awg-manager/firc-runtime")
	if err := os.MkdirAll(cache, 0700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(cache, "ext-migrate-stage-")
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
		return errors.New("cannot verify free space before external runtime migration")
	}
	if free < unpacked+2*activeSpec.PackageSize+(24<<20) {
		return ErrInsufficientDisk
	}
	ipkBytes, err := os.ReadFile(ipk)
	if err != nil {
		return err
	}
	if err := persistRuntimeIPKCache(cache, ipkBytes, spec, activeSpec, fromFallback); err != nil {
		return err
	}

	backupDir := i.externalBackupDir()
	manifest, err := i.captureExternalBackup(backupDir, extPkg, wasRunning, activeSpec, op)
	if err != nil {
		return fmt.Errorf("backup external FIRC config and state: %w", err)
	}
	if err := i.saveOperationWithHook(runtimeOperation{
		Action:          "migrate_external",
		Pending:         true,
		ExternalPackage: extPkg,
	}); err != nil {
		return err
	}
	i.setProgress(0.3)

	removedExternal := false
	completed := false
	defer func() {
		if completed {
			j := runtimeOperation{
				Action:          "migrate_external",
				Pending:         false,
				ExternalPackage: extPkg,
			}
			if result != nil {
				j.Error = result.Error()
			}
			if saveErr := i.saveOperationWithHook(j); saveErr != nil {
				result = errors.Join(result, fmt.Errorf("persist external migration journal: %w", saveErr))
				return
			}
			_ = os.RemoveAll(backupDir)
			return
		}
		if result == nil {
			return
		}
		if !removedExternal {
			if curExt, extErr := i.ExternalRuntimePackage(); extErr == nil && curExt == "" {
				removedExternal = true
			}
		}
		if !removedExternal {
			if rErr := i.restoreExternalBackupState(backupDir, manifest); rErr != nil {
				result = errors.Join(result, rErr)
			}
			if wasRunning && !manifest.WasDisabled && op != nil {
				rbCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
				if sErr := op.startRuntime(rbCtx); sErr != nil {
					result = errors.Join(result, fmt.Errorf("restart external FIRC after failed removal: %w", sErr))
				}
				cancel()
			}
			if saveErr := i.saveOperationWithHook(runtimeOperation{
				Action:          "migrate_external",
				Pending:         false,
				ExternalPackage: extPkg,
				Error:           result.Error(),
			}); saveErr != nil {
				result = errors.Join(result, fmt.Errorf("clear external migration journal after rollback: %w", saveErr))
			} else {
				_ = os.RemoveAll(backupDir)
			}
			return
		}
		if saveErr := i.saveOperationWithHook(runtimeOperation{
			Action:          "migrate_external",
			Pending:         true,
			ExternalPackage: extPkg,
			Error:           result.Error(),
		}); saveErr != nil {
			result = errors.Join(result, fmt.Errorf("persist pending external migration journal: %w", saveErr))
		}
	}()

	if wasRunning {
		if err := op.stopRuntime(ctx); err != nil {
			return fmt.Errorf("stop active FIRC before external package removal: %w", err)
		}
		if err := i.waitRuntimeStopped(ctx, op, 5*time.Second); err != nil {
			return err
		}
	}
	i.setProgress(0.45)

	if err := remover.RemoveExternal(ctx, extPkg); err != nil {
		return err
	}
	remainingExt, err := i.ExternalRuntimePackage()
	if err != nil {
		return fmt.Errorf("verify external FIRC removal: %w", err)
	}
	if remainingExt != "" {
		return fmt.Errorf("external FIRC package %q remained registered after removal", remainingExt)
	}
	removedExternal = true
	i.setProgress(0.6)

	cachedIPK := filepath.Join(cache, "runtime-"+manifest.TargetSpec.PackageSHA256+".ipk")
	if err := i.activateRuntimeFromExternalBackup(ctx, backupDir, cache, cachedIPK, manifest); err != nil {
		return err
	}
	// Package installation, config/var restoration, and installed.json receipt are complete.
	// Never keep the migration journal stuck Pending=true if post-migration daemon start fails.
	completed = true
	return i.restartExternalMigratedRuntime(ctx, op, manifest)
}

func (i *OnDemandInstaller) restartExternalMigratedRuntime(ctx context.Context, op *Operator, m externalBackupManifest) error {
	if !m.WasRunning || m.WasDisabled || op == nil {
		return nil
	}
	if err := op.startRuntime(ctx); err != nil {
		return fmt.Errorf("restart FIRC after external migration: %w", err)
	}
	if !i.isCurrentlyRunning(ctx, op) {
		return errors.New("FIRC did not become ready after external migration")
	}
	return nil
}

func (i *OnDemandInstaller) recoverExternalMigrationLocked(ctx context.Context, op *Operator, restartIfRunning bool) error {
	if i.packages == nil {
		return errors.New("runtime package manager unavailable for pending external migration recovery")
	}
	backupDir := i.externalBackupDir()
	manifest, err := i.verifyExternalBackup(backupDir)
	if err != nil {
		return fmt.Errorf("pending external migration requires intact backup (recovery_required): %w", err)
	}

	extPkg, extErr := i.ExternalRuntimePackage()
	if extErr != nil {
		return fmt.Errorf("inspect external package state during recovery: %w", extErr)
	}
	installedVer, verErr := i.packages.Version(ctx, RuntimePackageName)
	if verErr != nil {
		return fmt.Errorf("inspect runtime package state during recovery: %w", verErr)
	}

	if extPkg != "" && installedVer == "" {
		if err := i.restoreExternalBackupState(backupDir, manifest); err != nil {
			return err
		}
		if err := i.saveOperationWithHook(runtimeOperation{
			Action:          "migrate_external",
			Pending:         false,
			ExternalPackage: manifest.ExternalPackage,
		}); err != nil {
			return fmt.Errorf("persist startup external migration rollback journal: %w", err)
		}
		_ = os.RemoveAll(backupDir)
		return nil
	}

	cache := i.rootPath("/opt/var/cache/awg-manager/firc-runtime")
	cachedIPK := filepath.Join(cache, "runtime-"+manifest.TargetSpec.PackageSHA256+".ipk")
	if err := VerifyFileSHA256(cachedIPK, manifest.TargetSpec.PackageSHA256); err != nil {
		return fmt.Errorf("pending external migration cached runtime checksum invalid: %w", err)
	}
	if err := VerifyRuntimeIPK(cachedIPK, i.arch, manifest.TargetSpec.Version); err != nil {
		return fmt.Errorf("pending external migration cached runtime invalid: %w", err)
	}

	if extPkg != "" {
		remover, ok := i.packages.(ExternalPackageRemover)
		if !ok {
			return errors.New("external package remover unavailable for pending migration recovery")
		}
		if err := remover.RemoveExternal(ctx, extPkg); err != nil {
			return err
		}
	}

	if err := i.activateRuntimeFromExternalBackup(ctx, backupDir, cache, cachedIPK, manifest); err != nil {
		return err
	}
	var restartErr error
	errMsg := ""
	if restartIfRunning {
		restartErr = i.restartExternalMigratedRuntime(ctx, op, manifest)
		if restartErr != nil {
			errMsg = restartErr.Error()
		}
	}
	if err := i.saveOperationWithHook(runtimeOperation{
		Action:          "migrate_external",
		Pending:         false,
		ExternalPackage: manifest.ExternalPackage,
		Error:           errMsg,
	}); err != nil {
		return errors.Join(restartErr, fmt.Errorf("persist startup external migration recovery journal: %w", err))
	}
	_ = os.RemoveAll(backupDir)
	return restartErr
}
