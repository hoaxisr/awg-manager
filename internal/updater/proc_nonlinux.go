//go:build !linux

package updater

import osexec "os/exec"

func setUpgradeDetachedProcess(cmd *osexec.Cmd) {
	// No-op on non-Linux hosts used for local development builds.
}
