//go:build linux

package childproc

import (
	"os/exec"
	"syscall"
)

// SetProcessGroup detaches the child into its own session so a SIGKILL to
// the whole group can clean up any helpers it spawns (mirrors the
// Setsid:true used by internal/singbox.Process).
func SetProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// Terminate sends SIGTERM (graceful stop).
func Terminate(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

// Kill sends SIGKILL (forced stop after the grace period).
func Kill(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}

// Signal delivers a Unix signal to the process (Linux router target).
func Signal(pid int, sig syscall.Signal) error {
	return syscall.Kill(pid, sig)
}

// IsAlive probes process existence with signal 0 (sends nothing, just
// checks the kernel still knows the PID).
func IsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
