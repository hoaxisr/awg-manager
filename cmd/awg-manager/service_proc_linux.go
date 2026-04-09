//go:build linux

package main

import (
	"os/exec"
	"syscall"
)

func setServiceSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
