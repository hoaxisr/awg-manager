package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/config"
	"github.com/hoaxisr/awg-manager/internal/tunnel/netutil"
)

// executeOne dispatches a single action to the appropriate executor.

// persistWarn логирует провал записи runtime-полей, кроме одного законного
// исхода: записи уже нет. На путях остановки, очистки и обхода снимка туннель
// мог быть удалён, пока шла долгая работа снаружи лока (ровно тот случай, под
// который заведён storage.ErrNotFound), — предупреждать тут не о чем, а Warn
// на штатном «остановили и удалили» приучал бы не читать журнал.
func (o *Orchestrator) persistWarn(tunnelID, what string, err error) {
	if errors.Is(err, storage.ErrNotFound) {
		return
	}
	o.appLog.Warn("persist-state", tunnelID, what+": "+err.Error())
}

func (o *Orchestrator) executeOne(ctx context.Context, action Action) error {
	switch action.Type {
	case ActionColdStartKernel:
		return o.executeColdStartKernel(ctx, action)
	case ActionStartNativeWG:
		return o.executeStartNativeWG(ctx, action)
	case ActionReconcileNativeWG:
		return o.executeReconcileNativeWG(ctx, action)
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
		return nil
	}
}

// executeColdStartKernel creates a kernel tunnel from scratch.
// resolveWAN → config.WriteFile → build config → resolve endpoint IP →
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
	if err := config.WriteFile(stored); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	// Build config
	cfg := StoredToConfig(stored)
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
	addrWarnings, addrErr := checkSystemAddressConflict(cfg.Address, cfg.AddressIPv6, managedIfaces)
	for _, w := range addrWarnings {
		o.appLog.Warn("address-conflict", action.Tunnel, w)
	}
	if addrErr != nil {
		return fmt.Errorf("start %s: %w", action.Tunnel, addrErr)
	}

	// ColdStart
	if err := o.kernelOp.ColdStart(ctx, cfg); err != nil {
		return err
	}

	// Persist state. Всё, что выше, шло по снимку и заняло секунды RCI —
	// правка карточки, приехавшая за это время, лежит в записи и обязана её
	// пережить: мутатор ставит ТОЛЬКО runtime-поля на свежее чтение.
	trackedIP := o.kernelOp.GetTrackedEndpointIP(action.Tunnel)
	startedAt := time.Now().UTC().Format(time.RFC3339)
	if err := o.store.Update(action.Tunnel, func(t *storage.AWGTunnel) error {
		t.Enabled = true
		t.ActiveWAN = resolvedWAN
		t.StartedAt = startedAt
		if trackedIP != "" {
			t.ResolvedEndpointIP = trackedIP
		}
		return nil
	}); err != nil {
		o.appLog.Warn("persist-state", action.Tunnel, "kernel start: "+err.Error())
	}

	o.appLog.Info("start", action.Tunnel, "kernel tunnel started")
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
	cfg := StoredToConfig(stored)
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
	trackedIP := o.kernelOp.GetTrackedEndpointIP(action.Tunnel)
	if err := o.store.Update(action.Tunnel, func(t *storage.AWGTunnel) error {
		t.ActiveWAN = resolvedWAN
		if trackedIP != "" {
			t.ResolvedEndpointIP = trackedIP
		}
		return nil
	}); err != nil {
		o.appLog.Warn("persist-state", action.Tunnel, "kernel reconcile: "+err.Error())
	}

	o.appLog.Info("reconcile", action.Tunnel, "kernel tunnel reconciled")
	return nil
}

// executeSuspendKernel sets kernel tunnel link down without changing NDMS state.
// Used on WAN down — NDMS routing handles failover via auto flag.
// On WAN up, executeResumeKernel brings the link back up.
func (o *Orchestrator) executeSuspendKernel(ctx context.Context, action Action) error {
	if err := o.kernelOp.Suspend(ctx, action.Tunnel); err != nil {
		return err
	}
	o.appLog.Info("suspend", action.Tunnel, "kernel tunnel suspended")
	return nil
}

// executeResumeKernel sets kernel tunnel link up after a suspend.
// Used on WAN up — restores connectivity for tunnels that were suspended on WAN down.
func (o *Orchestrator) executeResumeKernel(ctx context.Context, action Action) error {
	if err := o.kernelOp.Resume(ctx, action.Tunnel); err != nil {
		return err
	}
	o.appLog.Info("resume", action.Tunnel, "kernel tunnel resumed")
	o.refreshEndpointRouteAfterResume(ctx, action.Tunnel)
	return nil
}

// refreshEndpointRouteAfterResume приводит маршрут до endpoint после возврата
// линка.
//
// Resume — это ровно `ip link set up`, о маршрутах он не знает. А пока линк
// лежал, ядро вычистило всё, что вело через этот интерфейс, включая хост-
// маршрут до endpoint; вдобавок после передозвона (PPPoE/DHCP) шлюз может
// оказаться другим. Раньше маршрут не переигрывался до следующего полного
// старта туннеля.
//
// Канал при этом НЕ менялся: ActionResumeKernel выдаётся только туннелю с
// явной привязкой и только когда поднялся тот же самый интерфейс
// (canStartOnWAN). Смена канала — это ветка ISPInterface=="" → Reconcile,
// который маршрут обновляет сам.
//
// Отказ не фатален — см. контракт SetupEndpointRoute; туннель уже поднят, и
// ронять его из-за маршрута нельзя.
func (o *Orchestrator) refreshEndpointRouteAfterResume(ctx context.Context, tunnelID string) {
	stored, err := o.store.Get(tunnelID)
	if err != nil || stored.Peer.Endpoint == "" {
		return
	}
	resolvedWAN, err := o.resolveWAN(ctx, stored.ISPInterface)
	if err != nil {
		o.appLog.Warn("resume", tunnelID, "маршрут до endpoint не обновлён, WAN не разрешён: "+err.Error())
		return
	}
	ip, err := o.kernelOp.SetupEndpointRoute(ctx, tunnelID, stored.Peer.Endpoint,
		o.resolveKernelDevice(resolvedWAN), resolvedWAN)
	if err != nil {
		o.appLog.Warn("resume", tunnelID, "маршрут до endpoint не обновлён: "+err.Error())
		return
	}
	// Свежий адрес обязан осесть в записи — как это делают старт и реконсайл.
	// Иначе следующий холодный старт засеет маршрут протухшим IP из стора
	// (execute.go, ветка ColdStart), и туннель пойдёт через мёртвый шлюз.
	if err := o.store.Update(tunnelID, func(t *storage.AWGTunnel) error {
		t.ActiveWAN = resolvedWAN
		if ip != "" {
			t.ResolvedEndpointIP = ip
		}
		return nil
	}); err != nil {
		o.persistWarn(tunnelID, "resume endpoint route", err)
	}
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
	activeWAN := o.nwgOp.ResolveActiveWAN(ctx, stored)
	// Адрес — в ту же транзакцию: ActionPersistRunning доедет лишь через
	// несколько действий, а до тех пор соседний туннель с тем же target
	// читает из стора прежний адрес и решает по нему, чей это host-route.
	trackedIP := o.nwgOp.GetTrackedEndpointIP(action.Tunnel)
	startedAt := time.Now().UTC().Format(time.RFC3339)
	if err := o.store.Update(action.Tunnel, func(t *storage.AWGTunnel) error {
		t.Enabled = true
		t.StartedAt = startedAt
		if activeWAN != "" {
			t.ActiveWAN = activeWAN
		}
		if trackedIP != "" {
			t.ResolvedEndpointIP = trackedIP
		}
		return nil
	}); err != nil {
		o.appLog.Warn("persist-state", action.Tunnel, "nwg start: "+err.Error())
	}

	wan := activeWAN
	if wan == "" {
		wan = stored.ActiveWAN
	}
	if wan == "" {
		wan = "unknown"
	}
	o.appLog.Info("start", action.Tunnel, fmt.Sprintf("NativeWG started, active WAN: %s", wan))
	return nil
}

// executeReconcileNativeWG brings a non-ASC NativeWG tunnel to its desired
// state idempotently. If the tunnel is already running WITH a handshake we
// skip the disruptive restart (which churns NDMS conf edges and feeds the
// boot race). If it is NOT handshaking — including the #183 case where NDMS
// brought the interface up without our kmod proxy (conf=running, peer up,
// but no handshake) — we run the full Start to (re)attach the proxy.
func (o *Orchestrator) executeReconcileNativeWG(ctx context.Context, action Action) error {
	if o.nwgOp == nil {
		return fmt.Errorf("NativeWG backend not available")
	}
	stored, err := o.store.Get(action.Tunnel)
	if err != nil {
		return tunnel.ErrNotFound
	}

	info := o.nwgOp.GetState(ctx, stored)
	if info.State == tunnel.StateRunning && info.HasHandshake {
		// Рестарт пропускаем, но слот в ядре надо усыновить: менеджер
		// модуля свежий и пустой, а слот жив (awg_proxy.ko не выгружался).
		// Без усыновления не зарегистрирован endpoint-страж — защиты от
		// протухшего DDNS-адреса на этом пути нет, — а RemoveTunnel при
		// следующей остановке становится no-op, и слот остаётся в ядре
		// навсегда (#702). Гейт по supportsASC не нужен: действие выдаётся
		// только не-ASC-прошивкам (decideBoot).
		//
		// Ошибка усыновления не валит действие: это путь «туннель и так
		// работает», ронять его нельзя.
		if err := o.nwgOp.RestoreKmodTunnel(ctx, stored); err != nil {
			o.appLog.Warn("reconcile", action.Tunnel,
				"NativeWG already running with handshake — skip restart, but kmod slot adoption failed: "+err.Error())
			return nil
		}
		o.appLog.Info("reconcile", action.Tunnel, "NativeWG already running with handshake — skip restart, kmod slot adopted")
		return nil
	}

	return o.executeStartNativeWG(ctx, action)
}

// executeStopKernel stops a kernel tunnel.
func (o *Orchestrator) executeStopKernel(ctx context.Context, action Action) error {
	if err := o.kernelOp.Stop(ctx, action.Tunnel); err != nil {
		return err
	}

	// Clear runtime-only fields. User intent (Enabled=false) is persisted by
	// ActionPersistStopped; restart/reconnect paths deliberately omit it.
	if err := o.store.Update(action.Tunnel, func(t *storage.AWGTunnel) error {
		t.ActiveWAN = ""
		t.StartedAt = ""
		return nil
	}); err != nil {
		o.persistWarn(action.Tunnel, "kernel stop", err)
	}

	o.appLog.Info("stop", action.Tunnel, "kernel tunnel stopped")
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

	// Clear runtime-only fields. User intent (Enabled=false) is persisted by
	// ActionPersistStopped; restart/reconnect paths deliberately omit it.
	if err := o.store.Update(action.Tunnel, func(t *storage.AWGTunnel) error {
		t.ActiveWAN = ""
		t.StartedAt = ""
		return nil
	}); err != nil {
		o.persistWarn(action.Tunnel, "nwg stop", err)
	}

	o.appLog.Info("stop", action.Tunnel, "NativeWG tunnel stopped")
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

	// Refresh persisted runtime state in a single save:
	//  - ActiveWAN: at boot NDMS may have picked a different WAN than stored.
	//  - ResolvedEndpointIP: RestoreKmodTunnel resolved the endpoint (or used cache);
	//    persist a freshly-resolved IP so the next boot has a current fallback.
	activeWAN := o.nwgOp.ResolveActiveWAN(ctx, stored)
	trackedIP := o.nwgOp.GetTrackedEndpointIP(action.Tunnel)
	var wanRefreshed, ipRefreshed bool
	if err := o.store.Update(action.Tunnel, func(t *storage.AWGTunnel) error {
		wanRefreshed = activeWAN != "" && t.ActiveWAN != activeWAN
		ipRefreshed = trackedIP != "" && t.ResolvedEndpointIP != trackedIP
		if !wanRefreshed && !ipRefreshed {
			return storage.ErrNoChange
		}
		if wanRefreshed {
			t.ActiveWAN = activeWAN
		}
		if ipRefreshed {
			t.ResolvedEndpointIP = trackedIP
		}
		return nil
	}); err != nil {
		o.persistWarn(action.Tunnel, "refresh runtime state", err)
	}
	if wanRefreshed {
		o.appLog.Info("restore-kmod", action.Tunnel, fmt.Sprintf("active WAN refreshed to %s", activeWAN))
	}
	if ipRefreshed {
		o.appLog.Info("restore-kmod", action.Tunnel, "resolved endpoint IP refreshed to "+trackedIP)
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
		ip, err := o.kernelOp.RestoreEndpointTracking(ctx, t.ID, t.Peer.Endpoint)
		if err != nil {
			o.appLog.Warn("restore-endpoint-tracking", t.ID, err.Error())
			continue
		}

		// Migration: fill ResolvedEndpointIP for tunnels from older versions.
		// Снимок List — только дешёвый гейт; решает свежая запись в мутаторе.
		if ip != "" && t.ResolvedEndpointIP == "" {
			migrated := false
			if err := o.store.Update(t.ID, func(fresh *storage.AWGTunnel) error {
				if fresh.ResolvedEndpointIP != "" {
					return storage.ErrNoChange
				}
				fresh.ResolvedEndpointIP = ip
				migrated = true
				return nil
			}); err != nil {
				o.persistWarn(t.ID, "endpoint IP", err)
			}
			if migrated {
				o.appLog.Info("migrate", t.ID, "persisted resolved endpoint IP "+ip)
			}
		}
		restored++
	}

	if restored > 0 {
		o.appLog.Info("restore-endpoint-tracking", "daemon", fmt.Sprintf("%d tunnel(s)", restored))
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
			o.appLog.Info("clear-stale-state", t.ID, "process dead")
			if err := o.store.Update(t.ID, func(fresh *storage.AWGTunnel) error {
				if fresh.ActiveWAN == "" && fresh.StartedAt == "" {
					return storage.ErrNoChange
				}
				fresh.ActiveWAN = ""
				fresh.StartedAt = ""
				return nil
			}); err != nil {
				o.persistWarn(t.ID, "clear stale runtime state", err)
			}
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

	config.RemoveFile(action.Tunnel)

	if err := o.store.Delete(action.Tunnel); err != nil {
		return fmt.Errorf("delete from storage: %w", err)
	}

	o.appLog.Info("delete", action.Tunnel, "kernel tunnel deleted")
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

	config.RemoveFile(stored.ID)

	if err := o.store.Delete(action.Tunnel); err != nil {
		return fmt.Errorf("delete from storage: %w", err)
	}

	o.appLog.Info("delete", action.Tunnel, "NativeWG tunnel deleted")
	return nil
}

// executePersistRunning persists enabled + runtime state for a tunnel
// that is confirmed running (e.g. after boot reconcile).
func (o *Orchestrator) executePersistRunning(action Action) error {
	stored, err := o.store.Get(action.Tunnel)
	if err != nil {
		return tunnel.ErrNotFound
	}

	var trackedIP string
	if stored.Backend == "nativewg" {
		if o.nwgOp != nil {
			trackedIP = o.nwgOp.GetTrackedEndpointIP(action.Tunnel)
		}
	} else if o.kernelOp != nil {
		trackedIP = o.kernelOp.GetTrackedEndpointIP(action.Tunnel)
	}
	startedAt := time.Now().UTC().Format(time.RFC3339)

	if err := o.store.Update(action.Tunnel, func(t *storage.AWGTunnel) error {
		t.Enabled = true
		if t.StartedAt == "" {
			t.StartedAt = startedAt
		}
		if trackedIP != "" {
			t.ResolvedEndpointIP = trackedIP
		}
		return nil
	}); err != nil {
		return fmt.Errorf("persist running state: %w", err)
	}
	return nil
}

// executePersistStopped clears runtime state for a stopped tunnel.
func (o *Orchestrator) executePersistStopped(action Action) error {
	if err := o.store.Update(action.Tunnel, func(t *storage.AWGTunnel) error {
		t.Enabled = false
		t.ActiveWAN = ""
		t.StartedAt = ""
		return nil
	}); err != nil {
		return fmt.Errorf("persist stopped state: %w", err)
	}
	return nil
}

// hostIface — интерфейс хоста в том виде, в каком его читает проверка
// конфликта адресов: имя, состояние и уже разобранные адреса.
//
// Шов отдаёт ГОТОВЫЕ адреса, а не net.Interface, и это не украшательство:
// Addrs() у net.Interface ходит в ядро по индексу, подменить его тестом
// нечем, поэтому прежний шов умел ровно одно — «пустой список». Обе ветки
// severity ниже при таком шве непроверяемы, а цена ошибки в них — либо
// невидимый конфликт адресов, либо туннель, который перестал стартовать.
type hostIface struct {
	Name  string
	Up    bool
	Addrs []string
}

// listInterfaces — шов над net.Interfaces: тесты подставляют свой список,
// иначе проверка конфликта адресов читает интерфейсы хоста разработчика.
var listInterfaces = hostInterfaces

// hostInterfaces — прод-половина шва. Петля отсеивается здесь, чтобы у
// проверки остался один смысл на функцию.
func hostInterfaces() ([]hostIface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]hostIface, 0, len(ifaces))
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		ips := make([]string, 0, len(addrs))
		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok {
				ips = append(ips, ipNet.IP.String())
			}
		}
		out = append(out, hostIface{Name: iface.Name, Up: iface.Flags&net.FlagUp != 0, Addrs: ips})
	}
	return out, nil
}

// checkSystemAddressConflict checks if ipv4 or ipv6 is already assigned to any
// system network interface. excludeIfaceNames are excluded from the check.
//
// Severity разведена по состоянию чужого интерфейса, и это не косметика.
// Погашенный интерфейс адрес ДЕРЖИТ — с точки зрения занятости он ничем не
// отличается от поднятого, и прежний пропуск не-UP делал конфликт невидимым
// ровно до момента, когда сирота поднимется (сирота opkgtun10 с адресом живого
// opkgtun13 в дампе с роутера). Но поднятым он станет не сейчас, а отказать
// сейчас значит уронить туннель, который до обновления работал. Поэтому:
// поднятый чужой интерфейс — отказ, погашенный — предупреждение вызывающему,
// и старт продолжается.
func checkSystemAddressConflict(ipv4, ipv6 string, excludeIfaceNames []string) (warnings []string, err error) {
	if ipv4 == "" && ipv6 == "" {
		return nil, nil
	}

	excludeSet := make(map[string]struct{}, len(excludeIfaceNames))
	for _, name := range excludeIfaceNames {
		excludeSet[name] = struct{}{}
	}

	ifaces, err := listInterfaces()
	if err != nil {
		return nil, nil // can't check — don't block start
	}

	for _, iface := range ifaces {
		if _, ok := excludeSet[iface.Name]; ok {
			continue
		}
		for _, ip := range iface.Addrs {
			taken := ""
			switch {
			case ipv4 != "" && ip == ipv4:
				taken = ipv4
			case ipv6 != "" && ip == ipv6:
				taken = ipv6
			default:
				continue
			}
			if iface.Up {
				return warnings, fmt.Errorf("%w: address %s already assigned to interface %s", tunnel.ErrAddressInUse, taken, iface.Name)
			}
			warnings = append(warnings, fmt.Sprintf("address %s is also assigned to %s (interface is down); it will collide if that interface comes up", taken, iface.Name))
		}
	}

	return warnings, nil
}
