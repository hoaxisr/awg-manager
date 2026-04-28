package connectivity

import (
	"context"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
)

// MatrixRunner triggers a single monitoring-matrix tick. Concrete impl is
// monitoring.Scheduler.RunOnce, but stays decoupled here so connectivity has
// no compile-time dependency on the monitoring package.
type MatrixRunner interface {
	RunOnce(ctx context.Context)
}

// HandshakeChecker verifies if a tunnel has completed WireGuard handshake.
type HandshakeChecker interface {
	HasHandshake(ctx context.Context, tunnelID string) bool
}

// Monitor reacts to "tunnel:state running" events: after the WireGuard
// handshake lands, it asks the monitoring scheduler to run an extra matrix
// tick. The matrix snapshot then drives card latency via the
// monitoring:matrix-update SSE event — no separate per-tunnel probe loop.
type Monitor struct {
	bus       *events.Bus
	matrix    MatrixRunner
	handshake HandshakeChecker
	triggerCh chan string
	stopCh    chan struct{}
	wg        sync.WaitGroup
}

// NewMonitor creates a Monitor that pokes the matrix scheduler after
// handshake. Call Start() to begin listening.
func NewMonitor(bus *events.Bus, matrix MatrixRunner, hs HandshakeChecker) *Monitor {
	return &Monitor{
		bus:       bus,
		matrix:    matrix,
		handshake: hs,
		triggerCh: make(chan string, 16),
		stopCh:    make(chan struct{}),
	}
}

// Start launches the background event listener.
func (m *Monitor) Start() {
	m.wg.Add(2)
	go m.loop()
	go m.listenStateEvents()
}

// Stop signals all goroutines to stop and waits.
func (m *Monitor) Stop() {
	close(m.stopCh)
	m.wg.Wait()
}

// listenStateEvents subscribes to the event bus and queues a matrix tick
// when a tunnel transitions to "running".
func (m *Monitor) listenStateEvents() {
	defer m.wg.Done()

	_, ch, unsub := m.bus.Subscribe()
	defer unsub()

	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if ev.Type != "tunnel:state" {
				continue
			}
			stateEv, ok := ev.Data.(events.TunnelStateEvent)
			if !ok {
				continue
			}
			if stateEv.State == "running" {
				select {
				case m.triggerCh <- stateEv.ID:
				default: // channel full, skip
				}
			}
		case <-m.stopCh:
			return
		}
	}
}

func (m *Monitor) loop() {
	defer m.wg.Done()
	for {
		select {
		case tunnelID := <-m.triggerCh:
			go m.runAfterHandshake(tunnelID)
		case <-m.stopCh:
			return
		}
	}
}

// runAfterHandshake waits up to 30s for the tunnel's WireGuard handshake,
// then asks the matrix scheduler to run an immediate tick. The matrix
// snapshot is what cards display, so the user sees fresh latency right
// after the tunnel comes up — without waiting for the next 60s tick.
func (m *Monitor) runAfterHandshake(tunnelID string) {
	if m.matrix == nil {
		return
	}
	if m.handshake != nil {
		deadline := time.After(30 * time.Second)
		poll := time.NewTicker(2 * time.Second)
		defer poll.Stop()
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			has := m.handshake.HasHandshake(ctx, tunnelID)
			cancel()
			if has {
				break
			}
			select {
			case <-poll.C:
				continue
			case <-deadline:
				return
			case <-m.stopCh:
				return
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	m.matrix.RunOnce(ctx)
}
