package connectivity

import (
	"context"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
)

// mockCheckLister returns a fixed list of tunnels.
type mockCheckLister struct {
	tunnels []TunnelForCheck
}

func (m *mockCheckLister) ListCheckableTunnels(_ context.Context) []TunnelForCheck {
	return m.tunnels
}

// mockChecker returns fixed check results.
type mockChecker struct {
	connected bool
	latencyMs *int
	err       error
}

func (m *mockChecker) Check(_ context.Context, _ string) (bool, *int, error) {
	return m.connected, m.latencyMs, m.err
}

func TestMonitor_PublishesConnectivity(t *testing.T) {
	bus := events.NewBus()

	latency := 42
	lister := &mockCheckLister{
		tunnels: []TunnelForCheck{
			{ID: "awg0", IfaceName: "opkgtun0", Method: "http", Target: "https://example.com"},
		},
	}
	checker := &mockChecker{
		connected: true,
		latencyMs: &latency,
	}

	mon := NewMonitor(bus, lister, checker, nil)

	// Subscribe so SubscriberCount > 0.
	_, ch, unsub := bus.Subscribe()
	defer unsub()

	// Call checkAll directly.
	mon.checkAll()

	select {
	case ev := <-ch:
		if ev.Type != "tunnel:connectivity" {
			t.Fatalf("expected tunnel:connectivity, got %s", ev.Type)
		}
		payload := ev.Data.(events.TunnelConnectivityEvent)
		if payload.ID != "awg0" {
			t.Fatalf("expected ID awg0, got %s", payload.ID)
		}
		if !payload.Connected {
			t.Fatal("expected Connected=true")
		}
		if payload.Latency == nil || *payload.Latency != 42 {
			t.Fatalf("expected Latency=42, got %v", payload.Latency)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for connectivity event")
	}
}

func TestMonitor_SkipsDisabled(t *testing.T) {
	bus := events.NewBus()

	lister := &mockCheckLister{
		tunnels: []TunnelForCheck{
			{ID: "awg0", IfaceName: "opkgtun0", Method: "disabled"},
		},
	}
	checker := &mockChecker{
		connected: true,
	}

	mon := NewMonitor(bus, lister, checker, nil)

	// Subscribe so SubscriberCount > 0.
	_, ch, unsub := bus.Subscribe()
	defer unsub()

	mon.checkAll()

	select {
	case ev := <-ch:
		t.Fatalf("expected no event for disabled tunnel, got %+v", ev)
	case <-time.After(50 * time.Millisecond):
		// good — no event published
	}
}
