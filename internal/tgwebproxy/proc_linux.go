//go:build !windows

package tgwebproxy

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// parseProcStat extracts field 22 (starttime) by locating the closing parenthesis of comm.
func parseProcStat(statStr string) (int64, error) {
	idx := strings.LastIndex(statStr, ")")
	if idx == -1 || idx+2 >= len(statStr) {
		return 0, errors.New("invalid stat format")
	}
	fields := strings.Fields(statStr[idx+2:])
	// Field 3 (state) is index 0
	// Field 22 (starttime) is index 19 (22 - 3 = 19)
	if len(fields) < 20 {
		return 0, errors.New("not enough fields in stat")
	}
	return strconv.ParseInt(fields[19], 10, 64)
}

// checkProcIdentity verifies that /proc/<pid> matches expected exe and cmdline.
func checkProcIdentity(pid int, expectedExe, expectedCmdline string) (bool, int64) {
	if pid <= 0 {
		return false, 0
	}

	// 1. Check exe
	exePath, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	if err != nil {
		return false, 0
	}
	if filepath.Base(exePath) != expectedExe && !strings.Contains(exePath, expectedExe) {
		return false, 0
	}

	// 2. Check cmdline
	cmdlineBytes, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return false, 0
	}
	// cmdline arguments are null-byte separated
	cmdline := string(bytes.ReplaceAll(cmdlineBytes, []byte{0}, []byte{' '}))
	if expectedCmdline != "" && !strings.Contains(cmdline, expectedCmdline) {
		return false, 0
	}

	// 3. Read start time from /proc/<pid>/stat (field 22)
	var startTime int64
	if statBytes, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil {
		if st, err := parseProcStat(string(statBytes)); err == nil {
			startTime = st
		}
	}

	return true, startTime
}

// checkSocketOwnership verifies that pid owns a listening socket on port.
func checkSocketOwnership(pid int, port int) (bool, error) {
	if pid <= 0 || port <= 0 {
		return false, errors.New("invalid pid or port")
	}

	fdDir := fmt.Sprintf("/proc/%d/fd", pid)
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return false, fmt.Errorf("readlink fd dir error: %w", err)
	}

	inodes := make(map[string]bool)
	for _, e := range entries {
		link, err := os.Readlink(filepath.Join(fdDir, e.Name()))
		if err == nil && strings.HasPrefix(link, "socket:[") {
			inode := strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")
			inodes[inode] = true
		}
	}

	if len(inodes) == 0 {
		return false, errors.New("no listening socket found for pid")
	}

	hexPort := fmt.Sprintf("%04X", port)
	for _, procPath := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := os.ReadFile(procPath)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 10 {
				localAddr := fields[1]
				state := fields[3]
				inode := fields[9]
				if state == "0A" && strings.HasSuffix(strings.ToUpper(localAddr), ":"+hexPort) {
					if inodes[inode] {
						return true, nil
					}
				}
			}
		}
	}
	return false, nil
}

func findProcByPattern(expectedExe, expectedCmdline string) (int, int64) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, 0
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		if ok, startTime := checkProcIdentity(pid, expectedExe, expectedCmdline); ok {
			return pid, startTime
		}
	}
	return 0, 0
}
