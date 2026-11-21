//go:build windows

package netbind

import (
	"errors"
	"fmt"
	"net"
	"syscall"

	"golang.org/x/sys/windows"
)

// IP_UNICAST_IF / IPV6_UNICAST_IF (ws2ipdef.h).
const (
	ipUnicastIf   = 31
	ipv6UnicastIf = 31
)

// BindDefault binds every new socket of the core to the interface that
// carries the default route now, before anything moves it. Returns that
// interface's index.
func BindDefault() (uint32, error) {
	var index uint32
	sa := &windows.SockaddrInet4{Addr: [4]byte{8, 8, 8, 8}}
	if err := windows.GetBestInterfaceEx(sa, &index); err != nil {
		return 0, fmt.Errorf("нет маршрута в интернет: %w", err)
	}
	if index == 0 {
		return 0, errors.New("нет маршрута в интернет")
	}
	BindIndex(index)
	return index, nil
}

var boundIndex uint32

// Index is the interface sockets are bound to, 0 when none.
func Index() uint32 { return boundIndex }

// BindIndex binds new sockets to interface index.
func BindIndex(index uint32) {
	boundIndex = index
	install(func(network, address string, c syscall.RawConn) error {
		// Loopback (the IPC socket, local proxies) must stay where it is.
		if host, _, err := net.SplitHostPort(address); err == nil {
			if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
				return nil
			}
		}
		var err error
		cerr := c.Control(func(fd uintptr) {
			h := windows.Handle(fd)
			// The IPv4 option takes the index in network byte order.
			be := int(index>>24 | (index>>8)&0xff00 | (index<<8)&0xff0000 | index<<24)
			err4 := windows.SetsockoptInt(h, windows.IPPROTO_IP, ipUnicastIf, be)
			err6 := windows.SetsockoptInt(h, windows.IPPROTO_IPV6, ipv6UnicastIf, int(index))
			// A socket takes one of the two, depending on its family.
			if err4 != nil && err6 != nil {
				err = fmt.Errorf("привязка к интерфейсу %d: %v", index, err4)
			}
		})
		if cerr != nil {
			return cerr
		}
		return err
	})
}
