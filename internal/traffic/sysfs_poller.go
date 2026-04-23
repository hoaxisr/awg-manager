package traffic

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Publisher receives SSE events. Implemented by *events.Bus in production.
type Publisher interface {
	Publish(eventType string, data any)
}

// HistoryFeeder is the minimal traffic-history surface used by the poller.
// *History satisfies this.
type HistoryFeeder interface {
	Feed(tunnelID string, rxBytes, txBytes int64)
}

// PollerLogger is the narrow logger used for structural errors
// (malformed counter values, permissions). Missing-interface errors
// are expected and silent.
type PollerLogger interface {
	Warnf(format string, args ...any)
}

// SysfsPoller reads /sys/class/net/<iface>/statistics/{rx,tx}_bytes
// for every running managed tunnel every interval, feeds rate points
// into History, and publishes a "tunnel:traffic" SSE event on every
// successful read.
//
// Alignment note (MIPS): no 64-bit atomic fields; ordering constraints
// therefore do not apply.
type SysfsPoller struct {
	lister   TunnelLister
	history  HistoryFeeder
	pub      Publisher
	log      PollerLogger
	root     string
	interval time.Duration

	stopCh    chan struct{}
	doneCh    chan struct{}
	started   atomic.Bool
	stopOnce  sync.Once
	startOnce sync.Once
}

// NewSysfsPoller wires the production poller. 10 s interval, standard sysfs root.
func NewSysfsPoller(lister TunnelLister, history HistoryFeeder, pub Publisher, log PollerLogger) *SysfsPoller {
	return newSysfsPoller(lister, history, pub, log, DefaultSysfsRoot, 10*time.Second)
}

// newSysfsPoller is the test-facing constructor; exposes root and interval.
func newSysfsPoller(lister TunnelLister, history HistoryFeeder, pub Publisher, log PollerLogger, root string, interval time.Duration) *SysfsPoller {
	if log == nil {
		log = nopPollerLogger{}
	}
	return &SysfsPoller{
		lister:   lister,
		history:  history,
		pub:      pub,
		log:      log,
		root:     root,
		interval: interval,
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
}

// Start launches the ticker goroutine. Safe to call multiple times.
func (p *SysfsPoller) Start() {
	p.startOnce.Do(func() {
		p.started.Store(true)
		go p.run()
	})
}

// Stop halts the ticker and waits for the goroutine to exit. Safe to call multiple times.
func (p *SysfsPoller) Stop() {
	p.stopOnce.Do(func() {
		close(p.stopCh)
	})
	if p.started.Load() {
		<-p.doneCh
	}
}

func (p *SysfsPoller) run() {
	defer close(p.doneCh)
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	// Fire once immediately so callers don't wait the full interval on startup.
	p.tick()
	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
			p.tick()
		}
	}
}

func (p *SysfsPoller) tick() {
	ctx, cancel := context.WithTimeout(context.Background(), p.interval)
	defer cancel()

	items := p.lister.RunningTunnels(ctx)
	if len(items) == 0 {
		return
	}

	for _, rt := range items {
		if rt.IfaceName == "" {
			continue
		}
		rx, tx, err := readSysfsCounters(p.root, rt.IfaceName)
		if err != nil {
			// Iface may legitimately disappear during start/stop.
			// Only log non-existence-neutral errors (malformed values, perm).
			if !os.IsNotExist(err) {
				p.log.Warnf("sysfs %s (%s): %v", rt.ID, rt.IfaceName, err)
			}
			continue
		}
		p.history.Feed(rt.ID, rx, tx)
		p.pub.Publish("tunnel:traffic", map[string]any{
			"id":      rt.ID,
			"rxBytes": rx,
			"txBytes": tx,
		})
	}
}

type nopPollerLogger struct{}

func (nopPollerLogger) Warnf(string, ...any) {}
