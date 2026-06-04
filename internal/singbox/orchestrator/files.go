package orchestrator

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// activePath returns the path where the slot's file lives when enabled.
func (o *Orchestrator) activePath(meta SlotMeta) string {
	return filepath.Join(o.configDir, meta.Filename)
}

// disabledPath returns the path where the slot's file lives when disabled.
func (o *Orchestrator) disabledPath(meta SlotMeta) string {
	return filepath.Join(o.configDir, disabledSubdir, meta.Filename)
}

// pendingDir is the subdirectory holding draft slot files. Sing-box's
// non-recursive -C skips it; orchestrator writes pending files here
// during staging and renames them into the active location on Apply.
func (o *Orchestrator) pendingDir() string {
	return filepath.Join(o.configDir, "pending")
}

// pendingPath returns where the pending (draft) copy of a slot lives.
func (o *Orchestrator) pendingPath(meta SlotMeta) string {
	return filepath.Join(o.pendingDir(), meta.Filename)
}

// ensureDirs creates configDir and configDir/disabled if missing.
func (o *Orchestrator) ensureDirs() error {
	if err := os.MkdirAll(o.configDir, 0755); err != nil {
		return fmt.Errorf("mkdir configDir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(o.configDir, disabledSubdir), 0755); err != nil {
		return fmt.Errorf("mkdir disabledSubdir: %w", err)
	}
	if err := os.MkdirAll(o.pendingDir(), 0755); err != nil {
		return fmt.Errorf("ensureDirs pending: %w", err)
	}
	return nil
}

// writeAtomic writes data to path atomically (unique temp + rename) via
// storage.AtomicWrite, so a crash or concurrent writer can't leave a partial
// or collided file.
func writeAtomic(path string, data []byte) error {
	return storage.AtomicWrite(path, data)
}

// fileExists returns true iff path exists and is a regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular()
}

// fileSize returns the size in bytes if the file exists, 0 otherwise.
func fileSize(path string) int {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return int(info.Size())
}

// scanDirForSlot returns (existsActive, existsDisabled).
func (o *Orchestrator) scanDirForSlot(meta SlotMeta) (bool, bool) {
	a := fileExists(o.activePath(meta))
	d := fileExists(o.disabledPath(meta))
	return a, d
}

// renameForToggle moves the slot file between active and disabled
// locations. No-op if already in target location. Returns nil if the
// slot has no file on disk (Save will create it later).
func (o *Orchestrator) renameForToggle(meta SlotMeta, enable bool) error {
	var src, dst string
	if enable {
		src = o.disabledPath(meta)
		dst = o.activePath(meta)
	} else {
		src = o.activePath(meta)
		dst = o.disabledPath(meta)
	}
	if _, err := os.Stat(src); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if _, err := os.Stat(dst); err == nil {
		// Both exist — pathological. Prefer src as truth, remove dst.
		if err := os.Remove(dst); err != nil {
			return err
		}
	}
	return os.Rename(src, dst)
}

// removeIfExists removes path; nil if missing. Other errors propagate.
func removeIfExists(path string) error {
	err := os.Remove(path)
	if err == nil {
		return nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
