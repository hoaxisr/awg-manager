package transport

import (
	"errors"
	"testing"
	"time"
)

// A carrier that takes nothing must not hold whoever sends control frames: TrySend drops the frame once
// the small control queue is full.
func TestBatchedTrySendNeverWaitsOnAStuckCarrier(t *testing.T) {
	inner := &transientInnerTransport{failures: -1}
	bt := NewBatchedTransport(inner)
	if err := bt.Start(); err != nil {
		t.Fatal(err)
	}
	defer bt.Stop()

	var accepted, refused int
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10*ctrlQueueDepth; i++ {
			switch err := bt.TrySend([]byte("ping")); {
			case err == nil:
				accepted++
			case errors.Is(err, ErrQueueFull):
				refused++
			default:
				t.Errorf("TrySend: %v", err)
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("TrySend blocked on a carrier that takes nothing")
	}
	if accepted == 0 || refused == 0 {
		t.Fatalf("accepted %d, refused %d: the control queue should take some and then refuse", accepted, refused)
	}
}

// Control frames (hello, ping) do not wait behind the data already queued.
func TestBatchedControlFramesGoAheadOfQueuedData(t *testing.T) {
	inner := &blockedInnerTransport{entered: make(chan struct{}), release: make(chan struct{})}
	bt := NewBatchedTransport(inner)
	bt.maxBatchCount = 8
	bt.lingerMs = 0
	if err := bt.Start(); err != nil {
		t.Fatal(err)
	}
	defer bt.Stop()

	if err := bt.Send([]byte("first")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-inner.entered: // the flush loop is busy with the first batch
	case <-time.After(time.Second):
		t.Fatal("inner send did not start")
	}
	for _, p := range []string{"d1", "d2", "d3"} {
		if err := bt.Send([]byte(p)); err != nil {
			t.Fatal(err)
		}
	}
	if err := bt.TrySend([]byte("ctrl")); err != nil {
		t.Fatal(err)
	}
	inner.releaseOnce.Do(func() { close(inner.release) })

	deadline := time.After(time.Second)
	for inner.sendCount() < 2 {
		select {
		case <-deadline:
			t.Fatalf("delivered %d batches, want 2", inner.sendCount())
		case <-time.After(5 * time.Millisecond):
		}
	}
	inner.mu.Lock()
	second := append([]byte(nil), inner.sent[1]...)
	inner.mu.Unlock()
	pkts, err := decodeBatch(second)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range pkts {
		got = append(got, string(p))
	}
	want := []string{"ctrl", "d1", "d2", "d3"}
	if len(got) != len(want) {
		t.Fatalf("second batch = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("second batch = %q, want %q", got, want)
		}
	}
}

// A flow waits while the carrier is merely slow, but a carrier that has refused every frame for stuckAfter
// no longer holds its senders: the packet is dropped, later ones at once, until the carrier takes a frame.
func TestBatchedSendGivesUpOnAStuckCarrierAndRecovers(t *testing.T) {
	inner := &transientInnerTransport{failures: -1, attempts: make(chan struct{}, 64)}
	bt := NewBatchedTransport(inner)
	bt.queue = make(chan []byte, 1)
	bt.maxBatchCount = 1
	bt.lingerMs = 0
	bt.stuckAfter = 100 * time.Millisecond
	if err := bt.Start(); err != nil {
		t.Fatal(err)
	}
	defer bt.Stop()

	if err := bt.Send([]byte("a")); err != nil { // the flush loop takes it and keeps offering it
		t.Fatal(err)
	}
	select {
	case <-inner.attempts:
	case <-time.After(time.Second):
		t.Fatal("the carrier was never offered the batch")
	}
	if err := bt.Send([]byte("b")); err != nil { // fills the queue
		t.Fatal(err)
	}

	start := time.Now()
	if err := bt.Send([]byte("c")); !errors.Is(err, ErrCarrierStuck) {
		t.Fatalf("send to a stuck carrier = %v, want ErrCarrierStuck", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("send waited %v for a stuck carrier", d)
	}
	start = time.Now()
	if err := bt.Send([]byte("d")); !errors.Is(err, ErrCarrierStuck) {
		t.Fatalf("second send = %v, want ErrCarrierStuck", err)
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("send waited %v although the carrier is known to be stuck", d)
	}

	inner.muFailures.Lock()
	inner.failures = 0 // the carrier takes frames again
	inner.muFailures.Unlock()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if err := bt.Send([]byte("e")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sends still fail after the carrier recovered")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRetryWaitBacksOffForACarrierThatStaysDown(t *testing.T) {
	for _, tc := range []struct {
		failing time.Duration
		want    time.Duration
	}{
		{0, 10 * time.Millisecond},
		{900 * time.Millisecond, 10 * time.Millisecond},
		{2 * time.Second, 100 * time.Millisecond},
		{time.Minute, 500 * time.Millisecond},
	} {
		if got := retryWait(tc.failing); got != tc.want {
			t.Fatalf("retryWait(%v) = %v, want %v", tc.failing, got, tc.want)
		}
	}
}
