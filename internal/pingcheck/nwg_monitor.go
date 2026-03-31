package pingcheck

import (
	"context"
	"sync"
	"time"

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

	stopCh chan struct{}
	wg     sync.WaitGroup

	// Previous snapshot for delta calculation.
	initialized bool
	prevFail    int
	prevSuccess int
	prevStatus  string
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

	// Calculate deltas. If current < prev, counters were reset (NDMS restart).
	failDelta := failCount - m.prevFail
	if failDelta < 0 {
		failDelta = failCount
	}
	successDelta := successCount - m.prevSuccess
	if successDelta < 0 {
		successDelta = successCount
	}

	now := time.Now()

	// Emit fail entries first (chronological: failures happened before recovery).
	for i := 0; i < failDelta; i++ {
		m.logBuffer.Add(LogEntry{
			Timestamp:  now,
			TunnelID:   m.tunnelID,
			TunnelName: m.tunnelName,
			Backend:    "nativewg",
			Success:    false,
			Latency:    LatencyNotAvailable,
			FailCount:  failCount,
			Threshold:  m.threshold,
		})
	}

	// Emit success entries.
	for i := 0; i < successDelta; i++ {
		m.logBuffer.Add(LogEntry{
			Timestamp:  now,
			TunnelID:   m.tunnelID,
			TunnelName: m.tunnelName,
			Backend:    "nativewg",
			Success:    true,
			Latency:    LatencyNotAvailable,
			FailCount:  0,
			Threshold:  m.threshold,
		})
	}

	// Emit state change entry on status transition.
	if status != m.prevStatus && m.prevStatus != "" {
		stateChange := "status_" + status // "status_fail" or "status_pass"
		m.logBuffer.Add(LogEntry{
			Timestamp:   now,
			TunnelID:    m.tunnelID,
			TunnelName:  m.tunnelName,
			Backend:     "nativewg",
			Success:     status == "pass",
			Latency:     -1,
			StateChange: stateChange,
			FailCount:   failCount,
			Threshold:   m.threshold,
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
