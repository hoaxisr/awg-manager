package probe

import (
	"net"
	"sync"
	"testing"
)

// netListenClosed returns the "host:port" of a listener that was closed
// again: connecting to it is refused immediately.
func netListenClosed() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return "", err
	}
	return addr, nil
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
