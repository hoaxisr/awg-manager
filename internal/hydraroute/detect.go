package hydraroute

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

var (
	hrneoBinary = "/opt/bin/hrneo"           //nolint:gochecknoglobals
	neoCommand  = "/opt/bin/neo"             //nolint:gochecknoglobals
	hrneoInit   = "/opt/etc/init.d/S99hrneo" //nolint:gochecknoglobals
	pidFile     = "/var/run/hrneo.pid"       //nolint:gochecknoglobals
)

// neoControlCommand returns the command used to start/stop/restart the daemon.
// The normal path is /opt/bin/neo, a postinst symlink to S99hrneo. When that
// symlink is missing (manual install, cleanup, interrupted upgrade) the init
// script accepts the same arguments, so fall back to it instead of failing
// with ENOENT while the status still says installed/running.
func neoControlCommand() string {
	if _, err := os.Stat(neoCommand); err == nil {
		return neoCommand
	}
	if _, err := os.Stat(hrneoInit); err == nil {
		return hrneoInit
	}
	return neoCommand
}

// Detect checks if HydraRoute Neo is installed and running.
func Detect() Status {
	s := Status{
		ProcessState: StateNotInstalled,
	}

	if _, err := os.Stat(hrneoBinary); err == nil {
		s.Installed = true
	}

	if !s.Installed {
		return s
	}
	s.ProcessState = StateStopped

	raw, err := os.ReadFile(pidFile)
	if err != nil {
		return s
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		s.ProcessState = StateDead
		return s
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		s.ProcessState = StateDead
		s.StalePID = pid
		return s
	}

	if err := proc.Signal(syscall.Signal(0)); err == nil {
		s.Running = true
		s.PID = pid
		s.ProcessState = StateRunning
		return s
	}
	s.ProcessState = StateDead
	s.StalePID = pid

	return s
}
