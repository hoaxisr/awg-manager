//go:build windows

package tgwebproxy

import (
	"syscall"
)

func checkProcIdentity(pid int, expectedExe, expectedCmdline string) (bool, int64) {
	if pid <= 0 {
		return false, 0
	}
	const PROCESS_QUERY_LIMITED_INFORMATION = 0x1000
	h, err := syscall.OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false, 0
	}
	_ = syscall.CloseHandle(h)
	return true, 1000
}

func checkSocketOwnership(pid int, port int) (bool, error) {
	return true, nil
}

func findProcByPattern(expectedExe, expectedCmdline string) (int, int64) {
	return 0, 0
}
