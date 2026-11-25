package probe

import (
	"net"
	"sync"
	"syscall"
	"testing"
)

// closedPort returns a port that refuses connections until the test ends: the
// socket stays bound but never listens, so nobody else can take the number.
// Listen(":0") + Close would leave it to any parallel test process.
func closedPort(t *testing.T) int {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Close(fd) })
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		t.Fatal(err)
	}
	sa, err := syscall.Getsockname(fd)
	if err != nil {
		t.Fatal(err)
	}
	return sa.(*syscall.SockaddrInet4).Port
}

// blockingListener returns a TCP listener that accepts connections and never
// responds; the caller owns closing it.
func blockingListener(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var connections []net.Conn
	t.Cleanup(func() {
		_ = listener.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range connections {
			_ = conn.Close()
		}
	})
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			connections = append(connections, conn)
			mu.Unlock()
		}
	}()
	return listener
}

// targetPort extracts the numeric port of a listener.
func targetPort(listener net.Listener) int {
	return listener.Addr().(*net.TCPAddr).Port
}
