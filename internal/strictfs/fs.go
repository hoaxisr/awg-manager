package strictfs

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"
	"time"
)

// Outcome represents the classified outcome of a filesystem mutation when an error occurs.
type Outcome string

const (
	// OutcomeNewApplied indicates the mutation successfully applied (new content is in place),
	// but a non-fatal step (like directory fsync) may have failed.
	OutcomeNewApplied Outcome = "new_applied"

	// OutcomeOldRetained indicates the target retained its previous content or absence cleanly.
	OutcomeOldRetained Outcome = "old_retained"

	// OutcomeAmbiguous indicates the destination state could not be conclusively determined,
	// requiring fail-closed administrative recovery.
	OutcomeAmbiguous Outcome = "ambiguous"
)

var (
	txidRegex   = regexp.MustCompile(`^[0-9]{14,24}(?:-[a-zA-Z0-9_]+)?$`)
	baseRegex   = regexp.MustCompile(`^[a-zA-Z0-9_\-\.]+$`)
	failpointMu sync.RWMutex
	failpoints  = make(map[string]error)
)

// Failpoint names for fault-injection in tests.
const (
	FPBeforeAtomicRename = "strictfs.before_atomic_rename"
	FPAfterRenamePreSync = "strictfs.after_rename_pre_sync"
	FPBeforeUnlinkSync   = "strictfs.before_unlink_sync"
	FPDuringWrite        = "strictfs.during_write"
)

// SetFailpoint sets an error to trigger at a given failpoint in tests.
func SetFailpoint(name string, err error) {
	failpointMu.Lock()
	defer failpointMu.Unlock()
	failpoints[name] = err
}

// ClearFailpoints clears all configured failpoints.
func ClearFailpoints() {
	failpointMu.Lock()
	defer failpointMu.Unlock()
	failpoints = make(map[string]error)
}

func checkFailpoint(name string) error {
	failpointMu.RLock()
	defer failpointMu.RUnlock()
	return failpoints[name]
}

// ValidateTxID validates that a transaction ID conforms to ^[0-9]{14,24}$.
func ValidateTxID(txid string) error {
	if !txidRegex.MatchString(txid) {
		return fmt.Errorf("invalid transaction ID format %q: must match ^[0-9]{14,24}$", txid)
	}
	return nil
}

// ValidateBasename validates that a basename does not contain directory separators or parent traverses.
func ValidateBasename(name string) error {
	if name == "" || name == "." || name == ".." {
		return fmt.Errorf("invalid empty or traversal basename %q", name)
	}
	if filepath.Base(name) != name {
		return fmt.Errorf("basename %q contains path separators", name)
	}
	if !baseRegex.MatchString(name) {
		return fmt.Errorf("basename %q contains invalid characters", name)
	}
	return nil
}

// ComputeFileDigest calculates the SHA-256 hex digest of the file at path.
// Returns an empty string and os.ErrNotExist if the file does not exist.
func ComputeFileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("failed to hash file %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ComputeBytesDigest calculates the SHA-256 hex digest of a byte slice.
func ComputeBytesDigest(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// SyncDir performs an fsync on the given directory path to guarantee directory metadata durability.
// On non-Windows platforms, it opens the directory and calls Sync().
func SyncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open directory for sync failed on %s: %w", dir, err)
	}
	defer d.Close()

	if err := d.Sync(); err != nil {
		return fmt.Errorf("fsync directory failed on %s: %w", dir, err)
	}
	return nil
}

// SyncFile opens the file at path and calls Sync() on it to guarantee durability.
func SyncFile(path string) error {
	flag := os.O_RDONLY
	if runtime.GOOS == "windows" {
		flag = os.O_RDWR
	}
	f, err := os.OpenFile(path, flag, 0)
	if err != nil {
		return fmt.Errorf("open file for sync failed on %s: %w", path, err)
	}
	defer f.Close()

	if err := f.Sync(); err != nil {
		return fmt.Errorf("fsync file failed on %s: %w", path, err)
	}
	return nil
}

// FsyncFile is an alias for SyncFile.
func FsyncFile(path string) error {
	return SyncFile(path)
}

// FsyncDirectory is an alias for SyncDir.
func FsyncDirectory(dir string) error {
	return SyncDir(dir)
}

// StrictWriteAtomic writes data to dest atomically via a temporary file with full fsync on file and parent directory.
func StrictWriteAtomic(dest string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(dest)
	base := filepath.Base(dest)

	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return fmt.Errorf("crypto rand failed: %w", err)
	}
	tmpName := filepath.Join(dir, fmt.Sprintf(".%s.tmp.%s.%d", base, hex.EncodeToString(rnd[:]), time.Now().UnixNano()))

	if err := checkFailpoint(FPDuringWrite); err != nil {
		return err
	}

	f, err := os.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return fmt.Errorf("open tmp file %s: %w", tmpName, err)
	}

	writeErr := func() error {
		if _, err := f.Write(data); err != nil {
			return err
		}
		return f.Sync()
	}()

	closeErr := f.Close()
	if writeErr != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("write/sync tmp file %s: %w", tmpName, writeErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close tmp file %s: %w", tmpName, closeErr)
	}

	if err := checkFailpoint(FPBeforeAtomicRename); err != nil {
		_ = os.Remove(tmpName)
		return err
	}

	if err := os.Rename(tmpName, dest); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename %s to %s: %w", tmpName, dest, err)
	}

	if err := checkFailpoint(FPAfterRenamePreSync); err != nil {
		return err
	}

	if err := SyncDir(dir); err != nil {
		return fmt.Errorf("sync dir %s after atomic write: %w", dir, err)
	}

	return nil
}

// StrictRename renames src to dst and performs fsync on the destination (and source if different) parent directory.
func StrictRename(src, dst string) error {
	srcDir := filepath.Dir(src)
	dstDir := filepath.Dir(dst)

	if err := checkFailpoint(FPBeforeAtomicRename); err != nil {
		return err
	}

	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("rename %s to %s: %w", src, dst, err)
	}

	if err := checkFailpoint(FPAfterRenamePreSync); err != nil {
		return err
	}

	if err := SyncDir(dstDir); err != nil {
		return fmt.Errorf("sync dst dir %s: %w", dstDir, err)
	}
	if srcDir != dstDir {
		if err := SyncDir(srcDir); err != nil {
			return fmt.Errorf("sync src dir %s: %w", srcDir, err)
		}
	}
	return nil
}

// StrictUnlink removes path and always fsyncs the parent directory, even if path was already absent (ENOENT).
func StrictUnlink(path string) error {
	dir := filepath.Dir(path)
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", path, err)
	}

	if err := checkFailpoint(FPBeforeUnlinkSync); err != nil {
		return err
	}

	if syncErr := SyncDir(dir); syncErr != nil {
		return fmt.Errorf("sync dir %s after unlink %s: %w", dir, path, syncErr)
	}
	return nil
}

// StrictQuarantine moves an invalid, corrupt, or suspect file into quarantineDir with full durability.
func StrictQuarantine(path, quarantineDir, reason string) (string, error) {
	if err := os.MkdirAll(quarantineDir, 0700); err != nil {
		return "", fmt.Errorf("create quarantine dir %s: %w", quarantineDir, err)
	}

	base := filepath.Base(path)
	target := filepath.Join(quarantineDir, fmt.Sprintf("%s.%d.%s.quarantine", base, time.Now().UnixNano(), reason))

	if err := StrictRename(path, target); err != nil {
		return "", fmt.Errorf("quarantine rename %s to %s: %w", path, target, err)
	}
	return target, nil
}

// InspectOutcome inspects src and dst to classify the state after a failure (e.g. post-rename directory sync error).
func InspectOutcome(src, dst string, expectedOldDigest, expectedNewDigest string) (Outcome, error) {
	dstDigest, dstErr := ComputeFileDigest(dst)
	srcDigest, srcErr := ComputeFileDigest(src)

	// If destination matches expected new digest:
	if dstErr == nil && dstDigest != "" && dstDigest == expectedNewDigest {
		if os.IsNotExist(srcErr) || src == dst || srcDigest != expectedNewDigest {
			return OutcomeNewApplied, nil
		}
		return OutcomeNewApplied, nil
	}

	// If destination matches expected old digest:
	if dstErr == nil && dstDigest != "" && dstDigest == expectedOldDigest {
		return OutcomeOldRetained, nil
	}

	// If destination does not exist, check if old is retained at src or dst was absent
	if os.IsNotExist(dstErr) {
		if expectedOldDigest == "" {
			return OutcomeOldRetained, nil
		}
		if srcErr == nil && srcDigest == expectedOldDigest {
			return OutcomeOldRetained, nil
		}
	}

	return OutcomeAmbiguous, fmt.Errorf("ambiguous state: dstDigest=%q (err=%v), srcDigest=%q (err=%v), expectedOld=%q, expectedNew=%q",
		dstDigest, dstErr, srcDigest, srcErr, expectedOldDigest, expectedNewDigest)
}

// WriteAndResolve writes atomic and resolves post-rename fsync ambiguities.
func WriteAndResolve(dest string, data []byte, perm os.FileMode, expectedOldDigest string) (Outcome, error) {
	newDigest := ComputeBytesDigest(data)
	err := StrictWriteAtomic(dest, data, perm)
	if err == nil {
		return OutcomeNewApplied, nil
	}

	outcome, inspectErr := InspectOutcome("", dest, expectedOldDigest, newDigest)
	if outcome == OutcomeNewApplied {
		if syncErr := SyncDir(filepath.Dir(dest)); syncErr == nil {
			return OutcomeNewApplied, nil
		}
		return OutcomeNewApplied, fmt.Errorf("write applied but dir fsync failed: %w", err)
	}
	if outcome == OutcomeOldRetained {
		return OutcomeOldRetained, err
	}
	return OutcomeAmbiguous, errors.Join(err, inspectErr)
}

// RenameAndResolve renames src to dst and resolves post-rename fsync ambiguities.
func RenameAndResolve(src, dst string, expectedOldDigest, expectedNewDigest string) (Outcome, error) {
	err := StrictRename(src, dst)
	if err == nil {
		return OutcomeNewApplied, nil
	}

	outcome, inspectErr := InspectOutcome(src, dst, expectedOldDigest, expectedNewDigest)
	if outcome == OutcomeNewApplied {
		if syncErr := SyncDir(filepath.Dir(dst)); syncErr == nil {
			return OutcomeNewApplied, nil
		}
		return OutcomeNewApplied, fmt.Errorf("rename applied but dir fsync failed: %w", err)
	}
	if outcome == OutcomeOldRetained {
		return OutcomeOldRetained, err
	}
	return OutcomeAmbiguous, errors.Join(err, inspectErr)
}
