//go:build !linux

package uplink

import "syscall"

func strictDeviceControl(string) func(string, string, syscall.RawConn) error {
	return func(string, string, syscall.RawConn) error { return ErrUnsupported }
}
