package shape

import (
	"net"
	"testing"
	"time"

	dtlsnet "github.com/pion/dtls/v3/pkg/net"
)

type fakeAddr struct{}

func (fakeAddr) Network() string { return "udp" }
func (fakeAddr) String() string  { return "fake" }

type fakePacketConn struct {
	writes [][]byte
}

func (*fakePacketConn) ReadFrom([]byte) (int, net.Addr, error) { return 0, fakeAddr{}, nil }
func (f *fakePacketConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	b := make([]byte, len(p))
	copy(b, p)
	f.writes = append(f.writes, b)
	return len(p), nil
}
func (*fakePacketConn) Close() error                     { return nil }
func (*fakePacketConn) LocalAddr() net.Addr              { return fakeAddr{} }
func (*fakePacketConn) SetDeadline(time.Time) error      { return nil }
func (*fakePacketConn) SetReadDeadline(time.Time) error  { return nil }
func (*fakePacketConn) SetWriteDeadline(time.Time) error { return nil }

func TestWrapPacketConnPassthrough(t *testing.T) {
	f := &fakePacketConn{}
	if got := WrapPacketConn(f, 0); got != f {
		t.Fatalf("interval=0 must return original conn, got %T", got)
	}
}

func TestWrapPacketConnForwards(t *testing.T) {
	f := &fakePacketConn{}
	wrapped := WrapPacketConn(f, time.Millisecond)
	if _, ok := wrapped.(*ShapedPacketConn); !ok {
		t.Fatalf("interval>0 must return *ShapedPacketConn, got %T", wrapped)
	}
	n, err := wrapped.WriteTo([]byte("hello"), fakeAddr{})
	if err != nil || n != 5 {
		t.Fatalf("WriteTo = (%d, %v)", n, err)
	}
	if len(f.writes) != 1 || string(f.writes[0]) != "hello" {
		t.Fatalf("inner writes = %v", f.writes)
	}
}

func TestShaperWaitNoIntervalReturns(t *testing.T) {
	done := make(chan struct{})
	go func() { New(0).Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Wait blocked with interval=0")
	}
}

type fakeListener struct {
	inner net.PacketConn
	addr  net.Addr
}

func (l *fakeListener) Accept() (net.PacketConn, net.Addr, error) { return l.inner, l.addr, nil }
func (*fakeListener) Close() error                                { return nil }
func (l *fakeListener) Addr() net.Addr                            { return l.addr }

func TestWrapPacketListenerPassthrough(t *testing.T) {
	var l dtlsnet.PacketListener = &fakeListener{addr: fakeAddr{}}
	if got := WrapPacketListener(l, 0); got != l {
		t.Fatalf("interval=0 must return original listener")
	}
}

func TestWrapPacketListenerWrapsAccepted(t *testing.T) {
	f := &fakePacketConn{}
	l := WrapPacketListener(&fakeListener{inner: f, addr: fakeAddr{}}, time.Millisecond)
	pc, _, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pc.(*ShapedPacketConn); !ok {
		t.Fatalf("accepted conn must be *ShapedPacketConn, got %T", pc)
	}
	if _, err := pc.WriteTo([]byte("x"), fakeAddr{}); err != nil {
		t.Fatal(err)
	}
	if len(f.writes) != 1 {
		t.Fatalf("inner writes = %d, want 1", len(f.writes))
	}
}
