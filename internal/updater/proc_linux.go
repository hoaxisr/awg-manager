//go:build linux

package updater

import (
	osexec "os/exec"
	"syscall"
)

func setUpgradeDetachedProcess(cmd *osexec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
