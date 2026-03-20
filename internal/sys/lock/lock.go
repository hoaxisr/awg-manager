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
	LockDir  = "/opt/var/lock/awg-manager"
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

// Unlock releases the lock.
func (l *Lock) Unlock() error {
	err := os.RemoveAll(l.path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("release lock: %w", err)
	}
	return nil
}

func (l *Lock) cleanStale() {
	info, err := os.Stat(l.path)
	if err != nil {
		return
	}

	if time.Since(info.ModTime()) > StaleAge {
		os.RemoveAll(l.path)
		return
	}

	pidFile := filepath.Join(l.path, "pid")
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		os.RemoveAll(l.path)
		return
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		os.RemoveAll(l.path)
		return
	}

	if err := proc.Signal(syscall.Signal(0)); err != nil {
		os.RemoveAll(l.path)
	}
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
