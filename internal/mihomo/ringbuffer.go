package mihomo

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// BoundedRingBuffer is a thread-safe, memory-bounded buffer that stores the
// most recent capacity bytes written to it. It implements io.Writer.
type BoundedRingBuffer struct {
	mu       sync.Mutex
	buf      []byte
	capacity int
}

// NewBoundedRingBuffer creates a buffer that retains at most capacity bytes in memory.
func NewBoundedRingBuffer(capacity int) *BoundedRingBuffer {
	if capacity <= 0 {
		capacity = 64 * 1024 // 64 KiB default
	}
	return &BoundedRingBuffer{
		buf:      make([]byte, 0, capacity),
		capacity: capacity,
	}
}

// Write appends p to the buffer, dropping the oldest bytes if capacity is exceeded.
func (r *BoundedRingBuffer) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	n := len(p)
	if len(p) >= r.capacity {
		// Retain only the trailing capacity bytes of p
		tail := p[len(p)-r.capacity:]
		if cap(r.buf) < r.capacity {
			r.buf = make([]byte, r.capacity)
		} else {
			r.buf = r.buf[:r.capacity]
		}
		copy(r.buf, tail)
		return n, nil
	}

	total := len(r.buf) + len(p)
	if total <= r.capacity {
		r.buf = append(r.buf, p...)
	} else {
		overflow := total - r.capacity
		// Shift remaining bytes left
		copy(r.buf, r.buf[overflow:])
		r.buf = r.buf[:len(r.buf)-overflow]
		r.buf = append(r.buf, p...)
	}
	return n, nil
}

// String returns a copy of the buffered bytes as a string.
func (r *BoundedRingBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}

// Bytes returns a copy of the buffered bytes.
func (r *BoundedRingBuffer) Bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	res := make([]byte, len(r.buf))
	copy(res, r.buf)
	return res
}

// Len returns the current length of stored bytes.
func (r *BoundedRingBuffer) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.buf)
}

// Cap returns the maximum capacity of the buffer.
func (r *BoundedRingBuffer) Cap() int {
	return r.capacity
}

// Reset clears the buffer.
func (r *BoundedRingBuffer) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = r.buf[:0]
}

// RotatingLogWriter is an io.WriteCloser that writes to a log file,
// rotating it when it exceeds maxSize bytes. It retains at most 1 backup file (.1),
// guaranteeing that total disk consumption never exceeds 2 * maxSize.
type RotatingLogWriter struct {
	mu       sync.Mutex
	filePath string
	maxSize  int64
	file     *os.File
	curSize  int64
}

// NewRotatingLogWriter creates or opens the log file with size-based rotation.
func NewRotatingLogWriter(filePath string, maxSize int64) (*RotatingLogWriter, error) {
	if maxSize <= 0 {
		maxSize = 512 * 1024 // 512 KiB default
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return nil, err
	}

	// Rotate on startup if existing log file is already oversized
	if fi, err := os.Stat(filePath); err == nil {
		if fi.Size() >= maxSize {
			_ = os.Remove(filePath + ".1")
			_ = os.Rename(filePath, filePath+".1")
		}
	}

	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}

	var curSize int64
	if fi, err := f.Stat(); err == nil {
		curSize = fi.Size()
	}

	return &RotatingLogWriter{
		filePath: filePath,
		maxSize:  maxSize,
		file:     f,
		curSize:  curSize,
	}, nil
}

// Write writes data to the current log file, rotating if the write exceeds maxSize.
func (w *RotatingLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return 0, errors.New("rotating log writer closed")
	}

	if w.curSize+int64(len(p)) > w.maxSize {
		_ = w.file.Close()
		_ = os.Remove(w.filePath + ".1")
		_ = os.Rename(w.filePath, w.filePath+".1")
		newFile, err := os.OpenFile(w.filePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			w.file = nil
			return 0, err
		}
		w.file = newFile
		w.curSize = 0
	}

	n, err := w.file.Write(p)
	w.curSize += int64(n)
	return n, err
}

// Close closes the underlying log file.
func (w *RotatingLogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		err := w.file.Close()
		w.file = nil
		return err
	}
	return nil
}

var _ io.WriteCloser = (*RotatingLogWriter)(nil)
