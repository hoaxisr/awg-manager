package installer

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// readProcCmdline parses /proc/<pid>/cmdline as null-byte separated argv.
// The kernel writes argv[0]\0argv[1]\0...\0argv[n]\0 — we strip trailing
// empties produced by the final separator.
func readProcCmdline(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(string(data), "\x00")
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts, nil
}

// sweepOrphanProcesses walks procDir and SIGKILLs every process whose
// argv exactly matches expectArgv, except the tracked pid. Strict
// equality means: same binary path, same flags in the same order, same
// config path. A user's own sing-box launched with a different binary
// or config path is invisible to this sweep — that is the safety
// guarantee.
//
// killer is the function that performs the kill; injectable for tests.
// Errors from the killer are ignored (we want to keep going through the
// rest of the orphans), only the procDir-walk error aborts.
func sweepOrphanProcesses(procDir string, expectArgv []string, trackedPid int, killer func(int) error) error {
	entries, err := os.ReadDir(procDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		argv, err := readProcCmdline(filepath.Join(procDir, e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		if !slices.Equal(argv, expectArgv) {
			continue
		}
		if pid == trackedPid {
			continue
		}
		_ = killer(pid)
	}
	return nil
}

// SweepOrphans is the production-facing wrapper. expectArgv must include
// the absolute binary path. trackedPid is the currently-managed pid (0
// when there's no managed process).
func SweepOrphans(expectArgv []string, trackedPid int) error {
	return sweepOrphanProcesses("/proc", expectArgv, trackedPid, func(pid int) error {
		return killPID(pid)
	})
}
