//go:build linux

package router

import (
	"errors"
	"os"
	"syscall"
)

func lockNetfilterHookFile(lockPath string) func() {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return func() {}
	}
	for {
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
}
