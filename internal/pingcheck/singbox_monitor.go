package pingcheck

import (
	"context"
	"errors"
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

	stopCh       chan struct{}
	wg           sync.WaitGroup
	mu           sync.Mutex
	failCount    int
	successCount int
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

func (m *singboxMonitor) runCheck(ctx context.Context) {
	delay, err := m.delayChecker.CheckOne(ctx, m.tag)
	if errors.Is(err, singbox.ErrCheckInFlight) {
		return // ничего не делаем, ждём следующего тика
	}
	if err != nil {
		delay = 0 // прочие ошибки считаем таймаутом
	}

	now := time.Now()
	success := delay > 0

	m.mu.Lock()
	prevFailCount := m.failCount
	if success {
		m.failCount = 0
		m.successCount++
	} else {
		m.failCount++
		m.successCount = 0
	}
	currentFailCount := m.failCount
	m.mu.Unlock()

	entry := LogEntry{
		Timestamp:  now,
		TunnelID:   m.tag,
		TunnelName: m.tunnelName,
		Success:    success,
		Latency:    delay,
		FailCount:  currentFailCount,
		Threshold:  m.threshold,
		Backend:    "singbox",
	}
	if !success {
		entry.Error = "timeout or unreachable"
	}

	if prevFailCount >= m.threshold && success {
		entry.StateChange = "status_pass"
	} else if prevFailCount < m.threshold && currentFailCount == m.threshold {
		entry.StateChange = "status_fail"
	}

	m.logBuffer.Add(entry)

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

	if m.bus != nil && entry.StateChange != "" {
		newStatus := "fail"
		if success {
			newStatus = "pass"
		}
		m.bus.Publish("pingcheck:state", events.PingCheckStateEvent{
			TunnelID:  m.tag,
			Status:    newStatus,
			FailCount: currentFailCount,
		})
		m.bus.Publish("pingcheck:state-change", map[string]any{"invalidated": true})
	}
}

// getFailCount returns the current fail count in a thread-safe manner.
func (m *singboxMonitor) getFailCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.failCount
}

// getSuccessCount returns the current success count in a thread-safe manner.
func (m *singboxMonitor) getSuccessCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.successCount
}

// stop terminates the monitor loop and waits for the goroutine to exit.
func (m *singboxMonitor) stop() {
	close(m.stopCh)
	m.wg.Wait()
}
