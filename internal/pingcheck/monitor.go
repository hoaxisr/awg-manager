package pingcheck

import (
	"time"
)

// runMonitorLoop runs the monitoring loop for a tunnel.
func (s *Service) runMonitorLoop(m *tunnelMonitor) {
	defer m.wg.Done()

	// Capture stopCh locally to avoid race with PauseMonitoring + StartMonitoring.
	// PauseMonitoring closes and nils m.stopCh, StartMonitoring creates a new one.
	// Without local capture, this goroutine could miss the close and pick up the
	// new channel, causing duplicate goroutines monitoring the same tunnel.
	stopCh := m.stopCh

	// Get check configuration
	config := s.getCheckConfig(m.tunnelID)
	if config == nil {
		return
	}

	// Determine initial interval
	interval := time.Duration(config.Interval) * time.Second
	if m.isDead {
		interval = time.Duration(config.DeadInterval) * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Wait before initial check (allows tunnel to establish connection)
	// Skip delay for dead tunnels - they need immediate checking
	if !m.isDead {
		select {
		case <-time.After(initialMonitoringDelay):
			// Continue to first check
		case <-stopCh:
			return
		}
	}

	// Perform initial check
	s.performCheckAndUpdate(m, config)

	// Reset ticker so first tick is a full interval after the initial check.
	// Without this, ticker counts from creation (before initial delay),
	// so first tick fires only interval-initialMonitoringDelay after initial check.
	ticker.Reset(interval)

	for {
		select {
		case <-ticker.C:
			// Refresh config in case it changed
			config = s.getCheckConfig(m.tunnelID)
			if config == nil {
				return
			}

			s.performCheckAndUpdate(m, config)

			// Adjust interval based on dead state
			newInterval := time.Duration(config.Interval) * time.Second
			if m.isDead {
				newInterval = time.Duration(config.DeadInterval) * time.Second
			}
			if newInterval != interval {
				interval = newInterval
				ticker.Reset(interval)
			}

		case <-stopCh:
			return
		case <-s.ctx.Done():
			return
		}
	}
}

// getCheckConfig returns the resolved check configuration for a tunnel.
func (s *Service) getCheckConfig(tunnelID string) *checkConfig {
	stored, err := s.tunnels.Get(tunnelID)
	if err != nil || stored.PingCheck == nil || !stored.PingCheck.Enabled {
		return nil
	}

	pc := stored.PingCheck

	config := &checkConfig{
		Method:        pc.Method,
		Target:        pc.Target,
		Interval:      pc.Interval,
		DeadInterval:  pc.DeadInterval,
		FailThreshold: pc.FailThreshold,
	}

	// Apply sensible defaults for zero values
	if config.Method == "" {
		config.Method = "icmp"
	}
	if config.Target == "" {
		config.Target = "8.8.8.8"
	}
	if config.Interval <= 0 {
		config.Interval = 45
	}
	if config.DeadInterval <= 0 {
		config.DeadInterval = 120
	}
	if config.FailThreshold <= 0 {
		config.FailThreshold = 3
	}

	return config
}

// performCheckAndUpdate performs a check and updates state accordingly.
func (s *Service) performCheckAndUpdate(m *tunnelMonitor, config *checkConfig) {
	// Dead tunnel: timer-based forced restart, no connectivity check.
	// When timer fires: Stop+Start → wait → verify with normal check.
	if m.isDead {
		s.handleDeadTick(m, config)
		return
	}

	ctx := s.ctx
	ifaceName := s.resolveIfaceName(m.tunnelID)
	result := performCheck(ctx, ifaceName, config.Method, config.Target)

	if ctx.Err() != nil {
		return
	}

	// Update state
	s.mu.Lock()
	m.lastCheck = time.Now()
	m.lastResult = &result
	stateChange := ""
	logFailCount := 0

	if result.Success {
		if m.failCount > 0 {
			m.failCount = 0
		}
		logFailCount = m.failCount
	} else {
		m.failCount++
		logFailCount = m.failCount // capture before potential grace reset
		if m.failCount >= config.FailThreshold {
			if time.Since(m.startedAt) < gracePeriod {
				// Grace period: log failures but don't kill the tunnel.
				// Gives WireGuard handshake time to establish after start/boot.
				// logFailCount keeps the real count (e.g. 3/3) for honest logging.
				stateChange = "grace"
				m.failCount = 0
			} else {
				stateChange = "dead"
				m.isDead = true
			}
		}
	}

	logLastCheck := m.lastCheck
	s.mu.Unlock()

	if stateChange == "dead" {
		s.handleDead(m.tunnelID)
	}

	entry := LogEntry{
		Timestamp:   logLastCheck,
		TunnelID:    m.tunnelID,
		TunnelName:  m.tunnelName,
		Success:     result.Success,
		Latency:     result.Latency,
		Error:       result.Error,
		FailCount:   logFailCount,
		Threshold:   config.FailThreshold,
		StateChange: stateChange,
	}
	s.logBuffer.Add(entry)
}

// postRestartDelay is the time to wait after forced restart before verifying connectivity.
const postRestartDelay = 15 * time.Second

// handleDeadTick handles a timer tick for a dead tunnel.
// When timer fires: spawn forced restart (stopInternal + startInternal) asynchronously,
// then wait and verify connectivity. Must be async because stopInternal → PauseMonitoring
// closes our captured stopCh — we need to return to the select loop to exit cleanly.
func (s *Service) handleDeadTick(m *tunnelMonitor, config *checkConfig) {
	s.mu.RLock()
	restartAfter := time.Duration(config.DeadInterval) * time.Second
	shouldRestart := !m.deadSince.IsZero() && time.Since(m.deadSince) >= restartAfter
	s.mu.RUnlock()

	if !shouldRestart {
		return
	}

	// Reset timer before attempting restart
	s.mu.Lock()
	m.deadSince = time.Now()
	m.failCount = 0
	s.mu.Unlock()

	// Capture values for the async goroutine.
	tunnelID := m.tunnelID
	tunnelName := m.tunnelName
	method := config.Method
	target := config.Target
	threshold := config.FailThreshold

	// Fire forced restart asynchronously.
	// HandleForcedRestart calls stopInternal (pauses this monitor goroutine via PauseMonitoring)
	// then startInternal (resumes monitoring with a fresh goroutine via StartMonitoring).
	// After restart: wait → verify connectivity → notify recovery callback.
	go func() {
		// Phase 1: forced restart (stopInternal + startInternal)
		if err := s.doForcedRestart(tunnelID); err != nil {
			s.logBuffer.Add(LogEntry{
				Timestamp:   time.Now(),
				TunnelID:    tunnelID,
				TunnelName:  tunnelName,
				Success:     false,
				Error:       "forced restart failed: " + err.Error(),
				StateChange: "forced_restart",
			})
			return
		}

		// Phase 2: wait for tunnel to establish connection
		select {
		case <-time.After(postRestartDelay):
		case <-s.ctx.Done():
			return
		}

		// Phase 3: verify connectivity
		ifaceName := s.resolveIfaceName(tunnelID)
		result := performCheck(s.ctx, ifaceName, method, target)
		if s.ctx.Err() != nil {
			return
		}

		stateChange := "forced_restart"
		if result.Success {
			stateChange = "alive"
			s.handleRecovery(tunnelID)
		}

		s.logBuffer.Add(LogEntry{
			Timestamp:   time.Now(),
			TunnelID:    tunnelID,
			TunnelName:  tunnelName,
			Success:     result.Success,
			Latency:     result.Latency,
			Error:       result.Error,
			FailCount:   0,
			Threshold:   threshold,
			StateChange: stateChange,
		})
	}()
}

// handleDead handles transition to dead state.
func (s *Service) handleDead(tunnelID string) {
	// Set deadSince on the monitor (under lock)
	s.mu.Lock()
	if m, exists := s.monitors[tunnelID]; exists {
		m.deadSince = time.Now()
	}
	cb := s.onMonitorEvent
	s.mu.Unlock()

	// Notify service controller
	if cb != nil {
		if err := cb(tunnelID, true); err != nil {
			s.logError(tunnelID, "Monitor dead callback failed", err.Error())
		}
	}
	s.logInfo(tunnelID, "Tunnel marked as dead")
}

// doForcedRestart calls the forced restart callback (Stop + Start).
// Returns error if restart failed.
func (s *Service) doForcedRestart(tunnelID string) error {
	s.mu.RLock()
	cb := s.onForcedRestart
	s.mu.RUnlock()

	if cb == nil {
		return nil
	}

	if err := cb(tunnelID); err != nil {
		s.logWarn(tunnelID, "Forced restart failed: "+err.Error())
		return err
	}

	s.logInfo(tunnelID, "Forced restart complete (stopInternal + startInternal)")
	return nil
}

// handleRecovery handles transition from dead to alive state.
// Called from the async restart goroutine after post-restart connectivity check succeeds.
func (s *Service) handleRecovery(tunnelID string) {
	s.mu.RLock()
	cb := s.onMonitorEvent
	s.mu.RUnlock()

	// Notify service controller (HandleMonitorRecovered)
	if cb != nil {
		if err := cb(tunnelID, false); err != nil {
			s.logWarn(tunnelID, "Recovery callback failed: "+err.Error())
			return
		}
	}
	s.logInfo(tunnelID, "Tunnel recovered")
}
