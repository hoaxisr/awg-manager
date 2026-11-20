//go:build windows

package serveringress

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"

	"github.com/hoaxisr/awg-manager/internal/childproc"
)

func (l *FileLock) Lock(txID string) error {
	l.mu.Lock()

	dir := filepath.Dir(l.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		l.mu.Unlock()
		return fmt.Errorf("mkdir lock dir: %w", err)
	}

	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		l.mu.Unlock()
		return fmt.Errorf("open lock file: %w", err)
	}

	var overlapped windows.Overlapped
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
	if err != nil {
		_ = f.Close()
		l.mu.Unlock()
		return ErrLockConflict
	}

	pid := os.Getpid()
	startTime, _ := childproc.StartTime(pid)
	owner := LockOwner{
		PID:           pid,
		StartTime:     startTime,
		TransactionID: txID,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	data, _ := json.Marshal(owner)
	_ = f.Truncate(0)
	_, _ = f.Seek(0, 0)
	_, _ = f.Write(data)
	_ = f.Sync()

	l.file = f
	l.isLocked = true
	return nil
}

func (l *FileLock) Unlock() error {
	defer l.mu.Unlock()
	if !l.isLocked || l.file == nil {
		return nil
	}

	_ = l.file.Truncate(0)
	_ = l.file.Sync()
	var overlapped windows.Overlapped
	_ = windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, &overlapped)
	_ = l.file.Close()
	l.file = nil
	l.isLocked = false
	return nil
}
