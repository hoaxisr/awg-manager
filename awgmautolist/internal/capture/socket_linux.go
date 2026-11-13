//go:build linux

package capture

import (
	"fmt"
	"net"
	"syscall"
)

const ethernetAll = 0x0003

func networkOrder16(value uint16) uint16 { return value<<8 | value>>8 }

type rawPacketSocket struct {
	fd int
}

func openPacketSocket(name string) (packetSocket, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil, err
	}
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(networkOrder16(ethernetAll)))
	if err != nil {
		return nil, err
	}
	closeOnError := func(err error) (packetSocket, error) {
		_ = syscall.Close(fd)
		return nil, err
	}
	address := &syscall.SockaddrLinklayer{Protocol: networkOrder16(ethernetAll), Ifindex: iface.Index}
	if err := syscall.Bind(fd, address); err != nil {
		return closeOnError(fmt.Errorf("bind: %w", err))
	}
	if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_RCVBUF, 4<<20); err != nil {
		return closeOnError(fmt.Errorf("receive buffer: %w", err))
	}
	if err := syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &syscall.Timeval{Usec: 500_000}); err != nil {
		return closeOnError(fmt.Errorf("receive timeout: %w", err))
	}
	return &rawPacketSocket{fd: fd}, nil
}

func (s *rawPacketSocket) Read(buffer []byte) (int, error) {
	n, _, err := syscall.Recvfrom(s.fd, buffer, 0)
	if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK || err == syscall.EINTR {
		return 0, errReadTimeout
	}
	return n, err
}

func (s *rawPacketSocket) Close() error {
	if s.fd < 0 {
		return nil
	}
	err := syscall.Close(s.fd)
	s.fd = -1
	return err
}
