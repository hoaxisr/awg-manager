//go:build !linux

package main

import "os/exec"

func setServiceSysProcAttr(cmd *exec.Cmd) {
	// No-op on non-Linux hosts.
}
