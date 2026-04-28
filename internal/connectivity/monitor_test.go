package connectivity

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
)

// mockMatrix counts RunOnce calls; safe under goroutines.
type mockMatrix struct {
	calls atomic.Int64
}

func (m *mockMatrix) RunOnce(_ context.Context) {
	m.calls.Add(1)
}

type mockHandshake struct {
	hasHandshake atomic.Bool
}

func (m *mockHandshake) HasHandshake(_ context.Context, _ string) bool {
	return m.hasHandshake.Load()
}

func TestMonitor_TriggersMatrixOnRunningEvent(t *testing.T) {
	bus := events.NewBus()
	matrix := &mockMatrix{}
	hs := &mockHandshake{}
	hs.hasHandshake.Store(true) // skip handshake wait

	mon := NewMonitor(bus, matrix, hs)
	mon.Start()
	defer mon.Stop()

	// Wait for listener to subscribe before publishing.
	deadline := time.After(time.Second)
	for bus.SubscriberCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for subscriber")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	bus.Publish("tunnel:state", events.TunnelStateEvent{
		ID:    "awg0",
		State: "running",
	})

	deadline = time.After(2 * time.Second)
	for matrix.calls.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("RunOnce was never invoked after running event")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestMonitor_IgnoresNonRunningStateEvents(t *testing.T) {
	bus := events.NewBus()
	matrix := &mockMatrix{}
	mon := NewMonitor(bus, matrix, nil)
	mon.Start()
	defer mon.Stop()

	deadline := time.After(time.Second)
	for bus.SubscriberCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for subscriber")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	bus.Publish("tunnel:state", events.TunnelStateEvent{
		ID:    "awg0",
		State: "stopped",
	})

	time.Sleep(80 * time.Millisecond)
	if matrix.calls.Load() != 0 {
		t.Fatalf("expected no RunOnce for non-running state, got %d", matrix.calls.Load())
	}
}
