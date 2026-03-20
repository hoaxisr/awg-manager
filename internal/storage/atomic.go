package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const FilePermission = 0644
const DirPermission = 0755

// AtomicWrite writes data to path atomically using temp file + rename.
func AtomicWrite(path string, data []byte) error {
	return AtomicWritePerm(path, data, FilePermission)
}

// AtomicWritePerm is like AtomicWrite but with custom file permissions.
func AtomicWritePerm(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)

	if err := os.MkdirAll(dir, DirPermission); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}

	tmpPath := fmt.Sprintf("%s.tmp.%d.%d", path, os.Getpid(), time.Now().UnixNano())

	if err := os.WriteFile(tmpPath, data, perm); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename temp to target: %w", err)
	}

	return nil
}
