//go:build !linux

package exec

import "os/exec"

func setCommandProcessGroup(cmd *exec.Cmd) {
	// No-op on non-Linux hosts used for local development builds.
}

func killCommandProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
