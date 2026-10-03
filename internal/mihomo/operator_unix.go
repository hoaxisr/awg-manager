//go:build !windows

package mihomo

import (
	"errors"
	"os"
	"syscall"
)

// sendGracefulStop sends SIGTERM to the process on Unix systems,
// allowing it to gracefully close connections and release resources.
func sendGracefulStop(p *os.Process) error {
	if p == nil {
		return nil
	}
	err := p.Signal(syscall.SIGTERM)
	if err != nil {
		if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	return nil
}
