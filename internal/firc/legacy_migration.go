package firc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const legacyBackupManifestVersion = 1

// ErrSimulatedProcessCrash aborts an in-flight migration immediately without
// running deferred in-process rollback or clearing the pending journal,
// simulating a sudden process crash or power loss at a fault-injection point.
var ErrSimulatedProcessCrash = errors.New("firc: simulated process crash during legacy migration")

// LegacyMigrationFaultHooks provides deterministic fault-injection hooks for
// testing power-loss durability, backup verification, journal persistence, and
// startup recovery of legacy FIRC ownership migration.
type LegacyMigrationFaultHooks struct {
	FileSync               func(relName string) error
	DirSync                func(dir string) error
	BackupRename           func(src, dst string) error
	ParentDirSync          func(dir string) error
	SaveOperation          func(action string, pending bool) error
	AfterOwnershipDetached func() error
	AfterPackageInstalled  func() error
}

// SetLegacyMigrationFaultHooks configures fault-injection hooks on OnDemandInstaller.
func (i *OnDemandInstaller) SetLegacyMigrationFaultHooks(hooks LegacyMigrationFaultHooks) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.legacyFaultHooks = hooks
}

func (i *OnDemandInstaller) getLegacyMigrationFaultHooks() LegacyMigrationFaultHooks {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.legacyFaultHooks
}

var legacyBundledRuntimePaths = map[string]bool{
	"/opt/sbin/fircd":                        true,
	"/opt/bin/fircd":                         true,
	"/opt/sbin/tunvless":                     true,
	"/opt/bin/tunvless":                      true,
	"/opt/etc/init.d/S99firc":                true,
	"/opt/etc/ndm/netfilter.d/100-firc":      true,
	"/opt/etc/ndm/iflayerchanged.d/100-firc": true,
	"/opt/etc/firc":                          true,
	"/opt/etc/firc/firc.conf":                true,
	"/opt/etc/firc/groups.yaml":              true,
	"/opt/etc/firc/SHA256SUMS":               true,
}

var legacyRemovableRuntimeFiles = []string{
	"opt/bin/fircd",
	"opt/bin/tunvless",
	"opt/sbin/fircd",
	"opt/sbin/tunvless",
	"opt/etc/init.d/S99firc",
	"opt/etc/ndm/iflayerchanged.d/100-firc",
	"opt/etc/ndm/netfilter.d/100-firc",
	"opt/etc/firc/SHA256SUMS",
}

func isLegacyBundledRuntimePath(rawLine string) bool {
	fields := strings.Fields(strings.TrimSpace(rawLine))
	if len(fields) == 0 {
		return false
	}
	raw := filepath.ToSlash(fields[0])
	if !strings.HasPrefix(raw, "/") {
		raw = "/" + raw
	}
	clean := strings.TrimSuffix(raw, "/")
	if clean == "" {
		return false
	}
	if legacyBundledRuntimePaths[clean] {
		return true
	}
	return clean == "/opt/etc/firc" || strings.HasPrefix(clean, "/opt/etc/firc/")
}

// HasLegacyBundledOwnership reports whether /opt/lib/opkg/info/awg-manager.list
// still claims ownership of legacy bundled FIRC runtime or config files.
func (i *OnDemandInstaller) HasLegacyBundledOwnership() (bool, error) {
	listPath := i.rootPath("/opt/lib/opkg/info/awg-manager.list")
	data, err := os.ReadFile(listPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read awg-manager ownership list: %w", err)
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		if isLegacyBundledRuntimePath(line) {
			return true, nil
		}
	}
	return false, nil
}

func (i *OnDemandInstaller) rootPath(absOrSlash string) string {
	rel := strings.TrimPrefix(filepath.ToSlash(absOrSlash), "/")
	if i.layout.RootDir == "" || i.layout.RootDir == "/" {
		return "/" + rel
	}
	return filepath.Join(i.layout.RootDir, filepath.FromSlash(rel))
}

type legacyBackedEntry struct {
	RelPath    string      `json:"relPath"`
	BackupName string      `json:"backupName,omitempty"`
	SHA256     string      `json:"sha256,omitempty"`
	Size       int64       `json:"size,omitempty"`
	IsSymlink  bool        `json:"isSymlink,omitempty"`
	LinkTarget string      `json:"linkTarget,omitempty"`
	Mode       os.FileMode `json:"mode"`
}

type legacyBackupManifest struct {
	Version         int                 `json:"version"`
	WasRunning      bool                `json:"wasRunning"`
	WasDisabled     bool                `json:"wasDisabled"`
	HasOwnerList    bool                `json:"hasOwnerList"`
	OwnerListSHA256 string              `json:"ownerListSha256,omitempty"`
	OwnerListSize   int64               `json:"ownerListSize,omitempty"`
	HasConffiles    bool                `json:"hasConffiles"`
	ConffilesSHA256 string              `json:"conffilesSha256,omitempty"`
	ConffilesSize   int64               `json:"conffilesSize,omitempty"`
	RuntimeEntries  []legacyBackedEntry `json:"runtimeEntries"`
	UserConfigFiles []legacyBackedEntry `json:"userConfigFiles"`
}

func (i *OnDemandInstaller) legacyBackupDir() string {
	return i.rootPath("/opt/var/cache/awg-manager/firc-runtime/legacy-migration-backup")
}

func (i *OnDemandInstaller) saveOperationWithHook(j runtimeOperation) error {
	hooks := i.getLegacyMigrationFaultHooks()
	if hooks.SaveOperation != nil {
		if err := hooks.SaveOperation(j.Action, j.Pending); err != nil {
			return err
		}
	}
	return i.saveOperation(j)
}

func backupSHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func writeAndSyncBackupFile(path, relName string, data []byte, perm os.FileMode, syncHook func(relName string) error) (string, int64, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return "", 0, fmt.Errorf("open backup file %s: %w", relName, err)
	}
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		return "", 0, fmt.Errorf("chmod backup file %s: %w", relName, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return "", 0, fmt.Errorf("write backup file %s: %w", relName, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return "", 0, fmt.Errorf("fsync backup file %s: %w", relName, err)
	}
	if syncHook != nil {
		if err := syncHook(relName); err != nil {
			_ = f.Close()
			return "", 0, fmt.Errorf("fsync backup file %s: %w", relName, err)
		}
	}
	if err := f.Close(); err != nil {
		return "", 0, fmt.Errorf("close backup file %s: %w", relName, err)
	}
	return backupSHA256Hex(data), int64(len(data)), nil
}

func syncBackupDir(dir string, hook func(dir string) error) error {
	if hook != nil {
		return hook(dir)
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	df, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open backup dir %s for fsync: %w", dir, err)
	}
	if err := df.Sync(); err != nil {
		_ = df.Close()
		return fmt.Errorf("fsync backup dir %s: %w", dir, err)
	}
	if err := df.Close(); err != nil {
		return fmt.Errorf("close backup dir %s after fsync: %w", dir, err)
	}
	return nil
}

func verifyBackedFileMatch(path, label, wantSHA256 string, wantSize int64) error {
	if wantSHA256 == "" {
		return fmt.Errorf("missing expected checksum for %s", label)
	}
	st, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat backup file %s: %w", label, err)
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("backup file %s is not a regular file", label)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read backup file %s: %w", label, err)
	}
	if int64(len(data)) != wantSize {
		return fmt.Errorf("backup file %s size mismatch: got %d, want %d", label, len(data), wantSize)
	}
	if got := backupSHA256Hex(data); !strings.EqualFold(got, wantSHA256) {
		return fmt.Errorf("backup file %s sha256 mismatch: got %s, want %s", label, got, wantSHA256)
	}
	return nil
}

func verifyBackedEntries(filesDir, group string, entries []legacyBackedEntry) error {
	for _, entry := range entries {
		if strings.TrimSpace(entry.RelPath) == "" {
			return fmt.Errorf("%s entry has empty relPath", group)
		}
		if entry.IsSymlink {
			if strings.TrimSpace(entry.LinkTarget) == "" {
				return fmt.Errorf("%s symlink %s has empty linkTarget", group, entry.RelPath)
			}
			continue
		}
		if strings.TrimSpace(entry.BackupName) == "" {
			return fmt.Errorf("%s entry %s has empty backupName", group, entry.RelPath)
		}
		if err := verifyBackedFileMatch(
			filepath.Join(filesDir, entry.BackupName),
			entry.RelPath+" ("+entry.BackupName+")",
			entry.SHA256,
			entry.Size,
		); err != nil {
			return err
		}
	}
	return nil
}

// verifyLegacyBackup validates the full integrity of an on-disk legacy migration
// backup directory (manifest.json, awg-manager.list, awg-manager.conffiles, and
// every referenced blob's size and SHA-256) before any destructive step or recovery.
func (i *OnDemandInstaller) verifyLegacyBackup(backupDir string) (legacyBackupManifest, error) {
	var m legacyBackupManifest
	manifestPath := filepath.Join(backupDir, "manifest.json")
	st, err := os.Lstat(manifestPath)
	if err != nil {
		return m, fmt.Errorf("read legacy migration backup manifest: %w", err)
	}
	if !st.Mode().IsRegular() {
		return m, errors.New("legacy migration backup manifest is not a regular file")
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return m, fmt.Errorf("read legacy migration backup manifest: %w", err)
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, fmt.Errorf("unmarshal legacy migration backup manifest: %w", err)
	}
	if m.Version != legacyBackupManifestVersion {
		return m, fmt.Errorf("unsupported legacy migration backup manifest version %d", m.Version)
	}
	if !m.HasOwnerList && len(m.RuntimeEntries) == 0 && len(m.UserConfigFiles) == 0 {
		return m, errors.New("legacy migration backup manifest contains no recoverable entries")
	}
	if m.HasOwnerList {
		if err := verifyBackedFileMatch(
			filepath.Join(backupDir, "awg-manager.list"),
			"awg-manager.list",
			m.OwnerListSHA256,
			m.OwnerListSize,
		); err != nil {
			return m, err
		}
	}
	if m.HasConffiles {
		if err := verifyBackedFileMatch(
			filepath.Join(backupDir, "awg-manager.conffiles"),
			"awg-manager.conffiles",
			m.ConffilesSHA256,
			m.ConffilesSize,
		); err != nil {
			return m, err
		}
	}
	filesDir := filepath.Join(backupDir, "blobs")
	if err := verifyBackedEntries(filesDir, "runtime", m.RuntimeEntries); err != nil {
		return m, err
	}
	if err := verifyBackedEntries(filesDir, "userConfig", m.UserConfigFiles); err != nil {
		return m, err
	}
	return m, nil
}

// StartLegacyMigration reserves an asynchronous legacy ownership migration job.
func (i *OnDemandInstaller) StartLegacyMigration(op *Operator) error {
	if err := i.reserve("migrate_legacy"); err != nil {
		return err
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if i.lockRuntime != nil {
			unlock := i.lockRuntime()
			defer unlock()
		}
		i.finish(i.migrateLegacyLocked(ctx, op))
	}()
	return nil
}

// MigrateLegacyBundledRuntime transactionally migrates FIRC file ownership from a
// legacy bundled awg-manager installation to standalone awg-firc-runtime while
// preserving user /opt/etc/firc/* configuration and active daemon state, rolling
// back files, opkg metadata, and running state on failure.
func (i *OnDemandInstaller) MigrateLegacyBundledRuntime(ctx context.Context, op *Operator) error {
	if err := i.reserve("migrate_legacy"); err != nil {
		return err
	}
	var err error
	func() {
		if i.lockRuntime != nil {
			unlock := i.lockRuntime()
			defer unlock()
		} else if op != nil {
			unlock := op.LockRuntimeMutation()
			defer unlock()
		}
		err = i.migrateLegacyLocked(ctx, op)
	}()
	i.finish(err)
	return err
}

// RecoverPendingLegacyMigration is an alias for RecoverPendingOperationOnStartup.
func (i *OnDemandInstaller) RecoverPendingLegacyMigration(ctx context.Context, op *Operator) error {
	return i.RecoverPendingOperationOnStartup(ctx, op)
}

// RecoverPendingOperationOnStartup inspects firc-runtime-operation.json during
// manager startup before any automatic sing-box migration or FIRC daemon start.
// If a previous package operation or legacy ownership migration was interrupted:
//   - For migrate_legacy: validates the full original backup in legacy-migration-backup
//     and deterministically rolls back to the pre-migration snapshot (removing any
//     partially/fully installed standalone package, restoring legacy files, user configs,
//     awg-manager.list/.conffiles, and preserving .awgm-disabled). If the backup is
//     missing or corrupt, it fails closed and keeps Pending=true so auto-start is blocked.
//   - For install/uninstall: reconciles against verified cache/receipt or fails closed.
func (i *OnDemandInstaller) RecoverPendingOperationOnStartup(ctx context.Context, op *Operator) error {
	if err := i.reserve("recover_startup"); err != nil {
		return err
	}
	var err error
	func() {
		if i.lockRuntime != nil {
			unlock := i.lockRuntime()
			defer unlock()
		} else if op != nil {
			unlock := op.LockRuntimeMutation()
			defer unlock()
		}
		err = i.recoverPendingLocked(ctx, op)
	}()
	i.finish(err)
	return err
}

func (i *OnDemandInstaller) recoverPendingLocked(ctx context.Context, op *Operator) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	priorOp, journalErr := i.readOperation()
	if journalErr != nil {
		return fmt.Errorf("corrupt runtime operation journal (recovery_required): %w", journalErr)
	}
	backupDir := i.legacyBackupDir()
	if !priorOp.Pending {
		_ = os.RemoveAll(backupDir)
		_ = os.RemoveAll(i.externalBackupDir())
		return nil
	}

	switch priorOp.Action {
	case "migrate_external":
		return i.recoverExternalMigrationLocked(ctx, op, false)

	case "migrate_legacy":
		manifest, err := i.verifyLegacyBackup(backupDir)
		if err != nil {
			return fmt.Errorf("pending legacy migration requires intact original backup (recovery_required): %w", err)
		}
		rollbackManifest := manifest
		rollbackManifest.WasRunning = false
		if rbErr := i.rollbackLegacyBackup(ctx, backupDir, rollbackManifest, op); rbErr != nil {
			return fmt.Errorf("rollback interrupted legacy migration on startup: %w", rbErr)
		}
		if manifest.HasOwnerList {
			legacyOwned, ownErr := i.HasLegacyBundledOwnership()
			if ownErr != nil {
				return fmt.Errorf("verify restored legacy ownership on startup: %w", ownErr)
			}
			if !legacyOwned {
				return errors.New("restored awg-manager.list does not contain legacy FIRC ownership entries")
			}
		}
		if i.packages != nil {
			if ver, verErr := i.packages.Version(ctx, RuntimePackageName); verErr != nil || ver != "" {
				return fmt.Errorf("standalone runtime package still present after startup rollback (ver=%q, err=%v)", ver, verErr)
			}
		}
		if saveErr := i.saveOperationWithHook(runtimeOperation{
			Action:  "migrate_legacy",
			Pending: false,
		}); saveErr != nil {
			return fmt.Errorf("persist startup legacy migration recovery journal: %w", saveErr)
		}
		_ = os.RemoveAll(backupDir)
		return nil

	case "uninstall":
		if i.packages == nil {
			return errors.New("runtime package manager unavailable for pending uninstall recovery")
		}
		if ver, verErr := i.packages.Version(ctx, RuntimePackageName); verErr != nil || ver != "" {
			if rmErr := i.packages.Remove(ctx, RuntimePackageName); rmErr != nil {
				return fmt.Errorf("complete interrupted runtime uninstall on startup: %w", rmErr)
			}
		}
		cleanupExtNetfilter(ctx, i.layout.RootDir)
		cleanupPoolRejectBarrierForRoot(ctx, i.layout.RootDir)
		ver, verErr := i.packages.Version(ctx, RuntimePackageName)
		if verErr != nil || ver != "" {
			return fmt.Errorf("runtime remains installed after startup uninstall recovery (ver=%q, err=%v)", ver, verErr)
		}
		binFircd := i.rootPath("/opt/bin/fircd")
		binTunvless := i.rootPath("/opt/bin/tunvless")
		for _, p := range []string{i.layout.FircdBin, i.layout.TunvlessBin, binFircd, binTunvless, i.layout.InitScript, i.layout.NDMNetfilterHook, i.layout.NDMIfLayerHook} {
			if _, err := os.Lstat(p); !os.IsNotExist(err) {
				return fmt.Errorf("runtime file remains after startup uninstall recovery: %s", p)
			}
		}
		return i.saveOperationWithHook(runtimeOperation{Action: "uninstall", Pending: false})

	case "install":
		if i.packages == nil {
			return errors.New("runtime package manager unavailable for pending install recovery")
		}
		cache := i.rootPath("/opt/var/cache/awg-manager/firc-runtime")
		receiptBytes, rErr := os.ReadFile(filepath.Join(cache, "installed.json"))
		if rErr == nil {
			var receipt FircBinarySpec
			if json.Unmarshal(receiptBytes, &receipt) == nil && receipt.IsAvailable() {
				cachedIPK := filepath.Join(cache, "runtime-"+receipt.PackageSHA256+".ipk")
				if VerifyFileSHA256(cachedIPK, receipt.PackageSHA256) == nil && VerifyRuntimeIPK(cachedIPK, i.arch, receipt.Version) == nil {
					ver, verErr := i.packages.Version(ctx, RuntimePackageName)
					if verErr != nil || ver != receipt.Version || verifyInstalledRuntime(cachedIPK, i.arch, receipt.Version, i.layout) != nil {
						if instErr := i.packages.Install(ctx, cachedIPK); instErr != nil {
							return fmt.Errorf("reinstall verified runtime package during startup recovery: %w", instErr)
						}
						ver, verErr = i.packages.Version(ctx, RuntimePackageName)
					}
					if verErr == nil && ver == receipt.Version && verifyInstalledRuntime(cachedIPK, i.arch, receipt.Version, i.layout) == nil {
						return i.saveOperationWithHook(runtimeOperation{Action: "install", Pending: false})
					}
				}
			}
		}
		return errors.New("interrupted runtime install requires recovery before starting FIRC")

	default:
		return fmt.Errorf("unknown pending runtime operation %q requires recovery", priorOp.Action)
	}
}

func (i *OnDemandInstaller) isCurrentlyRunning(ctx context.Context, op *Operator) bool {
	if i.fircRunningFn != nil {
		return i.fircRunningFn(ctx)
	}
	if op != nil {
		return op.IsRunning(ctx)
	}
	return false
}

func (i *OnDemandInstaller) isTunvlessCurrentlyRunning(ctx context.Context) bool {
	if i.tunvlessRunningFn != nil {
		return i.tunvlessRunningFn(ctx)
	}
	return false
}

func (i *OnDemandInstaller) migrateLegacyLocked(ctx context.Context, op *Operator) (result error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := i.checkExternalPackageOwnership(); err != nil {
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

	priorOp, journalErr := i.readOperation()
	if journalErr != nil {
		return fmt.Errorf("cannot read runtime recovery journal: %w", journalErr)
	}
	backupDir := i.legacyBackupDir()
	var manifest legacyBackupManifest
	reusingInterruptedBackup := false
	if priorOp.Pending {
		if priorOp.Action != "migrate_legacy" {
			return fmt.Errorf("cannot migrate legacy runtime while %q operation is pending recovery", priorOp.Action)
		}
		verified, verifyErr := i.verifyLegacyBackup(backupDir)
		if verifyErr != nil {
			return fmt.Errorf("interrupted legacy migration requires intact original backup (recovery_required): %w", verifyErr)
		}
		manifest = verified
		reusingInterruptedBackup = true
	}

	spec, fromFallback := i.installSpec()
	if !spec.IsAvailable() {
		return ErrReleaseUnavailable
	}

	legacyOwned, err := i.HasLegacyBundledOwnership()
	if err != nil {
		return err
	}
	if !legacyOwned && !reusingInterruptedBackup {
		installedVer, verErr := i.packages.Version(ctx, RuntimePackageName)
		if verErr == nil && installedVer == spec.Version {
			return nil
		}
		return errors.New("no legacy bundled awg-manager FIRC ownership to migrate")
	}

	currentlyRunning := i.isCurrentlyRunning(ctx, op)
	tunRunning := i.isTunvlessCurrentlyRunning(ctx)
	if (currentlyRunning || tunRunning) && op == nil {
		if currentlyRunning {
			return ErrFircRunning
		}
		return ErrTunvlessRunning
	}

	required := spec.PackageSize*4 + (24 << 20)
	if i.freeDisk == nil {
		return errors.New("firc: free-space probe unavailable")
	}
	free, ok := i.freeDisk(filepath.Dir(i.layout.FircdBin))
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
	stage, err := os.MkdirTemp(cache, "migrate-stage-")
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
		return errors.New("cannot verify free space before legacy runtime migration")
	}
	if free < unpacked+2*activeSpec.PackageSize+(24<<20) {
		return ErrInsufficientDisk
	}

	if !reusingInterruptedBackup {
		var err error
		manifest, err = i.captureLegacyBackup(backupDir, currentlyRunning || tunRunning, op)
		if err != nil {
			return fmt.Errorf("backup legacy FIRC runtime before migration: %w", err)
		}
	}

	if err := i.saveOperationWithHook(runtimeOperation{Action: "migrate_legacy", Pending: true}); err != nil {
		return err
	}
	i.setProgress(0.35)

	ipkBytes, err := os.ReadFile(ipk)
	if err != nil {
		return err
	}
	if err := persistRuntimeIPKCache(cache, ipkBytes, spec, activeSpec, fromFallback); err != nil {
		return err
	}

	hooks := i.getLegacyMigrationFaultHooks()
	mutated := false
	rollbackComplete := false
	defer func() {
		if errors.Is(result, ErrSimulatedProcessCrash) {
			return
		}
		if result != nil && mutated {
			rbCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			if rbErr := i.rollbackLegacyBackup(rbCtx, backupDir, manifest, op); rbErr != nil {
				result = errors.Join(result, fmt.Errorf("legacy migration rollback failed: %w", rbErr))
			} else {
				rollbackComplete = true
			}
		}
		pending := result != nil && ((mutated && !rollbackComplete) || (priorOp.Pending && !rollbackComplete && !mutated))
		j := runtimeOperation{Action: "migrate_legacy", Pending: pending}
		if result != nil {
			j.Error = result.Error()
		}
		saveErr := i.saveOperationWithHook(j)
		if saveErr != nil {
			result = errors.Join(result, fmt.Errorf("persist legacy migration journal: %w", saveErr))
		} else if !j.Pending && (result == nil || rollbackComplete) {
			_ = os.RemoveAll(backupDir)
		}
	}()

	mutated = true
	if currentlyRunning || tunRunning {
		if err := op.stopRuntime(ctx); err != nil {
			return fmt.Errorf("stop active FIRC before ownership migration: %w", err)
		}
		if i.isCurrentlyRunning(ctx, op) || i.isTunvlessCurrentlyRunning(ctx) {
			return errors.New("FIRC or tunvless remained running after stop before migration")
		}
	}

	if err := i.stripLegacyEntriesFromAwgManagerOpkgInfo(); err != nil {
		return fmt.Errorf("detach legacy FIRC entries from awg-manager opkg metadata: %w", err)
	}
	for _, rel := range legacyRemovableRuntimeFiles {
		p := i.rootPath(rel)
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove legacy runtime file %s: %w", rel, err)
		}
	}
	if hooks.AfterOwnershipDetached != nil {
		if err := hooks.AfterOwnershipDetached(); err != nil {
			return err
		}
	}
	i.setProgress(0.6)

	if err := i.packages.Install(ctx, ipk); err != nil {
		return fmt.Errorf("install standalone runtime package during migration: %w", err)
	}
	version, err := i.packages.Version(ctx, RuntimePackageName)
	if err != nil {
		return err
	}
	if version != activeSpec.Version {
		return fmt.Errorf("migrated runtime version %q, expected %q", version, activeSpec.Version)
	}
	if hooks.AfterPackageInstalled != nil {
		if err := hooks.AfterPackageInstalled(); err != nil {
			return err
		}
	}

	if err := i.restoreAndVerifyUserConfigs(backupDir, manifest.UserConfigFiles); err != nil {
		return fmt.Errorf("restore user FIRC configuration after migration: %w", err)
	}
	if err := verifyInstalledRuntime(ipk, i.arch, activeSpec.Version, i.layout); err != nil {
		return fmt.Errorf("%w: %v", ErrLayoutIncomplete, err)
	}
	stillLegacy, err := i.HasLegacyBundledOwnership()
	if err != nil {
		return err
	}
	if stillLegacy {
		return errors.New("awg-manager still claims legacy FIRC ownership after migration")
	}

	receipt, err := json.Marshal(activeSpec)
	if err != nil {
		return err
	}
	if err := atomicWriteYAMLFile(filepath.Join(cache, "installed.json"), receipt, 0600); err != nil {
		return err
	}
	i.setProgress(0.85)

	if manifest.WasRunning && !manifest.WasDisabled && op != nil {
		if err := op.startRuntime(ctx); err != nil {
			return fmt.Errorf("restart FIRC after ownership migration: %w", err)
		}
		if !i.isCurrentlyRunning(ctx, op) {
			return errors.New("FIRC did not become ready after ownership migration")
		}
	}
	return nil
}

func (i *OnDemandInstaller) captureLegacyBackup(backupDir string, wasRunning bool, op *Operator) (legacyBackupManifest, error) {
	hooks := i.getLegacyMigrationFaultHooks()
	var m legacyBackupManifest
	m.Version = legacyBackupManifestVersion
	m.WasRunning = wasRunning
	disabledMarker := i.rootPath("/opt/etc/firc/.awgm-disabled")
	if op != nil {
		m.WasDisabled = op.ManuallyDisabled()
	} else {
		_, err := os.Lstat(disabledMarker)
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

	ownerList := i.rootPath("/opt/lib/opkg/info/awg-manager.list")
	if data, err := os.ReadFile(ownerList); err == nil {
		sum, size, wErr := writeAndSyncBackupFile(
			filepath.Join(tmpDir, "awg-manager.list"),
			"awg-manager.list",
			data,
			0600,
			hooks.FileSync,
		)
		if wErr != nil {
			return m, wErr
		}
		m.HasOwnerList = true
		m.OwnerListSHA256 = sum
		m.OwnerListSize = size
	} else if !os.IsNotExist(err) {
		return m, err
	}

	conffiles := i.rootPath("/opt/lib/opkg/info/awg-manager.conffiles")
	if data, err := os.ReadFile(conffiles); err == nil {
		sum, size, wErr := writeAndSyncBackupFile(
			filepath.Join(tmpDir, "awg-manager.conffiles"),
			"awg-manager.conffiles",
			data,
			0600,
			hooks.FileSync,
		)
		if wErr != nil {
			return m, wErr
		}
		m.HasConffiles = true
		m.ConffilesSHA256 = sum
		m.ConffilesSize = size
	} else if !os.IsNotExist(err) {
		return m, err
	}

	seq := 0
	for _, rel := range legacyRemovableRuntimeFiles {
		entry, ok, err := i.snapshotBackedEntry(filesDir, rel, &seq, hooks.FileSync)
		if err != nil {
			return m, err
		}
		if ok {
			m.RuntimeEntries = append(m.RuntimeEntries, entry)
		}
	}

	userEntries, err := i.walkAndBackupDir("/opt/etc/firc", "opt/etc/firc/", filesDir, func(relSlash string) bool {
		return relSlash == "SHA256SUMS"
	}, hooks.FileSync, &seq)
	if err != nil {
		return m, err
	}
	m.UserConfigFiles = userEntries

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
			return m, fmt.Errorf("rename backup dir %s -> %s: %w", tmpDir, backupDir, err)
		}
	} else if err := os.Rename(tmpDir, backupDir); err != nil {
		return m, fmt.Errorf("rename backup dir %s -> %s: %w", tmpDir, backupDir, err)
	}
	cleanupTmp = false

	if err := syncParentDir(backupDir, hooks.ParentDirSync); err != nil {
		return m, fmt.Errorf("sync legacy backup parent dir: %w", err)
	}
	verified, err := i.verifyLegacyBackup(backupDir)
	if err != nil {
		return m, fmt.Errorf("verify captured legacy backup: %w", err)
	}
	return verified, nil
}

func (i *OnDemandInstaller) stripLegacyEntriesFromAwgManagerOpkgInfo() error {
	filterFile := func(path string) error {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		var kept []string
		lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
		for idx, line := range lines {
			if idx == len(lines)-1 && line == "" {
				continue
			}
			if isLegacyBundledRuntimePath(line) {
				continue
			}
			kept = append(kept, line)
		}
		var out string
		if len(kept) > 0 {
			out = strings.Join(kept, "\n") + "\n"
		}
		return atomicWriteYAMLFile(path, []byte(out), 0644)
	}
	if err := filterFile(i.rootPath("/opt/lib/opkg/info/awg-manager.list")); err != nil {
		return err
	}
	return filterFile(i.rootPath("/opt/lib/opkg/info/awg-manager.conffiles"))
}

func (i *OnDemandInstaller) restoreBackedEntries(backupDir string, entries []legacyBackedEntry, verify bool) error {
	filesDir := filepath.Join(backupDir, "blobs")
	for _, entry := range entries {
		dest := i.rootPath(entry.RelPath)
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return err
		}
		if entry.IsSymlink {
			_ = os.Remove(dest)
			if err := os.Symlink(entry.LinkTarget, dest); err != nil {
				return err
			}
			if verify {
				got, err := os.Readlink(dest)
				if err != nil || filepath.ToSlash(got) != entry.LinkTarget {
					return fmt.Errorf("restored symlink mismatch at %s", entry.RelPath)
				}
			}
			continue
		}
		blobPath := filepath.Join(filesDir, entry.BackupName)
		if entry.SHA256 != "" {
			if err := verifyBackedFileMatch(blobPath, entry.RelPath, entry.SHA256, entry.Size); err != nil {
				return err
			}
		}
		want, err := os.ReadFile(blobPath)
		if err != nil {
			return err
		}
		_ = os.Remove(dest)
		mode := entry.Mode
		if mode == 0 {
			mode = 0600
		}
		if err := atomicWriteYAMLFile(dest, want, mode); err != nil {
			return err
		}
		if verify {
			got, err := os.ReadFile(dest)
			if err != nil {
				return err
			}
			if !bytes.Equal(got, want) {
				return fmt.Errorf("restored file content mismatch at %s", entry.RelPath)
			}
		}
	}
	return nil
}

func (i *OnDemandInstaller) restoreAndVerifyUserConfigs(backupDir string, entries []legacyBackedEntry) error {
	return i.restoreBackedEntries(backupDir, entries, true)
}

func (i *OnDemandInstaller) rollbackLegacyBackup(ctx context.Context, backupDir string, m legacyBackupManifest, op *Operator) error {
	var errs []error
	if op != nil {
		if err := op.stopRuntime(ctx); err != nil {
			errs = append(errs, fmt.Errorf("stop FIRC during rollback: %w", err))
		}
	}
	if i.packages != nil {
		if ver, verErr := i.packages.Version(ctx, RuntimePackageName); verErr != nil || ver != "" {
			if rmErr := i.packages.Remove(ctx, RuntimePackageName); rmErr != nil {
				errs = append(errs, fmt.Errorf("remove partial runtime package during rollback: %w", rmErr))
			}
		}
	}
	for rel := range runtimePayload {
		_ = os.Remove(i.rootPath(rel))
	}
	if err := i.restoreBackedEntries(backupDir, m.RuntimeEntries, true); err != nil {
		errs = append(errs, fmt.Errorf("restore legacy runtime files: %w", err))
	}
	if err := i.restoreBackedEntries(backupDir, m.UserConfigFiles, true); err != nil {
		errs = append(errs, fmt.Errorf("restore user config files: %w", err))
	}
	if m.WasDisabled {
		disabledTargets := []string{i.rootPath("/opt/etc/firc/.awgm-disabled")}
		if op != nil && op.disabledPath() != "" && filepath.Clean(op.disabledPath()) != filepath.Clean(disabledTargets[0]) {
			disabledTargets = append(disabledTargets, op.disabledPath())
		}
		for _, dp := range disabledTargets {
			if err := os.MkdirAll(filepath.Dir(dp), 0700); err != nil {
				errs = append(errs, err)
			} else if err := atomicWriteYAMLFile(dp, []byte("disabled by user\n"), 0600); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if m.HasOwnerList {
		ownerBackup := filepath.Join(backupDir, "awg-manager.list")
		if m.OwnerListSHA256 != "" {
			if err := verifyBackedFileMatch(ownerBackup, "awg-manager.list", m.OwnerListSHA256, m.OwnerListSize); err != nil {
				errs = append(errs, err)
			}
		}
		data, err := os.ReadFile(ownerBackup)
		if err != nil {
			errs = append(errs, err)
		} else if err := atomicWriteYAMLFile(i.rootPath("/opt/lib/opkg/info/awg-manager.list"), data, 0644); err != nil {
			errs = append(errs, err)
		}
	}
	if m.HasConffiles {
		confBackup := filepath.Join(backupDir, "awg-manager.conffiles")
		if m.ConffilesSHA256 != "" {
			if err := verifyBackedFileMatch(confBackup, "awg-manager.conffiles", m.ConffilesSHA256, m.ConffilesSize); err != nil {
				errs = append(errs, err)
			}
		}
		data, err := os.ReadFile(confBackup)
		if err != nil {
			errs = append(errs, err)
		} else if err := atomicWriteYAMLFile(i.rootPath("/opt/lib/opkg/info/awg-manager.conffiles"), data, 0644); err != nil {
			errs = append(errs, err)
		}
	}
	if m.WasRunning && !m.WasDisabled && op != nil {
		if err := op.startRuntime(ctx); err != nil {
			errs = append(errs, fmt.Errorf("restart legacy FIRC during rollback: %w", err))
		} else if !i.isCurrentlyRunning(ctx, op) {
			errs = append(errs, errors.New("legacy FIRC did not become ready after rollback"))
		}
	}
	return errors.Join(errs...)
}
