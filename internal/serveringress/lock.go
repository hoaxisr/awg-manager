package serveringress

import (
	"errors"
	"os"
	"sync"
)

var ErrLockConflict = errors.New("server ingress lock held by another process")

type IngressLocker interface {
	Lock(txID string) error
	Unlock() error
}

type FileLock struct {
	mu       sync.Mutex
	path     string
	file     *os.File
	isLocked bool
}

func NewFileLock(path string) *FileLock {
	return &FileLock{
		path: path,
	}
}

type LockOwner struct {
	PID           int    `json:"pid"`
	StartTime     uint64 `json:"start_time,omitempty"`
	TransactionID string `json:"transaction_id,omitempty"`
	CreatedAt     string `json:"created_at"`
}
