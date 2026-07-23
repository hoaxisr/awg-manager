//go:build linux

package wdtt

import (
	"os/exec"
	"syscall"
)

func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func terminate(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

func kill(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}

func isAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
