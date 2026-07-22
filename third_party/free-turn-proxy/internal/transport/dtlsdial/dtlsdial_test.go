package dtlsdial

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// fakePC blocks forever on ReadFrom and accepts writes - enough to keep the
// DTLS handshake stuck so we can exercise the gate/cancel paths.
type fakePC struct{}

func (fakePC) ReadFrom(_ []byte) (int, net.Addr, error) {
	select {} // block forever
}
func (fakePC) WriteTo(b []byte, _ net.Addr) (int, error) { return len(b), nil }
func (fakePC) Close() error                              { return nil }
func (fakePC) LocalAddr() net.Addr                       { return &net.UDPAddr{IP: net.IPv4zero} }
func (fakePC) SetDeadline(time.Time) error               { return nil }
func (fakePC) SetReadDeadline(time.Time) error           { return nil }
func (fakePC) SetWriteDeadline(time.Time) error          { return nil }

// When HandshakeSem is full and ctx fires, Dial must return ctx.Err()
// without ever touching the underlying PacketConn.
func TestDial_SemBlocksUntilCtx(t *testing.T) {
	sem := make(chan struct{}, 1)
	sem <- struct{}{} // pre-fill: no slots free
	d := &Dialer{HandshakeSem: sem, HandshakeTimeout: 5 * time.Second}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	peer := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1}
	_, err := d.Dial(ctx, fakePC{}, peer)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
}

var _ = errors.New
