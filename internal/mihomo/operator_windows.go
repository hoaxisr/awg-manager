//go:build windows

package mihomo

import (
	"errors"
	"os"
)

// sendGracefulStop terminates the process on Windows using Process.Kill(),
// as Windows does not implement POSIX SIGTERM.
func sendGracefulStop(p *os.Process) error {
	if p == nil {
		return nil
	}
	err := p.Kill()
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
