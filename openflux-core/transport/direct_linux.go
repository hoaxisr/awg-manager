//go:build linux || android

package transport

import (
	"net"
	"syscall"
	"time"
)

// tcpNotSentLowat is TCP_NOTSENT_LOWAT (linux/tcp.h): the most data the kernel
// keeps queued in a socket that has not been sent yet. Without it the send
// buffer autotunes to megabytes and everything written ahead of a fresh packet
// is latency; with it a write blocks once that much waits, and the tunnel's own
// queues (which are bounded) and the stack above feel the pressure instead.
const tcpNotSentLowat = 25

// tcpUserTimeout is TCP_USER_TIMEOUT (linux/tcp.h): how long data may stay
// unacknowledged before the kernel drops the connection.
const tcpUserTimeout = 18

// setDeadPeerTimeout closes the connection once what was sent on it has gone
// unanswered for [d]. A client that left a network (a phone off its Wi-Fi)
// leaves its connection half open: nothing closes it, the exit keeps writing
// into it and, serving one connection at a time, does not take the client's
// new one until the kernel gives the old one up - minutes of retransmission.
func setDeadPeerTimeout(c net.Conn, d time.Duration) {
	tc, ok := c.(*net.TCPConn)
	if !ok || d <= 0 {
		return
	}
	raw, err := tc.SyscallConn()
	if err != nil {
		return
	}
	_ = raw.Control(func(fd uintptr) {
		_ = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, tcpUserTimeout, int(d/time.Millisecond))
	})
}

func setNotSentLowat(c net.Conn, bytes int) {
	tc, ok := c.(*net.TCPConn)
	if !ok || bytes <= 0 {
		return
	}
	raw, err := tc.SyscallConn()
	if err != nil {
		return
	}
	_ = raw.Control(func(fd uintptr) {
		_ = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, tcpNotSentLowat, bytes)
	})
}
