//go:build linux

package terminal

import (
	"os"
	"os/exec"
	"syscall"
)

func setTerminalSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateProcess(proc *os.Process) error {
	return proc.Signal(syscall.SIGTERM)
}
