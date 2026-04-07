package traffic

import (
	"context"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
)

// mockLister returns a fixed list of running tunnels.
type mockLister struct {
	tunnels []RunningTunnel
}

func (m *mockLister) RunningTunnels(_ context.Context) []RunningTunnel {
	return m.tunnels
}


func TestCollector_PublishesOnChange(t *testing.T) {
	bus := events.NewBus()
	history := New()
	defer history.Stop()

	lister := &mockLister{
		tunnels: []RunningTunnel{{ID: "awg0", BackendType: "kernel", IfaceName: "opkgtun0", RxBytes: 100, TxBytes: 200, LastHandshake: time.Unix(1000, 0)}},
	}

	c := NewCollector(bus, history, lister)

	_, ch, unsub := bus.Subscribe()
	defer unsub()

	// First collect — should publish (first observation).
	c.collect()
	select {
	case ev := <-ch:
		if ev.Type != "tunnel:traffic" {
			t.Fatalf("expected tunnel:traffic, got %s", ev.Type)
		}
		payload := ev.Data.(events.TunnelTrafficEvent)
		if payload.ID != "awg0" || payload.RxBytes != 100 || payload.TxBytes != 200 {
			t.Fatalf("unexpected payload: %+v", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for first event")
	}

	// Second collect with same values — should NOT publish.
	c.collect()
	select {
	case ev := <-ch:
		t.Fatalf("expected no event, got %+v", ev)
	case <-time.After(50 * time.Millisecond):
		// good
	}

	// Update counters — should publish again.
	lister.tunnels = []RunningTunnel{{ID: "awg0", BackendType: "kernel", IfaceName: "opkgtun0", RxBytes: 300, TxBytes: 400, LastHandshake: time.Unix(1000, 0)}}
	c.collect()
	select {
	case ev := <-ch:
		payload := ev.Data.(events.TunnelTrafficEvent)
		if payload.RxBytes != 300 || payload.TxBytes != 400 {
			t.Fatalf("unexpected payload after change: %+v", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for changed event")
	}
}

func TestCollector_SkipsWithoutSubscribers(t *testing.T) {
	bus := events.NewBus()
	history := New()
	defer history.Stop()

	lister := &mockLister{
		tunnels: []RunningTunnel{{ID: "awg0", RxBytes: 100, TxBytes: 200}},
	}

	c := NewCollector(bus, history, lister)

	// No subscribers — collect should not panic and prev should remain empty.
	c.collect()

	c.mu.Lock()
	n := len(c.prev)
	c.mu.Unlock()

	if n != 0 {
		t.Fatalf("expected prev to be empty with no subscribers, got %d entries", n)
	}
}

func TestCollector_CleansUpRemovedTunnels(t *testing.T) {
	bus := events.NewBus()
	history := New()
	defer history.Stop()

	lister := &mockLister{
		tunnels: []RunningTunnel{{ID: "awg0", RxBytes: 100, TxBytes: 200}},
	}

	c := NewCollector(bus, history, lister)

	// Subscribe so collect actually runs.
	_, _, unsub := bus.Subscribe()
	defer unsub()

	// First collect — awg0 should be in prev.
	c.collect()

	c.mu.Lock()
	_, exists := c.prev["awg0"]
	c.mu.Unlock()
	if !exists {
		t.Fatal("expected awg0 in prev after first collect")
	}

	// Remove tunnel from lister.
	lister.tunnels = nil

	// Second collect — awg0 should be cleaned up.
	c.collect()

	c.mu.Lock()
	_, exists = c.prev["awg0"]
	c.mu.Unlock()
	if exists {
		t.Fatal("expected awg0 to be removed from prev after tunnel disappeared")
	}
}
