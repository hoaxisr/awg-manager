// Package lock provides mkdir-based file locking compatible with BusyBox/Entware.
package lock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	// LockDir lives on the /var/run tmpfs: a lock must not survive a reboot
	// anyway, and the mkdir+pid+rmdir per store write used to hit the
	// Entware flash on every tunnel record update (#854).
	LockDir  = "/var/run/awg-manager/lock"
	StaleAge = 5 * time.Minute
)

var ErrLockHeld = errors.New("lock is held by another process")

// Lock represents a mkdir-based file lock.
type Lock struct {
	name    string
	path    string
	lockDir string
}

// NewWithDir creates a new lock with a custom lock directory.
func NewWithDir(name, lockDir string) *Lock {
	return &Lock{
		name:    name,
		path:    filepath.Join(lockDir, name+".lock.d"),
		lockDir: lockDir,
	}
}

// TryLock attempts to acquire the lock without blocking.
func (l *Lock) TryLock() error {
	l.cleanStale()

	if err := os.MkdirAll(filepath.Dir(l.path), 0755); err != nil {
		return fmt.Errorf("create lock parent dir: %w", err)
	}

	if err := os.Mkdir(l.path, 0755); err != nil {
		if os.IsExist(err) {
			return ErrLockHeld
		}
		return fmt.Errorf("acquire lock: %w", err)
	}

	pidFile := filepath.Join(l.path, "pid")
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0644); err != nil {
		os.RemoveAll(l.path)
		return fmt.Errorf("write lock PID: %w", err)
	}

	return nil
}

// Unlock releases the lock. A lock directory that now belongs to another process
// (our stale entry was swept and re-taken) is left alone.
func (l *Lock) Unlock() error {
	if pid, ok := l.ownerPID(); ok && pid != os.Getpid() {
		return nil
	}
	err := os.RemoveAll(l.path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("release lock: %w", err)
	}
	return nil
}

// ownerPID reads the pid file; ok is false when it is missing or unparsable.
func (l *Lock) ownerPID() (int, bool) {
	data, err := os.ReadFile(filepath.Join(l.path, "pid"))
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	return pid, err == nil
}

// cleanStale removes a lock whose owner is gone. The PID check comes first: a
// live owner keeps the lock however long it has held it (the store contract is a
// timeout for the waiter, not a takeover). Age is the fallback only for a torn
// acquire (directory without a pid file); an unparsable pid file is swept at once.
func (l *Lock) cleanStale() {
	info, err := os.Stat(l.path)
	if err != nil {
		return
	}
	pidFile := filepath.Join(l.path, "pid")
	data, err := os.ReadFile(pidFile)
	if err != nil {
		if time.Since(info.ModTime()) > StaleAge {
			os.RemoveAll(l.path)
		}
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		os.RemoveAll(l.path)
		return
	}
	if proc, err := os.FindProcess(pid); err == nil && proc.Signal(syscall.Signal(0)) == nil {
		return
	}
	os.RemoveAll(l.path)
}

// WaitLockDir is like WaitLock but uses a custom lock directory.
func WaitLockDir(name, lockDir string, timeout time.Duration) (*Lock, error) {
	l := NewWithDir(name, lockDir)
	deadline := time.Now().Add(timeout)

	for {
		if err := l.TryLock(); err == nil {
			return l, nil
		} else if !errors.Is(err, ErrLockHeld) {
			return nil, err
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("lock %q: timeout after %v", name, timeout)
		}

		time.Sleep(100 * time.Millisecond)
	}
}
