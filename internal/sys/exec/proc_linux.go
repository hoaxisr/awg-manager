//go:build linux

package exec

import (
	"os/exec"
	"syscall"
)

func setCommandProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// On deadline, kill the whole process group, not just the direct child:
	// init-script style commands (e.g. "S99zapret restart") spawn daemons that
	// inherit our stdout/stderr pipes, and Go's default Cancel (Process.Kill)
	// leaves them alive holding the pipe — cmd.Run() then blocks forever and
	// the timeout never fires.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

func killCommandProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
