package singbox

import (
	"sync"
	"testing"
)

type fakePub struct {
	mu   sync.Mutex
	evts []map[string]any
}

func (p *fakePub) Publish(_ string, data any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if m, ok := data.(map[string]any); ok {
		p.evts = append(p.evts, m)
	}
}

func (p *fakePub) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.evts)
}

func newTestWatchdog(pub StatusPublisher) *Watchdog {
	// Operator is unused by publishIfFlipped; nil is fine for this isolated
	// test of the flip-detection logic.
	w := &Watchdog{pub: pub}
	w.lastRunning.Store(-1)
	return w
}

func TestWatchdog_PublishIfFlipped_SuppressesInitialTick(t *testing.T) {
	pub := &fakePub{}
	w := newTestWatchdog(pub)

	w.publishIfFlipped(true)
	if pub.count() != 0 {
		t.Errorf("first tick must not publish, got %d events", pub.count())
	}
}

func TestWatchdog_PublishIfFlipped_FiresOnTransition(t *testing.T) {
	pub := &fakePub{}
	w := newTestWatchdog(pub)

	w.publishIfFlipped(true)  // initial: stored but suppressed
	w.publishIfFlipped(true)  // same → suppressed
	w.publishIfFlipped(false) // flip → publish
	w.publishIfFlipped(false) // same → suppressed
	w.publishIfFlipped(true)  // flip → publish

	if pub.count() != 2 {
		t.Fatalf("expected 2 events (2 flips), got %d", pub.count())
	}
	if pub.evts[0]["resource"] != resourceSingboxStatus {
		t.Errorf("event[0] resource = %v, want %s", pub.evts[0]["resource"], resourceSingboxStatus)
	}
	if pub.evts[0]["reason"] != "watchdog" {
		t.Errorf("event[0] reason = %v, want watchdog", pub.evts[0]["reason"])
	}
}

func TestWatchdog_PublishIfFlipped_NilPublisherSafe(t *testing.T) {
	w := newTestWatchdog(nil)
	// Must not panic even with flips and nil publisher.
	w.publishIfFlipped(true)
	w.publishIfFlipped(false)
	w.publishIfFlipped(true)
}
