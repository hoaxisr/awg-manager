//go:build linux

package strictfs

import (
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
)

type lockCloser struct {
	f *os.File
}

func (l *lockCloser) Close() error {
	defer l.f.Close()
	// Unlock via fcntl
	flock := unix.Flock_t{
		Type:   unix.F_UNLCK,
		Whence: int16(os.SEEK_SET),
		Start:  0,
		Len:    0,
	}
	_ = unix.FcntlFlock(l.f.Fd(), unix.F_SETLK, &flock)
	return nil
}

// LockTransaction acquires an interprocess lock for the coordinator.
// It opens transaction.lock with O_CLOEXEC and acquires an F_SETLKW fcntl lock.
// The lock file is never deleted.
func LockTransaction(dir string) (io.Closer, error) {
	lockPath := filepath.Join(dir, "transaction.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	// Ensure O_CLOEXEC is set
	unix.CloseOnExec(int(f.Fd()))

	flock := unix.Flock_t{
		Type:   unix.F_WRLCK,
		Whence: int16(os.SEEK_SET),
		Start:  0,
		Len:    0,
	}

	if err := unix.FcntlFlock(f.Fd(), unix.F_SETLKW, &flock); err != nil {
		f.Close()
		return nil, fmt.Errorf("acquire fcntl lock: %w", err)
	}

	return &lockCloser{f: f}, nil
}
