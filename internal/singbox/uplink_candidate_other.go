//go:build !linux

package singbox

import "os/exec"

func setCandidateParentDeath(*exec.Cmd) {}
func candidateOwnsPort(int, int) bool   { return false }
