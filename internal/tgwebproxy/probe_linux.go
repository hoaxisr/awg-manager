//go:build !windows

package tgwebproxy

import (
	"net"
	"syscall"
	"time"
)

func bindDeviceControl(device string) func(network, address string, c syscall.RawConn) error {
	if device == "" || device == "direct" {
		return nil
	}
	return func(network, address string, c syscall.RawConn) error {
		var opErr error
		err := c.Control(func(fd uintptr) {
			opErr = syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, device)
		})
		if err != nil {
			return err
		}
		return opErr
	}
}

func dialWithDevice(network, addr, device string, timeout time.Duration) (net.Conn, error) {
	d := &net.Dialer{
		Timeout: timeout,
		Control: bindDeviceControl(device),
	}
	return d.Dial(network, addr)
}
