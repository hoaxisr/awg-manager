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
