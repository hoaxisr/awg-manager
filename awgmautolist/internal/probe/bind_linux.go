//go:build linux

package probe

import (
	"fmt"
	"net"
	"syscall"
)

// bindUDPInterface pins a UDP socket to one kernel interface with
// SO_BINDTODEVICE. Supported on Linux only; other builds return an error.
func bindUDPInterface(conn *net.UDPConn, name string) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var inner error
	if err := raw.Control(func(fd uintptr) {
		inner = syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, name)
	}); err != nil {
		return err
	}
	if inner != nil {
		return fmt.Errorf("SO_BINDTODEVICE %s: %w", name, inner)
	}
	return nil
}
