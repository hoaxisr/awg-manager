package singbox

import (
	"context"
	"sync"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/events"
)

type fakePub struct {
	mu   sync.Mutex
	evts []events.ResourceInvalidatedEvent
}

func (p *fakePub) Publish(_ string, data any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if m, ok := data.(events.ResourceInvalidatedEvent); ok {
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
	if pub.evts[0].Resource != events.ResourceSingboxStatus {
		t.Errorf("event[0] resource = %v, want %s", pub.evts[0].Resource, events.ResourceSingboxStatus)
	}
	if pub.evts[0].Reason != "watchdog" {
		t.Errorf("event[0] reason = %v, want watchdog", pub.evts[0].Reason)
	}
}

func TestWatchdog_PublishIfFlipped_NilPublisherSafe(t *testing.T) {
	w := newTestWatchdog(nil)
	// Must not panic even with flips and nil publisher.
	w.publishIfFlipped(true)
	w.publishIfFlipped(false)
	w.publishIfFlipped(true)
}

// Единственный авто-рестарт упавшего sing-box живёт в tick → Reconcile; до сих пор
// пиновался только publishIfFlipped, и `w.op.Reconcile(ctx)` → nil был зелёным.
func TestWatchdog_Tick_ReconcilesWhenProcessDown(t *testing.T) {
	op := newTestOperator(t, nil)
	op.activeWorkFn = func() bool { return true } // иначе Reconcile вышел бы рано — туннелей в конфиге нет
	waitStarts, _ := seedStartSeam(op)

	w := NewWatchdog(op, nil, nil)
	w.swept.Store(true) // орфан-свип ходит по /proc — не наш предмет и не хост-тест

	w.tick(context.Background())

	if *waitStarts != 1 {
		t.Fatalf("startAndWait через Reconcile = %d, want 1: watchdog не поднял упавший sing-box", *waitStarts)
	}
}
