//go:build !windows

package serveringress

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

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

	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		_ = f.Close()
		l.mu.Unlock()
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return ErrLockConflict
		}
		return fmt.Errorf("flock: %w", err)
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
	err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	_ = l.file.Close()
	l.file = nil
	l.isLocked = false
	if err != nil {
		return fmt.Errorf("flock unlock: %w", err)
	}
	return nil
}
