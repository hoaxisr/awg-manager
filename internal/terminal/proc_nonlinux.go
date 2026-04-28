//go:build !linux

package terminal

import (
	"os"
	"os/exec"
)

func setTerminalSysProcAttr(cmd *exec.Cmd) {
	// No-op on non-Linux hosts.
}

func terminateProcess(proc *os.Process) error {
	return proc.Kill()
}

// killOrphanTtyd is a no-op on non-Linux — orphan-cleanup is router-only
// (Linux + /proc) and dev hosts (macOS/etc) don't run ttyd anyway.
func killOrphanTtyd() []int {
	return nil
}
