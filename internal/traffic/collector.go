package traffic

import (
	"context"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
)

const collectInterval = 15 * time.Second

// RunningTunnel describes a tunnel that is currently active,
// including its traffic metrics (already collected by the lister).
type RunningTunnel struct {
	ID            string
	BackendType   string // "kernel" or "nativewg"
	IfaceName     string
	RxBytes       int64
	TxBytes       int64
	LastHandshake time.Time
	ConnectedAt   string // RFC3339 or empty
}

// TunnelLister returns the list of currently running tunnels with their traffic data.
// Implementations collect state+traffic in a single call per tunnel.
type TunnelLister interface {
	RunningTunnels(ctx context.Context) []RunningTunnel
}

// SystemTunnelLister returns traffic data for system (non-managed) WireGuard tunnels.
type SystemTunnelLister interface {
	RunningSystemTunnels(ctx context.Context) []RunningTunnel
}

// snapshot stores the last observed traffic counters for change detection.
type snapshot struct {
	rxBytes       int64
	txBytes       int64
	lastHandshake time.Time
}

// Collector periodically reads traffic counters for running tunnels
// and publishes tunnel:traffic events via the event bus.
type Collector struct {
	bus          *events.Bus
	history      *History
	lister       TunnelLister
	systemLister SystemTunnelLister // optional, set via SetSystemLister
	mu           sync.Mutex
	prev         map[string]snapshot
	stopCh       chan struct{}
	wg           sync.WaitGroup
}

// NewCollector creates a Collector. Call Start() to begin collecting.
func NewCollector(bus *events.Bus, history *History, lister TunnelLister) *Collector {
	return &Collector{
		bus:     bus,
		history: history,
		lister:  lister,
		prev:    make(map[string]snapshot),
		stopCh:  make(chan struct{}),
	}
}

// SetSystemLister sets an optional lister for system (non-managed) WireGuard tunnels.
// Safe to call after Start() — protected by mu.
func (c *Collector) SetSystemLister(sl SystemTunnelLister) {
	c.mu.Lock()
	c.systemLister = sl
	c.mu.Unlock()
}

// Start launches the background collection loop.
func (c *Collector) Start() {
	c.wg.Add(1)
	go c.loop()
}

// Stop signals the collection loop to stop and waits for it to finish.
func (c *Collector) Stop() {
	close(c.stopCh)
	c.wg.Wait()
}

// ClearTunnel removes cached data for a deleted tunnel.
func (c *Collector) ClearTunnel(id string) {
	c.mu.Lock()
	delete(c.prev, id)
	c.mu.Unlock()
}

func (c *Collector) loop() {
	defer c.wg.Done()
	ticker := time.NewTicker(collectInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.collect()
		case <-c.stopCh:
			return
		}
	}
}

func (c *Collector) collect() {
	if c.bus.SubscriberCount() == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	running := c.lister.RunningTunnels(ctx)

	// Read systemLister under lock (may be set concurrently via SetSystemLister).
	c.mu.Lock()
	sl := c.systemLister
	c.mu.Unlock()

	if sl != nil {
		running = append(running, sl.RunningSystemTunnels(ctx)...)
	}

	// Build set of running IDs for cleanup.
	runningSet := make(map[string]struct{}, len(running))

	c.mu.Lock()
	defer c.mu.Unlock()

	for _, t := range running {
		runningSet[t.ID] = struct{}{}

		prev, hasPrev := c.prev[t.ID]

		c.prev[t.ID] = snapshot{
			rxBytes:       t.RxBytes,
			txBytes:       t.TxBytes,
			lastHandshake: t.LastHandshake,
		}

		if !hasPrev || prev.rxBytes != t.RxBytes || prev.txBytes != t.TxBytes || !prev.lastHandshake.Equal(t.LastHandshake) {
			var hs string
			if !t.LastHandshake.IsZero() {
				hs = t.LastHandshake.Format(time.RFC3339)
			}
			c.bus.Publish("tunnel:traffic", events.TunnelTrafficEvent{
				ID:            t.ID,
				RxBytes:       t.RxBytes,
				TxBytes:       t.TxBytes,
				LastHandshake: hs,
				StartedAt:     t.ConnectedAt,
			})
		}

		if c.history != nil {
			c.history.Feed(t.ID, t.RxBytes, t.TxBytes)
		}
	}

	// Clean up entries for tunnels no longer running.
	for id := range c.prev {
		if _, ok := runningSet[id]; !ok {
			delete(c.prev, id)
		}
	}
}
