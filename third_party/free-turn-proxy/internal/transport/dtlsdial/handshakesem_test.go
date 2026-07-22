package dtlsdial

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// TestHandshakeSemBlocksWhenFull verifies the contract: when HandshakeSem
// is at capacity, a new Dial blocks on the slot acquisition and respects
// ctx cancellation. Validates the bound that prevents unbounded concurrent
// handshakes under high -n values.
func TestHandshakeSemBlocksWhenFull(t *testing.T) {
	sem := make(chan struct{}, 1)
	sem <- struct{}{} // pre-fill: cap = 1, full

	d := &Dialer{HandshakeSem: sem}

	pc, err := net.ListenPacket("udp", "127.0.0.1:0") //nolint:noctx
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer pc.Close() //nolint:errcheck
	peer := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = d.Dial(ctx, pc, peer)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v want DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Fatalf("Dial returned too fast (%v); expected to block on sem", elapsed)
	}
}
