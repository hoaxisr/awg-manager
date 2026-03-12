// Package proc provides process management utilities for daemon processes.
package proc

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// PIDDir is the default directory for PID files.
const PIDDir = "/opt/var/run/awg-manager"

// PIDPath returns the full path to a PID file for the given process name.
func PIDPath(name string) string {
	return filepath.Join(PIDDir, name+".pid")
}

// ReadPID reads the PID from a PID file.
func ReadPID(pidFile string) (int, error) {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, fmt.Errorf("pid file does not exist: %s", pidFile)
		}
		return 0, fmt.Errorf("read pid file: %w", err)
	}

	pidStr := strings.TrimSpace(string(data))
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return 0, fmt.Errorf("invalid pid in file %s: %q", pidFile, pidStr)
	}

	if pid <= 0 {
		return 0, fmt.Errorf("invalid pid value: %d", pid)
	}

	return pid, nil
}

// WritePID writes a PID to a PID file.
func WritePID(pidFile string, pid int) error {
	dir := filepath.Dir(pidFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create pid directory: %w", err)
	}

	data := []byte(strconv.Itoa(pid) + "\n")
	if err := os.WriteFile(pidFile, data, 0644); err != nil {
		return fmt.Errorf("write pid file: %w", err)
	}

	return nil
}

// RemovePID removes a PID file.
func RemovePID(pidFile string) error {
	err := os.Remove(pidFile)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove pid file: %w", err)
	}
	return nil
}

// ValidatePID checks if a process with the given PID is alive and not a zombie.
func ValidatePID(pid int) bool {
	if pid <= 0 {
		return false
	}

	// Check for zombie state via /proc/PID/stat before signal check,
	// because kill(zombiePID, 0) returns success (process entry exists).
	if isZombie(pid) {
		return false
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	err = proc.Signal(syscall.Signal(0))
	return err == nil
}

// ProcessStartTime returns the start time of a process as an RFC3339 string.
// Reads field 22 (starttime) from /proc/<pid>/stat — the time the process
// started after system boot, in clock ticks. Combined with system boot time
// from /proc/stat, this gives an absolute timestamp.
func ProcessStartTime(pid int) string {
	if pid <= 0 {
		return ""
	}

	// Read process start time in clock ticks since boot
	statData, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	// Fields after last ')' — format: ") STATE field4 field5 ... field22"
	s := string(statData)
	i := strings.LastIndex(s, ")")
	if i == -1 || i+2 >= len(s) {
		return ""
	}
	fields := strings.Fields(s[i+2:])
	// field22 is starttime, index 0 = state (field3), so starttime = index 19
	if len(fields) < 20 {
		return ""
	}
	startTicks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return ""
	}

	// Read boot time from /proc/stat
	procStat, err := os.ReadFile("/proc/stat")
	if err != nil {
		return ""
	}
	var bootTime uint64
	for _, line := range strings.Split(string(procStat), "\n") {
		if strings.HasPrefix(line, "btime ") {
			parts := strings.Fields(line)
			if len(parts) == 2 {
				bootTime, _ = strconv.ParseUint(parts[1], 10, 64)
			}
			break
		}
	}
	if bootTime == 0 {
		return ""
	}

	// Clock ticks per second (sysconf(_SC_CLK_TCK), almost always 100 on Linux)
	const clockTick = 100
	startSec := bootTime + startTicks/clockTick

	return time.Unix(int64(startSec), 0).UTC().Format(time.RFC3339)
}

// isZombie checks if a process is in zombie (Z) state via /proc/PID/stat.
// Format: "PID (comm) STATE ...", STATE is a single character after the last ')'.
func isZombie(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false // can't read → not zombie (probably doesn't exist)
	}
	// Find state char after last ')' — handles comm with spaces/parens
	s := string(data)
	i := strings.LastIndex(s, ")")
	if i == -1 || i+2 >= len(s) {
		return false
	}
	return s[i+2] == 'Z'
}
