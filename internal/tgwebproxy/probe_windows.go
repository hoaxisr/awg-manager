//go:build windows

package tgwebproxy

import (
	"net"
	"time"
)

func dialWithDevice(network, addr, device string, timeout time.Duration) (net.Conn, error) {
	d := &net.Dialer{
		Timeout: timeout,
	}
	return d.Dial(network, addr)
}
