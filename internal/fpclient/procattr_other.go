//go:build !linux

package fpclient

import "os/exec"

func setParentDeath(*exec.Cmd) {}
