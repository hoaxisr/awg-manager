package pingcheck

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// LoggingService provides logging functionality.
type LoggingService interface {
	Log(category, action, target, message string)
	LogWarn(category, action, target, message string)
	LogError(category, action, target, message, errorMsg string)
}

const logCategoryPingCheck = "system"

// initialMonitoringDelay is the delay before first check for newly started tunnels.
// This allows the tunnel to establish connection before monitoring begins.
const initialMonitoringDelay = 30 * time.Second

// MonitorCallback is called when a tunnel transitions between dead/alive states.
// isDead=true: tunnel is dead. isDead=false: tunnel recovered.
// On recovery (isDead=false), returning an error means recovery failed —
// pingcheck stays in dead state and retries after DeadInterval.
type MonitorCallback func(tunnelID string, isDead bool) error

// ForcedRestartCallback is called when the dead interval timer fires.
// Restarts the tunnel without clearing dead state — recovery is confirmed
// only when a subsequent handshake check succeeds.
type ForcedRestartCallback func(tunnelID string) error

// Service manages ping check monitoring for all tunnels.
type Service struct {
	settings *storage.SettingsStore
	tunnels  *storage.AWGTunnelStore
	log      *logger.Logger
	logger   LoggingService

	onMonitorEvent  MonitorCallback
	onForcedRestart ForcedRestartCallback

	mu        sync.RWMutex
	monitors  map[string]*tunnelMonitor
	logBuffer *LogBuffer
	running   bool
	stopCh    chan struct{}
	ctx       context.Context
	cancel    context.CancelFunc
}

// gracePeriod is how long after start a tunnel is immune from being marked dead.
// Gives WireGuard handshake time to establish, especially at boot.
const gracePeriod = 120 * time.Second

// tunnelMonitor tracks monitoring state for a single tunnel.
type tunnelMonitor struct {
	tunnelID   string
	tunnelName string
	failCount  int
	paused     bool
	isDead     bool
	deadSince  time.Time // when tunnel entered dead state (zero = not dead)
	startedAt  time.Time // when monitoring started (for grace period)
	lastCheck  time.Time
	lastResult *CheckResult
	stopCh     chan struct{}
	wg         sync.WaitGroup // tracks goroutine lifecycle
}

// checkConfig holds resolved check configuration for a tunnel.
type checkConfig struct {
	Method        string
	Target        string
	Interval      int
	DeadInterval  int
	FailThreshold int
}

// NewService creates a new ping check service.
func NewService(
	settings *storage.SettingsStore,
	tunnels *storage.AWGTunnelStore,
	log *logger.Logger,
) *Service {
	return &Service{
		settings:  settings,
		tunnels:   tunnels,
		log:       log,
		monitors:  make(map[string]*tunnelMonitor),
		logBuffer: NewLogBuffer(),
		running:   false,
	}
}

// SetMonitorCallback sets the callback for dead/alive state transitions.
func (s *Service) SetMonitorCallback(fn MonitorCallback) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onMonitorEvent = fn
}

// SetForcedRestartCallback sets the callback for forced restart attempts.
func (s *Service) SetForcedRestartCallback(fn ForcedRestartCallback) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onForcedRestart = fn
}

// SetLoggingService sets the logging service.
func (s *Service) SetLoggingService(ls LoggingService) {
	s.logger = ls
}

// Start begins the monitoring service.
func (s *Service) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return
	}

	s.running = true
	s.stopCh = make(chan struct{})
	s.ctx, s.cancel = context.WithCancel(context.Background())

	s.logInfo("", "PingCheck service started")
}

// Stop stops the monitoring service and all tunnel monitors.
func (s *Service) Stop() {
	s.mu.Lock()

	if !s.running {
		s.mu.Unlock()
		return
	}

	s.running = false
	close(s.stopCh)
	if s.cancel != nil {
		s.cancel()
	}

	// Collect monitors and signal stop under lock
	var monitors []*tunnelMonitor
	for _, m := range s.monitors {
		if m.stopCh != nil {
			close(m.stopCh)
			m.stopCh = nil
		}
		monitors = append(monitors, m)
	}
	s.monitors = make(map[string]*tunnelMonitor)
	s.mu.Unlock()

	// Wait outside lock to avoid deadlock
	for _, m := range monitors {
		m.wg.Wait()
	}

	s.logBuffer.Stop()
	s.logInfo("", "PingCheck service stopped")
}

// StartMonitoring begins monitoring a specific tunnel.
// Called via reconcile hooks when a tunnel starts successfully.
func (s *Service) StartMonitoring(tunnelID string, tunnelName string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if already monitoring
	if m, exists := s.monitors[tunnelID]; exists {
		// If paused, resume it
		if m.paused {
			m.paused = false
			m.failCount = 0
			m.isDead = false
			m.deadSince = time.Time{}
			m.startedAt = time.Now()
			m.stopCh = make(chan struct{})
			m.wg.Add(1)
			go s.runMonitorLoop(m)
			s.logInfo(tunnelID, "Resumed monitoring tunnel")
		}
		return
	}

	// Check if global ping check is enabled
	settings, err := s.settings.Get()
	if err != nil || !settings.PingCheck.Enabled {
		return
	}

	// Check if tunnel has ping check enabled
	stored, err := s.tunnels.Get(tunnelID)
	if err != nil || stored.PingCheck == nil || !stored.PingCheck.Enabled {
		return
	}

	m := &tunnelMonitor{
		tunnelID:   tunnelID,
		tunnelName: tunnelName,
		failCount:  0,
		isDead:     false,
		startedAt:  time.Now(),
	}

	s.monitors[tunnelID] = m

	s.logInfo(tunnelID, "Started monitoring tunnel: "+tunnelName)

	m.stopCh = make(chan struct{})
	m.wg.Add(1)
	go s.runMonitorLoop(m)
}

// StopMonitoring stops monitoring a specific tunnel.
func (s *Service) StopMonitoring(tunnelID string) {
	s.mu.Lock()
	m, exists := s.monitors[tunnelID]
	if !exists {
		s.mu.Unlock()
		return
	}
	delete(s.monitors, tunnelID)
	if m.stopCh != nil {
		close(m.stopCh)
		m.stopCh = nil
	}
	s.mu.Unlock()

	m.wg.Wait() // Safe: outside lock
	s.logInfo(tunnelID, "Stopped monitoring tunnel")
}

// PauseMonitoring pauses monitoring for a tunnel (e.g., manual stop).
func (s *Service) PauseMonitoring(tunnelID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if m, exists := s.monitors[tunnelID]; exists {
		m.paused = true
		m.failCount = 0
		m.isDead = false
		m.deadSince = time.Time{}
		if m.stopCh != nil {
			close(m.stopCh)
			m.stopCh = nil
		}
		s.logInfo(tunnelID, "Paused monitoring tunnel")
	}
}

// ResumeMonitoring resumes monitoring for a tunnel.
func (s *Service) ResumeMonitoring(tunnelID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if m, exists := s.monitors[tunnelID]; exists && m.paused {
		m.paused = false
		m.failCount = 0
		m.stopCh = make(chan struct{})
		m.wg.Add(1)
		go s.runMonitorLoop(m)
		s.logInfo(tunnelID, "Resumed monitoring tunnel")
	}
}

// ResetFailCount resets the fail counter for a tunnel.
func (s *Service) ResetFailCount(tunnelID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if m, exists := s.monitors[tunnelID]; exists {
		m.failCount = 0
	}
}

// GetLogs returns all log entries.
func (s *Service) GetLogs() []LogEntry {
	return s.logBuffer.GetAll()
}

// GetTunnelLogs returns log entries for a specific tunnel.
func (s *Service) GetTunnelLogs(tunnelID string) []LogEntry {
	return s.logBuffer.GetByTunnel(tunnelID)
}

// ClearLogs removes all log entries.
func (s *Service) ClearLogs() {
	s.logBuffer.Clear()
}

// GetStatus returns the current status of all monitored tunnels.
func (s *Service) GetStatus() []TunnelStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []TunnelStatus

	monitoredIDs := make(map[string]bool)

	for tunnelID, m := range s.monitors {
		monitoredIDs[tunnelID] = true
		config := s.getCheckConfig(tunnelID)

		status := "disabled"
		if config != nil {
			if m.paused {
				status = "paused"
			} else if m.isDead {
				status = "dead"
			} else {
				status = "alive"
			}
		}

		var lastCheck *time.Time
		if !m.lastCheck.IsZero() {
			lastCheck = &m.lastCheck
		}

		failThreshold := 3
		method := "http"
		lastLatency := 0
		if config != nil {
			failThreshold = config.FailThreshold
			method = config.Method
		}
		if m.lastResult != nil {
			lastLatency = m.lastResult.Latency
		}

		result = append(result, TunnelStatus{
			TunnelID:        tunnelID,
			TunnelName:      m.tunnelName,
			Enabled:         config != nil,
			Status:          status,
			Method:          method,
			LastCheck:       lastCheck,
			LastLatency:     lastLatency,
			FailCount:       m.failCount,
			FailThreshold:   failThreshold,
			IsDeadByMonitor: m.isDead,
		})
	}

	// Include running tunnels with monitoring disabled via toggle.
	// Only show tunnels that are actually running — stopped tunnels
	// should not appear in the monitoring list at all.
	tunnels, err := s.tunnels.List()
	if err == nil {
		for _, t := range tunnels {
			if monitoredIDs[t.ID] || t.PingCheck == nil {
				continue
			}
			// Fast sysfs check — no subprocess or network call
			ifaceName := tunnel.NewNames(t.ID).IfaceName
			if _, err := os.Stat(fmt.Sprintf("/sys/class/net/%s", ifaceName)); err != nil {
				continue
			}
			result = append(result, TunnelStatus{
				TunnelID:      t.ID,
				TunnelName:    t.Name,
				Enabled:       false,
				Status:        "disabled",
				Method:        "http",
				FailThreshold: 3,
			})
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].TunnelID < result[j].TunnelID
	})

	return result
}

// CheckAllNow triggers immediate checks on all monitored tunnels.
func (s *Service) CheckAllNow() {
	s.mu.RLock()
	if !s.running {
		s.mu.RUnlock()
		return
	}
	tunnelIDs := make([]string, 0, len(s.monitors))
	for id := range s.monitors {
		tunnelIDs = append(tunnelIDs, id)
	}
	s.mu.RUnlock()

	for _, tunnelID := range tunnelIDs {
		s.mu.RLock()
		m, exists := s.monitors[tunnelID]
		s.mu.RUnlock()

		if !exists || m.paused {
			continue
		}

		config := s.getCheckConfig(tunnelID)
		if config == nil {
			continue
		}

		s.performCheckAndUpdate(m, config)
	}
}

// IsEnabled returns whether ping check is globally enabled.
func (s *Service) IsEnabled() bool {
	settings, err := s.settings.Get()
	if err != nil {
		return false
	}
	return settings.PingCheck.Enabled
}

// StartMonitoringAllRunning starts monitoring for all running tunnels.
// Used when PingCheck is toggled ON in settings — already-running tunnels
// won't get lifecycle hooks, so we scan and start monitoring for them.
func (s *Service) StartMonitoringAllRunning() {
	tunnels, err := s.tunnels.List()
	if err != nil {
		s.logError("", "Failed to list tunnels for monitoring", err.Error())
		return
	}

	for _, t := range tunnels {
		if t.PingCheck == nil || !t.PingCheck.Enabled {
			continue
		}
		// Fast sysfs check — tunnel is running if its interface exists
		ifaceName := tunnel.NewNames(t.ID).IfaceName
		if _, err := os.Stat(fmt.Sprintf("/sys/class/net/%s", ifaceName)); err != nil {
			continue
		}
		s.StartMonitoring(t.ID, t.Name)
	}
}

// StopMonitoringAll stops monitoring for all tunnels.
func (s *Service) StopMonitoringAll() {
	s.mu.Lock()
	var monitors []*tunnelMonitor
	for _, m := range s.monitors {
		if m.stopCh != nil {
			close(m.stopCh)
			m.stopCh = nil
		}
		monitors = append(monitors, m)
	}
	s.monitors = make(map[string]*tunnelMonitor)
	s.mu.Unlock()

	for _, m := range monitors {
		m.wg.Wait()
	}

	s.logInfo("", "Stopped all monitoring")
}

// logInfo logs an info message.
func (s *Service) logInfo(target, message string) {
	if s.logger != nil {
		s.logger.Log(logCategoryPingCheck, "pingcheck", target, message)
	}
}

// logWarn logs a warning message.
func (s *Service) logWarn(target, message string) {
	if s.logger != nil {
		s.logger.LogWarn(logCategoryPingCheck, "pingcheck", target, message)
	}
}

// logError logs an error message.
func (s *Service) logError(target, message, err string) {
	if s.logger != nil {
		s.logger.LogError(logCategoryPingCheck, "pingcheck", target, message, err)
	}
}
