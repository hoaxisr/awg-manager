package tgwebproxy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	ErrOperationInProgress = errors.New("operation_in_progress")
	ErrTransactionFailed   = errors.New("transaction_failed")
)

type FilePayload struct {
	TargetPath string
	Content    []byte
	Mode       os.FileMode
}

type ProcessLifecycleHook interface {
	CaptureWorkerStates() map[string]WorkerProcessState
	ApplyWorkers(cfg Config) error
	CheckReadiness(cfg Config) error
	RestoreWorkerStates(states map[string]WorkerProcessState) error
}

type TransactionCoordinator struct {
	mu      sync.Mutex
	baseDir string // e.g. /opt/etc/awg-manager/tproxy
}

func NewTransactionCoordinator(baseDir string) *TransactionCoordinator {
	return &TransactionCoordinator{
		baseDir: baseDir,
	}
}

func (tc *TransactionCoordinator) manifestPath() string {
	return filepath.Join(tc.baseDir, "transaction.json")
}

func fsyncFile(f *os.File) error {
	if f == nil {
		return nil
	}
	return f.Sync()
}



func calculateSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyFileAtomic(src, dst string, mode os.FileMode, uid, gid int) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open src %s: %w", src, err)
	}
	defer in.Close()

	dstDir := filepath.Dir(dst)
	if err := os.MkdirAll(dstDir, 0700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dstDir, err)
	}
	_ = os.Chmod(dstDir, 0700)

	tmp, err := os.CreateTemp(dstDir, "."+filepath.Base(dst)+".tmp.*")
	if err != nil {
		return fmt.Errorf("create temp in %s: %w", dstDir, err)
	}
	tmpPath := tmp.Name()
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("copy to temp %s: %w", tmpPath, err)
	}

	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp %s: %w", tmpPath, err)
	}

	if err := setFileOwnership(tmpPath, uid, gid); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chown temp %s: %w", tmpPath, err)
	}

	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp %s: %w", tmpPath, err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp %s: %w", tmpPath, err)
	}

	if err := os.Rename(tmpPath, dst); err != nil {
		return fmt.Errorf("rename %s to %s: %w", tmpPath, dst, err)
	}
	cleanupTmp = false

	return fsyncDir(dstDir)
}

func (tc *TransactionCoordinator) writeManifestAtomic(manifest *TransactionManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}

	tmpPath := tc.manifestPath() + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("open manifest tmp: %w", err)
	}

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write manifest tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("fsync manifest tmp: %w", err)
	}
	_ = f.Close()

	if err := os.Rename(tmpPath, tc.manifestPath()); err != nil {
		return fmt.Errorf("rename manifest: %w", err)
	}
	return fsyncDir(tc.baseDir)
}

// ExecuteTransaction runs a 5-file transactional configuration change with full crash-safety.
func (tc *TransactionCoordinator) ExecuteTransaction(
	txID string,
	files []FilePayload,
	hook ProcessLifecycleHook,
	cfg Config,
	validateFn func(stagingDir string) error,
) error {
	if !tc.mu.TryLock() {
		return ErrOperationInProgress
	}
	defer tc.mu.Unlock()

	if err := os.MkdirAll(tc.baseDir, 0700); err != nil {
		return fmt.Errorf("create base dir: %w", err)
	}
	_ = os.Chmod(tc.baseDir, 0700)

	stagingDir := filepath.Join(tc.baseDir, "staging."+txID)
	backupDir := filepath.Join(tc.baseDir, "backup."+txID)

	if err := os.MkdirAll(stagingDir, 0700); err != nil {
		return fmt.Errorf("create staging dir: %w", err)
	}
	_ = os.Chmod(stagingDir, 0700)

	if err := os.MkdirAll(backupDir, 0700); err != nil {
		_ = os.RemoveAll(stagingDir)
		return fmt.Errorf("create backup dir: %w", err)
	}
	_ = os.Chmod(backupDir, 0700)

	manifest := &TransactionManifest{
		TxID:               txID,
		Phase:              PhaseStaged,
		CreatedAt:          time.Now().UTC().Format(time.RFC3339),
		Files:              make([]ManifestFile, 0, len(files)),
		ProcessStateBefore: make(map[string]WorkerProcessState),
	}

	if hook != nil {
		manifest.ProcessStateBefore = hook.CaptureWorkerStates()
	}

	// 1. Stage all files with 0600 and fsync
	for _, fp := range files {
		mode := fp.Mode
		if mode == 0 {
			mode = 0600
		}
		stagedPath := filepath.Join(stagingDir, filepath.Base(fp.TargetPath))
		sf, err := os.OpenFile(stagedPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			_ = os.RemoveAll(stagingDir)
			_ = os.RemoveAll(backupDir)
			return fmt.Errorf("create staged file %s: %w", stagedPath, err)
		}
		if _, err := sf.Write(fp.Content); err != nil {
			_ = sf.Close()
			_ = os.RemoveAll(stagingDir)
			_ = os.RemoveAll(backupDir)
			return fmt.Errorf("write staged file %s: %w", stagedPath, err)
		}
		if err := sf.Sync(); err != nil {
			_ = sf.Close()
			_ = os.RemoveAll(stagingDir)
			_ = os.RemoveAll(backupDir)
			return fmt.Errorf("fsync staged file %s: %w", stagedPath, err)
		}
		_ = sf.Close()

		mf := ManifestFile{
			TargetPath:  fp.TargetPath,
			StagingPath: stagedPath,
			BackupPath:  filepath.Join(backupDir, filepath.Base(fp.TargetPath)),
			Sha256New:   calculateSHA256(fp.Content),
			Mode:        mode,
			UID:         0,
			GID:         0,
		}

		// 2. Snapshot existing target file if present
		if fi, err := os.Stat(fp.TargetPath); err == nil && !fi.IsDir() {
			mf.ExistedBefore = true
			if oldHash, err := fileSHA256(fp.TargetPath); err == nil {
				mf.Sha256Old = oldHash
			}
			mf.UID, mf.GID = getFileOwnership(fi)
			if err := copyFileAtomic(fp.TargetPath, mf.BackupPath, fi.Mode().Perm(), mf.UID, mf.GID); err != nil {
				_ = os.RemoveAll(stagingDir)
				_ = os.RemoveAll(backupDir)
				return fmt.Errorf("backup target file %s: %w", fp.TargetPath, err)
			}
		} else {
			mf.ExistedBefore = false
		}

		manifest.Files = append(manifest.Files, mf)
	}

	_ = fsyncDir(stagingDir)
	_ = fsyncDir(backupDir)

	// 3. Write manifest in "staged" phase
	manifest.Phase = PhaseStaged
	if err := tc.writeManifestAtomic(manifest); err != nil {
		_ = os.RemoveAll(stagingDir)
		_ = os.RemoveAll(backupDir)
		return fmt.Errorf("write staged manifest: %w", err)
	}

	// 4. Validation hook
	if validateFn != nil {
		if err := validateFn(stagingDir); err != nil {
			_ = os.Remove(tc.manifestPath())
			_ = os.RemoveAll(stagingDir)
			_ = os.RemoveAll(backupDir)
			_ = fsyncDir(tc.baseDir)
			return fmt.Errorf("validation failed: %w", err)
		}
	}

	// 5. CRITICAL: Transition manifest to "applying" BEFORE any target file is modified
	manifest.Phase = PhaseApplying
	if err := tc.writeManifestAtomic(manifest); err != nil {
		rbErr := tc.performRollbackLocked(manifest, hook)
		if rbErr != nil {
			return fmt.Errorf("write applying manifest failed (%w) and rollback failed: %v", err, rbErr)
		}
		return fmt.Errorf("write applying manifest failed: %w (rolled back successfully)", err)
	}

	// 6. Replace target files one by one with atomic rename and fsync
	for _, mf := range manifest.Files {
		_ = os.MkdirAll(filepath.Dir(mf.TargetPath), 0700)
		_ = os.Chmod(filepath.Dir(mf.TargetPath), 0700)
		if err := copyFileAtomic(mf.StagingPath, mf.TargetPath, mf.Mode, mf.UID, mf.GID); err != nil {
			rbErr := tc.performRollbackLocked(manifest, hook)
			if rbErr != nil {
				return fmt.Errorf("apply target file %s failed (%w) and rollback failed: %v", mf.TargetPath, err, rbErr)
			}
			return fmt.Errorf("apply target file %s failed: %w (rolled back successfully)", mf.TargetPath, err)
		}
		_ = fsyncDir(filepath.Dir(mf.TargetPath))
	}

	// 7. Apply process transitions and readiness
	if hook != nil {
		if err := hook.ApplyWorkers(cfg); err != nil {
			rbErr := tc.performRollbackLocked(manifest, hook)
			if rbErr != nil {
				return fmt.Errorf("apply workers failed (%w) and rollback failed: %v", err, rbErr)
			}
			return fmt.Errorf("apply workers failed: %w (rolled back successfully)", err)
		}
		if err := hook.CheckReadiness(cfg); err != nil {
			rbErr := tc.performRollbackLocked(manifest, hook)
			if rbErr != nil {
				return fmt.Errorf("workers readiness check failed (%w) and rollback failed: %v", err, rbErr)
			}
			return fmt.Errorf("workers readiness check failed: %w (rolled back successfully)", err)
		}
	}

	// 8. Transition manifest to "committed"
	manifest.Phase = PhaseCommitted
	if err := tc.writeManifestAtomic(manifest); err != nil {
		return fmt.Errorf("write committed manifest: %w", err)
	}

	// 9. Unlink journal and fsync baseDir
	_ = os.Remove(tc.manifestPath())
	_ = fsyncDir(tc.baseDir)

	// 10. Clean up staging and backup
	_ = os.RemoveAll(stagingDir)
	_ = os.RemoveAll(backupDir)
	_ = fsyncDir(tc.baseDir)

	return nil
}

// performRollbackLocked restores previous files and worker states without destroying backup on failure.
func (tc *TransactionCoordinator) performRollbackLocked(manifest *TransactionManifest, hook ProcessLifecycleHook) error {
	manifest.Phase = PhaseRollingBack
	if err := tc.writeManifestAtomic(manifest); err != nil {
		return fmt.Errorf("set rollback phase in manifest: %w", err)
	}

	var rollbackErrs []error

	for _, mf := range manifest.Files {
		targetDir := filepath.Dir(mf.TargetPath)
		if mf.ExistedBefore {
			if _, err := os.Stat(mf.BackupPath); err != nil {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("backup file missing for %s: %w", mf.TargetPath, err))
				continue
			}
			if err := copyFileAtomic(mf.BackupPath, mf.TargetPath, mf.Mode, mf.UID, mf.GID); err != nil {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback restore %s: %w", mf.TargetPath, err))
				continue
			}
			// Verify restored file SHA-256 against recorded Sha256Old
			if mf.Sha256Old != "" {
				restoredHash, err := fileSHA256(mf.TargetPath)
				if err != nil {
					rollbackErrs = append(rollbackErrs, fmt.Errorf("verify restored hash %s: %w", mf.TargetPath, err))
				} else if restoredHash != mf.Sha256Old {
					rollbackErrs = append(rollbackErrs, fmt.Errorf("restored file %s hash mismatch (got %s, want %s)", mf.TargetPath, restoredHash, mf.Sha256Old))
				}
			}
		} else {
			if err := os.Remove(mf.TargetPath); err != nil && !os.IsNotExist(err) {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback remove uncommitted %s: %w", mf.TargetPath, err))
			}
		}
		if err := fsyncDir(targetDir); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback fsync dir %s: %w", targetDir, err))
		}
	}

	if hook != nil {
		if err := hook.RestoreWorkerStates(manifest.ProcessStateBefore); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback restore workers: %w", err))
		}
	}

	if len(rollbackErrs) > 0 {
		// DO NOT delete manifest or backup! Leave in PhaseRollingBack so subsequent StartupReconcile can retry
		return fmt.Errorf("rollback encountered %d errors: %w", len(rollbackErrs), errors.Join(rollbackErrs...))
	}

	// Rollback fully verified and successful: clean up journal and staging/backup
	if err := os.Remove(tc.manifestPath()); err != nil && !os.IsNotExist(err) {
		rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback remove manifest: %w", err))
	}
	if err := fsyncDir(tc.baseDir); err != nil {
		rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback fsync base dir after manifest removal: %w", err))
	}
	if err := os.RemoveAll(filepath.Join(tc.baseDir, "staging."+manifest.TxID)); err != nil && !os.IsNotExist(err) {
		rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback remove staging dir: %w", err))
	}
	if err := os.RemoveAll(filepath.Join(tc.baseDir, "backup."+manifest.TxID)); err != nil && !os.IsNotExist(err) {
		rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback remove backup dir: %w", err))
	}
	if err := fsyncDir(tc.baseDir); err != nil {
		rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback final fsync base dir: %w", err))
	}

	if len(rollbackErrs) > 0 {
		return fmt.Errorf("rollback cleanup encountered %d errors: %w", len(rollbackErrs), errors.Join(rollbackErrs...))
	}

	return nil
}

// StartupReconcile checks for uncommitted transactions after power outage or crash.
func (tc *TransactionCoordinator) StartupReconcile(hook ProcessLifecycleHook) error {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	manifestData, err := os.ReadFile(tc.manifestPath())
	if err != nil {
		if os.IsNotExist(err) {
			tc.cleanupOrphanStagingDirs()
			return nil
		}
		return fmt.Errorf("read manifest: %w", err)
	}

	var manifest TransactionManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		// Corrupt manifest: halt managed workers, do not auto-delete
		if hook != nil {
			_ = hook.RestoreWorkerStates(map[string]WorkerProcessState{})
		}
		return fmt.Errorf("corrupt transaction manifest: %w", err)
	}

	switch manifest.Phase {
	case PhaseStaged:
		// Target files were not modified yet: clean staging & backup
		_ = os.Remove(tc.manifestPath())
		_ = os.RemoveAll(filepath.Join(tc.baseDir, "staging."+manifest.TxID))
		_ = os.RemoveAll(filepath.Join(tc.baseDir, "backup."+manifest.TxID))
		_ = fsyncDir(tc.baseDir)
		return nil

	case PhaseApplying, PhaseRollingBack:
		// Target files might be partially changed: perform rollback from snapshot
		return tc.performRollbackLocked(&manifest, hook)

	case PhaseCommitted:
		// Target files already accepted: remove journal and residual directories, do NOT rollback
		_ = os.Remove(tc.manifestPath())
		_ = os.RemoveAll(filepath.Join(tc.baseDir, "staging."+manifest.TxID))
		_ = os.RemoveAll(filepath.Join(tc.baseDir, "backup."+manifest.TxID))
		_ = fsyncDir(tc.baseDir)
		return nil

	default:
		return fmt.Errorf("unknown transaction phase: %s", manifest.Phase)
	}
}

func (tc *TransactionCoordinator) cleanupOrphanStagingDirs() {
	entries, err := os.ReadDir(tc.baseDir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-1 * time.Hour)
	for _, entry := range entries {
		name := entry.Name()
		if (len(name) > 8 && name[:8] == "staging.") || (len(name) > 7 && name[:7] == "backup.") {
			fullPath := filepath.Join(tc.baseDir, name)
			if fi, err := entry.Info(); err == nil && fi.ModTime().Before(cutoff) {
				_ = os.RemoveAll(fullPath)
			}
		}
	}
	_ = fsyncDir(tc.baseDir)
}
