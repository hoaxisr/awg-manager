package transport

import (
	"bufio"
	"io"
	"net"
	"testing"
	"time"
)

// An exit's direct carrier when a client comes back on a new connection (a phone that left its Wi-Fi): the
// new connection takes over from one that went quiet, never from a live one, and a connection that sends no
// record (a port scanner) is dropped.

func startTakeoverExit(t *testing.T) (*DirectTransport, string, chan []byte) {
	t.Helper()
	ln, err := netListen()
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	exit := NewDirectTransport(DefaultConfig(), DirectConfig{IsExit: true, ListenAddr: addr, HandshakeTimeout: 2 * time.Second, MaxRecordBytes: 65535})
	exit.quietTakeover, exit.adoptWait = 400*time.Millisecond, 1500*time.Millisecond
	got := make(chan []byte, 64)
	exit.Receive(func(p []byte) { got <- append([]byte(nil), p...) })
	if err := exit.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exit.Stop() })
	return exit, addr, got
}

func dialRaw(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func sendRecord(t *testing.T, c net.Conn, payload string) {
	t.Helper()
	rec := append([]byte{byte(len(payload) >> 8), byte(len(payload))}, payload...)
	if _, err := c.Write(rec); err != nil {
		t.Fatal(err)
	}
}

func expect(t *testing.T, got chan []byte, want string, within time.Duration) {
	t.Helper()
	select {
	case p := <-got:
		if string(p) != want {
			t.Fatalf("got %q, want %q", p, want)
		}
	case <-time.After(within):
		t.Fatalf("%q not delivered within %v", want, within)
	}
}

func closedWithin(c net.Conn, d time.Duration) bool {
	_ = c.SetReadDeadline(time.Now().Add(d))
	_, err := bufio.NewReader(c).ReadByte()
	return err == io.EOF || (err != nil && !isTimeout(err))
}

func isTimeout(err error) bool {
	ne, ok := err.(net.Error)
	return ok && ne.Timeout()
}

func TestDirectNewConnectionTakesOverFromAQuietOne(t *testing.T) {
	exit, addr, got := startTakeoverExit(t)
	old := dialRaw(t, addr)
	sendRecord(t, old, "old")
	expect(t, got, "old", 2*time.Second)

	// The old connection goes quiet (the phone left the network); the new one is taken once it has been quiet long enough.
	moved := dialRaw(t, addr)
	sendRecord(t, moved, "moved")
	expect(t, got, "moved", 2*time.Second)
	if !closedWithin(old, time.Second) {
		t.Fatal("the quiet connection was not closed")
	}
	if !exit.IsConnected() {
		t.Fatal("the exit is not connected after the takeover")
	}
	sendRecord(t, moved, "after")
	expect(t, got, "after", time.Second)
}

func TestDirectLiveConnectionIsNotPushedOut(t *testing.T) {
	_, addr, got := startTakeoverExit(t)
	live := dialRaw(t, addr)
	sendRecord(t, live, "live")
	expect(t, got, "live", 2*time.Second)

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-time.After(100 * time.Millisecond):
				rec := []byte{0, 4, 'p', 'i', 'n', 'g'}
				if _, err := live.Write(rec); err != nil {
					return
				}
			}
		}
	}()
	stray := dialRaw(t, addr)
	sendRecord(t, stray, "stray")
	if !closedWithin(stray, 3*time.Second) {
		t.Fatal("a stray connection was kept while the live one kept sending")
	}
	close(stop)
	<-done
	for len(got) > 0 {
		if p := <-got; string(p) == "stray" {
			t.Fatal("a stray connection's record was delivered")
		}
	}
}

func TestDirectConnectionWithoutARecordIsDropped(t *testing.T) {
	_, addr, got := startTakeoverExit(t)
	scanner := dialRaw(t, addr)
	if _, err := scanner.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	if !closedWithin(scanner, 3*time.Second) {
		t.Fatal("a connection that never completed a record was kept")
	}
	select {
	case p := <-got:
		t.Fatalf("delivered %q from a scanner", p)
	default:
	}
}
