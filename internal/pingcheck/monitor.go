package pingcheck

import (
	"fmt"
	"net"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

const (
	handshakeTimeout  = 30 * time.Second
	handshakePollFreq = 2 * time.Second
	maxBackoff        = 30 * time.Minute
)

// runMonitorLoop runs the simple health sensor loop for a kernel tunnel.
func (s *Service) runMonitorLoop(m *tunnelMonitor) {
	defer m.wg.Done()

	config := s.getCheckConfig(m.tunnelID)
	if config == nil {
		return
	}

	interval := time.Duration(config.Interval) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			config = s.getCheckConfig(m.tunnelID)
			if config == nil {
				return
			}
			s.sensorTick(m, config)

		case <-m.stopCh:
			return
		case <-s.ctx.Done():
			return
		}
	}
}

// sensorTick performs one check cycle.
func (s *Service) sensorTick(m *tunnelMonitor, config *checkConfig) {
	ifaceName := s.resolveIfaceName(m.tunnelID)
	result := performCheck(s.ctx, ifaceName, config.Method, config.Target)
	if s.ctx.Err() != nil {
		return
	}

	now := time.Now()
	s.mu.Lock()
	m.lastCheck = now
	m.lastResult = &result
	s.mu.Unlock()

	if result.Success {
		s.mu.Lock()
		m.failCount = 0
		m.restartCount = 0
		s.mu.Unlock()

		s.logBuffer.Add(LogEntry{
			Timestamp:  now,
			TunnelID:   m.tunnelID,
			TunnelName: m.tunnelName,
			Success:    true,
			Latency:    result.Latency,
			FailCount:  0,
			Threshold:  config.FailThreshold,
		})
		return
	}

	s.mu.Lock()
	m.failCount++
	failCount := m.failCount
	s.mu.Unlock()

	s.logBuffer.Add(LogEntry{
		Timestamp:  now,
		TunnelID:   m.tunnelID,
		TunnelName: m.tunnelName,
		Success:    false,
		Latency:    result.Latency,
		Error:      result.Error,
		FailCount:  failCount,
		Threshold:  config.FailThreshold,
	})

	if failCount < config.FailThreshold {
		return
	}

	s.doLinkToggle(m, config, ifaceName)
}

// doLinkToggle performs link down → re-resolve → link up → wait handshake → backoff.
func (s *Service) doLinkToggle(m *tunnelMonitor, config *checkConfig, ifaceName string) {
	s.logInfo(m.tunnelID, fmt.Sprintf("Connectivity lost (%d/%d fails), toggling link",
		m.failCount, config.FailThreshold))

	// 1. Re-resolve DNS endpoint before link down (while DNS may still work)
	stored, _ := s.tunnels.Get(m.tunnelID)
	var newEndpoint string
	if stored != nil {
		newEndpoint = tryResolveEndpoint(stored.Peer.Endpoint)
	}

	// 2. Link down — NDMS switches to fallback immediately
	//    conf: running preserved (user intent intact), link: pending
	exec.Run(s.ctx, "/opt/sbin/ip", "link", "set", ifaceName, "down")

	// 3. Re-apply endpoint if resolved to new IP
	if newEndpoint != "" && stored != nil {
		exec.Run(s.ctx, "/opt/sbin/awg", "set", ifaceName,
			"peer", stored.Peer.PublicKey,
			"endpoint", newEndpoint)
	}

	// 4. Link up — WireGuard re-initiates handshake
	exec.Run(s.ctx, "/opt/sbin/ip", "link", "set", ifaceName, "up")

	// 5. Wait for handshake
	ok := s.waitHandshake(ifaceName)

	s.mu.Lock()
	m.restartCount++
	m.failCount = 0
	restartCount := m.restartCount
	s.mu.Unlock()

	stateChange := "link_toggle"
	if ok {
		stateChange = "recovered"
		s.logInfo(m.tunnelID, "Link toggle successful, handshake restored")
	} else {
		s.logWarn(m.tunnelID, fmt.Sprintf("Link toggle: no handshake, backoff #%d", restartCount))
	}

	s.logBuffer.Add(LogEntry{
		Timestamp:   time.Now(),
		TunnelID:    m.tunnelID,
		TunnelName:  m.tunnelName,
		Success:     ok,
		FailCount:   0,
		Threshold:   config.FailThreshold,
		StateChange: stateChange,
	})

	// 6. Backoff if handshake didn't restore
	if !ok {
		backoff := time.Duration(config.Interval) * time.Second * time.Duration(restartCount*restartCount)
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
		s.logInfo(m.tunnelID, fmt.Sprintf("Backoff %v before next cycle", backoff))
		select {
		case <-time.After(backoff):
		case <-m.stopCh:
		case <-s.ctx.Done():
		}
	}
}

// tryResolveEndpoint resolves a hostname endpoint to IP:port.
// Returns "" if endpoint is already an IP or resolution fails.
func tryResolveEndpoint(endpoint string) string {
	if endpoint == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return ""
	}
	if net.ParseIP(host) != nil {
		return "" // already an IP
	}
	ips, err := net.LookupHost(host)
	if err != nil || len(ips) == 0 {
		return ""
	}
	return net.JoinHostPort(ips[0], port)
}

// waitHandshake polls awg show for a fresh handshake after link toggle.
func (s *Service) waitHandshake(ifaceName string) bool {
	deadline := time.After(handshakeTimeout)
	poll := time.NewTicker(handshakePollFreq)
	defer poll.Stop()

	for {
		select {
		case <-poll.C:
			if s.wg == nil {
				continue
			}
			show, err := s.wg.Show(s.ctx, ifaceName)
			if err != nil {
				continue
			}
			if show.HasRecentHandshake(3 * time.Minute) {
				return true
			}
		case <-deadline:
			return false
		case <-s.ctx.Done():
			return false
		}
	}
}
