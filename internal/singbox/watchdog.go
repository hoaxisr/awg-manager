package singbox

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/singbox/installer"
)

// StatusPublisher is the minimal SSE surface the watchdog needs. Satisfied
// by *events.Bus; тесты подставляют свой фейк.
type StatusPublisher interface {
	Publish(eventType string, data any)
}

const defaultWatchdogInterval = 30 * time.Second

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

	// swept is flipped true after the first-tick orphan sweep so it runs
	// exactly once per awg-manager process lifetime.
	swept atomic.Bool
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
	if !w.swept.Load() {
		w.runOrphanSweep()
		w.swept.Store(true)
	}

	running, _ := w.op.proc.IsRunning()
	if !running {
		// Debug, not Info: sing-box is legitimately down on a manual stop, a
		// fresh install with no config, or any idle-but-installed state, and
		// Reconcile then no-ops — an Info line here fires 2880×/day and reads
		// as "reconciling" when nothing happens. Real restart/suppression
		// events are logged by autoRestartIfCrashed on state change.
		w.log.Debug("watchdog: sing-box not running, reconciling")
		if err := w.op.Reconcile(ctx); err != nil {
			w.log.Warn("watchdog reconcile failed", "err", err)
		}
		running, _ = w.op.proc.IsRunning()
	}
	w.publishIfFlipped(running)
}

// runOrphanSweep argv-strict kills sing-box processes that match our
// managed [binary, run, -C, configPath] argv but are not the tracked
// pid. Closes the lingering #40 race where a previous awg-manager
// session crashed mid-flight leaving sing-box still alive.
//
// Strict argv equality means a user-launched sing-box on /opt/bin/ with
// a different config path is invisible — guaranteed by the absolute
// binary path established by the managed-binary architecture.
func (w *Watchdog) runOrphanSweep() {
	if w.op == nil {
		return
	}
	binary := w.op.binary
	configPath := w.op.configPath
	if binary == "" || configPath == "" {
		return
	}
	expectArgv := []string{binary, "run", "-C", configPath}

	trackedPid := 0
	if running, pid := w.op.proc.IsRunning(); running {
		trackedPid = pid
	}

	if err := installer.SweepOrphans(expectArgv, trackedPid); err != nil {
		w.log.Warn("orphan sweep failed", "err", err)
	}
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
	events.PublishInvalidatedTo(w.pub, events.ResourceSingboxStatus, "watchdog")
}
