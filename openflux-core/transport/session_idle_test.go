package transport

import (
	"sync/atomic"
	"testing"
	"time"
)

func reconnect(wires ...*startCountingWire) {
	for _, w := range wires {
		w.mu.Lock()
		w.drop = false
		w.mu.Unlock()
	}
}

func exitSent(ew map[string]*startCountingWire) int {
	n := 0
	for _, w := range ew {
		n += sentCount(w)
	}
	return n
}

// An exit whose client went away (closed, killed or out of reach: it cannot
// tell which) stops talking after idleTimeout of silence: no keepalives into
// carriers nobody reads.
func TestExitFallsSilentAfterClientLeaves(t *testing.T) {
	client, exit, _, ew := linkedSessions(t, "direct", "yandex")
	fastKeepalive(client, exit)
	exit.idleTimeout = 200 * time.Millisecond
	startPair(t, client, exit)

	_ = client.Stop()
	eventually(t, "the exit to fall idle", func() bool { return !exit.IsConnected() })
	before := exitSent(ew)
	time.Sleep(400 * time.Millisecond) // twenty keepalive intervals
	if after := exitSent(ew); after != before {
		t.Fatalf("an idle exit sent %d packets", after-before)
	}
	if err := exit.Send(testIPv4(40, 6)); err == nil {
		t.Fatal("an idle exit accepted data for a client that is gone")
	}
}

// A connected client with no traffic still sends keepalives, so the exit
// does not fall idle under it.
func TestExitStaysAwakeForQuietClient(t *testing.T) {
	client, exit, _, _ := linkedSessions(t, "direct")
	fastKeepalive(client, exit)
	exit.idleTimeout = 200 * time.Millisecond
	var got atomic.Int32
	exit.Receive(func([]byte) { got.Add(1) })
	startPair(t, client, exit)

	time.Sleep(600 * time.Millisecond)
	if !exit.IsConnected() {
		t.Fatal("the exit fell idle under a connected client")
	}
	if err := client.Send(testIPv4(40, 6)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "data delivery", func() bool { return got.Load() == 1 })
}

// A client that comes back (the app opened again) wakes the idle exit with
// its handshake.
func TestIdleExitAcceptsReturningClient(t *testing.T) {
	client, exit, cw, _ := linkedSessions(t, "direct")
	fastKeepalive(client, exit)
	exit.idleTimeout = 200 * time.Millisecond
	var got atomic.Int32
	exit.Receive(func([]byte) { got.Add(1) })
	startPair(t, client, exit)
	_ = client.Stop()
	eventually(t, "the exit to fall idle", func() bool { return !exit.IsConnected() })

	returning := restartOn(t, false, cw["direct"])
	if err := returning.Start(); err != nil {
		t.Fatalf("an idle exit did not accept a returning client: %v", err)
	}
	eventually(t, "data from the returning client", func() bool {
		_ = returning.Send(testIPv4(40, 6))
		return got.Load() > 0
	})
}

// A client that was out of reach without noticing (it never pings, like a
// peer that predates keepalive) still holds the old handshake; its first
// packet wakes the exit, which says hello, and the client takes it over.
func TestIdleExitWakesForClientWithOldHandshake(t *testing.T) {
	client, exit, cw, ew := linkedSessions(t, "direct")
	fastKeepalive(exit)
	exit.idleTimeout = 200 * time.Millisecond
	client.keepaliveInterval = time.Hour
	var got atomic.Int32
	exit.Receive(func([]byte) { got.Add(1) })
	startPair(t, client, exit)

	blackhole(cw["direct"], ew["direct"])
	eventually(t, "the exit to fall idle", func() bool { return !exit.IsConnected() })
	reconnect(cw["direct"], ew["direct"])
	if !client.IsConnected() {
		t.Fatal("the client noticed the outage; this test wants one that did not")
	}
	eventually(t, "data from the client after the outage", func() bool {
		_ = client.Send(testIPv4(40, 6))
		return got.Load() > 0
	})
}
