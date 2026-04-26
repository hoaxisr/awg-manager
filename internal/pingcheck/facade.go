package pingcheck

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
)

// nwgOpPollAdapter adapts nwg.OperatorNativeWG to the nwgPollSource interface.
type nwgOpPollAdapter struct {
	op      *nwg.OperatorNativeWG
	tunnels *storage.AWGTunnelStore
}

func (a *nwgOpPollAdapter) PollPingCheck(ctx context.Context, tunnelID string) (*ndms.PingCheckProfileStatus, error) {
	stored, err := a.tunnels.Get(tunnelID)
	if err != nil {
		return nil, err
	}
	return a.op.GetPingCheckStatus(ctx, stored)
}

// Facade unifies kernel (custom loop), NativeWG (NDMS native), and Singbox ping-check
// behind a single interface. All dispatch is based on stored.Backend.
type Facade struct {
	custom   *Service
	tunnels  *storage.AWGTunnelStore
	settings *storage.SettingsStore
	nwgOp    *nwg.OperatorNativeWG
	bus      *events.Bus

	nwgSource       nwgPollSource // nil when nwgOp is nil; overridable for tests
	nwgMonMu        sync.RWMutex
	nwgMonitors     map[string]*nwgMonitor
	nwgLatencyProbe func(context.Context, string) int // returns latency in ms, <=0 when unavailable

	// Singbox fields
	singboxDir   string
	delayChecker *singbox.DelayChecker

	singboxMonMu     sync.Mutex
	singboxMonitors  map[string]*singboxMonitor // key = tag
	singboxConfigs   map[string]*SingboxCheckConfig
	singboxCfgLoaded bool

	appLog *logging.ScopedLogger

	ctx    context.Context
	cancel context.CancelFunc
}

// NewFacade creates a unified ping-check facade.
// nwgOp may be nil if NativeWG is unavailable.
func NewFacade(custom *Service, tunnels *storage.AWGTunnelStore, settings *storage.SettingsStore, nwgOp *nwg.OperatorNativeWG, singboxDir string, delayChecker *singbox.DelayChecker, appLogger logging.AppLogger) *Facade {
	ctx, cancel := context.WithCancel(context.Background())
	f := &Facade{
		custom:      custom,
		tunnels:     tunnels,
		settings:    settings,
		nwgOp:       nwgOp,
		nwgMonitors: make(map[string]*nwgMonitor),

		singboxDir:      singboxDir,
		delayChecker:    delayChecker,
		singboxMonitors: make(map[string]*singboxMonitor),
		singboxConfigs:  make(map[string]*SingboxCheckConfig),

		appLog: logging.NewScopedLogger(appLogger, logging.GroupTunnel, logging.SubPingcheck),

		ctx:    ctx,
		cancel: cancel,
	}
	if nwgOp != nil {
		f.nwgSource = &nwgOpPollAdapter{op: nwgOp, tunnels: tunnels}
	}
	return f
}

// SetNativeWGLatencyProbe sets an optional probe used by NativeWG monitors
// to obtain real latency values (for example, via testing connectivity checks).
// Probe should return latency in milliseconds, or <=0 when unavailable.
func (f *Facade) SetNativeWGLatencyProbe(fn func(context.Context, string) int) {
	f.nwgLatencyProbe = fn
}

// SetEventBus sets the event bus for SSE publishing.
func (f *Facade) SetEventBus(bus *events.Bus) {
	f.bus = bus
	f.custom.SetEventBus(bus)
}

func (f *Facade) isNativeWG(tunnelID string) bool {
	stored, err := f.tunnels.Get(tunnelID)
	if err != nil {
		return false
	}
	return stored.Backend == "nativewg"
}

// StartMonitoring starts monitoring for a tunnel.
// NativeWG: configures NDMS native ping-check profile (unless skipConfigure is true).
// Kernel: delegates to custom loop.
// Pass skipConfigure=true when the NDMS profile was already configured by the caller
// (e.g. in the API handler) to avoid a redundant delete→create cycle.
func (f *Facade) StartMonitoring(tunnelID, tunnelName string, skipConfigure ...bool) {
	if f.isNativeWG(tunnelID) {
		if len(skipConfigure) == 0 || !skipConfigure[0] {
			f.configureNativeWGPingCheck(tunnelID)
		}
		f.startNwgMonitor(tunnelID, tunnelName)
		return
	}
	f.custom.StartMonitoring(tunnelID, tunnelName)
}

// StopMonitoring stops monitoring for a tunnel.
// NativeWG: removes NDMS native ping-check profile.
// Kernel: delegates to custom loop.
func (f *Facade) StopMonitoring(tunnelID string) {
	if f.isNativeWG(tunnelID) {
		f.stopNwgMonitor(tunnelID)
		f.removeNativeWGPingCheck(tunnelID)
		return
	}
	f.custom.StopMonitoring(tunnelID)
}

// GetStatus returns unified status from both engines.
func (f *Facade) GetStatus() []TunnelStatus {
	result := f.custom.GetStatus()

	// Merge NativeWG statuses from NDMS
	if f.nwgOp != nil {
		nwgStatuses := f.getNativeWGStatuses()
		result = append(result, nwgStatuses...)
	}

	// Merge Singbox statuses
	if f.delayChecker != nil {
		sbStatuses := f.getSingboxStatuses()
		result = append(result, sbStatuses...)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].TunnelID < result[j].TunnelID
	})

	return result
}

// getNativeWGStatuses queries NDMS for ping-check status of all NativeWG tunnels.
// Returns an empty slice if the firmware does not have the pingcheck component
// installed — in that mode nativewg monitoring is not available at all, so the
// monitoring page shows only kernel tunnels.
func (f *Facade) getNativeWGStatuses() []TunnelStatus {
	if !ndmsinfo.HasPingCheckComponent() {
		return nil
	}

	tunnels, err := f.tunnels.List()
	if err != nil {
		return nil
	}

	ctx := context.Background()
	var result []TunnelStatus

	for _, t := range tunnels {
		if t.Backend != "nativewg" {
			continue
		}

		// Tunnels without monitoring configured or with monitoring disabled:
		// skip the RCI call and return a 'disabled' placeholder so the UI
		// can still render an "enable monitoring" affordance.
		if t.PingCheck == nil || !t.PingCheck.Enabled {
			result = append(result, TunnelStatus{
				TunnelID:      t.ID,
				TunnelName:    t.Name,
				Enabled:       false,
				Backend:       "nativewg",
				Status:        "disabled",
				Method:        "icmp",
				FailThreshold: 3,
			})
			continue
		}

		status, err := f.nwgOp.GetPingCheckStatus(ctx, &t)
		if err != nil {
			continue
		}

		ts := TunnelStatus{
			TunnelID:   t.ID,
			TunnelName: t.Name,
			Enabled:    t.PingCheck.Enabled,
			Backend:    "nativewg",
			Method:     status.Mode,
		}

		if !status.Exists {
			ts.Status = "disabled"
			ts.FailThreshold = 3
		} else {
			ts.FailThreshold = status.MaxFails
			ts.FailCount = status.FailCount
			ts.SuccessCount = status.SuccessCount
			ts.TunnelRunning = status.Bound

			// When the interface is down, the ping-check profile has no bound
			// interface → status/counts are meaningless. Show "stopped" so the
			// UI can distinguish "monitoring enabled but tunnel not running"
			// from "alive and checking".
			if !status.Bound {
				ts.Status = "stopped"
			} else {
				switch status.Status {
				case "pass":
					ts.Status = "alive"
				case "fail":
					if status.FailCount > 0 {
						// Active failures — NDMS is counting towards threshold.
						ts.Status = "recovering"
						ts.RestartCount = 1
					} else if f.isNwgRestartDetected(t.ID) {
						// Post-restart: NDMS reset counters after interface
						// restart, no checks completed yet. Show "recovering"
						// so user sees the tunnel was just restarted.
						ts.Status = "recovering"
						ts.RestartCount = 1
					} else {
						// Fresh start or stale "fail" with no active failures.
						ts.Status = "alive"
					}
				default:
					ts.Status = "alive" // pending/unknown → treat as alive
				}
			}
		}

		result = append(result, ts)
	}

	return result
}

// isNwgRestartDetected returns true if the nwgMonitor for the given tunnel
// has detected a recent NDMS-initiated interface restart (counters zeroed
// after failure). Returns false if no monitor exists or no restart detected.
func (f *Facade) isNwgRestartDetected(tunnelID string) bool {
	f.nwgMonMu.RLock()
	mon, ok := f.nwgMonitors[tunnelID]
	f.nwgMonMu.RUnlock()
	if !ok {
		return false
	}
	return mon.restartDetected
}

// loadSingboxConfigsIfNeeded загружает конфигурации мониторинга singbox из файла.
func (f *Facade) loadSingboxConfigsIfNeeded() {
	if f.singboxCfgLoaded {
		return
	}
	cfgs, err := loadSingboxConfigs(f.singboxDir)
	if err != nil {
		return
	}
	f.singboxConfigs = cfgs
	f.singboxCfgLoaded = true
}

// isSingbox проверяет, относится ли тег к singbox-туннелю.
func (f *Facade) isSingbox(tag string) bool {
	f.loadSingboxConfigsIfNeeded()
	_, exists := f.singboxConfigs[tag]
	return exists
}

// StartMonitoringByTag запускает мониторинг singbox туннеля по тегу.
func (f *Facade) StartMonitoringByTag(tag, tunnelName string) {
	f.loadSingboxConfigsIfNeeded()
	cfg, ok := f.singboxConfigs[tag]
	if !ok {
		cfg = &SingboxCheckConfig{Enabled: false}
		f.singboxConfigs[tag] = cfg
	}
	if !cfg.Enabled {
		return
	}

	interval := time.Duration(cfg.Interval) * time.Second
	if interval < 5*time.Second {
		interval = 10 * time.Second
	}

	f.singboxMonMu.Lock()
	// Stop existing monitor if any
	if old, exists := f.singboxMonitors[tag]; exists {
		old.stop()
		delete(f.singboxMonitors, tag)
	}

	mon := &singboxMonitor{
		tag:          tag,
		tunnelName:   tunnelName,
		interval:     interval,
		threshold:    cfg.FailThreshold,
		logBuffer:    f.custom.logBuffer,
		delayChecker: f.delayChecker,
		bus:          f.bus,
		stopCh:       make(chan struct{}),
	}
	mon.wg.Add(1)
	go mon.run(f.ctx)
	f.singboxMonitors[tag] = mon
	f.singboxMonMu.Unlock()
	f.appLog.Info("pingcheck", tag, "Singbox monitoring started")
}

// StopMonitoringByTag останавливает мониторинг singbox туннеля.
func (f *Facade) StopMonitoringByTag(tag string) {
	f.singboxMonMu.Lock()
	mon, ok := f.singboxMonitors[tag]
	if ok {
		delete(f.singboxMonitors, tag)
	}
	f.singboxMonMu.Unlock()
	if ok {
		mon.stop()
		f.appLog.Info("pingcheck", tag, "Singbox monitoring stopped")
	}
}

// GetTunnelPingStatusByTag возвращает легковесный статус для списка туннелей.
func (f *Facade) GetTunnelPingStatusByTag(tag string) TunnelPingInfo {
	f.loadSingboxConfigsIfNeeded()
	cfg, ok := f.singboxConfigs[tag]
	if !ok || !cfg.Enabled {
		return TunnelPingInfo{Status: "disabled"}
	}
	f.singboxMonMu.Lock()
	mon, active := f.singboxMonitors[tag]
	f.singboxMonMu.Unlock()
	if !active {
		return TunnelPingInfo{Status: "disabled"}
	}
	if mon.failCount > 0 {
		return TunnelPingInfo{
			Status:        "recovering",
			FailCount:     mon.failCount,
			FailThreshold: cfg.FailThreshold,
		}
	}
	return TunnelPingInfo{
		Status:        "alive",
		FailThreshold: cfg.FailThreshold,
	}
}

// SaveSingboxConfig persists singbox monitoring configuration for a tag.
func (f *Facade) SaveSingboxConfig(tag string, cfg SingboxCheckConfig) error {
	f.loadSingboxConfigsIfNeeded()
	f.singboxConfigs[tag] = &cfg
	err := saveSingboxConfigs(f.singboxDir, f.singboxConfigs)
	if err != nil {
		f.appLog.Warn("pingcheck", tag, "Failed to save singbox config: "+err.Error())
	} else {
		f.appLog.Info("pingcheck", tag, "Singbox ping config saved")
	}
	return err
}

// getSingboxStatuses возвращает список статусов для всех singbox-туннелей.
func (f *Facade) getSingboxStatuses() []TunnelStatus {
	f.loadSingboxConfigsIfNeeded()
	var result []TunnelStatus
	for tag, cfg := range f.singboxConfigs {
		ts := TunnelStatus{
			TunnelID:      tag,
			TunnelName:    tag, // можно будет улучшить позже
			Enabled:       cfg.Enabled,
			Backend:       "singbox",
			Status:        "disabled",
			Method:        "delay",
			FailThreshold: cfg.FailThreshold,
		}
		if cfg.Enabled {
			f.singboxMonMu.Lock()
			mon, active := f.singboxMonitors[tag]
			f.singboxMonMu.Unlock()
			if active {
				ts.FailCount = mon.failCount
				if mon.failCount >= cfg.FailThreshold {
					ts.Status = "recovering"
				} else {
					ts.Status = "alive"
				}
			} else {
				ts.Status = "stopped"
			}
		}
		result = append(result, ts)
	}
	return result
}

// startNwgMonitor creates and starts a poll-based nwgMonitor for the given tunnel.
// Skipped if the nwgSource is nil (NativeWG unavailable) or PingCheck is not enabled.
// Not safe for concurrent calls with the same tunnelID — callers are single-threaded
// per tunnel (lifecycle hooks from service layer hold per-tunnel locks).
func (f *Facade) startNwgMonitor(tunnelID, tunnelName string) {
	if f.nwgSource == nil {
		return
	}

	stored, err := f.tunnels.Get(tunnelID)
	if err != nil || stored.PingCheck == nil || !stored.PingCheck.Enabled {
		return
	}

	interval := time.Duration(stored.PingCheck.Interval) * time.Second
	if interval < 5*time.Second {
		interval = 10 * time.Second
	}

	mon := &nwgMonitor{
		tunnelID:     tunnelID,
		tunnelName:   tunnelName,
		interval:     interval,
		threshold:    stored.PingCheck.FailThreshold,
		logBuffer:    f.custom.logBuffer,
		source:       f.nwgSource,
		latencyProbe: f.nwgLatencyProbe,
		bus:          f.bus,
		stopCh:       make(chan struct{}),
		triggerCh:    make(chan struct{}, 1),
	}

	// Extract and stop the old monitor (if any) outside the lock
	// to avoid holding the mutex during wg.Wait().
	f.nwgMonMu.Lock()
	old, hadOld := f.nwgMonitors[tunnelID]
	if hadOld {
		delete(f.nwgMonitors, tunnelID)
	}
	f.nwgMonMu.Unlock()

	if hadOld {
		old.stop()
	}

	mon.wg.Add(1)
	go mon.run(f.ctx)

	f.nwgMonMu.Lock()
	f.nwgMonitors[tunnelID] = mon
	f.nwgMonMu.Unlock()
}

// stopNwgMonitor stops and removes the nwgMonitor for the given tunnel.
func (f *Facade) stopNwgMonitor(tunnelID string) {
	f.nwgMonMu.Lock()
	mon, ok := f.nwgMonitors[tunnelID]
	if ok {
		delete(f.nwgMonitors, tunnelID)
	}
	f.nwgMonMu.Unlock()

	if ok {
		mon.stop()
	}
}

// configureNativeWGPingCheck creates/updates the NDMS ping-check profile
// for a running nativewg tunnel (called when pingcheck is toggled ON at runtime).
func (f *Facade) configureNativeWGPingCheck(tunnelID string) {
	if f.nwgOp == nil {
		return
	}
	stored, err := f.tunnels.Get(tunnelID)
	if err != nil {
		return
	}

	// If PingCheck is nil or disabled, skip configuration.
	if stored.PingCheck == nil || !stored.PingCheck.Enabled {
		return
	}

	pcCfg := ndms.PingCheckConfig{
		Host:           stored.PingCheck.Target,
		Mode:           stored.PingCheck.Method,
		MinSuccess:     stored.PingCheck.MinSuccess,
		UpdateInterval: stored.PingCheck.Interval,
		MaxFails:       stored.PingCheck.FailThreshold,
		Timeout:        stored.PingCheck.Timeout,
		Port:           stored.PingCheck.Port,
		Restart:        stored.PingCheck.Restart,
	}
	if pcCfg.MinSuccess == 0 {
		pcCfg.MinSuccess = 1
	}
	_ = f.nwgOp.ConfigurePingCheck(context.Background(), stored, pcCfg)
}

// removeNativeWGPingCheck removes the NDMS ping-check profile
// for a nativewg tunnel (called when pingcheck is toggled OFF at runtime).
func (f *Facade) removeNativeWGPingCheck(tunnelID string) {
	if f.nwgOp == nil {
		return
	}
	stored, err := f.tunnels.Get(tunnelID)
	if err != nil {
		return
	}
	if stored.PingCheck == nil || !stored.PingCheck.Enabled {
		return
	}
	_ = f.nwgOp.RemovePingCheck(context.Background(), stored)
}

// GetTunnelPingStatus returns lightweight ping status for a single tunnel.
// NativeWG: queries NDMS ping-check. Kernel: delegates to custom monitor loop.
func (f *Facade) GetTunnelPingStatus(tunnelID string) TunnelPingInfo {
	if f.isNativeWG(tunnelID) {
		return f.getNativeWGTunnelPingStatus(tunnelID)
	}
	return f.custom.GetTunnelPingStatus(tunnelID)
}

// getNativeWGTunnelPingStatus queries NDMS ping-check for a single NativeWG tunnel.
func (f *Facade) getNativeWGTunnelPingStatus(tunnelID string) TunnelPingInfo {
	if f.nwgOp == nil {
		return TunnelPingInfo{Status: "disabled"}
	}
	stored, err := f.tunnels.Get(tunnelID)
	if err != nil || stored.PingCheck == nil || !stored.PingCheck.Enabled {
		return TunnelPingInfo{Status: "disabled"}
	}

	status, err := f.nwgOp.GetPingCheckStatus(context.Background(), stored)
	if err != nil || !status.Exists {
		return TunnelPingInfo{Status: "disabled"}
	}

	info := TunnelPingInfo{
		FailCount:     status.FailCount,
		FailThreshold: status.MaxFails,
	}

	switch {
	case status.Status == "pass":
		info.Status = "alive"
	case status.Status == "fail" && status.FailCount > 0:
		info.Status = "recovering"
	case status.Status == "fail" && f.isNwgRestartDetected(tunnelID):
		// Post-restart: counters zeroed, NDMS hasn't confirmed recovery yet.
		info.Status = "recovering"
	default:
		info.Status = "alive"
	}

	return info
}

// GetLogs returns logs (kernel custom loop only, NDMS has no log history).
func (f *Facade) GetLogs() []LogEntry {
	return f.custom.GetLogs()
}

// GetTunnelLogs returns logs for a specific tunnel.
func (f *Facade) GetTunnelLogs(tunnelID string) []LogEntry {
	return f.custom.GetTunnelLogs(tunnelID)
}

// ClearLogs clears all logs.
func (f *Facade) ClearLogs() {
	f.custom.ClearLogs()
}

// CheckAllNow triggers immediate checks on every monitored tunnel.
// Kernel tunnels: synchronous check via the custom loop.
// NativeWG tunnels: pokes the nwgMonitor's poll loop so it hits NDMS on
// the next scheduler tick instead of waiting for its periodic timer —
// NDMS itself runs checks on its own schedule, but our delta→log
// translation only happens when we poll, so "Проверить" would appear to
// do nothing on NativeWG until the next natural tick. Pokes are
// non-blocking; coalesced via the triggerCh's 1-slot buffer.
func (f *Facade) CheckAllNow() {
	f.custom.CheckAllNow()

	f.nwgMonMu.Lock()
	monitors := make([]*nwgMonitor, 0, len(f.nwgMonitors))
	for _, m := range f.nwgMonitors {
		monitors = append(monitors, m)
	}
	f.nwgMonMu.Unlock()

	for _, m := range monitors {
		m.triggerPoll()
	}

	// Poke singbox monitors
	f.singboxMonMu.Lock()
	for _, m := range f.singboxMonitors {
		go m.runCheck(f.ctx)
	}
	f.singboxMonMu.Unlock()
}

// IsEnabled returns whether ping check is globally enabled.
func (f *Facade) IsEnabled() bool {
	return f.custom.IsEnabled()
}

// StartMonitoringAllRunning starts monitoring for all running tunnels.
// Kernel tunnels: custom loop. NativeWG tunnels: start poll-based nwgMonitor.
func (f *Facade) StartMonitoringAllRunning() {
	f.custom.StartMonitoringAllRunning()

	// Start NativeWG monitors for running tunnels with ping-check enabled.
	tunnels, err := f.tunnels.List()
	if err != nil {
		return
	}
	for _, t := range tunnels {
		if t.Backend != "nativewg" || t.PingCheck == nil || !t.PingCheck.Enabled {
			continue
		}
		f.startNwgMonitor(t.ID, t.Name)
	}

	// Start singbox monitors for enabled configs
	f.loadSingboxConfigsIfNeeded()
	for tag, cfg := range f.singboxConfigs {
		if cfg.Enabled {
			f.StartMonitoringByTag(tag, tag) // name = tag for now
		}
	}
}

// StopMonitoringAll stops all monitoring.
func (f *Facade) StopMonitoringAll() {
	f.custom.StopMonitoringAll()

	// Stop all NativeWG monitors.
	f.nwgMonMu.Lock()
	monitors := make(map[string]*nwgMonitor, len(f.nwgMonitors))
	for k, v := range f.nwgMonitors {
		monitors[k] = v
	}
	f.nwgMonitors = make(map[string]*nwgMonitor)
	f.nwgMonMu.Unlock()

	for _, mon := range monitors {
		mon.stop()
	}

	f.singboxMonMu.Lock()
	for tag, mon := range f.singboxMonitors {
		mon.stop()
		delete(f.singboxMonitors, tag)
	}
	f.singboxMonMu.Unlock()
}

// Stop stops all monitoring: cancels nwgMonitor goroutines, then stops the custom service.
func (f *Facade) Stop() {
	f.cancel()

	f.nwgMonMu.Lock()
	monitors := make([]*nwgMonitor, 0, len(f.nwgMonitors))
	for _, mon := range f.nwgMonitors {
		monitors = append(monitors, mon)
	}
	f.nwgMonitors = make(map[string]*nwgMonitor)
	f.nwgMonMu.Unlock()

	for _, mon := range monitors {
		mon.stop()
	}

	f.singboxMonMu.Lock()
	for _, mon := range f.singboxMonitors {
		mon.stop()
	}
	f.singboxMonitors = make(map[string]*singboxMonitor)
	f.singboxMonMu.Unlock()

	f.custom.Stop()
}
