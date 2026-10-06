//go:build !linux

package strictfs

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type nopCloser struct {
	f *os.File
}

func (n *nopCloser) Close() error {
	return n.f.Close()
}

// LockTransaction acquires an interprocess lock for the coordinator.
func LockTransaction(dir string) (io.Closer, error) {
	lockPath := filepath.Join(dir, "transaction.lock")
	// On Windows, we just open with exclusive share mode (which Go doesn't easily expose cleanly without syscalls,
	// but we just use simple create/open for local testing).
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open lock: %w", err)
	}
	return &nopCloser{f: f}, nil
}
