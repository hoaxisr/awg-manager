package netconn

import (
	"bytes"
	"net"
	"sync"
	"testing"
	"time"
)

// pipeRecorder is a net.Conn that records the slice handed to each Write.
type pipeRecorder struct {
	net.Conn
	mu     sync.Mutex
	writes [][]byte
}

func (p *pipeRecorder) Write(b []byte) (int, error) {
	p.mu.Lock()
	cp := make([]byte, len(b))
	copy(cp, b)
	p.writes = append(p.writes, cp)
	p.mu.Unlock()
	return len(b), nil
}

func newRecorder() *pipeRecorder {
	a, b := net.Pipe()
	go func() { _ = b.Close() }()
	_ = a
	return &pipeRecorder{Conn: dummyConn{}}
}

type dummyConn struct{ net.Conn }

func (dummyConn) Close() error                     { return nil }
func (dummyConn) LocalAddr() net.Addr              { return nil }
func (dummyConn) RemoteAddr() net.Addr             { return nil }
func (dummyConn) SetDeadline(time.Time) error      { return nil }
func (dummyConn) SetReadDeadline(time.Time) error  { return nil }
func (dummyConn) SetWriteDeadline(time.Time) error { return nil }
func (dummyConn) Read(_ []byte) (int, error)       { return 0, nil }

func TestSplitFirstWriteConn_SplitsFirstWrite(t *testing.T) {
	t.Parallel()

	rec := newRecorder()
	c := &SplitFirstWriteConn{Conn: rec, SplitAt: 6}
	payload := []byte("ABCDEF01234567")
	n, err := c.Write(payload)
	if err != nil {
		t.Fatalf("Write err: %v", err)
	}
	if n != len(payload) {
		t.Fatalf("Write n=%d want %d", n, len(payload))
	}
	if len(rec.writes) != 2 {
		t.Fatalf("expected 2 underlying writes, got %d", len(rec.writes))
	}
	if !bytes.Equal(rec.writes[0], payload[:6]) {
		t.Fatalf("first segment = %q, want %q", rec.writes[0], payload[:6])
	}
	if !bytes.Equal(rec.writes[1], payload[6:]) {
		t.Fatalf("second segment = %q, want %q", rec.writes[1], payload[6:])
	}
}

func TestSplitFirstWriteConn_SubsequentWritesPassThrough(t *testing.T) {
	t.Parallel()

	rec := newRecorder()
	c := &SplitFirstWriteConn{Conn: rec, SplitAt: 6}
	if _, err := c.Write([]byte("ABCDEF01234567")); err != nil {
		t.Fatalf("first Write: %v", err)
	}
	if _, err := c.Write([]byte("XYZXYZ")); err != nil {
		t.Fatalf("second Write: %v", err)
	}
	if len(rec.writes) != 3 {
		t.Fatalf("expected 3 underlying writes after second call (2+1), got %d", len(rec.writes))
	}
	if !bytes.Equal(rec.writes[2], []byte("XYZXYZ")) {
		t.Fatalf("third write = %q, want XYZXYZ", rec.writes[2])
	}
}

func TestSplitFirstWriteConn_ShortFirstWriteNoSplit(t *testing.T) {
	t.Parallel()

	rec := newRecorder()
	c := &SplitFirstWriteConn{Conn: rec, SplitAt: 6}
	if _, err := c.Write([]byte("ABC")); err != nil { // len < SplitAt
		t.Fatalf("Write: %v", err)
	}
	if len(rec.writes) != 1 {
		t.Fatalf("expected single underlying write, got %d", len(rec.writes))
	}
	if !bytes.Equal(rec.writes[0], []byte("ABC")) {
		t.Fatalf("write = %q, want ABC", rec.writes[0])
	}
}

func TestSplitFirstWriteConn_DelayApplied(t *testing.T) {
	t.Parallel()

	rec := newRecorder()
	c := &SplitFirstWriteConn{Conn: rec, SplitAt: 4, Delay: 25 * time.Millisecond}
	start := time.Now()
	if _, err := c.Write([]byte("ABCDEFGH")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 20*time.Millisecond {
		t.Fatalf("expected delay >= 20ms, got %v", elapsed)
	}
}
