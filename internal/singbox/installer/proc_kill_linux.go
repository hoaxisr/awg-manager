//go:build linux

package installer

import "syscall"

func killPID(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}
