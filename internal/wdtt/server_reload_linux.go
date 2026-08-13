//go:build linux

package wdtt

import "syscall"

func signalProcessHUP(pid int) error {
	if pid <= 0 {
		return syscall.ESRCH
	}
	return syscall.Kill(pid, syscall.SIGHUP)
}
