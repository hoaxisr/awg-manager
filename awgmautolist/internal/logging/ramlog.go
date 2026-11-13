// Package logging implements the single bounded log file in router RAM.
package logging

import (
	"fmt"
	"os"
	"sync"
)

const MaxLogBytes int64 = 5 << 20

var truncationMarker = []byte("awgmautolist: log reached 5 MiB; truncated without rotation\n")

// RAMLog is an io.Writer that truncates the same file at 5 MiB. It never
// creates .old or numbered copies.
type RAMLog struct {
	mu   sync.Mutex
	file *os.File
	size int64
}

func OpenRAMLog(path string) (*RAMLog, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	stat, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if _, err := file.Seek(0, 2); err != nil {
		file.Close()
		return nil, err
	}
	return &RAMLog{file: file, size: stat.Size()}, nil
}

func (w *RAMLog) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return 0, os.ErrClosed
	}
	originalLen := len(p)
	if originalLen == 0 {
		return 0, nil
	}
	if w.size+int64(len(p)) <= MaxLogBytes {
		n, err := w.file.Write(p)
		w.size += int64(n)
		return n, err
	}
	if err := w.truncateLocked(); err != nil {
		return 0, err
	}
	remaining := MaxLogBytes - w.size
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	if err != nil {
		return n, err
	}
	// The complete input was accepted for logging, although its tail was
	// dropped to preserve the hard file-size bound.
	return originalLen, nil
}

func (w *RAMLog) truncateLocked() error {
	if err := w.file.Truncate(0); err != nil {
		return err
	}
	if _, err := w.file.Seek(0, 0); err != nil {
		return err
	}
	w.size = 0
	n, err := w.file.Write(truncationMarker)
	w.size = int64(n)
	return err
}

func (w *RAMLog) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func (w *RAMLog) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return "closed RAM log"
	}
	return fmt.Sprintf("RAM log %s (%d bytes)", w.file.Name(), w.size)
}
