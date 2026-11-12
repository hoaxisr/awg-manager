//go:build !windows

package warpscout

import (
	"os"
	"syscall"
)

func terminate(p *os.Process) error {
	if p == nil {
		return nil
	}
	return p.Signal(syscall.SIGTERM)
}
