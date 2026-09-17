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

	// Каждый переход публикует ДВА ключа. Снимок туннелей несёт живое
	// `running`, а публиковался только на наших мутациях — карточки оставались
	// «работает» после смерти движка, пока пилюля статуса показывала
	// «остановлен». Событие закрывает дыру мгновенно и не стоит опроса.
	if pub.count() != 6 {
		t.Fatalf("expected 6 events (2 flips × 3 resources), got %d", pub.count())
	}
	seen := map[events.Resource]int{}
	for _, e := range pub.evts {
		if e.Reason != "watchdog" {
			t.Errorf("reason = %v, want watchdog", e.Reason)
		}
		seen[e.Resource]++
	}
	for _, want := range []events.Resource{
		events.ResourceSingboxStatus,
		events.ResourceSingboxTunnels,
		events.ResourceDeviceProxyRuntime,
	} {
		if seen[want] != 2 {
			t.Errorf("ключ %s опубликован %d раз, ожидали 2 (по разу на переход)", want, seen[want])
		}
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
