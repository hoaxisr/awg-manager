package pingcheck

import (
	"context"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/ndms"
)

// LatencyNotAvailable marks a NativeWG log entry whose latency could not be
// measured: NDMS gives no per-check timing, and the fallback probe either
// failed or ran a method that does not measure time at all. It is a sentinel,
// NOT a value — the UI must render it as "unknown" and keep it out of
// avg/min/max (issue #629). The reason travels separately, in
// TunnelStatus.LatencyNote.
const LatencyNotAvailable = -1

// Escalation: NDMS restarts the interface on its own, but its restart brings
// the tunnel back up with the very same config. When that does not help
// nwgEscalateAfter times in a row, we do what the user does by hand — a full
// Stop+Start through the tunnel service (#702).
const (
	nwgEscalateAfter   = 3
	nwgEscalateBackoff = 15 * time.Minute
)

// nwgPollSource abstracts NDMS polling for testability.
type nwgPollSource interface {
	PollPingCheck(ctx context.Context, tunnelID string) (*ndms.PingCheckProfileStatus, error)
}

// nwgMonitor polls NDMS ping-check status for a single NativeWG tunnel
// and converts counter deltas into LogEntry records in the shared LogBuffer.
type nwgMonitor struct {
	tunnelID     string
	tunnelName   string
	interval     time.Duration
	threshold    int
	logBuffer    *LogBuffer
	source       nwgPollSource
	latencyProbe func(context.Context, string) (int, string)
	bus          *events.Bus

	stopCh    chan struct{}
	triggerCh chan struct{} // buffered(1); pokes run() to poll immediately
	wg        sync.WaitGroup

	// Previous snapshot for delta calculation.
	initialized bool
	prevFail    int
	prevSuccess int
	prevStatus  string
	prevBound   bool
	lastLatency int
	// lastLatencyNote explains an absent measurement; empty when measured.
	lastLatencyNote string
	startupPhase    bool

	// restartDetected is set when nwgMonitor detects that NDMS restarted
	// the tunnel interface (counters reset after failure). Cleared on first
	// successful check after restart.
	restartDetected bool

	// restarter performs a full tunnel restart (Stop+Start through the
	// tunnel service). nil when escalation is not wired.
	restarter func(context.Context, string) error
	// allowRestart mirrors stored.PingCheck.Restart — the user allowed us
	// to touch the tunnel.
	allowRestart bool
	// onFruitlessRestart reports an NDMS restart with no successful check
	// since the previous one and returns the length of the series.
	// onCheckSuccess resets it. Both live in the Facade: our own restart
	// recreates the monitor, so this state must NOT be a monitor field.
	onFruitlessRestart func(tunnelID string) int
	onCheckSuccess     func(tunnelID string)
	// canEscalate is the Facade's backoff gate; on approval it records the
	// time itself.
	canEscalate func(tunnelID string) bool
}

// maybeEscalate restarts the whole tunnel when consecutive NDMS restarts
// produced no successful check at all. The backoff is held by the Facade —
// the state must survive monitor recreation caused by our own restart.
func (m *nwgMonitor) maybeEscalate(series int) {
	if !m.allowRestart || m.restarter == nil || m.canEscalate == nil {
		return
	}
	if series < nwgEscalateAfter {
		return
	}
	if !m.canEscalate(m.tunnelID) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := m.restarter(ctx, m.tunnelID); err != nil {
			m.logBuffer.Add(LogEntry{
				Timestamp: time.Now(), TunnelID: m.tunnelID, TunnelName: m.tunnelName,
				Backend: "nativewg", Success: false, StateChange: "restart_failed",
				Error: err.Error(),
			})
			return
		}
		m.logBuffer.Add(LogEntry{
			Timestamp: time.Now(), TunnelID: m.tunnelID, TunnelName: m.tunnelName,
			Backend: "nativewg", Success: true, StateChange: "escalated_restart",
		})
	}()
}

// publishLog publishes a log entry as an SSE event.
func (m *nwgMonitor) publishLog(entry LogEntry) {
	if m.bus == nil {
		return
	}
	m.bus.Publish("pingcheck:log", events.PingCheckLogEvent{
		Timestamp:   entry.Timestamp.Format(time.RFC3339),
		TunnelID:    entry.TunnelID,
		TunnelName:  entry.TunnelName,
		Success:     entry.Success,
		Latency:     entry.Latency,
		Error:       entry.Error,
		FailCount:   entry.FailCount,
		Threshold:   entry.Threshold,
		StateChange: entry.StateChange,
		Backend:     entry.Backend,
	})
}

// processDelta compares current counters with previous snapshot,
// emits LogEntry records for each detected check, and updates state.
// Called once per poll interval.
func (m *nwgMonitor) processDelta(failCount, successCount int, status string, bound bool) {
	latency := m.lastLatency
	if latency <= 0 {
		latency = LatencyNotAvailable
	}

	if !m.initialized {
		// First poll: set baseline and emit one initial log entry so UI log
		// reflects immediate check after enabling monitoring.
		m.prevFail = failCount
		m.prevSuccess = successCount
		m.prevStatus = status
		m.prevBound = bound
		m.initialized = true
		m.startupPhase = true

		// Warmup: a freshly started tunnel reports NDMS's provisional fail/0/0
		// before the check interval has ticked. That is not a real failure, so
		// emit NO initial log entry (a bogus "✗" would drive the UI to 100% loss
		// and a red history bar) and publish a neutral "" status so dnsroute
		// failover does not treat the warmup as down. The first real check
		// (fail or success counter > 0) flows through the delta path below.
		warmup := status == "fail" && failCount == 0 && successCount == 0
		if !warmup {
			success := status != "fail"
			entry := LogEntry{
				Timestamp:   time.Now(),
				TunnelID:    m.tunnelID,
				TunnelName:  m.tunnelName,
				Backend:     "nativewg",
				Success:     success,
				Latency:     latency,
				FailCount:   failCount,
				Threshold:   m.threshold,
				StateChange: "initial",
			}
			m.logBuffer.Add(entry)
			m.publishLog(entry)
		}

		// Still publish current state immediately so internal subscribers
		// (dnsroute failover) react. Frontend polls the status list; the
		// invalidation hint below prompts an immediate refetch.
		if m.bus != nil {
			publishStatus := status
			if warmup {
				publishStatus = "" // pending — not a real fail
			}
			m.bus.Publish("pingcheck:state", events.PingCheckStateEvent{
				TunnelID:     m.tunnelID,
				Status:       publishStatus,
				FailCount:    failCount,
				SuccessCount: successCount,
			})
		}
		m.bus.PublishInvalidated(events.ResourcePingcheck, "state-change")
		return
	}

	// Detect NDMS-initiated interface restart:
	// 1. Bound transition: interface was down, now back up with zeroed counters
	// 2. Counter reset: was failing (prevFail > 0), now counters zeroed
	if m.initialized {
		countersZeroed := failCount == 0 && successCount == 0
		boundTransition := !m.prevBound && bound
		counterReset := m.prevFail > 0 && countersZeroed

		if countersZeroed && (boundTransition || counterReset) {
			m.restartDetected = true
			series := 0
			if m.onFruitlessRestart != nil {
				series = m.onFruitlessRestart(m.tunnelID)
			}
			m.maybeEscalate(series)
		}
		// Clear restart flag once NDMS reports first success after restart.
		if m.restartDetected && successCount > 0 {
			m.restartDetected = false
			if m.onCheckSuccess != nil {
				m.onCheckSuccess(m.tunnelID)
			}
		}
	}

	// Calculate deltas. If current < prev, counters were reset
	// (NDMS restart or fail→recovery cycle resets successcount).
	failDelta := failCount - m.prevFail
	if failDelta < 0 {
		failDelta = failCount
	}
	successDelta := successCount - m.prevSuccess
	if successDelta < 0 {
		successDelta = successCount
	}

	// Any fresh successful check clears the fruitless-restart series, even
	// when restartDetected is false: the series lives in the Facade and
	// survives monitor recreation, while restartDetected does not. Without
	// this, a monitor recreated between the NDMS restart and the recovery
	// (settings save, manual restart) would leave a stale series behind and
	// escalate one restart too early (#702). Must key off the delta, not the
	// raw successCount — that one keeps a stale non-zero value throughout a
	// failure streak.
	if successDelta > 0 && m.onCheckSuccess != nil {
		m.onCheckSuccess(m.tunnelID)
	}

	// NDMS can report a transient startup mix where, in one poll window,
	// we see one fail and one success while the current state is already pass
	// (status=pass, failCount=0). This creates a noisy "FAIL -> OK" pair
	// right after INIT even though the tunnel is already healthy.
	// Suppress only this startup-only mixed delta.
	if m.startupPhase &&
		status == "pass" &&
		failDelta > 0 &&
		successDelta > 0 {
		failDelta = 0
	}

	now := time.Now()
	totalDelta := failDelta + successDelta

	// Distribute timestamps across the poll interval.
	// NDMS may perform multiple checks per our poll (e.g. NDMS checks
	// every ~5s, we poll every 10s → delta=2). Give each entry a
	// unique timestamp spread evenly over the interval.
	entryTS := func(index int) time.Time {
		if totalDelta <= 1 {
			return now
		}
		offset := m.interval * time.Duration(totalDelta-1-index) / time.Duration(totalDelta)
		return now.Add(-offset)
	}

	entryIdx := 0

	// Emit fail entries first (chronological: failures happened before recovery).
	for i := 0; i < failDelta; i++ {
		entry := LogEntry{
			Timestamp:  entryTS(entryIdx),
			TunnelID:   m.tunnelID,
			TunnelName: m.tunnelName,
			Backend:    "nativewg",
			Success:    false,
			Latency:    latency,
			FailCount:  failCount,
			Threshold:  m.threshold,
		}
		m.logBuffer.Add(entry)
		m.publishLog(entry)
		entryIdx++
	}

	// Emit success entries.
	for i := 0; i < successDelta; i++ {
		entry := LogEntry{
			Timestamp:  entryTS(entryIdx),
			TunnelID:   m.tunnelID,
			TunnelName: m.tunnelName,
			Backend:    "nativewg",
			Success:    true,
			Latency:    latency,
			FailCount:  0,
			Threshold:  m.threshold,
		}
		m.logBuffer.Add(entry)
		m.publishLog(entry)
		entryIdx++
	}

	// Emit state change entry on status transition.
	if status != m.prevStatus && m.prevStatus != "" {
		stateChange := "status_" + status // "status_fail" or "status_pass"
		entry := LogEntry{
			Timestamp:   now,
			TunnelID:    m.tunnelID,
			TunnelName:  m.tunnelName,
			Backend:     "nativewg",
			Success:     status == "pass",
			Latency:     latency,
			StateChange: stateChange,
			FailCount:   failCount,
			Threshold:   m.threshold,
		}
		m.logBuffer.Add(entry)
		m.publishLog(entry)
	}

	// Publish state on every poll for internal subscribers (dnsroute
	// failover). Frontend polls the list; the invalidation hint below
	// prompts an immediate refetch on state changes.
	if m.bus != nil {
		m.bus.Publish("pingcheck:state", events.PingCheckStateEvent{
			TunnelID:        m.tunnelID,
			Status:          status,
			FailCount:       failCount,
			SuccessCount:    successCount,
			RestartDetected: m.restartDetected,
		})
	}
	// Only invalidate when status actually transitioned. processDelta runs
	// on every poll tick (~5s) per monitored tunnel; firing unconditionally
	// would trigger excessive refetches. Initial state is published from
	// the !m.initialized branch above (which returns early).
	if status != m.prevStatus {
		m.bus.PublishInvalidated(events.ResourcePingcheck, "state-change")
	}

	m.prevFail = failCount
	m.prevSuccess = successCount
	m.prevStatus = status
	m.prevBound = bound
	if status == "pass" && successCount > 0 {
		m.startupPhase = false
	}
}

// pollOnce performs a single NDMS poll and feeds the delta through
// processDelta. MUST be called from run() only (accesses unsynchronised
// monitor state). External callers use triggerPoll() to schedule a poll
// via run()'s select loop.
func (m *nwgMonitor) pollOnce(ctx context.Context) {
	status, err := m.source.PollPingCheck(ctx, m.tunnelID)
	if err != nil || status == nil || !status.Exists {
		return // skip this poll, retry next interval
	}
	if m.latencyProbe != nil {
		m.lastLatency, m.lastLatencyNote = m.latencyProbe(ctx, m.tunnelID)
	} else {
		m.lastLatency = LatencyNotAvailable
		m.lastLatencyNote = "проба времени отклика не настроена"
	}

	// Sync poll interval with actual NDMS check interval on first poll.
	// Prevents emitting N duplicate entries when our interval differs
	// from the NDMS interval (e.g., we poll at 10s but NDMS checks at 5s).
	if !m.initialized && status.Interval > 0 {
		actual := time.Duration(status.Interval) * time.Second
		if actual != m.interval && actual >= 3*time.Second {
			m.interval = actual
		}
	}

	m.threshold = status.MaxFails
	m.processDelta(status.FailCount, status.SuccessCount, status.Status, status.Bound)
}

// triggerPoll asks run() to execute a poll out of schedule. Non-blocking:
// if a trigger is already queued the call is a no-op. Safe to call from
// any goroutine; the actual poll runs inside run() so there is no race
// with ticker-driven polls.
func (m *nwgMonitor) triggerPoll() {
	select {
	case m.triggerCh <- struct{}{}:
	default:
	}
}

// run starts the poll loop. Blocks until stop() is called.
func (m *nwgMonitor) run(ctx context.Context) {
	defer m.wg.Done()

	// Run first poll immediately after monitor start.
	m.pollOnce(ctx)

	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			prevInterval := m.interval
			m.pollOnce(ctx)
			if m.interval != prevInterval {
				ticker.Reset(m.interval)
			}

		case <-m.triggerCh:
			prevInterval := m.interval
			m.pollOnce(ctx)
			if m.interval != prevInterval {
				ticker.Reset(m.interval)
			}

		case <-m.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

// stop signals the poll loop to exit and waits for it.
// Safe to call only once per monitor (Facade guarantees this via nwgMonMu).
func (m *nwgMonitor) stop() {
	close(m.stopCh)
	m.wg.Wait()
}
