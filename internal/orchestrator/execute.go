package orchestrator

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
	"github.com/hoaxisr/awg-manager/internal/tunnel/netutil"
)

// executeOne dispatches a single action to the appropriate executor.
func (o *Orchestrator) executeOne(ctx context.Context, action Action) error {
	switch action.Type {
	case ActionColdStartKernel:
		return o.executeColdStartKernel(ctx, action)
	case ActionStartNativeWG:
		return o.executeStartNativeWG(ctx, action)
	case ActionStopKernel:
		return o.executeStopKernel(ctx, action)
	case ActionStopNativeWG:
		return o.executeStopNativeWG(ctx, action)
	case ActionSuspendProxy:
		return o.executeSuspendProxy(ctx, action)
	case ActionRestoreKmod:
		return o.executeRestoreKmod(ctx, action)
	case ActionRestoreEndpointTracking:
		return o.executeRestoreEndpointTracking(ctx)
	case ActionLinkToggle:
		// Placeholder: pingcheck calls ip link down/up directly.
		return nil
	case ActionReconcileKernel:
		return o.executeReconcileKernel(ctx, action)
	case ActionSuspendKernel:
		return o.executeSuspendKernel(ctx, action)
	case ActionResumeKernel:
		return o.executeResumeKernel(ctx, action)

	// Monitoring
	case ActionStartMonitoring:
		if o.pingCheck == nil {
			return nil
		}
		stored, err := o.store.Get(action.Tunnel)
		if err != nil {
			return nil
		}
		// NativeWG: NDMS profile already configured by ActionConfigurePingCheck,
		// skip redundant configure to avoid double delete→create cycle.
		skipConfigure := stored.Backend == "nativewg"
		o.pingCheck.StartMonitoring(action.Tunnel, stored.Name, skipConfigure)
		return nil
	case ActionStopMonitoring:
		if o.pingCheck == nil {
			return nil
		}
		o.pingCheck.StopMonitoring(action.Tunnel)
		return nil
	case ActionConfigurePingCheck:
		if o.nwgOp == nil {
			return nil
		}
		stored, err := o.store.Get(action.Tunnel)
		if err != nil || stored.PingCheck == nil || !stored.PingCheck.Enabled {
			return nil
		}
		minSuccess := stored.PingCheck.MinSuccess
		if minSuccess == 0 {
			minSuccess = 1
		}
		pcCfg := ndms.PingCheckConfig{
			Host:           stored.PingCheck.Target,
			Mode:           stored.PingCheck.Method,
			MinSuccess:     minSuccess,
			UpdateInterval: stored.PingCheck.Interval,
			MaxFails:       stored.PingCheck.FailThreshold,
			Timeout:        stored.PingCheck.Timeout,
			Port:           stored.PingCheck.Port,
			Restart:        stored.PingCheck.Restart,
		}
		return o.nwgOp.ConfigurePingCheck(ctx, stored, pcCfg)
	case ActionRemovePingCheck:
		if o.nwgOp == nil {
			return nil
		}
		stored, err := o.store.Get(action.Tunnel)
		if err != nil {
			return nil
		}
		return o.nwgOp.RemovePingCheck(ctx, stored)

	// Routing
	case ActionApplyDNSRoutes, ActionReconcileDNSRoutes:
		if o.dnsRoute == nil {
			return nil
		}
		return o.dnsRoute.Reconcile(ctx)
	case ActionApplyStaticRoutes:
		if o.staticRoute == nil {
			return nil
		}
		return o.staticRoute.OnTunnelStart(ctx, action.Tunnel, action.Iface)
	case ActionRemoveStaticRoutes:
		if o.staticRoute == nil {
			return nil
		}
		return o.staticRoute.OnTunnelStop(ctx, action.Tunnel)
	case ActionReconcileStaticRoutes:
		if o.staticRoute == nil {
			return nil
		}
		return o.staticRoute.Reconcile(ctx)
	case ActionApplyClientRoutes:
		if o.clientRoute == nil {
			return nil
		}
		return o.clientRoute.OnTunnelStart(ctx, action.Tunnel, action.Iface)
	case ActionRemoveClientRoutes:
		if o.clientRoute == nil {
			return nil
		}
		return o.clientRoute.OnTunnelStop(ctx, action.Tunnel)

	// Delete-specific route cleanup: removes storage + NDMS routes before interface is destroyed.
	case ActionDeleteDNSRoutes:
		if o.dnsRoute == nil {
			return nil
		}
		return o.dnsRoute.OnTunnelDelete(ctx, action.Tunnel)
	case ActionDeleteStaticRoutes:
		if o.staticRoute == nil {
			return nil
		}
		return o.staticRoute.OnTunnelDelete(ctx, action.Tunnel)
	case ActionDeleteClientRoutes:
		if o.clientRoute == nil {
			return nil
		}
		return o.clientRoute.OnTunnelDelete(ctx, action.Tunnel)

	case ActionDeleteKernel:
		return o.executeDeleteKernel(ctx, action)
	case ActionDeleteNativeWG:
		return o.executeDeleteNativeWG(ctx, action)
	case ActionPersistRunning:
		return o.executePersistRunning(action)
	case ActionPersistStopped:
		return o.executePersistStopped(action)
	default:
		// Live config actions (ActionApplyConfig, ActionSetMTU, etc.) — not yet implemented.
		return nil
	}
}

// executeColdStartKernel creates a kernel tunnel from scratch.
// resolveWAN → writeConfigFile → build config → resolve endpoint IP →
// check address conflict → kernelOp.ColdStart → persist state.
func (o *Orchestrator) executeColdStartKernel(ctx context.Context, action Action) error {
	stored, err := o.store.Get(action.Tunnel)
	if err != nil {
		return tunnel.ErrNotFound
	}

	// Resolve WAN
	resolvedWAN, err := o.resolveWAN(ctx, stored.ISPInterface)
	if err != nil {
		return fmt.Errorf("resolve WAN: %w", err)
	}
	if stored.ISPInterface != "" && !tunnel.IsTunnelRoute(stored.ISPInterface) &&
		o.wanModel.Known(resolvedWAN) && !o.wanModel.IsUp(resolvedWAN) {
		return fmt.Errorf("WAN %s is down", resolvedWAN)
	}

	// Write config file
	if err := writeConfigFile(stored); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	// Build config
	cfg := storedToConfig(stored)
	cfg.ISPInterface = resolvedWAN
	cfg.KernelDevice = o.resolveKernelDevice(resolvedWAN)
	cfg.DefaultRoute = stored.DefaultRoute
	cfg.Endpoint = stored.Peer.Endpoint

	// Resolve endpoint IP
	ip, err := netutil.ResolveEndpointIP(stored.Peer.Endpoint)
	if err != nil {
		return fmt.Errorf("start %s: endpoint resolve failed: %w", action.Tunnel, err)
	}
	cfg.EndpointIP = ip

	// Check address conflict
	managedIfaces := collectManagedIfaceNames(o.store)
	if err := checkSystemAddressConflict(cfg.Address, cfg.AddressIPv6, managedIfaces); err != nil {
		return fmt.Errorf("start %s: %w", action.Tunnel, err)
	}

	// ColdStart
	if err := o.kernelOp.ColdStart(ctx, cfg); err != nil {
		return err
	}

	// Persist state
	stored.Enabled = true
	stored.ActiveWAN = resolvedWAN
	stored.StartedAt = time.Now().UTC().Format(time.RFC3339)
	if trackedIP := o.kernelOp.GetTrackedEndpointIP(action.Tunnel); trackedIP != "" {
		stored.ResolvedEndpointIP = trackedIP
	}
	if err := o.store.Save(stored); err != nil {
		o.logWarn(action.Tunnel, "failed to persist state: %s", err.Error())
	}

	o.logInfo(action.Tunnel, "kernel tunnel started")
	return nil
}

// executeReconcileKernel re-applies system config around an already-running kernel tunnel.
// Used after daemon restart when the kernel process and interface survived but
// firewall/DNS/routing state was lost. Calls operator.Reconcile which re-applies
// NDMS config + WG config + addresses + routing + firewall.
func (o *Orchestrator) executeReconcileKernel(ctx context.Context, action Action) error {
	stored, err := o.store.Get(action.Tunnel)
	if err != nil {
		return tunnel.ErrNotFound
	}

	// Resolve WAN
	resolvedWAN, err := o.resolveWAN(ctx, stored.ISPInterface)
	if err != nil {
		return fmt.Errorf("resolve WAN: %w", err)
	}

	// Build config
	cfg := storedToConfig(stored)
	cfg.ISPInterface = resolvedWAN
	cfg.KernelDevice = o.resolveKernelDevice(resolvedWAN)
	cfg.DefaultRoute = stored.DefaultRoute
	cfg.Endpoint = stored.Peer.Endpoint
	if stored.ResolvedEndpointIP != "" {
		cfg.EndpointIP = stored.ResolvedEndpointIP
	}

	// Reconcile (works with already-running process)
	if err := o.kernelOp.Reconcile(ctx, cfg); err != nil {
		return err
	}

	// Persist resolved WAN
	stored.ActiveWAN = resolvedWAN
	if trackedIP := o.kernelOp.GetTrackedEndpointIP(action.Tunnel); trackedIP != "" {
		stored.ResolvedEndpointIP = trackedIP
	}
	if err := o.store.Save(stored); err != nil {
		o.logWarn(action.Tunnel, "failed to persist state: %s", err.Error())
	}

	o.logInfo(action.Tunnel, "kernel tunnel reconciled")
	return nil
}

// executeSuspendKernel sets kernel tunnel link down without changing NDMS state.
// Used on WAN down — NDMS routing handles failover via auto flag.
// On WAN up, executeResumeKernel brings the link back up.
func (o *Orchestrator) executeSuspendKernel(ctx context.Context, action Action) error {
	if err := o.kernelOp.Suspend(ctx, action.Tunnel); err != nil {
		return err
	}
	o.logInfo(action.Tunnel, "kernel tunnel suspended")
	return nil
}

// executeResumeKernel sets kernel tunnel link up after a suspend.
// Used on WAN up — restores connectivity for tunnels that were suspended on WAN down.
func (o *Orchestrator) executeResumeKernel(ctx context.Context, action Action) error {
	if err := o.kernelOp.Resume(ctx, action.Tunnel); err != nil {
		return err
	}
	o.logInfo(action.Tunnel, "kernel tunnel resumed")
	return nil
}

// executeStartNativeWG starts a NativeWG tunnel via the NWG operator.
func (o *Orchestrator) executeStartNativeWG(ctx context.Context, action Action) error {
	if o.nwgOp == nil {
		return fmt.Errorf("NativeWG backend not available")
	}

	stored, err := o.store.Get(action.Tunnel)
	if err != nil {
		return tunnel.ErrNotFound
	}

	if err := o.nwgOp.Start(ctx, stored); err != nil {
		return err
	}

	// Persist runtime state. ResolveActiveWAN reads peer.via from RCI
	// (NDMS fills it with the actually selected WAN even when we passed
	// empty ISPInterface) and resolves it to a kernel name like "ppp0"
	// matching what /etc/ndm/iflayerchanged.d/50-awg-manager.sh sends in
	// WAN events. Empty result preserves the previous ActiveWAN — protects
	// against transient RCI failure when re-starting an already-running tunnel.
	stored.Enabled = true
	stored.StartedAt = time.Now().UTC().Format(time.RFC3339)
	if activeWAN := o.nwgOp.ResolveActiveWAN(ctx, stored); activeWAN != "" {
		stored.ActiveWAN = activeWAN
	}
	if err := o.store.Save(stored); err != nil {
		o.logWarn(action.Tunnel, "failed to persist state: %s", err.Error())
	}

	wan := stored.ActiveWAN
	if wan == "" {
		wan = "unknown"
	}
	o.logInfo(action.Tunnel, "NativeWG tunnel started (active WAN: %s)", wan)
	return nil
}

// executeStopKernel stops a kernel tunnel.
func (o *Orchestrator) executeStopKernel(ctx context.Context, action Action) error {
	if err := o.kernelOp.Stop(ctx, action.Tunnel); err != nil {
		return err
	}

	// Clear runtime state
	stored, err := o.store.Get(action.Tunnel)
	if err == nil {
		stored.Enabled = false
		stored.ActiveWAN = ""
		stored.StartedAt = ""
		_ = o.store.Save(stored)
	}

	o.logInfo(action.Tunnel, "kernel tunnel stopped")
	return nil
}

// executeStopNativeWG stops a NativeWG tunnel.
func (o *Orchestrator) executeStopNativeWG(ctx context.Context, action Action) error {
	if o.nwgOp == nil {
		return fmt.Errorf("NativeWG backend not available")
	}

	stored, err := o.store.Get(action.Tunnel)
	if err != nil {
		return tunnel.ErrNotFound
	}

	if err := o.nwgOp.Stop(ctx, stored); err != nil {
		return err
	}

	// Clear runtime state
	stored.Enabled = false
	stored.ActiveWAN = ""
	stored.StartedAt = ""
	_ = o.store.Save(stored)

	o.logInfo(action.Tunnel, "NativeWG tunnel stopped")
	return nil
}

// executeSuspendProxy suspends a NativeWG proxy (WAN down, keep conf: running).
func (o *Orchestrator) executeSuspendProxy(ctx context.Context, action Action) error {
	if o.nwgOp == nil {
		return fmt.Errorf("NativeWG backend not available")
	}

	stored, err := o.store.Get(action.Tunnel)
	if err != nil {
		return tunnel.ErrNotFound
	}

	return o.nwgOp.SuspendProxy(ctx, stored)
}

// executeRestoreKmod restores the kmod proxy entry for a running NativeWG tunnel at boot.
func (o *Orchestrator) executeRestoreKmod(ctx context.Context, action Action) error {
	if o.nwgOp == nil {
		return fmt.Errorf("NativeWG backend not available")
	}

	stored, err := o.store.Get(action.Tunnel)
	if err != nil {
		return tunnel.ErrNotFound
	}

	if err := o.nwgOp.RestoreKmodTunnel(ctx, stored); err != nil {
		return err
	}

	// Refresh ActiveWAN — at boot, the tunnel survived a router restart and
	// NDMS may have picked a different WAN than what was previously stored.
	if activeWAN := o.nwgOp.ResolveActiveWAN(ctx, stored); activeWAN != "" && stored.ActiveWAN != activeWAN {
		stored.ActiveWAN = activeWAN
		if err := o.store.Save(stored); err != nil {
			o.logWarn(action.Tunnel, "failed to persist refreshed ActiveWAN: %s", err.Error())
		}
		o.logInfo(action.Tunnel, "restored kmod tunnel, refreshed active WAN to %s", activeWAN)
	}

	return nil
}

// executeRestoreEndpointTracking restores endpoint route tracking for
// all running kernel tunnels on daemon restart.
func (o *Orchestrator) executeRestoreEndpointTracking(ctx context.Context) error {
	tunnels, err := o.store.List()
	if err != nil {
		return fmt.Errorf("list tunnels: %w", err)
	}

	restored := 0
	for _, t := range tunnels {
		// NativeWG: NDMS manages endpoint routing natively
		if t.Backend == "nativewg" {
			continue
		}
		// Skip if no endpoint
		if t.Peer.Endpoint == "" {
			continue
		}
		// Skip if not running
		stateInfo := o.stateMgr.GetState(ctx, t.ID)
		if stateInfo.State != tunnel.StateRunning {
			continue
		}

		// Restore tracking (route already exists in system)
		isp := t.ActiveWAN
		if isp == "" {
			// Migration: tunnel from older version without ActiveWAN
			if resolved, err := o.resolveWAN(ctx, t.ISPInterface); err == nil {
				isp = resolved
			} else {
				o.logWarn(t.ID, "no stored ActiveWAN, resolve failed: %s", err.Error())
			}
		}
		ip, err := o.kernelOp.RestoreEndpointTracking(ctx, t.ID, t.Peer.Endpoint, isp)
		if err != nil {
			o.logWarn(t.ID, "failed to restore endpoint tracking: %s", err.Error())
			continue
		}

		// Migration: fill ResolvedEndpointIP for tunnels from older versions
		if ip != "" && t.ResolvedEndpointIP == "" {
			t.ResolvedEndpointIP = ip
			if err := o.store.Save(&t); err != nil {
				o.logWarn(t.ID, "failed to persist state: %s", err.Error())
			}
			o.logInfo(t.ID, "migrated: persisted resolved endpoint IP %s", ip)
		}
		restored++
	}

	if restored > 0 {
		o.logInfo("daemon", "restored endpoint tracking for %d tunnel(s)", restored)
	}

	// Clean up stale ActiveWAN/StartedAt for dead tunnels
	for _, t := range tunnels {
		if t.ActiveWAN == "" && t.StartedAt == "" {
			continue
		}
		if t.Backend == "nativewg" {
			continue
		}
		stateInfo := o.stateMgr.GetState(ctx, t.ID)
		if !stateInfo.ProcessRunning {
			o.logInfo(t.ID, "clearing stale ActiveWAN/StartedAt (process dead)")
			o.store.ClearRuntimeState(t.ID)
		}
	}

	return nil
}

// executeDeleteKernel fully removes a kernel tunnel.
func (o *Orchestrator) executeDeleteKernel(ctx context.Context, action Action) error {
	stored, err := o.store.Get(action.Tunnel)
	if err != nil {
		return tunnel.ErrNotFound
	}

	if err := o.kernelOp.Delete(ctx, stored); err != nil {
		return err
	}

	confPath := tunnel.NewNames(action.Tunnel).ConfPath
	_ = os.Remove(confPath)

	if err := o.store.Delete(action.Tunnel); err != nil {
		return fmt.Errorf("delete from storage: %w", err)
	}

	o.cleanupTunnelLock(action.Tunnel)
	o.logInfo(action.Tunnel, "kernel tunnel deleted")
	return nil
}

// executeDeleteNativeWG fully removes a NativeWG tunnel.
func (o *Orchestrator) executeDeleteNativeWG(ctx context.Context, action Action) error {
	if o.nwgOp == nil {
		return fmt.Errorf("NativeWG backend not available")
	}

	stored, err := o.store.Get(action.Tunnel)
	if err != nil {
		return tunnel.ErrNotFound
	}

	if err := o.nwgOp.Delete(ctx, stored); err != nil {
		return err
	}

	confPath := filepath.Join(confDir, stored.ID+".conf")
	_ = os.Remove(confPath)

	if err := o.store.Delete(action.Tunnel); err != nil {
		return fmt.Errorf("delete from storage: %w", err)
	}

	o.cleanupTunnelLock(action.Tunnel)
	o.logInfo(action.Tunnel, "NativeWG tunnel deleted")
	return nil
}

// executePersistRunning persists enabled + runtime state for a tunnel
// that is confirmed running (e.g. after boot reconcile).
func (o *Orchestrator) executePersistRunning(action Action) error {
	stored, err := o.store.Get(action.Tunnel)
	if err != nil {
		return tunnel.ErrNotFound
	}

	stored.Enabled = true
	if action.WAN != "" {
		stored.ActiveWAN = action.WAN
	}
	if stored.StartedAt == "" {
		stored.StartedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if trackedIP := o.kernelOp.GetTrackedEndpointIP(action.Tunnel); trackedIP != "" {
		stored.ResolvedEndpointIP = trackedIP
	}

	if err := o.store.Save(stored); err != nil {
		return fmt.Errorf("persist running state: %w", err)
	}
	return nil
}

// executePersistStopped clears runtime state for a stopped tunnel.
func (o *Orchestrator) executePersistStopped(action Action) error {
	stored, err := o.store.Get(action.Tunnel)
	if err != nil {
		return tunnel.ErrNotFound
	}

	stored.Enabled = false
	stored.ActiveWAN = ""
	stored.StartedAt = ""

	if err := o.store.Save(stored); err != nil {
		return fmt.Errorf("persist stopped state: %w", err)
	}
	return nil
}

// checkSystemAddressConflict checks if ipv4 or ipv6 is already assigned to any
// system network interface. excludeIfaceNames are excluded from the check.
func checkSystemAddressConflict(ipv4, ipv6 string, excludeIfaceNames []string) error {
	if ipv4 == "" && ipv6 == "" {
		return nil
	}

	excludeSet := make(map[string]struct{}, len(excludeIfaceNames))
	for _, name := range excludeIfaceNames {
		excludeSet[name] = struct{}{}
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return nil // can't check — don't block start
	}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if _, ok := excludeSet[iface.Name]; ok {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP.String()

			if ipv4 != "" && ip == ipv4 {
				return fmt.Errorf("%w: address %s already assigned to interface %s", tunnel.ErrAddressInUse, ipv4, iface.Name)
			}
			if ipv6 != "" && ip == ipv6 {
				return fmt.Errorf("%w: address %s already assigned to interface %s", tunnel.ErrAddressInUse, ipv6, iface.Name)
			}
		}
	}

	return nil
}
