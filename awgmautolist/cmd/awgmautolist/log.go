package main

import (
	"os"
	"sync"
)

const maxLogBytes = 1 << 20

type rotatingLog struct {
	mu   sync.Mutex
	path string
	file *os.File
}

func openRotatingLog(path string) (*rotatingLog, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &rotatingLog{path: path, file: file}, nil
}

func (w *rotatingLog) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return 0, os.ErrClosed
	}
	st, err := w.file.Stat()
	if err != nil {
		return 0, err
	}
	if st.Size() > 0 && st.Size()+int64(len(p)) > maxLogBytes {
		if err := w.file.Close(); err != nil {
			return 0, err
		}
		w.file = nil
		_ = os.Remove(w.path + ".old")
		if err := os.Rename(w.path, w.path+".old"); err != nil {
			return 0, err
		}
		file, err := os.OpenFile(w.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return 0, err
		}
		w.file = file
	}
	return w.file.Write(p)
}

func (w *rotatingLog) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}
