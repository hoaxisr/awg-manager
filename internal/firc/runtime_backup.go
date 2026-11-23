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
)

func (i *OnDemandInstaller) rootPath(absOrSlash string) string {
	rel := strings.TrimPrefix(filepath.ToSlash(absOrSlash), "/")
	if i.layout.RootDir == "" || i.layout.RootDir == "/" {
		return "/" + rel
	}
	return filepath.Join(i.layout.RootDir, filepath.FromSlash(rel))
}

type backedEntry struct {
	RelPath    string      `json:"relPath"`
	BackupName string      `json:"backupName,omitempty"`
	SHA256     string      `json:"sha256,omitempty"`
	Size       int64       `json:"size,omitempty"`
	IsSymlink  bool        `json:"isSymlink,omitempty"`
	LinkTarget string      `json:"linkTarget,omitempty"`
	Mode       os.FileMode `json:"mode"`
}

func backupSHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func writeAndSyncBackupFile(path, relName string, data []byte, perm os.FileMode) (string, int64, error) {
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
	if err := f.Close(); err != nil {
		return "", 0, fmt.Errorf("close backup file %s: %w", relName, err)
	}
	return backupSHA256Hex(data), int64(len(data)), nil
}

func syncBackupDir(dir string) error {
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

func verifyBackedEntries(filesDir, group string, entries []backedEntry) error {
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

// RecoverPendingOperationOnStartup inspects firc-runtime-operation.json during
// manager startup before any automatic sing-box migration or FIRC daemon start.
// If a previous package operation was interrupted:
//   - For migrate_external: rolls back via the external migration backup.
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
	if !priorOp.Pending {
		_ = os.RemoveAll(i.externalBackupDir())
		return nil
	}

	switch priorOp.Action {
	case "migrate_external":
		return i.recoverExternalMigrationLocked(ctx, op, false)

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
		return i.saveOperation(runtimeOperation{Action: "uninstall", Pending: false})

	case "install", "upgrade":
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
						return i.saveOperation(runtimeOperation{Action: priorOp.Action, Pending: false})
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

func (i *OnDemandInstaller) restoreBackedEntries(backupDir string, entries []backedEntry, verify bool) error {
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

func (i *OnDemandInstaller) restoreAndVerifyUserConfigs(backupDir string, entries []backedEntry) error {
	return i.restoreBackedEntries(backupDir, entries, true)
}
