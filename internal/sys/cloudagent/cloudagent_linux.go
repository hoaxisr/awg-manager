//go:build linux

package cloudagent

import (
	"context"
	"errors"
	"syscall"
)

func reset(ctx context.Context) (int, error) {
	return resetWith(ctx, "/proc", func(pid int) error {
		// SIGTERM triggers immediate clean respawn by parent ndm (observed ~19s to ROUTINE
		// vs ~5-6m waiting on stale coalagent data UDP sockets; SIGHUP only reloads config).
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		return nil
	})
}
