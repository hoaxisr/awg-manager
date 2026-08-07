//go:build linux

package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func tunFdSockPath() string {
	if compatTunFdSock == nil {
		return ""
	}
	return strings.TrimSpace(*compatTunFdSock)
}

// fdPacketConn — plain IP read/write on TUN fd (как Android VPNService fd).
type fdPacketConn struct {
	f    *os.File
	name string
}

func newFdPacketConn(f *os.File, name string) *fdPacketConn {
	return &fdPacketConn{f: f, name: name}
}

func (c *fdPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, err := c.f.Read(p)
	if err != nil {
		return 0, nil, err
	}
	return n, &net.IPAddr{IP: net.IPv4(127, 0, 0, 1)}, nil
}

func (c *fdPacketConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return c.f.Write(p)
}

func (c *fdPacketConn) Close() error                       { return c.f.Close() }
func (c *fdPacketConn) LocalAddr() net.Addr                  { return &net.IPAddr{IP: net.IPv4(127, 0, 0, 1)} }
func (c *fdPacketConn) SetDeadline(t time.Time) error      { return c.f.SetDeadline(t) }
func (c *fdPacketConn) SetReadDeadline(t time.Time) error  { return c.f.SetReadDeadline(t) }
func (c *fdPacketConn) SetWriteDeadline(t time.Time) error { return c.f.SetWriteDeadline(t) }

// recvTunFD ждёт SCM_RIGHTS на unix-socket (awg-manager → wt-client, как APK Kotlin).
func recvTunFD(ctx context.Context, sockPath string) (*os.File, error) {
	_ = os.Remove(sockPath)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", sockPath, err)
	}
	defer ln.Close()

	type res struct {
		f   *os.File
		err error
	}
	ch := make(chan res, 1)
	go func() {
		_ = ln.(*net.UnixListener).SetDeadline(time.Now().Add(90 * time.Second))
		conn, err := ln.Accept()
		if err != nil {
			ch <- res{nil, fmt.Errorf("accept: %w", err)}
			return
		}
		defer conn.Close()
		uc, ok := conn.(*net.UnixConn)
		if !ok {
			ch <- res{nil, fmt.Errorf("expected UnixConn")}
			return
		}
		f, err := recvFDFromUnixConn(uc)
		ch <- res{f, err}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		log.Printf("[RAW] TUN fd получен через %s", sockPath)
		return r.f, nil
	}
}

func recvFDFromUnixConn(uc *net.UnixConn) (*os.File, error) {
	buf := make([]byte, 32)
	oob := make([]byte, syscall.CmsgSpace(4))
	n, oobn, _, _, err := uc.ReadMsgUnix(buf, oob)
	if err != nil {
		return nil, fmt.Errorf("ReadMsgUnix: %w", err)
	}
	_ = n
	msgs, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil {
		return nil, fmt.Errorf("ParseSocketControlMessage: %w", err)
	}
	for _, m := range msgs {
		if m.Header.Level != unix.SOL_SOCKET || m.Header.Type != unix.SCM_RIGHTS {
			continue
		}
		fds, err := unix.ParseUnixRights(&m)
		if err != nil {
			return nil, err
		}
		if len(fds) == 0 {
			return nil, fmt.Errorf("SCM_RIGHTS: no fds")
		}
		return os.NewFile(uintptr(fds[0]), "tun"), nil
	}
	return nil, fmt.Errorf("SCM_RIGHTS not found")
}
