package realityserver

import (
	"net"
	"testing"
	"time"
)

// F825: сверх лимита Accept ждёт, закрытие соединения освобождает слот, а
// выданное соединение остаётся *net.TCPConn (CloseWrite нужен REALITY).
func TestLimitListener(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := newLimitListener(inner, 1)
	defer ln.Close()
	accepted := make(chan net.Conn, 2)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	for i := 0; i < 2; i++ {
		c, err := net.Dial("tcp", inner.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
	}
	first := <-accepted
	if _, ok := first.(interface{ CloseWrite() error }); !ok {
		t.Fatal("accepted conn lost CloseWrite")
	}
	select {
	case <-accepted:
		t.Fatal("second conn accepted over the limit")
	case <-time.After(200 * time.Millisecond):
	}
	_ = first.Close()
	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("slot not released on Close")
	}
}

// Ревью F825: Close будит Accept, ждущий слота на полном лимите.
func TestLimitListener_CloseUnblocksAccept(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := newLimitListener(inner, 1)
	c, err := net.Dial("tcp", inner.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	first, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	errc := make(chan error, 1)
	go func() { _, err := ln.Accept(); errc <- err }()
	time.Sleep(50 * time.Millisecond)
	_ = ln.Close()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("Accept after Close returned a conn")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Accept still blocked after Close")
	}
}
