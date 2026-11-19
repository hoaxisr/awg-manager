//go:build linux

package realityserver

import (
	"syscall"
)

func bindToDeviceControl(iface string) func(network, address string, c syscall.RawConn) error {
	if iface == "" {
		return nil
	}
	return func(network, address string, c syscall.RawConn) error {
		var sockErr error
		err := c.Control(func(fd uintptr) {
			sockErr = syscall.BindToDevice(int(fd), iface)
		})
		if err != nil {
			return err
		}
		return sockErr
	}
}

func sockMarkControl(mark int) func(network, address string, c syscall.RawConn) error {
	if mark == 0 {
		return nil
	}
	return func(network, address string, c syscall.RawConn) error {
		var sockErr error
		err := c.Control(func(fd uintptr) {
			sockErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, mark)
		})
		if err != nil {
			return err
		}
		return sockErr
	}
}
