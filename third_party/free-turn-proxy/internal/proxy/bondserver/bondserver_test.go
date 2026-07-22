package bondserver

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/wire/bondframe"
)

// fakeStream implements laneStream over an in-memory ring; reads block when
// empty, writes go to a sink. SetDeadline is a no-op (tests cancel via ctx).
type fakeStream struct {
	mu      sync.Mutex
	sink    bytes.Buffer
	writeFn func(p []byte) (int, error) // optional override
}

func newFakeStream() *fakeStream { return &fakeStream{} }

func (*fakeStream) Read([]byte) (int, error) { return 0, io.EOF }

func (f *fakeStream) Write(p []byte) (int, error) {
	if f.writeFn != nil {
		return f.writeFn(p)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sink.Write(p)
}

func (*fakeStream) SetDeadline(time.Time) error { return nil }

func (f *fakeStream) writtenFrames(t *testing.T) []bondframe.Frame {
	t.Helper()
	f.mu.Lock()
	buf := bytes.NewReader(f.sink.Bytes())
	f.mu.Unlock()
	var out []bondframe.Frame
	for {
		fr, err := bondframe.ReadFrame(buf)
		if err != nil {
			if err == io.EOF {
				return out
			}
			t.Fatalf("unexpected read err: %v", err)
		}
		out = append(out, fr)
	}
}

func newTestConn(t *testing.T) *serverConn {
	t.Helper()
	deps := &Deps{}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	return &serverConn{
		deps:   deps,
		id:     42,
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
		ready:  make(chan struct{}, 1),
		recvCh: make(chan bondframe.Frame, 1024),
	}
}

// TestCopyBondToBackendReorder pushes data frames out-of-seq + FIN and verifies
// backend receives bytes in seq order, then write side is closed.
func TestCopyBondToBackendReorder(t *testing.T) {
	c := newTestConn(t)
	backendA, backendB := net.Pipe()
	defer backendA.Close() //nolint:errcheck
	defer backendB.Close() //nolint:errcheck

	collected := make(chan []byte, 1)
	go func() {
		var buf bytes.Buffer
		_, err := io.Copy(&buf, backendB)
		_ = err
		collected <- buf.Bytes()
	}()

	done := make(chan struct{})
	go func() {
		c.copyBondToBackend(backendA)
		_ = backendA.Close()
		close(done)
	}()

	// out-of-order: seq 2, 0, FIN(3), 1
	c.recvCh <- bondframe.Frame{Type: bondframe.FrameData, Seq: 2, Data: []byte("C")}
	c.recvCh <- bondframe.Frame{Type: bondframe.FrameData, Seq: 0, Data: []byte("A")}
	c.recvCh <- bondframe.Frame{Type: bondframe.FrameFIN, Seq: 3}
	c.recvCh <- bondframe.Frame{Type: bondframe.FrameData, Seq: 1, Data: []byte("B")}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("copyBondToBackend did not return after FIN")
	}

	got := <-collected
	if string(got) != "ABC" {
		t.Fatalf("backend got %q want %q", got, "ABC")
	}
}

// TestCopyBondToBackendPendingDrains exercises the pending map: many gap
// frames buffered, then the missing one arrives and drains the whole run.
func TestCopyBondToBackendPendingDrains(t *testing.T) {
	c := newTestConn(t)
	backendA, backendB := net.Pipe()
	defer backendA.Close() //nolint:errcheck
	defer backendB.Close() //nolint:errcheck

	collected := make(chan []byte, 1)
	go func() {
		var buf bytes.Buffer
		_, err := io.Copy(&buf, backendB)
		_ = err
		collected <- buf.Bytes()
	}()

	done := make(chan struct{})
	go func() {
		c.copyBondToBackend(backendA)
		_ = backendA.Close()
		close(done)
	}()

	const N = 64
	// push seq 1..N-1 first (all pending), then seq 0 to unlock the run.
	for i := range N {
		if i == 0 {
			continue
		}
		c.recvCh <- bondframe.Frame{Type: bondframe.FrameData, Seq: uint64(i), Data: []byte{byte(i)}}
	}
	c.recvCh <- bondframe.Frame{Type: bondframe.FrameData, Seq: 0, Data: []byte{0}}
	c.recvCh <- bondframe.Frame{Type: bondframe.FrameFIN, Seq: uint64(N)}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("copyBondToBackend did not return after FIN")
	}

	got := <-collected
	if len(got) != N {
		t.Fatalf("backend got %d bytes want %d", len(got), N)
	}
	for i := range N {
		if got[i] != byte(i) {
			t.Fatalf("byte %d = %d want %d", i, got[i], i)
		}
	}
}

// TestWriteToNextLaneRoundRobin verifies frames rotate across lanes by index.
func TestWriteToNextLaneRoundRobin(t *testing.T) {
	c := newTestConn(t)
	l0 := &serverLane{index: 0, stream: newFakeStream()}
	l1 := &serverLane{index: 1, stream: newFakeStream()}
	l2 := &serverLane{index: 2, stream: newFakeStream()}
	c.lanes = []*serverLane{l0, l1, l2}

	var idx uint64
	for seq := range 6 {
		if err := c.writeToNextLane(uint64(seq), []byte{byte(seq)}, &idx); err != nil {
			t.Fatalf("write seq %d: %v", seq, err)
		}
	}

	for i, lane := range []*serverLane{l0, l1, l2} {
		fs, ok := lane.stream.(*fakeStream)
		if !ok {
			t.Fatalf("lane %d stream not fakeStream", i)
		}
		frames := fs.writtenFrames(t)
		if len(frames) != 2 {
			t.Fatalf("lane %d got %d frames want 2", i, len(frames))
		}
		// lane i should hold seqs i and i+3
		if frames[0].Seq != uint64(i) || frames[1].Seq != uint64(i+3) {
			t.Fatalf("lane %d seqs %d,%d want %d,%d", i, frames[0].Seq, frames[1].Seq, i, i+3)
		}
	}
}

// TestWriteToNextLaneChurn drops a failing lane and continues on survivors.
func TestWriteToNextLaneChurn(t *testing.T) {
	c := newTestConn(t)
	good := newFakeStream()
	bad := newFakeStream()
	bad.writeFn = func(_ []byte) (int, error) { return 0, errors.New("boom") }
	l0 := &serverLane{index: 0, stream: bad}
	l1 := &serverLane{index: 1, stream: good}
	c.lanes = []*serverLane{l0, l1}

	var idx uint64
	for seq := range 4 {
		if err := c.writeToNextLane(uint64(seq), []byte{byte(seq)}, &idx); err != nil {
			t.Fatalf("seq %d: %v", seq, err)
		}
	}

	// bad lane should be removed after first attempt.
	if got := len(c.snapshotLanes()); got != 1 {
		t.Fatalf("lanes after churn = %d want 1", got)
	}
	frames := good.writtenFrames(t)
	if len(frames) != 4 {
		t.Fatalf("survivor got %d frames want 4", len(frames))
	}
	for i, f := range frames {
		if f.Seq != uint64(i) {
			t.Fatalf("frame %d seq=%d want %d", i, f.Seq, i)
		}
	}
}

// TestWriteToNextLaneAllFail returns the underlying error when no lane survives.
func TestWriteToNextLaneAllFail(t *testing.T) {
	c := newTestConn(t)
	mk := func(idx uint16) *serverLane {
		s := newFakeStream()
		s.writeFn = func(_ []byte) (int, error) { return 0, errors.New("boom") }
		return &serverLane{index: idx, stream: s}
	}
	c.lanes = []*serverLane{mk(0), mk(1)}

	var idx uint64
	err := c.writeToNextLane(0, []byte("x"), &idx)
	if err == nil {
		t.Fatal("expected error when all lanes fail")
	}
	if got := len(c.snapshotLanes()); got != 0 {
		t.Fatalf("lanes after all-fail = %d want 0", got)
	}
}

// TestRegistryGetDedup ensures concurrent get() calls for the same ConnID
// share one serverConn, and that completion removes it from the registry.
func TestRegistryGetDedup(t *testing.T) {
	r := NewRegistry(Deps{})
	ctx := t.Context()

	c1 := r.get(ctx, 7, "127.0.0.1:1")
	c2 := r.get(ctx, 7, "127.0.0.1:1")
	if c1 != c2 {
		t.Fatal("expected same conn for same id")
	}

	close(c1.done)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		_, ok := r.conns[7]
		r.mu.Unlock()
		if !ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("registry did not drop conn after done")
}
