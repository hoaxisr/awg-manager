//go:build !linux

package probe

import (
	"fmt"
	"net"
)

// bindUDPInterface has no portable implementation outside Linux.
func bindUDPInterface(*net.UDPConn, string) error {
	return fmt.Errorf("interface binding is supported only on Linux")
}
