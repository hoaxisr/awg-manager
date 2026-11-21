package transport

import (
	"bytes"
	"math/rand"
	"sync"
	"testing"
	"time"
)

// A hint of a datagram's worth is a hard limit: no batch of several packets goes over it, a packet that does
// not fit starts the next batch (and is neither lost nor moved), and one larger than the limit goes alone.
func TestBatchedTransportStrictHintIsAHardLimit(t *testing.T) {
	const limit = 1000
	inner := &fakeTransport{loopback: true}
	bt := NewBatchedTransport(inner, limit)
	bt.lingerMs = 20
	if !bt.strictBatch {
		t.Fatal("a hint of 1000 bytes is not taken as a hard limit")
	}
	var mu sync.Mutex
	var got [][]byte
	bt.Receive(func(p []byte) {
		mu.Lock()
		got = append(got, append([]byte(nil), p...))
		mu.Unlock()
	})
	if err := bt.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer bt.Stop()

	rnd := rand.New(rand.NewSource(7))
	var want [][]byte
	for i := 0; i < 60; i++ {
		size := 100 + (i*37)%500
		if i == 30 {
			size = 1500 // larger than the limit: alone
		}
		p := make([]byte, size)
		rnd.Read(p)
		p[0] = byte(i)
		want = append(want, p)
		if err := bt.Send(p); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n == len(want) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d packets arrived", n, len(want))
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("packet %d changed or moved", i)
		}
	}
	inner.mu.Lock()
	defer inner.mu.Unlock()
	multi, alone := 0, 0
	for i, frame := range inner.sent {
		pkts, err := decodeBatch(frame)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		total := 0
		for _, p := range pkts {
			total += 2 + len(p)
		}
		if len(pkts) > 1 {
			multi++
			if total > limit {
				t.Fatalf("frame %d holds %d packets in %d bytes, over the limit of %d", i, len(pkts), total, limit)
			}
		} else {
			alone++
		}
	}
	if multi == 0 {
		t.Fatal("nothing was coalesced: the test does not exercise the limit")
	}
	t.Logf("%d frames: %d coalesced, %d single", len(inner.sent), multi, alone)
}

// Without a small hint a batch may run over its size by the last packet it took, as it always did.
func TestBatchedTransportLargeHintIsATarget(t *testing.T) {
	for _, hint := range []int{0, 8192, 500000} {
		if bt := NewBatchedTransport(&fakeTransport{}, hint); bt.strictBatch {
			t.Fatalf("a hint of %d is a hard limit", hint)
		}
	}
	if bt := NewBatchedTransport(&fakeTransport{}); bt.strictBatch {
		t.Fatal("no hint is a hard limit")
	}
}
