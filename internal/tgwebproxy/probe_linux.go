//go:build !windows

package tgwebproxy

import (
	"net"
	"syscall"
	"time"
)

func dialWithDevice(network, addr, device string, timeout time.Duration) (net.Conn, error) {
	d := &net.Dialer{
		Timeout: timeout,
	}
	if device != "" {
		d.Control = func(network, address string, c syscall.RawConn) error {
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
	return d.Dial(network, addr)
}
