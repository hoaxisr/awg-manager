package pingcheck

import (
	"context"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wg"
)

// ifaceUp — шов над проверкой флага UP интерфейса: `tunnelRunning` kernel-записи
// в статусе мониторинга (у nativewg ту же роль играет Bound из NDMS).
var ifaceUp = func(name string) bool {
	ifi, err := net.InterfaceByName(name)
	return err == nil && ifi.Flags&net.FlagUp != 0
}

// wgClient is the subset of wg.Client needed by the health sensor.
type wgClient interface {
	Show(ctx context.Context, iface string) (*wg.ShowResult, error)
	LatestHandshake(ctx context.Context, iface string) (time.Time, error)
}

// Service manages ping check monitoring for all tunnels.
type Service struct {
	settings *storage.SettingsStore
	tunnels  *storage.AWGTunnelStore
	wg       wgClient
	appLog   *logging.ScopedLogger
	bus      *events.Bus

	mu        sync.RWMutex
	monitors  map[string]*tunnelMonitor
	logBuffer *LogBuffer
	running   bool
	ctx       context.Context
	cancel    context.CancelFunc

	// handshakeTimeout controls how long waitHandshake waits for a fresh
	// handshake after link toggle before considering recovery failed.
	handshakeTimeout time.Duration
}

// tunnelMonitor tracks monitoring state for a single tunnel.
type tunnelMonitor struct {
	tunnelID      string
	tunnelName    string
	failCount     int
	restartCount  int
	failThreshold int
	lastCheck     time.Time
	lastResult    *CheckResult
	stopCh        chan struct{}
	wg            sync.WaitGroup
	// tickMu сериализует sensorTick между циклом монитора и check-now:
	// два параллельных лечения на одном интерфейсе недопустимы.
	tickMu sync.Mutex
}

// checkConfig holds resolved check configuration for a tunnel.
type checkConfig struct {
	Method        string
	Target        string
	CheckURL      string
	DNSServers    []string
	Interval      int
	FailThreshold int
}

// NewService creates a new ping check service.
func NewService(
	settings *storage.SettingsStore,
	tunnels *storage.AWGTunnelStore,
	wgClient wgClient,
	appLogger logging.AppLogger,
) *Service {
	return &Service{
		settings:         settings,
		tunnels:          tunnels,
		wg:               wgClient,
		appLog:           logging.NewScopedLogger(appLogger, logging.GroupTunnel, logging.SubPingcheck),
		monitors:         make(map[string]*tunnelMonitor),
		logBuffer:        NewLogBuffer(),
		handshakeTimeout: handshakeTimeout,
	}
}

// SetEventBus sets the event bus for SSE publishing.
func (s *Service) SetEventBus(bus *events.Bus) { s.bus = bus }

// Start begins the monitoring service.
func (s *Service) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return
	}

	s.running = true
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
// skipConfigure is unused for kernel tunnels (only relevant for NativeWG via Facade).
func (s *Service) StartMonitoring(tunnelID string, tunnelName string, skipConfigure ...bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.monitors[tunnelID]; exists {
		return
	}

	stored, err := s.tunnels.Get(tunnelID)
	if err != nil || stored.PingCheck == nil || !stored.PingCheck.Enabled {
		return
	}

	m := &tunnelMonitor{
		tunnelID:   tunnelID,
		tunnelName: tunnelName,
		stopCh:     make(chan struct{}),
	}

	s.monitors[tunnelID] = m
	m.wg.Add(1)
	go s.runMonitorLoop(m)

	s.logInfo(tunnelID, "Started monitoring: "+tunnelName)
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

		// Бэкенд берём из записи: живой монитор бывает не только у kernel —
		// зеркальную запись прокси-выхода этот цикл тоже перечисляет, и
		// зашитое "kernel" врало о её природе.
		backend := "kernel"
		if stored, err := s.tunnels.Get(tunnelID); err == nil && stored.Backend != "" {
			backend = stored.Backend
		}

		// Как nwgCardStatus: лежащий интерфейс — «stopped», но не во время
		// лечения (failCount на пороге — окно down/up ещё идёт). Зеркало
		// wdtt-raw не лечится и счётчик не сбрасывает — для него это не окно.
		running := ifaceUp(s.resolveIfaceName(tunnelID))
		status := "disabled"
		if config != nil {
			switch {
			case (m.restartCount > 0 && (m.lastResult == nil || !m.lastResult.Success)) ||
				(backend == "kernel" && m.failCount >= config.FailThreshold):
				status = "recovering"
			case !running:
				status = "stopped"
			default:
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
			TunnelID:      tunnelID,
			TunnelName:    m.tunnelName,
			Enabled:       config != nil,
			Backend:       backend,
			Status:        status,
			Method:        method,
			LastCheck:     lastCheck,
			LastLatency:   lastLatency,
			FailCount:     m.failCount,
			FailThreshold: failThreshold,
			RestartCount:  m.restartCount,
			TunnelRunning: running,
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
			// NativeWG: handled by facade via NDMS native ping-check
			if t.Backend == "nativewg" {
				continue
			}
			// WDTT Raw registry rows are not kernel AWG tunnels: авто-скан
			// их не подхватывает намеренно. Мониторинг raw-записи включается
			// её собственным конфигом, и ровно тот путь резолвит живое имя
			// интерфейса (resolveIfaceName, требование 8) — это не баг.
			if t.Backend == "wdtt-raw" {
				continue
			}
			// Fast sysfs check — no subprocess or network call
			ifaceName := s.resolveIfaceName(t.ID)
			if _, err := os.Stat(fmt.Sprintf("/sys/class/net/%s", ifaceName)); err != nil {
				continue
			}
			result = append(result, TunnelStatus{
				TunnelID:      t.ID,
				TunnelName:    t.Name,
				Enabled:       false,
				Backend:       "kernel",
				Status:        "disabled",
				Method:        "http",
				FailThreshold: 3,
				TunnelRunning: ifaceUp(ifaceName),
			})
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].TunnelID < result[j].TunnelID
	})

	return result
}

// GetTunnelPingStatus returns lightweight ping status for a single tunnel.
// Reads only in-memory monitor state — no I/O.
// Returns TunnelPingInfo with Status="disabled" if tunnel has no active monitor.
func (s *Service) GetTunnelPingStatus(tunnelID string) TunnelPingInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	m, ok := s.monitors[tunnelID]
	if !ok {
		return TunnelPingInfo{Status: "disabled"}
	}

	info := TunnelPingInfo{
		Status:        "alive",
		RestartCount:  m.restartCount,
		FailCount:     m.failCount,
		FailThreshold: m.failThreshold,
	}
	if info.RestartCount > 0 && (m.lastResult == nil || !m.lastResult.Success) {
		info.Status = "recovering"
	}
	return info
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

		if !exists {
			continue
		}

		config := s.getCheckConfig(tunnelID)
		if config == nil {
			continue
		}

		s.performCheckAndUpdate(m, config)
	}
}

// checkNowAsync запускает внеочередную проверку kernel-монитора, не блокируя
// вызывающего: sensorTick на пороге уходит в лечение (ждёт рукопожатие до
// 30 с, затем backoff до maxBackoff), и HTTP-хендлер check-now висел бы всё
// это время. Занятый монитор (тик или лечение в процессе) пропускается —
// второе лечение на том же интерфейсе недопустимо. Горутина учтена в m.wg
// под s.mu вместе с проверкой stopCh: StopMonitoring закрывает канал под тем
// же локом и ждёт её, а закрытый канал мгновенно выводит из ожидания
// рукопожатия и backoff (doLinkToggle снимает канал под локом до лечения).
func (s *Service) checkNowAsync(m *tunnelMonitor, config *checkConfig) {
	s.mu.RLock()
	stopCh := m.stopCh
	if stopCh != nil {
		m.wg.Add(1)
	}
	s.mu.RUnlock()
	if stopCh == nil {
		return
	}
	go func() {
		defer m.wg.Done()
		if !m.tickMu.TryLock() {
			return
		}
		defer m.tickMu.Unlock()
		select {
		case <-stopCh:
			return
		default:
		}
		s.sensorTick(m, config)
	}()
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
		// NativeWG: NDMS native ping-check, skip custom loop
		if t.Backend == "nativewg" {
			continue
		}
		// Fast sysfs check — tunnel is running if its interface exists
		ifaceName := s.resolveIfaceName(t.ID)
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

// getCheckConfig reads storage and returns resolved check config for a tunnel.
// Returns nil if tunnel not found or ping check not enabled.
func (s *Service) getCheckConfig(tunnelID string) *checkConfig {
	stored, err := s.tunnels.Get(tunnelID)
	if err != nil || stored.PingCheck == nil || !stored.PingCheck.Enabled {
		return nil
	}

	pc := stored.PingCheck
	interval := pc.Interval
	if interval <= 0 {
		interval = 30
	}
	failThreshold := pc.FailThreshold
	if failThreshold <= 0 {
		failThreshold = 3
	}

	return &checkConfig{
		Method:        pc.Method,
		Target:        pc.Target,
		CheckURL:      s.connectivityCheckURL(),
		DNSServers:    tunnel.ParseDNSList(stored.Interface.DNS),
		Interval:      interval,
		FailThreshold: failThreshold,
	}
}

func (s *Service) connectivityCheckURL() string {
	if s == nil || s.settings == nil {
		return storage.DefaultConnectivityCheckURL
	}
	settings, err := s.settings.Get()
	if err != nil || settings == nil || settings.ConnectivityCheckURL == "" {
		return storage.DefaultConnectivityCheckURL
	}
	return settings.ConnectivityCheckURL
}

// performCheckAndUpdate performs a single check and updates monitor state.
// Used by CheckAllNow for immediate checks.
func (s *Service) performCheckAndUpdate(m *tunnelMonitor, config *checkConfig) {
	s.checkNowAsync(m, config)
}

// resolveIfaceName returns the kernel interface name for a tunnel,
// using NativeWG names (nwgN) for nativewg backend, the live iface of the
// mirror record for wdtt-raw, kernel names (opkgtunN/awgmN) otherwise.
func (s *Service) resolveIfaceName(tunnelID string) string {
	if stored, err := s.tunnels.Get(tunnelID); err == nil {
		if stored.Backend == "nativewg" {
			return nwg.NewNWGNames(stored.NWGIndex).IfaceName
		}
		// Имя raw-записи цифр не несёт: NewNames("wdttraw-de") даёт
		// opkgtun0 — ЧУЖОЙ живой интерфейс, и health бил бы по нему
		// (требование 8). Живое имя знает только сама запись.
		if stored.Backend == "wdtt-raw" && strings.TrimSpace(stored.RawKernelIface) != "" {
			return strings.TrimSpace(stored.RawKernelIface)
		}
	}
	return tunnel.NewNames(tunnelID).IfaceName
}

// addLogEntry adds a log entry to the buffer and publishes it via SSE.
func (s *Service) addLogEntry(entry LogEntry) {
	s.logBuffer.Add(entry)
	if s.bus == nil {
		return
	}
	s.bus.Publish("pingcheck:log", events.PingCheckLogEvent{
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

// logInfo logs an info message.
func (s *Service) logInfo(target, message string) {
	s.appLog.Info("pingcheck", target, message)
}

// logWarn logs a warning message.
func (s *Service) logWarn(target, message string) {
	s.appLog.Warn("pingcheck", target, message)
}

// logError logs an error message.
func (s *Service) logError(target, message, err string) {
	s.appLog.Warn("pingcheck", target, message+": "+err)
}
