package pingcheck

import (
	"context"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/ndms"
)

// LatencyNotAvailable is used for NativeWG log entries where NDMS
// does not provide per-check latency data. Frontend hides the value.
const LatencyNotAvailable = -1

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
	latencyProbe func(context.Context, string) int
	bus          *events.Bus

	stopCh chan struct{}
	wg     sync.WaitGroup

	// Previous snapshot for delta calculation.
	initialized  bool
	prevFail     int
	prevSuccess  int
	prevStatus   string
	prevBound    bool
	lastLatency  int
	startupPhase bool

	// restartDetected is set when nwgMonitor detects that NDMS restarted
	// the tunnel interface (counters reset after failure). Cleared on first
	// successful check after restart.
	restartDetected bool
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

		// Still publish current state immediately so internal subscribers
		// (dnsroute failover) react. Frontend polls the status list; the
		// invalidation hint below prompts an immediate refetch.
		if m.bus != nil {
			m.bus.Publish("pingcheck:state", events.PingCheckStateEvent{
				TunnelID:     m.tunnelID,
				Status:       status,
				FailCount:    failCount,
				SuccessCount: successCount,
			})
		}
		publishInvalidatedBus(m.bus, "pingcheck", "state-change")
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
		}
		// Clear restart flag once NDMS reports first success after restart.
		if m.restartDetected && successCount > 0 {
			m.restartDetected = false
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
		publishInvalidatedBus(m.bus, "pingcheck", "state-change")
	}

	m.prevFail = failCount
	m.prevSuccess = successCount
	m.prevStatus = status
	m.prevBound = bound
	if status == "pass" && successCount > 0 {
		m.startupPhase = false
	}
}

// run starts the poll loop. Blocks until stop() is called.
func (m *nwgMonitor) run(ctx context.Context) {
	defer m.wg.Done()

	pollOnce := func() {
		status, err := m.source.PollPingCheck(ctx, m.tunnelID)
		if err != nil || status == nil || !status.Exists {
			return // skip this poll, retry next interval
		}
		if m.latencyProbe != nil {
			m.lastLatency = m.latencyProbe(ctx, m.tunnelID)
		} else {
			m.lastLatency = LatencyNotAvailable
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

	// Run first poll immediately after monitor start.
	pollOnce()

	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			prevInterval := m.interval
			pollOnce()
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
