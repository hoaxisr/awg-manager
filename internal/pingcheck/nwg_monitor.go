package pingcheck

import (
	"context"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
)

// LatencyNotAvailable is used for NativeWG log entries where NDMS
// does not provide per-check latency data. Frontend hides the value.
const LatencyNotAvailable = -1

// nwgPollSource abstracts NDMS polling for testability.
type nwgPollSource interface {
	PollPingCheck(ctx context.Context, tunnelID string) (*ndms.PingCheckStatus, error)
}

// nwgMonitor polls NDMS ping-check status for a single NativeWG tunnel
// and converts counter deltas into LogEntry records in the shared LogBuffer.
type nwgMonitor struct {
	tunnelID   string
	tunnelName string
	interval   time.Duration
	threshold  int
	logBuffer  *LogBuffer
	source     nwgPollSource
	bus        *events.Bus

	stopCh chan struct{}
	wg     sync.WaitGroup

	// Previous snapshot for delta calculation.
	initialized bool
	prevFail    int
	prevSuccess int
	prevStatus  string
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
func (m *nwgMonitor) processDelta(failCount, successCount int, status string) {
	if !m.initialized {
		// First poll: set baseline, emit nothing.
		m.prevFail = failCount
		m.prevSuccess = successCount
		m.prevStatus = status
		m.initialized = true
		return
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
			Latency:    LatencyNotAvailable,
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
			Latency:    LatencyNotAvailable,
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
			Latency:     -1,
			StateChange: stateChange,
			FailCount:   failCount,
			Threshold:   m.threshold,
		}
		m.logBuffer.Add(entry)
		m.publishLog(entry)
	}

	// Publish state on every poll so frontend counters stay current.
	if m.bus != nil {
		m.bus.Publish("pingcheck:state", events.PingCheckStateEvent{
			TunnelID:     m.tunnelID,
			Status:       status,
			FailCount:    failCount,
			SuccessCount: successCount,
		})
	}

	m.prevFail = failCount
	m.prevSuccess = successCount
	m.prevStatus = status
}

// run starts the poll loop. Blocks until stop() is called.
func (m *nwgMonitor) run(ctx context.Context) {
	defer m.wg.Done()

	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			status, err := m.source.PollPingCheck(ctx, m.tunnelID)
			if err != nil || status == nil || !status.Exists {
				continue // skip this poll, retry next interval
			}

			// Sync poll interval with actual NDMS check interval on first poll.
			// Prevents emitting N duplicate entries when our interval differs
			// from the NDMS interval (e.g., we poll at 10s but NDMS checks at 5s).
			if !m.initialized && status.Interval > 0 {
				actual := time.Duration(status.Interval) * time.Second
				if actual != m.interval && actual >= 3*time.Second {
					m.interval = actual
					ticker.Reset(actual)
				}
			}

			m.threshold = status.MaxFails
			m.processDelta(status.FailCount, status.SuccessCount, status.Status)

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
