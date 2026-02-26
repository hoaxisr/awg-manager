package pingcheck

import (
	"time"
)

// runMonitorLoop runs the monitoring loop for a tunnel.
func (s *Service) runMonitorLoop(m *tunnelMonitor) {
	defer m.wg.Done()

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
		case <-m.stopCh:
			return
		}
	}

	// Perform initial check
	s.performCheckAndUpdate(m, config)

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

		case <-m.stopCh:
			return
		case <-s.ctx.Done():
			return
		}
	}
}

// getCheckConfig returns the resolved check configuration for a tunnel.
func (s *Service) getCheckConfig(tunnelID string) *checkConfig {
	settings, err := s.settings.Get()
	if err != nil || !settings.PingCheck.Enabled {
		return nil
	}

	stored, err := s.tunnels.Get(tunnelID)
	if err != nil || stored.PingCheck == nil || !stored.PingCheck.Enabled {
		return nil
	}

	pc := stored.PingCheck
	defaults := settings.PingCheck.Defaults

	config := &checkConfig{
		Method:        defaults.Method,
		Target:        defaults.Target,
		Interval:      defaults.Interval,
		DeadInterval:  defaults.DeadInterval,
		FailThreshold: defaults.FailThreshold,
	}

	// Override with custom settings if enabled
	if pc.UseCustomSettings {
		if pc.Method != "" {
			config.Method = pc.Method
		}
		if pc.Target != "" {
			config.Target = pc.Target
		}
		if pc.Interval > 0 {
			config.Interval = pc.Interval
		}
		if pc.DeadInterval > 0 {
			config.DeadInterval = pc.DeadInterval
		}
		if pc.FailThreshold > 0 {
			config.FailThreshold = pc.FailThreshold
		}
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
	result := performCheck(ctx, m.tunnelID, config.Method, config.Target)

	if ctx.Err() != nil {
		return
	}

	// Update state
	s.mu.Lock()
	m.lastCheck = time.Now()
	m.lastResult = &result
	stateChange := ""

	if result.Success {
		if m.failCount > 0 {
			m.failCount = 0
		}
	} else {
		m.failCount++
		if m.failCount >= config.FailThreshold {
			if time.Since(m.startedAt) < gracePeriod {
				// Grace period: log failures but don't kill the tunnel.
				// Gives WireGuard handshake time to establish after start/boot.
				m.failCount = 0
			} else {
				stateChange = "dead"
				m.isDead = true
			}
		}
	}
	s.mu.Unlock()

	if stateChange == "dead" {
		s.handleDead(m.tunnelID)
	}

	entry := LogEntry{
		Timestamp:   m.lastCheck,
		TunnelID:    m.tunnelID,
		TunnelName:  m.tunnelName,
		Success:     result.Success,
		Latency:     result.Latency,
		Error:       result.Error,
		FailCount:   m.failCount,
		Threshold:   config.FailThreshold,
		StateChange: stateChange,
	}
	s.logBuffer.Add(entry)
}

// postRestartDelay is the time to wait after forced restart before verifying connectivity.
const postRestartDelay = 15 * time.Second

// handleDeadTick handles a timer tick for a dead tunnel.
// No connectivity check — just wait for the restart timer to fire.
// When it fires: forced restart (Stop+Start) → wait → verify with normal check (HTTP/ICMP).
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

	// Attempt forced restart (Stop + Start)
	if err := s.doForcedRestart(m.tunnelID); err != nil {
		s.logBuffer.Add(LogEntry{
			Timestamp:   time.Now(),
			TunnelID:    m.tunnelID,
			TunnelName:  m.tunnelName,
			Success:     false,
			Error:       "forced restart failed: " + err.Error(),
			StateChange: "forced_restart",
		})
		return
	}

	// Wait for tunnel to establish connection
	select {
	case <-time.After(postRestartDelay):
	case <-s.ctx.Done():
		return
	case <-m.stopCh:
		return
	}

	// Verify with normal check (HTTP/ICMP)
	result := performCheck(s.ctx, m.tunnelID, config.Method, config.Target)
	if s.ctx.Err() != nil {
		return
	}

	s.mu.Lock()
	m.lastCheck = time.Now()
	m.lastResult = &result
	stateChange := "forced_restart"

	if result.Success {
		stateChange = "alive"
		m.isDead = false
		m.deadSince = time.Time{}
		m.failCount = 0
	}
	s.mu.Unlock()

	if stateChange == "alive" {
		s.handleRecovery(m.tunnelID)
	}

	s.logBuffer.Add(LogEntry{
		Timestamp:   m.lastCheck,
		TunnelID:    m.tunnelID,
		TunnelName:  m.tunnelName,
		Success:     result.Success,
		Latency:     result.Latency,
		Error:       result.Error,
		FailCount:   m.failCount,
		Threshold:   config.FailThreshold,
		StateChange: stateChange,
	})
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

	s.logInfo(tunnelID, "Forced restart, verifying connectivity...")
	return nil
}

// handleRecovery handles transition from dead to alive state.
func (s *Service) handleRecovery(tunnelID string) {
	s.mu.RLock()
	cb := s.onMonitorEvent
	s.mu.RUnlock()

	// Notify service controller — if recovery fails, stay dead
	if cb != nil {
		if err := cb(tunnelID, false); err != nil {
			s.logWarn(tunnelID, "Recovery failed, staying dead: "+err.Error())
			// Rollback: stay in dead state, retry after DeadInterval
			s.mu.Lock()
			if m, exists := s.monitors[tunnelID]; exists {
				m.isDead = true
				m.deadSince = time.Now()
			}
			s.mu.Unlock()
			return
		}
	}
	s.logInfo(tunnelID, "Tunnel recovered")
}

