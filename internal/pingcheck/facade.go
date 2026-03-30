package pingcheck

import (
	"context"
	"sort"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
)

// Facade unifies kernel (custom loop) and NativeWG (NDMS native) ping-check
// behind a single interface. All dispatch is based on stored.Backend.
type Facade struct {
	custom   *Service
	tunnels  *storage.AWGTunnelStore
	settings *storage.SettingsStore
	nwgOp    *nwg.OperatorNativeWG
}

// NewFacade creates a unified ping-check facade.
// nwgOp may be nil if NativeWG is unavailable.
func NewFacade(custom *Service, tunnels *storage.AWGTunnelStore, settings *storage.SettingsStore, nwgOp *nwg.OperatorNativeWG) *Facade {
	return &Facade{
		custom:   custom,
		tunnels:  tunnels,
		settings: settings,
		nwgOp:    nwgOp,
	}
}

func (f *Facade) isNativeWG(tunnelID string) bool {
	stored, err := f.tunnels.Get(tunnelID)
	if err != nil {
		return false
	}
	return stored.Backend == "nativewg"
}

// StartMonitoring starts monitoring for a tunnel.
// NativeWG: configures NDMS native ping-check profile.
// Kernel: delegates to custom loop.
func (f *Facade) StartMonitoring(tunnelID, tunnelName string) {
	if f.isNativeWG(tunnelID) {
		f.configureNativeWGPingCheck(tunnelID)
		return
	}
	f.custom.StartMonitoring(tunnelID, tunnelName)
}

// StopMonitoring stops monitoring for a tunnel.
// NativeWG: removes NDMS native ping-check profile.
// Kernel: delegates to custom loop.
func (f *Facade) StopMonitoring(tunnelID string) {
	if f.isNativeWG(tunnelID) {
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

	sort.Slice(result, func(i, j int) bool {
		return result[i].TunnelID < result[j].TunnelID
	})

	return result
}

// getNativeWGStatuses queries NDMS for ping-check status of all NativeWG tunnels.
func (f *Facade) getNativeWGStatuses() []TunnelStatus {
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

		// Tunnels without PingCheck config: show as disabled (user can toggle on)
		if t.PingCheck == nil {
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

		if !status.Exists || !t.PingCheck.Enabled {
			ts.Status = "disabled"
			ts.FailThreshold = 3
		} else {
			ts.FailThreshold = status.MaxFails
			ts.FailCount = status.FailCount
			ts.SuccessCount = status.SuccessCount

			switch status.Status {
			case "pass":
				ts.Status = "alive"
			case "fail":
				// NDMS keeps status="fail" after restart even when failCount
				// resets to 0.  With no active failures the tunnel is healthy.
				if status.FailCount > 0 {
					ts.Status = "recovering"
					ts.RestartCount = 1
				} else {
					ts.Status = "alive"
				}
			default:
				ts.Status = "alive" // pending/unknown → treat as alive
			}
		}

		result = append(result, ts)
	}

	return result
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

// getPingCheckDefaults returns default PingCheck config from global settings.
func (f *Facade) getPingCheckDefaults() *storage.TunnelPingCheck {
	if f.settings == nil {
		return nil
	}
	settings, err := f.settings.Get()
	if err != nil {
		return nil
	}
	defaults := settings.PingCheck.Defaults
	return &storage.TunnelPingCheck{
		Enabled:       true,
		Method:        defaults.Method,
		Target:        defaults.Target,
		Interval:      defaults.Interval,
		DeadInterval:  defaults.DeadInterval,
		FailThreshold: defaults.FailThreshold,
		MinSuccess:    1,
		Timeout:       5,
		Restart:       true,
	}
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
// NativeWG: queries NDMS. Kernel: delegates to custom loop.
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
	if err != nil {
		return TunnelPingInfo{Status: "disabled"}
	}
	if stored.PingCheck == nil || !stored.PingCheck.Enabled {
		return TunnelPingInfo{Status: "disabled"}
	}

	status, err := f.nwgOp.GetPingCheckStatus(context.Background(), stored)
	if err != nil {
		return TunnelPingInfo{Status: "disabled"}
	}

	if !status.Exists {
		return TunnelPingInfo{Status: "disabled"}
	}

	info := TunnelPingInfo{
		FailCount:     status.FailCount,
		FailThreshold: status.MaxFails,
	}

	switch status.Status {
	case "pass":
		info.Status = "alive"
	case "fail":
		// NDMS doesn't expose restart count; infer recovering from fail status.
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

// CheckAllNow triggers immediate checks (kernel only, NDMS checks on its own schedule).
func (f *Facade) CheckAllNow() {
	f.custom.CheckAllNow()
}

// IsEnabled returns whether ping check is globally enabled.
func (f *Facade) IsEnabled() bool {
	return f.custom.IsEnabled()
}

// StartMonitoringAllRunning starts monitoring for all running tunnels.
// Kernel tunnels: custom loop. NativeWG tunnels: skipped (NDMS manages).
func (f *Facade) StartMonitoringAllRunning() {
	f.custom.StartMonitoringAllRunning()
}

// StopMonitoringAll stops all monitoring (kernel custom loop only).
func (f *Facade) StopMonitoringAll() {
	f.custom.StopMonitoringAll()
}

// Stop stops the custom monitoring service.
func (f *Facade) Stop() {
	f.custom.Stop()
}
