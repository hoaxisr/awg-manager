package hydraroute

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

const (
	hrneoBinary = "/opt/bin/hrneo"
	neoCommand  = "/opt/bin/neo"
	pidFile     = "/var/run/hrneo.pid"
)

// Detect checks if HydraRoute Neo is installed and running.
func Detect() Status {
	var s Status

	if _, err := os.Stat(hrneoBinary); err == nil {
		s.Installed = true
	}

	if !s.Installed {
		return s
	}

	raw, err := os.ReadFile(pidFile)
	if err != nil {
		return s
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return s
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return s
	}

	if err := proc.Signal(syscall.Signal(0)); err == nil {
		s.Running = true
	}

	return s
}
