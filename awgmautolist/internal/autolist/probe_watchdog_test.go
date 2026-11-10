package autolist

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

type concurrentReadTracker struct {
	reader *io.PipeReader
	active atomic.Int64
	max    atomic.Int64
}

func (r *concurrentReadTracker) Read(p []byte) (int, error) {
	active := r.active.Add(1)
	defer r.active.Add(-1)
	for {
		previous := r.max.Load()
		if active <= previous || r.max.CompareAndSwap(previous, active) {
			break
		}
	}
	return r.reader.Read(p)
}

func (r *concurrentReadTracker) Close() error { return r.reader.Close() }

func TestWatchdogDelayedFirstByteUsesSingleReader(t *testing.T) {
	pipeReader, pipeWriter := io.Pipe()
	reader := &concurrentReadTracker{reader: pipeReader}
	t.Cleanup(func() {
		_ = reader.Close()
		_ = pipeWriter.Close()
	})
	type result struct {
		body []byte
		err  error
	}
	finished := make(chan result, 1)
	stall := 25 * time.Millisecond
	go func() {
		body, err := readWithStallWatch(context.Background(), reader, ProbeBodyCap, stall)
		finished <- result{body: body, err: err}
	}()

	// Несколько прежних stall-интервалов до первого байта не должны создавать
	// дополнительные читатели. До первого байта действует только общий context.
	time.Sleep(4 * stall)
	if got := reader.max.Load(); got != 1 {
		t.Fatalf("concurrent readers before first byte = %d, want 1", got)
	}
	if _, err := pipeWriter.Write([]byte("delayed")); err != nil {
		t.Fatal(err)
	}
	_ = pipeWriter.Close()
	select {
	case got := <-finished:
		if string(got.body) != "delayed" || !errors.Is(got.err, io.EOF) {
			t.Fatalf("delayed first byte result = %q, %v", got.body, got.err)
		}
		if active := reader.active.Load(); active != 0 {
			t.Fatalf("readers still active after EOF = %d", active)
		}
	case <-time.After(time.Second):
		t.Fatal("delayed first byte did not complete")
	}
}

func TestWatchdogCancellationClosesBlockedRead(t *testing.T) {
	pipeReader, pipeWriter := io.Pipe()
	reader := &concurrentReadTracker{reader: pipeReader}
	defer pipeWriter.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	started := time.Now()
	body, err := readWithStallWatch(ctx, reader, ProbeBodyCap, time.Hour)
	if !errors.Is(err, context.DeadlineExceeded) || len(body) != 0 {
		t.Fatalf("cancel result = %q, %v", body, err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("blocked read did not finish promptly after cancellation")
	}
	if active := reader.active.Load(); active != 0 {
		t.Fatalf("readers leaked after cancellation = %d", active)
	}
	if got := reader.max.Load(); got != 1 {
		t.Fatalf("readers during cancellation = %d, want 1", got)
	}
}
