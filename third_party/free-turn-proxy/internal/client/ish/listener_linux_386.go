//go:build linux && 386

package ish

import (
	"io"
	"net"
	"os"
	"syscall"
	"time"
	"unsafe"
)

type listener struct {
	net.Listener
	f  *os.File
	fd int
}

// WrapListener подменяет стандартный net.Listener на legacy-syscall listener,
// заточенный под iSH-симулятор на iOS, где нет современного `accept4`.
func WrapListener(ln net.Listener) (net.Listener, error) {
	tl, ok := ln.(*net.TCPListener)
	if !ok {
		return ln, nil
	}
	f, err := tl.File()
	if err != nil {
		return nil, err
	}

	// держим ссылку на *os.File, чтобы GC не закрыл FD.
	return &listener{Listener: ln, f: f, fd: int(f.Fd())}, nil
}

func (l *listener) Accept() (net.Conn, error) {
	// ставим listener-сокет в blocking. Go по умолчанию делает его non-blocking.
	// Это избавляет от time.Sleep в spin-loop, который триггерит futex_time64 SIGSYS
	// в современном Go на iSH.
	if err := syscall.SetNonblock(l.fd, false); err != nil {
		return nil, err
	}

	for {
		addr := make([]byte, 128)
		addrlen := uintptr(128)

		// i386 сетевые syscall'ы мультиплексируются через socketcall (102).
		// SYS_ACCEPT - subcall 5.
		args := [3]uintptr{uintptr(l.fd), uintptr(unsafe.Pointer(&addr[0])), uintptr(unsafe.Pointer(&addrlen))}

		// Syscall6 - чтобы хватило регистров аргументов на этой платформе.
		r1, _, errno := syscall.Syscall6(102, 5, uintptr(unsafe.Pointer(&args)), 0, 0, 0, 0)
		if errno != 0 {
			if errno == syscall.EINTR {
				continue
			}
			return nil, errno
		}

		nfd := int(r1)
		_ = syscall.SetsockoptInt(nfd, syscall.IPPROTO_TCP, syscall.TCP_NODELAY, 1)
		_ = syscall.SetsockoptInt(nfd, syscall.SOL_SOCKET, syscall.SO_RCVBUF, 256*1024)
		_ = syscall.SetsockoptInt(nfd, syscall.SOL_SOCKET, syscall.SO_SNDBUF, 256*1024)

		// избегаем net.FileConn - она регистрирует fd в Go epoll poller'е, что в
		// iSH стабильно падает с EEXIST. Возвращаем кастомный blocking net.Conn.
		conn := &ishConn{fd: nfd}
		return conn, nil
	}
}

func (l *listener) Close() error {
	// закрываем и дублированный FD, и оригинальный listener.
	err1 := l.f.Close()
	err2 := l.Listener.Close()
	if err1 != nil {
		return err1
	}
	return err2
}

// ishConn обходит сетевой poller Go, чтобы не словить EEXIST в iSH.
type ishConn struct {
	fd int
}

func (c *ishConn) Read(b []byte) (n int, err error) {
	for {
		n, err = syscall.Read(c.fd, b)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return n, err
		}
		if n == 0 {
			return 0, os.ErrClosed
		}
		return n, nil
	}
}

func (c *ishConn) Write(b []byte) (n int, err error) {
	for n < len(b) {
		written, writeErr := syscall.Write(c.fd, b[n:])
		if writeErr == syscall.EINTR {
			continue
		}
		if writeErr != nil {
			return n, writeErr
		}
		if written == 0 {
			return n, io.ErrShortWrite
		}
		n += written
	}
	return n, nil
}

func (c *ishConn) Close() error {
	return syscall.Close(c.fd)
}

func (c *ishConn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9000}
}

func (c *ishConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0}
}

func (c *ishConn) SetDeadline(t time.Time) error      { return nil }
func (c *ishConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *ishConn) SetWriteDeadline(t time.Time) error { return nil }
