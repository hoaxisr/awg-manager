//go:build linux

package mihomo

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

const (
	staleProcessTermGrace = 750 * time.Millisecond
	staleProcessKillGrace = 750 * time.Millisecond
	staleProcessPoll      = 25 * time.Millisecond
)

// cleanupStaleManagedProcesses removes only orphaned processes with the exact
// argv emitted by Operator.Start. This covers the first upgrade from releases
// whose daemon shutdown did not reap Mihomo, without touching other Mihomo
// instances using another binary, config directory, or command-line option.
func cleanupStaleManagedProcesses(binaryPath, configDir string) error {
	pids, err := findManagedDaemonPIDs("/proc", binaryPath, configDir, os.Getpid())
	if err != nil || len(pids) == 0 {
		return err
	}

	var signalErrs []error
	for _, pid := range pids {
		if !managedDaemonStillMatches("/proc", pid, binaryPath, configDir) {
			continue
		}
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			signalErrs = append(signalErrs, fmt.Errorf("terminate stale Mihomo pid %d: %w", pid, err))
		}
	}

	remaining := waitForManagedDaemons("/proc", pids, binaryPath, configDir, staleProcessTermGrace)
	for _, pid := range remaining {
		if !managedDaemonStillMatches("/proc", pid, binaryPath, configDir) {
			continue
		}
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			signalErrs = append(signalErrs, fmt.Errorf("kill stale Mihomo pid %d: %w", pid, err))
		}
	}

	remaining = waitForManagedDaemons("/proc", remaining, binaryPath, configDir, staleProcessKillGrace)
	if len(remaining) > 0 {
		signalErrs = append(signalErrs, fmt.Errorf("stale Mihomo process still running: pid %v", remaining))
	}
	return errors.Join(signalErrs...)
}

func findManagedDaemonPIDs(procRoot, binaryPath, configDir string, selfPID int) ([]int, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, fmt.Errorf("scan stale Mihomo processes: %w", err)
	}
	pids := make([]int, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || pid == selfPID {
			continue
		}
		if managedDaemonStillMatches(procRoot, pid, binaryPath, configDir) {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

func managedDaemonStillMatches(procRoot string, pid int, binaryPath, configDir string) bool {
	cmdline, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "cmdline"))
	return err == nil && isManagedDaemonCmdline(cmdline, binaryPath, configDir)
}

func waitForManagedDaemons(procRoot string, pids []int, binaryPath, configDir string, timeout time.Duration) []int {
	deadline := time.Now().Add(timeout)
	for {
		remaining := pids[:0]
		for _, pid := range pids {
			if managedDaemonStillMatches(procRoot, pid, binaryPath, configDir) {
				remaining = append(remaining, pid)
			}
		}
		if len(remaining) == 0 || !time.Now().Before(deadline) {
			return remaining
		}
		pids = remaining
		time.Sleep(staleProcessPoll)
	}
}
