package connectivity

import (
	"context"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
)

const checkInterval = 60 * time.Second

// TunnelForCheck describes a tunnel that needs connectivity checking.
type TunnelForCheck struct {
	ID        string
	IfaceName string
	Method    string // "http", "ping", "handshake", "disabled"
	Target    string
}

// CheckLister returns tunnels that need connectivity checks.
type CheckLister interface {
	ListCheckableTunnels(ctx context.Context) []TunnelForCheck
}

// Checker performs a single connectivity check for a tunnel.
type Checker interface {
	Check(ctx context.Context, tunnelID string) (connected bool, latencyMs *int, err error)
}

// HandshakeChecker verifies if a tunnel has completed WireGuard handshake.
type HandshakeChecker interface {
	HasHandshake(ctx context.Context, tunnelID string) bool
}

// Monitor periodically checks connectivity for running tunnels
// and publishes tunnel:connectivity events via the event bus.
// Also listens for tunnel:state "running" events — waits for handshake, then checks.
type Monitor struct {
	bus       *events.Bus
	lister    CheckLister
	checker   Checker
	handshake HandshakeChecker
	triggerCh chan string // tunnel ID to check after handshake
	stopCh    chan struct{}
	wg        sync.WaitGroup
}

// NewMonitor creates a Monitor. Call Start() to begin checking.
func NewMonitor(bus *events.Bus, lister CheckLister, checker Checker, hs HandshakeChecker) *Monitor {
	return &Monitor{
		bus:       bus,
		lister:    lister,
		checker:   checker,
		handshake: hs,
		triggerCh: make(chan string, 16),
		stopCh:    make(chan struct{}),
	}
}

// Start launches the background check loop and event listener.
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

// listenStateEvents subscribes to the event bus and triggers immediate
// connectivity check when a tunnel transitions to "running".
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
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			m.checkAll()
		case tunnelID := <-m.triggerCh:
			go m.checkOne(tunnelID)
		case <-m.stopCh:
			return
		}
	}
}

// checkOne waits for handshake then checks connectivity for a single tunnel.
// Runs in its own goroutine (fire-and-forget from loop).
func (m *Monitor) checkOne(tunnelID string) {
	if m.bus.SubscriberCount() == 0 {
		return
	}

	// Wait for handshake (poll every 2s, timeout 30s).
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
				return // no handshake within 30s — skip check
			case <-m.stopCh:
				return
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	connected, latency, _ := m.checker.Check(ctx, tunnelID)

	m.bus.Publish("tunnel:connectivity", events.TunnelConnectivityEvent{
		ID:        tunnelID,
		Connected: connected,
		Latency:   latency,
	})
}

func (m *Monitor) checkAll() {
	if m.bus.SubscriberCount() == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tunnels := m.lister.ListCheckableTunnels(ctx)

	for _, t := range tunnels {
		if t.Method == "disabled" {
			continue
		}

		connected, latency, _ := m.checker.Check(ctx, t.ID)

		m.bus.Publish("tunnel:connectivity", events.TunnelConnectivityEvent{
			ID:        t.ID,
			Connected: connected,
			Latency:   latency,
		})
	}
}
