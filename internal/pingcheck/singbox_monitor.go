package pingcheck

import (
	"context"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/singbox"
)

// singboxMonitor runs a periodic delay-based connectivity check for a singbox tunnel.
// It mimics the behaviour of nwgMonitor but uses DelayChecker (Clash API)
// instead of NDMS polling.
type singboxMonitor struct {
	tag          string
	tunnelName   string
	interval     time.Duration
	threshold    int
	logBuffer    *LogBuffer
	delayChecker *singbox.DelayChecker
	bus          *events.Bus

	stopCh    chan struct{}
	wg        sync.WaitGroup
	failCount int
}

// run starts the monitoring loop. It should be launched as a goroutine.
func (m *singboxMonitor) run(ctx context.Context) {
	defer m.wg.Done()

	// Run an immediate check on start
	m.runCheck(ctx)

	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			m.runCheck(ctx)
		case <-m.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

// runCheck performs a single delay test and updates the monitor state.
// It is also triggered externally by CheckAllNow.
func (m *singboxMonitor) runCheck(ctx context.Context) {
	delay, err := m.delayChecker.CheckOne(ctx, m.tag)
	if err != nil {
		delay = 0
	}

	now := time.Now()
	success := delay > 0

	if success {
		m.failCount = 0
	} else {
		m.failCount++
	}

	// Build log entry
	entry := LogEntry{
		Timestamp:  now,
		TunnelID:   m.tag, // using tag as identifier
		TunnelName: m.tunnelName,
		Success:    success,
		Latency:    delay,
		FailCount:  m.failCount,
		Threshold:  m.threshold,
		Backend:    "singbox",
	}
	if !success {
		entry.Error = "timeout or unreachable"
	}
	if m.failCount >= m.threshold && success {
		entry.StateChange = "recovered"
	} else if m.failCount >= m.threshold {
		entry.StateChange = "link_toggle" // placeholder; no actual toggle possible
	}

	// Add to shared log buffer (same one used by kernel/nativewg monitors)
	m.logBuffer.Add(entry)

	// Publish SSE event if bus is set
	if m.bus != nil {
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

	// Optionally publish state change event
	if m.bus != nil && (entry.StateChange == "link_toggle" || entry.StateChange == "recovered") {
		newStatus := "fail"
		if success {
			newStatus = "pass"
		}
		m.bus.Publish("pingcheck:state", events.PingCheckStateEvent{
			TunnelID:  m.tag,
			Status:    newStatus,
			FailCount: m.failCount,
		})
	}
}

// stop terminates the monitor loop and waits for the goroutine to exit.
func (m *singboxMonitor) stop() {
	close(m.stopCh)
	m.wg.Wait()
}
