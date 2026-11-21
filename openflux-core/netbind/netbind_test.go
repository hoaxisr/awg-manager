package netbind

import (
	"errors"
	"net"
	"syscall"
	"testing"
	"time"
)

func TestWrapKeepsOwnControlAndFollowsBinding(t *testing.T) {
	defer current.Store(nil)
	var calls []string
	d := Wrap(&net.Dialer{Timeout: time.Second, Control: func(string, string, syscall.RawConn) error {
		calls = append(calls, "own")
		return nil
	}})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	// Unbound: only the dialer's own Control runs.
	c, err := d.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if len(calls) != 1 {
		t.Fatalf("calls %v", calls)
	}

	// Bound: the binding runs after it, and its error fails the dial.
	install(func(string, string, syscall.RawConn) error {
		calls = append(calls, "bind")
		return errors.New("refused")
	})
	if !Bound() {
		t.Fatal("not bound")
	}
	if _, err := d.Dial("tcp", ln.Addr().String()); err == nil {
		t.Fatal("dial should fail through the binding")
	}
	if len(calls) != 3 || calls[1] != "own" || calls[2] != "bind" {
		t.Fatalf("calls %v", calls)
	}
}
