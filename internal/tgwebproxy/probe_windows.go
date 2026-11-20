//go:build windows

package tgwebproxy

import (
	"net"
	"syscall"
	"time"
)

func bindDeviceControl(device string) func(network, address string, c syscall.RawConn) error {
	return nil
}

func dialWithDevice(network, addr, device string, timeout time.Duration) (net.Conn, error) {
	d := &net.Dialer{
		Timeout: timeout,
	}
	return d.Dial(network, addr)
}
