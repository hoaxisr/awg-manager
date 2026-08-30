package main

import (
	"context"
	"fmt"
	"time"

	"log/slog"

	"github.com/hoaxisr/awg-manager/internal/accesspolicy"
	"github.com/hoaxisr/awg-manager/internal/connectivity"
	"github.com/hoaxisr/awg-manager/internal/dnsroute"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/managed"
	"github.com/hoaxisr/awg-manager/internal/monitoring"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	ndmsevents "github.com/hoaxisr/awg-manager/internal/ndms/events"
	ndmsmetrics "github.com/hoaxisr/awg-manager/internal/ndms/metrics"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/staticroute"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
	"github.com/hoaxisr/awg-manager/internal/traffic"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/systemtunnel"
)

// setupOrchestrator creates the lifecycle orchestrator and everything wired
// through it: system tunnels, monitoring, managed WG server, static/DNS
// route services, failover, access policies and the NDMS event dispatcher.
func (a *app) setupOrchestrator() {
	// Create orchestrator — single brain for all lifecycle decisions.
	a.orch = orchestrator.New(a.awgStore, a.operator, a.nwgOp, a.stateMgr, a.wanModel, a.loggingService)
	a.tunnelService.SetOrchestrator(a.orch)
	a.nwgOp.SetHookNotifier(a.orch) // operators register expected hooks before InterfaceUp/Down
	// OS5 kernel operator also uses ExpectHook (via OpkgTun two-layer arch).
	if os5Op, ok := a.operator.(interface {
		SetHookNotifier(tunnel.HookNotifier)
	}); ok {
		os5Op.SetHookNotifier(a.orch)
	}
	a.orch.SetSupportsASC(ndmsinfo.SupportsWireguardASC)
	a.orch.SetPingCheck(a.pingCheckFacade)
	// dnsRouteService wiring to orchestrator happens later, after ndmsCommands is built.
	a.orch.SetClientRoute(a.clientRouteService)

	// Wire HookNotifier for NDMS Commands — orchestrator exists now.
	a.ndmsCommands.SetHookNotifier(a.orch)

	// System WireGuard tunnels (read-only + ASC editing) — wired to NDMS CQRS layer.
	a.systemTunnelSvc = systemtunnel.New(a.ndmsQueries, a.ndmsCommands)

	// Monitoring service (target × tunnel matrix probing). Constructed here so
	// it can include Keenetic-native (system) tunnels in the matrix via the
	// systemTunnelLister adapter.
	a.monitoringService = monitoring.NewService(monitoring.SchedulerDeps{
		TunnelLister:  a.tunnelService,
		TunnelStore:   a.awgStore,
		SettingsStore: a.settingsStore,
		SystemTunnels: &monitoringSystemTunnelAdapter{svc: a.systemTunnelSvc},
		Prober:        monitoring.NewTCPProber(),
		ICMPProber:    monitoring.NewICMPProber(),
		Log:           a.loggingService,
	})
	a.deferOnExit(a.monitoringService.Stop)

	// Managed WireGuard server service — wired to the new NDMS layer.
	a.managedService = managed.New(
		a.ndmsTransportClient,
		a.ndmsSaveCoord,
		a.ndmsQueries,
		a.ndmsCommands,
		a.settingsStore,
		slog.Default().With("component", "managed"),
		a.loggingService,
	)

	// Static route service — wired to NDMS RouteCommands.
	a.staticRouteService = staticroute.New(a.staticRouteStore, a.ndmsCommands.Routes, a.catalog, a.loggingService)
	a.orch.SetStaticRoute(a.staticRouteService)

	// DNS route service — wired to NDMS CQRS layer.
	a.dnsRouteService = dnsroute.NewService(a.dnsRouteStore, a.ndmsQueries, a.ndmsCommands, a.catalog, a.loggingService)

	// DNS route failover — switches DNS targets when pingcheck detects tunnel failure.
	a.dnsFailover = dnsroute.NewFailoverManager(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := a.dnsRouteService.Reconcile(ctx); err != nil {
			a.bootLog.Warn("dns-failover-reconcile", "", err.Error())
			return err
		}
		return nil
	})
	a.dnsFailover.SetLogger(dnsFailoverJournal{log: logging.NewScopedLogger(a.loggingService, logging.GroupRouting, logging.SubDnsRoute)})
	a.dnsRouteService.SetHydraRoute(a.hydraService)
	a.dnsRouteService.SetFailoverManager(a.dnsFailover)
	a.dnsFailover.SetAffectedListsLookup(a.dnsRouteService.LookupAffectedLists)

	if osdetect.Is5() {
		a.orch.SetDNSRoute(a.dnsRouteService)
	}

	// DNS route subscription auto-refresh scheduler
	a.dnsRefreshScheduler = dnsroute.NewScheduler(a.dnsRouteService, a.settingsStore, a.loggingService)

	// Access policy service (NDMS ip policy management) — wired to CQRS layer.
	a.accessPolicySvc = accesspolicy.New(a.ndmsCommands.Policies, a.ndmsCommands.Interfaces, a.ndmsQueries, a.settingsStore, a.loggingService, ndmsquery.NewPolicyMarkStore(a.ndmsTransportClient, nil))
	// Route SetInterfaceUp for managed tunnels through the orchestrator
	// lifecycle (NativeWG needs kmod proxy + endpoint rewrite, not a raw NDMS
	// flip — issue #183). System interfaces keep the raw flip.
	a.accessPolicySvc.SetTunnelLifecycle(orchLifecycleAdapter{a.orch}, storeManagedTunnelResolver{a.awgStore})

	// HydraRoute NDMS wiring — now that ndmsCommands/Queries are ready.
	a.hydraService.SetQueries(a.ndmsQueries)
	a.hydraService.SetPolicies(a.ndmsCommands.Policies)

	a.ndmsDispatcher = ndmsevents.NewDispatcher(a.ndmsQueries, eventsLogger(a.loggingService))

	// NDMS hook fired — invalidate all 7 routing-section polling stores.
	// Each client's storeRegistry.invalidateResource() triggers a fresh
	// REST GET for that section. No need to snapshot server-side anymore.
	a.ndmsDispatcher.SetRoutingChanged(func() {
		for _, key := range []events.Resource{
			events.ResourceRoutingDnsRoutes,
			events.ResourceRoutingStaticRoutes,
			events.ResourceRoutingAccessPolicies,
			events.ResourceRoutingPolicyDevices,
			events.ResourceRoutingPolicyInterfaces,
			events.ResourceRoutingClientRoutes,
			events.ResourceRoutingTunnels,
		} {
			a.eventBus.PublishInvalidated(key, "ndms-change")
		}
	})

	a.ndmsDispatcher.Start()

	ndmsInstaller := ndmsevents.NewInstaller(eventsLogger(a.loggingService))
	if err := ndmsInstaller.Install(); err != nil {
		a.bootLog.Warn("ndms-hook-installer", "", err.Error())
	}

}

// setupEventWiring connects the event bus consumers: metrics pollers,
// orchestrator hooks, geo-download progress, DNS failover listener and the
// connectivity monitor.
func (a *app) setupEventWiring() {
	ndmsRunningProvider := newRunningInterfacesAdapter(a.systemTunnelSvc, a.awgStore, a.settingsStore)

	a.ndmsMetricsPoller = ndmsmetrics.New(
		a.ndmsQueries.Peers,
		a.eventBus,
		ndmsRunningProvider,
		a.eventBus,
		metricsLogger(a.loggingService),
	)
	a.ndmsMetricsPoller.SetHistoryFeeder(a.trafficHistory)

	// Managed-tunnel metrics: read /sys/class/net/<iface>/statistics directly.
	// One poller per process — handles both kernel (opkgtun*, awgm*) and
	// nativewg (nwg*) tunnels. Runs alongside ndmsMetricsPoller, which now
	// serves only servers and non-managed system WG tunnels.
	a.sysfsTrafficPoller = traffic.NewSysfsPoller(
		a.tunnelService,
		a.trafficHistory,
		a.eventBus,
		metricsLogger(a.loggingService),
		a.loggingService,
	)
	a.sysfsTrafficPoller.Start()

	a.orch.SetEventBus(a.eventBus)
	// Refresh the NDMS interface cache when a kernel tunnel is confirmed up:
	// OpkgTun iflayerchanged hooks are unreliable, so the cache otherwise keeps
	// a frozen "down" snapshot and policy/WAN/all-interface lists misreport the
	// tunnel as down (#328). Async — Invalidate does a blocking HTTP.
	a.orch.SetInterfaceInvalidator(func(name string) { go a.ndmsQueries.Interfaces.Invalidate(name) })
	// Second-guess an external conf=disabled edge before stopping a tunnel:
	// FetchSummary goes to NDMS on every call, so it sees the interface as it
	// is now, not as the last hook left it (#669).
	a.orch.SetConfLayerProbe(func(ctx context.Context, name string) (bool, error) {
		details, err := a.ndmsQueries.Interfaces.FetchSummary(ctx, name)
		if err != nil || details == nil {
			return false, err
		}
		return details.Intent() == ndms.IntentUp, nil
	})
	// Full hr-neo restart on tunnel-running — NDMS assigns fwmarks only
	// during rci_create_policies (hr-neo startup), so tunnels appearing
	// after startup would miss CONNMARK rules without this.
	if a.hydraService != nil {
		a.orch.SetOnTunnelRunning(func(id string) {
			go a.hydraService.ScheduleRestart("tunnel-running: " + id)
		})
	}
	a.loggingService.SetEventBus(a.eventBus)
	a.tunnelService.SetEventBus(a.eventBus)
	a.pingCheckFacade.SetEventBus(a.eventBus)
	a.monitoringService.SetEventBus(a.eventBus)

	// Stream geo-file download progress over SSE so the UI can show a
	// real progress bar instead of a guess.
	a.geoDataStore.SetProgressReporter(func(rawURL, fileType, phase string, downloaded, total int64, errMsg string) {
		a.eventBus.Publish("hydraroute:geo-progress", events.GeoDownloadProgressEvent{
			URL:        rawURL,
			FileType:   fileType,
			Downloaded: downloaded,
			Total:      total,
			Phase:      phase,
			Error:      errMsg,
		})
	})

	// Start DNS failover listener after event bus is wired
	a.dnsFailover.SetEventBus(a.eventBus)
	a.dnsFailover.StartListener(a.eventBus)
	a.deferOnExit(a.dnsFailover.StopListener)

	// Traffic publishing is now handled by ndmsMetricsPoller (started by
	// Server.SetMetricsPoller wiring). It feeds trafficHistory and emits
	// tunnel:traffic + server:updated events via one ticker + narrow RCI.

	// Connectivity Monitor — handshake-trigger only. After a tunnel reaches
	// "running" + handshake we ask the monitoring scheduler for an immediate
	// matrix tick so cards show fresh latency without waiting up to 60s.
	// All actual probing happens inside monitoring.Scheduler.
	connAdapter := connectivity.NewAdapter(a.tunnelService)
	connMonitor := connectivity.NewMonitor(a.eventBus, a.monitoringService.Scheduler(), connAdapter, a.loggingService)
	connMonitor.Start()
	a.deferOnExit(connMonitor.Stop)

}

// dnsFailoverJournal адаптирует журнал приложения к минимальному
// Logger-интерфейсу failover-менеджера (до сих пор он подключался только
// в тестах — срабатывания failover в проде не оставляли следа).
type dnsFailoverJournal struct{ log *logging.ScopedLogger }

func (j dnsFailoverJournal) Warnf(format string, args ...interface{}) {
	j.log.Warn("failover", "", fmt.Sprintf(format, args...))
}

func (j dnsFailoverJournal) Infof(format string, args ...interface{}) {
	j.log.Info("failover", "", fmt.Sprintf(format, args...))
}
