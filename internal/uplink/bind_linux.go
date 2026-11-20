//go:build linux

package uplink

import (
	"golang.org/x/sys/unix"
	"syscall"
)

func strictDeviceControl(device string) func(string, string, syscall.RawConn) error {
	return func(_, _ string, c syscall.RawConn) error {
		var sockErr error
		err := c.Control(func(fd uintptr) {
			sockErr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, device)
		})
		if err != nil {
			return err
		}
		return sockErr
	}
}
