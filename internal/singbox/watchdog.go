package singbox

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

// StatusPublisher is the minimal SSE surface the watchdog needs. Satisfied
// by *events.Bus — same pattern used by DelayChecker to keep the singbox
// package independent of the events import.
type StatusPublisher interface {
	Publish(eventType string, data any)
}

const (
	defaultWatchdogInterval  = 30 * time.Second
	eventResourceInvalidated = "resource:invalidated"
	resourceSingboxStatus    = "singbox.status"
)

// Watchdog periodically verifies that sing-box is running whenever the
// on-disk config expects at least one tunnel to be up, restarting the
// daemon via Operator.Reconcile on crash. Publishes a resource invalidation
// hint on every running/stopped flip so the UI refetches /singbox/status
// immediately instead of waiting for its next poll.
type Watchdog struct {
	op       *Operator
	pub      StatusPublisher
	interval time.Duration
	log      *slog.Logger

	// lastRunning holds the previous tick's state: 1 running, 0 stopped,
	// -1 uninitialised. atomic so Run and future inspectors don't race.
	lastRunning atomic.Int32
}

// NewWatchdog constructs a watchdog with the default 30s interval. pub may
// be nil in which case no SSE events are emitted (useful for tests).
func NewWatchdog(op *Operator, pub StatusPublisher, log *slog.Logger) *Watchdog {
	if log == nil {
		log = slog.Default()
	}
	w := &Watchdog{
		op:       op,
		pub:      pub,
		interval: defaultWatchdogInterval,
		log:      log,
	}
	w.lastRunning.Store(-1)
	return w
}

// Run blocks until ctx is cancelled. Performs one tick immediately so a
// crash detected on awgm boot is recovered in the first sweep instead of
// after an interval.
func (w *Watchdog) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		w.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// tick is one sweep: if sing-box is down, reconcile (which starts it and
// resyncs NDMS Proxy interfaces). When the daemon is alive we skip Reconcile
// to avoid hammering NDMS with proxy-sync queries every 30s — startup
// Reconcile already handled the initial sync.
func (w *Watchdog) tick(ctx context.Context) {
	running, _ := w.op.proc.IsRunning()
	if !running {
		w.log.Info("watchdog: sing-box not running, reconciling")
		if err := w.op.Reconcile(ctx); err != nil {
			w.log.Warn("watchdog reconcile failed", "err", err)
		}
		running, _ = w.op.proc.IsRunning()
	}
	w.publishIfFlipped(running)
}

// publishIfFlipped emits a resource-invalidation hint only when the running
// state actually changes, so normal ticks don't flood the SSE channel.
// First-ever tick (prev == -1) is treated as "initial sync" and suppressed
// — the UI will learn the state from its regular poll of /singbox/status.
func (w *Watchdog) publishIfFlipped(running bool) {
	cur := int32(0)
	if running {
		cur = 1
	}
	prev := w.lastRunning.Swap(cur)
	if prev == -1 || prev == cur {
		return
	}
	if w.pub == nil {
		return
	}
	w.pub.Publish(eventResourceInvalidated, map[string]any{
		"resource": resourceSingboxStatus,
		"reason":   "watchdog",
	})
}
