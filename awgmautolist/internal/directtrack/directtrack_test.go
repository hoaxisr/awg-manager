package directtrack

import (
	"net/netip"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/packet"
)

func testFlow(port uint16) packet.Flow {
	return packet.Flow{
		Src: netip.MustParseAddr("192.168.1.10"), Dst: netip.MustParseAddr("203.0.113.10"),
		SrcPort: port, DstPort: 443, Protocol: packet.ProtocolTCP,
	}
}

func TestTrackerUsesCompleteFiveTupleAndLatestRoute(t *testing.T) {
	tracker := New(2, time.Minute)
	now := time.Unix(100, 0)
	first, second := testFlow(50000), testFlow(50001)
	tracker.Observe(first, true, now)
	tracker.Observe(second, false, now)
	if got := tracker.Classify(first, now); got != Direct {
		t.Fatalf("first route = %v", got)
	}
	if got := tracker.Classify(second, now); got != NonDirect {
		t.Fatalf("second route = %v", got)
	}
	tracker.Observe(first, false, now.Add(time.Second))
	if got := tracker.Classify(first, now); got != NonDirect {
		t.Fatalf("older direct hid latest non-direct route: %v", got)
	}

	missingPort := first
	missingPort.SrcPort = 0
	tracker.Observe(missingPort, true, now)
	if got := tracker.Classify(missingPort, now); got != Unknown {
		t.Fatalf("incomplete tuple route = %v", got)
	}
}

func TestTrackerTTLAndBoundedEviction(t *testing.T) {
	tracker := New(1, 10*time.Second)
	now := time.Unix(100, 0)
	tracker.Observe(testFlow(50000), true, now)
	if got := tracker.Classify(testFlow(50000), now.Add(10*time.Second)); got != Direct {
		t.Fatalf("route expired at exact TTL boundary: %v", got)
	}
	if got := tracker.Classify(testFlow(50000), now.Add(11*time.Second)); got != Unknown {
		t.Fatalf("expired route = %v", got)
	}

	tracker.Observe(testFlow(50001), true, now)
	tracker.Observe(testFlow(50002), false, now.Add(time.Second))
	if got := tracker.Classify(testFlow(50001), now.Add(time.Second)); got != Unknown {
		t.Fatal("capacity did not evict oldest flow")
	}
	if got := tracker.Classify(testFlow(50002), now.Add(time.Second)); got != NonDirect {
		t.Fatal("newest bounded entry was not retained")
	}
}

func TestTrackerPruneIsAmortized(t *testing.T) {
	tracker := New(8, time.Minute)
	now := time.Unix(100, 0)
	tracker.Observe(testFlow(50000), true, now)
	first := tracker.lastPrune
	tracker.Observe(testFlow(50001), true, now.Add(100*time.Millisecond))
	if !tracker.lastPrune.Equal(first) {
		t.Fatal("full route prune repeated within one second")
	}
	tracker.Observe(testFlow(50002), true, now.Add(time.Second))
	if !tracker.lastPrune.After(first) {
		t.Fatal("route prune did not run at the interval boundary")
	}
}
