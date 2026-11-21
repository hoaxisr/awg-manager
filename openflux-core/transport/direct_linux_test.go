//go:build linux

package transport

import (
	"net"
	"syscall"
	"testing"
	"time"
)

// A direct connection gives a half-open peer up in seconds: TCP_USER_TIMEOUT is set on it.
func TestDeadPeerTimeoutIsSet(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		if c, err := ln.Accept(); err == nil {
			defer c.Close()
			time.Sleep(time.Second)
		}
	}()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	setDeadPeerTimeout(c, directDeadPeerTimeout)
	raw, err := c.(*net.TCPConn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var got int
	var gerr error
	_ = raw.Control(func(fd uintptr) { got, gerr = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_TCP, tcpUserTimeout) })
	if gerr != nil {
		t.Fatal(gerr)
	}
	if want := int(directDeadPeerTimeout / time.Millisecond); got != want {
		t.Fatalf("TCP_USER_TIMEOUT = %d ms, want %d", got, want)
	}
}
