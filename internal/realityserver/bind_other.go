//go:build !linux

package realityserver

import "syscall"

func bindToDeviceControl(iface string) func(network, address string, c syscall.RawConn) error {
	return nil
}

func sockMarkControl(mark int) func(network, address string, c syscall.RawConn) error {
	return nil
}
