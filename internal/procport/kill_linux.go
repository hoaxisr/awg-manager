//go:build linux

package procport

import (
	"time"

	"github.com/hoaxisr/awg-manager/internal/childproc"
)

func terminatePID(pid int) error {
	if err := childproc.Terminate(pid); err != nil {
		return err
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !childproc.IsAlive(pid) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return childproc.Kill(pid)
}
