//go:build linux

package singbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func setCandidateParentDeath(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}

// Verify the listener belongs to this exact, unreaped process. Reserving then
// closing an ephemeral port alone leaves a port-reuse race before child bind.
func candidateOwnsPort(pid, port int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/net/tcp", pid))
	if err != nil {
		return false
	}
	address := fmt.Sprintf("0100007F:%04X", port)
	inodes := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 9 && fields[1] == address && fields[3] == "0A" {
			inodes["socket:["+fields[9]+"]"] = true
		}
	}
	fds, _ := filepath.Glob(fmt.Sprintf("/proc/%d/fd/*", pid))
	for _, fd := range fds {
		link, _ := os.Readlink(fd)
		if inodes[link] {
			return true
		}
	}
	return false
}
