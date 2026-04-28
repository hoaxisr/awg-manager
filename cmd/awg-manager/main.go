package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"log/slog"

	"github.com/hoaxisr/awg-manager/internal/accesspolicy"
	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/auth"
	"github.com/hoaxisr/awg-manager/internal/cleanup"
	"github.com/hoaxisr/awg-manager/internal/clientroute"
	"github.com/hoaxisr/awg-manager/internal/connectivity"
	"github.com/hoaxisr/awg-manager/internal/deviceproxy"
	"github.com/hoaxisr/awg-manager/internal/dnscheck"
	"github.com/hoaxisr/awg-manager/internal/dnsroute"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/hydraroute"
	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/managed"
	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	ndmsevents "github.com/hoaxisr/awg-manager/internal/ndms/events"
	ndmsmetrics "github.com/hoaxisr/awg-manager/internal/ndms/metrics"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	ndmstransport "github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/pingcheck"
	"github.com/hoaxisr/awg-manager/internal/rci"
	"github.com/hoaxisr/awg-manager/internal/routing"
	"github.com/hoaxisr/awg-manager/internal/server"
	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/staticroute"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/kmod"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
	"github.com/hoaxisr/awg-manager/internal/terminal"
	"github.com/hoaxisr/awg-manager/internal/testing"
	"github.com/hoaxisr/awg-manager/internal/traffic"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/backend"
	"github.com/hoaxisr/awg-manager/internal/tunnel/external"
	"github.com/hoaxisr/awg-manager/internal/tunnel/firewall"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ops"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
	"github.com/hoaxisr/awg-manager/internal/tunnel/state"
	"github.com/hoaxisr/awg-manager/internal/tunnel/systemtunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wg"
	"github.com/hoaxisr/awg-manager/internal/updater"
)

const (
	defaultDataDir = "/opt/etc/awg-manager"
	defaultWebRoot = "/opt/share/www/awg-manager"
	pidFile        = "/opt/var/run/awg-manager.pid"
)

// version is set via ldflags at build time
var version = "dev"

func main() {
	dataDir := flag.String("data-dir", defaultDataDir, "Data directory path")
	webRoot := flag.String("web-root", defaultWebRoot, "Path to static web files")
	showVersion := flag.Bool("version", false, "Show version and exit")
	cleanup := flag.Bool("cleanup", false, "Stop and delete all tunnels, then exit (for uninstall)")
	serviceAction := flag.String("service", "", "Service management (start|stop|restart|status)")
	forceBoot := flag.Bool("force-boot", false, "Simulate boot mode (for testing boot path on running router)")
	flag.Parse()

	// Ensure Go can find CA certificates on entware-based systems (Keenetic).
	// Must run before any HTTPS calls (kmod download, etc.).
	ensureCACerts()

	if *showVersion {
		fmt.Printf("awg-manager version %s\n", version)
		os.Exit(0)
	}

	// Cleanup mode: delete all tunnels and exit
	if *cleanup {
		runCleanup(*dataDir)
		os.Exit(0)
	}

	// Service management (start/stop/restart/status)
	if *serviceAction != "" {
		runService(*serviceAction, *dataDir, *webRoot)
		os.Exit(0)
	}

	if err := os.MkdirAll(*dataDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create data dir: %v\n", err)
		os.Exit(1)
	}

	uptime := getUptime()

	log := logger.New()
	defer log.Close()

	// Settings (load first to get server config)
	settingsStore := storage.NewSettingsStore(*dataDir)
	settings, err := settingsStore.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load settings: %v\n", err)
		os.Exit(1)
	}

	awgStore := storage.NewAWGTunnelStore(
		filepath.Join(*dataDir, "tunnels"),
		log,
	)

	// Fetch NDMS version info via RCI (single HTTP call, cached for all consumers).
	// At early boot, retry until NDMS responds (up to 30s).
	ndmsTimeout := time.Second // normal restart: single attempt
	if uptime > 0 && uptime < 120 {
		ndmsTimeout = 30 * time.Second // boot: wait for NDMS
	}
	if err := ndmsinfo.Init(context.Background(), ndmsTimeout); err != nil {
		log.Warn("NDMS version info not available", map[string]interface{}{"error": err.Error()})
	}

	// Load kernel module if available (before backend detection)
	kmodLoader := kmod.New()

	// Clean up old SoC-based module directories from previous IPK versions
	kmodLoader.CleanupLegacyModules()
	// EnsureModule: select bundled .ko if available → insmod
	if err := kmodLoader.EnsureModule(context.Background()); err != nil {
		log.Warn("Kernel module not available", map[string]interface{}{"error": err.Error()})
	}

	// Logging service (created early — injected into tunnel service, pingcheck, dnsroute, operator, state, firewall, nwg)
	loggingService := logging.NewService(settingsStore)
	bootLog := logging.NewScopedLogger(loggingService, logging.GroupSystem, logging.SubBoot)
	defer loggingService.Stop()

	// === NEW NDMS LAYER (CQRS: query.Queries + command.Commands) ===
	// Transport + Queries are constructed early so downstream consumers
	// (state.Manager, routing.Catalog, etc.) can depend on them. Commands
	// + SaveCoordinator are constructed later (they depend on eventBus +
	// orchestrator).
	ndmsSem := ndmstransport.NewSemaphore(4)
	ndmsTransportClient := ndmstransport.New(ndmsSem)

	ndmsQueries := ndmsquery.NewQueries(ndmsquery.Deps{
		Getter: ndmsTransportClient,
		Logger: queryLogger(loggingService),
		IsOS5:  osdetect.Is5,
	})

	// Initialize SystemInfoStore at boot — one-shot fetch of /show/version.
	{
		initCtx, initCancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := ndmsQueries.SystemInfo.Init(initCtx); err != nil {
			log.Warnf("ndms sysinfo init: %v", err)
		}
		initCancel()
	}

	// Warm NDMS list caches before accepting clients so the first SSE snapshot
	// is not empty while the caches populate lazily. Failures are non-fatal:
	// the corresponding sections will appear in RoutingSnapshot.Missing and
	// the UI will prompt the user to retry.
	{
		warmCtx, warmCancel := context.WithTimeout(context.Background(), 15*time.Second)
		if _, err := ndmsQueries.Policies.List(warmCtx); err != nil {
			log.Warnf("ndms prewarm policies: %v", err)
		}
		if _, err := ndmsQueries.Hotspot.List(warmCtx); err != nil {
			log.Warnf("ndms prewarm hotspot: %v", err)
		}
		if _, err := ndmsQueries.Interfaces.List(warmCtx); err != nil {
			log.Warnf("ndms prewarm interfaces: %v", err)
		}
		if _, err := ndmsQueries.RunningConfig.Lines(warmCtx); err != nil {
			log.Warnf("ndms prewarm running-config: %v", err)
		}
		warmCancel()
	}

	// Create tunnel service components
	wgClient := wg.New()
	backendImpl := backend.New(log)
	stateMgr := state.New(ndmsQueries.Interfaces, wgClient, backendImpl, loggingService)
	firewallMgr := firewall.New(backendImpl.Type() == backend.TypeKernel, osdetect.Is5(), loggingService)

	// Build NDMS CQRS Commands eagerly so the Operator can consume them.
	// HookNotifier is wired later (ndmsCommands.SetHookNotifier(orch)) once
	// the orchestrator exists — this breaks the construction cycle.
	eventBus := events.NewBus()
	ndmsSaveCoord := ndmscommand.NewSaveCoordinator(
		ndmsTransportClient,
		eventBus,
		3*time.Second,
		10*time.Second,
	)
	ndmsCommands := ndmscommand.NewCommands(ndmscommand.Deps{
		Poster:       ndmsTransportClient,
		Save:         ndmsSaveCoord,
		Queries:      ndmsQueries,
		HookNotifier: nil, // wired after orchestrator construction below
		IsOS5:        osdetect.Is5,
	})

	operator := ops.NewOperator(ndmsQueries, ndmsCommands, wgClient, backendImpl, firewallMgr, log)

	// Create NativeWG operator
	rciClient := rci.New()
	nwgOp := nwg.NewOperator(log, ndmsQueries, ndmsCommands, ndmsTransportClient, rciClient, loggingService)

	// Load awg_proxy.ko if firmware < 5.1 Alpha 4
	if !ndmsinfo.SupportsWireguardASC() {
		if err := nwgOp.EnsureKmodLoaded(); err != nil {
			log.Warn("awg_proxy.ko not available", map[string]interface{}{"error": err.Error()})
		}
	}

	// Create WAN state model (populated at boot, updated by hooks).
	// Re-populate callback fires when a hook reports an unknown interface
	// (USB hotplug, new PPPoE configured after boot, etc.).
	wanModel := wan.NewModel()
	wanModel.SetRepopulateFn(func() {
		populateWANModel(context.Background(), ndmsQueries, wanModel, log)
	})

	// Create the main tunnel service
	tunnelService := service.New(awgStore, nwgOp, operator, stateMgr, log, wanModel, loggingService)

	// Migrate legacy ISPInterface="none" to "" (auto) for tunnels from older versions.
	tunnelService.MigrateISPInterfaceNone()
	tunnelService.MigrateEmptyBackend()

	// Routing catalog — unified tunnel listing for all routing subsystems
	catalog := routing.NewCatalog(
		&tunnelProviderAdapter{svc: tunnelService, store: awgStore},
		ndmsQueries.Interfaces,
		&storeAdapter{store: awgStore},
	)

	// HydraRoute Neo integration (optional — detected at startup)
	hydraService := hydraroute.NewService(catalog, log)
	geoDataStore := hydraroute.NewGeoDataStore(*dataDir)
	hydraService.SetGeoDataStore(geoDataStore)
	// Adopt any geo files already listed in hrneo.conf (e.g. added manually
	// before awg-manager was installed) so they show up in the UI. Adoption
	// is stat-only — TagCount is populated lazily in the background.
	if cfg, err := hydraroute.ReadConfig(); err == nil {
		if n, err := geoDataStore.AdoptExternalFiles(cfg); err != nil {
			log.Warn("Failed to adopt external geo files", map[string]interface{}{"error": err.Error()})
		} else if n > 0 {
			log.Info("Adopted external geo files from hrneo.conf", map[string]interface{}{"count": n})
		}
	}
	// Warm up tag cache for entries with TagCount=0 in a background goroutine.
	// Runs sequentially (one file at a time) to keep I/O pressure low — the
	// streaming parser holds ~64 KB RAM regardless of file size.
	go func() {
		for _, e := range geoDataStore.List() {
			if e.TagCount != 0 {
				continue
			}
			if _, err := geoDataStore.GetTags(e.Path); err != nil {
				log.Warn("Background tag parse failed", map[string]interface{}{
					"path":  e.Path,
					"error": err.Error(),
				})
			}
		}
	}()
	// NDMS wiring (SetQueries + SetPolicies) happens after ndmsCommands is
	// constructed — see below.

	// DNS route service (OS5 only — routes domains through tunnels via NDMS)
	// (constructed later, after ndmsCommands is available.)
	dnsRouteStore := dnsroute.NewStore(*dataDir)
	if _, err := dnsRouteStore.Load(); err != nil {
		log.Warn("Failed to load dns-routes", map[string]interface{}{"error": err.Error()})
	}

	hydraService.SetDnsListProvider(func() []hydraroute.DnsListInfo {
		data := dnsRouteStore.GetCached()
		if data == nil {
			return nil
		}
		var lists []hydraroute.DnsListInfo
		for _, list := range data.Lists {
			if list.Backend != "hydraroute" || !list.Enabled || len(list.Routes) == 0 {
				continue
			}
			lists = append(lists, hydraroute.DnsListInfo{
				TunnelID: list.Routes[0].TunnelID,
				Subnets:  list.Subnets,
			})
		}
		return lists
	})

	// Static route service for IP-based routing through tunnels
	// (constructed later, after ndmsCommands is available).
	staticRouteStore := storage.NewStaticRouteStore(*dataDir)

	// Create external tunnel service
	externalService := external.NewService(awgStore, settingsStore, tunnelService, log)

	// System WireGuard tunnels (read-only + ASC editing) — constructed later,
	// after ndmsQueries/ndmsCommands are available.
	var systemTunnelSvc *systemtunnel.ServiceImpl

	testService := testing.NewService(awgStore, log, loggingService)

	// Ping check service
	pingCheckService := pingcheck.NewService(settingsStore, awgStore, wgClient, log, loggingService)
	pingCheckService.Start()
	defer pingCheckService.Stop()

	// Auth components
	keeneticClient := auth.NewKeeneticClient()
	sessionStore := auth.NewSessionStore()
	sessionStore.SetLogger(log)
	defer sessionStore.Stop()

	operator.SetAppLogger(loggingService)

	// Traffic history (in-memory, 24h)
	trafficHistory := traffic.New()
	defer trafficHistory.Stop()

	// Updater service
	updaterService := updater.New(version, settingsStore, log, loggingService)
	updaterService.Start()
	defer updaterService.Stop()

	// Managed WireGuard server service — constructed after ndmsCommands is built.
	var managedService *managed.Service

	// Terminal manager (ttyd lifecycle)
	terminalManager := terminal.New(loggingService)

	// Autostart ttyd if already installed — silent, non-blocking.
	if terminalManager.IsInstalled(context.Background()) {
		if port, err := terminalManager.Start(context.Background()); err != nil {
			log.Warn("ttyd autostart failed", map[string]interface{}{"error": err.Error()})
		} else {
			log.Info("ttyd autostarted", map[string]interface{}{"port": port})
		}
	}

	// Client route service (per-device VPN routing)
	clientRouteStore := storage.NewClientRouteStore(*dataDir)
	clientRouteService := clientroute.New(
		clientRouteStore,
		operator,
		catalog,
		loggingService,
	)
	// Create orchestrator — single brain for all lifecycle decisions.
	orch := orchestrator.New(awgStore, operator, nwgOp, stateMgr, wanModel, log, loggingService)
	tunnelService.SetOrchestrator(orch)
	nwgOp.SetHookNotifier(orch) // operators register expected hooks before InterfaceUp/Down
	// OS5 kernel operator also uses ExpectHook (via OpkgTun two-layer arch).
	if os5Op, ok := operator.(interface {
		SetHookNotifier(tunnel.HookNotifier)
	}); ok {
		os5Op.SetHookNotifier(orch)
	}
	orch.SetSupportsASC(ndmsinfo.SupportsWireguardASC)
	// dnsRouteService wiring to orchestrator happens later, after ndmsCommands is built.
	orch.SetClientRoute(clientRouteService)

	// Wire HookNotifier for NDMS Commands — orchestrator exists now.
	ndmsCommands.SetHookNotifier(orch)

	// System WireGuard tunnels (read-only + ASC editing) — wired to NDMS CQRS layer.
	systemTunnelSvc = systemtunnel.New(ndmsQueries, ndmsCommands)

	// Managed WireGuard server service — wired to the new NDMS layer.
	managedService = managed.New(
		ndmsTransportClient,
		ndmsSaveCoord,
		ndmsQueries,
		ndmsCommands,
		settingsStore,
		slog.Default().With("component", "managed"),
		loggingService,
	)

	// Static route service — wired to NDMS RouteCommands.
	staticRouteService := staticroute.New(staticRouteStore, ndmsCommands.Routes, catalog, log, loggingService)
	orch.SetStaticRoute(staticRouteService)

	// DNS route service — wired to NDMS CQRS layer.
	dnsRouteService := dnsroute.NewService(dnsRouteStore, ndmsQueries, ndmsCommands, catalog, log, loggingService)

	// DNS route failover — switches DNS targets when pingcheck detects tunnel failure.
	dnsFailover := dnsroute.NewFailoverManager(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := dnsRouteService.Reconcile(ctx); err != nil {
			log.Warnf("dns failover reconcile: %v", err)
			return err
		}
		return nil
	})
	dnsRouteService.SetHydraRoute(hydraService)
	dnsRouteService.SetFailoverManager(dnsFailover)
	dnsFailover.SetLogger(log)
	dnsFailover.SetAffectedListsLookup(dnsRouteService.LookupAffectedLists)

	if osdetect.Is5() {
		orch.SetDNSRoute(dnsRouteService)
	}

	// DNS route subscription auto-refresh scheduler
	dnsRefreshScheduler := dnsroute.NewScheduler(dnsRouteService, settingsStore, log)
	dnsRefreshScheduler.Start()

	// Access policy service (NDMS ip policy management) — wired to CQRS layer.
	accessPolicySvc := accesspolicy.New(ndmsCommands.Policies, ndmsCommands.Interfaces, ndmsQueries, settingsStore, log, loggingService)

	// HydraRoute NDMS wiring — now that ndmsCommands/Queries are ready.
	hydraService.SetQueries(ndmsQueries)
	hydraService.SetPolicies(ndmsCommands.Policies)

	ndmsDispatcher := ndmsevents.NewDispatcher(ndmsQueries, eventsLogger(loggingService))

	// NDMS hook fired — invalidate all 7 routing-section polling stores.
	// Each client's storeRegistry.invalidateResource() triggers a fresh
	// REST GET for that section. No need to snapshot server-side anymore.
	ndmsDispatcher.SetRoutingChanged(func() {
		for _, key := range []string{
			api.ResourceRoutingDnsRoutes,
			api.ResourceRoutingStaticRoutes,
			api.ResourceRoutingAccessPolicies,
			api.ResourceRoutingPolicyDevices,
			api.ResourceRoutingPolicyInterfaces,
			api.ResourceRoutingClientRoutes,
			api.ResourceRoutingTunnels,
		} {
			eventBus.Publish("resource:invalidated", events.ResourceInvalidatedEvent{
				Resource: key,
				Reason:   "ndms-change",
			})
		}
	})

	ndmsDispatcher.Start()

	ndmsInstaller := ndmsevents.NewInstaller(eventsLogger(loggingService))
	if err := ndmsInstaller.Install(); err != nil {
		log.Warnf("ndms hook installer: %v", err)
	}

	ndmsRunningProvider := newRunningInterfacesAdapter(systemTunnelSvc, awgStore, settingsStore)

	ndmsMetricsPoller := ndmsmetrics.New(
		ndmsQueries.Peers,
		eventBus,
		ndmsRunningProvider,
		eventBus,
		metricsLogger(loggingService),
	)
	ndmsMetricsPoller.SetHistoryFeeder(trafficHistory)

	// Managed-tunnel metrics: read /sys/class/net/<iface>/statistics directly.
	// One poller per process — handles both kernel (opkgtun*, awgm*) and
	// nativewg (nwg*) tunnels. Runs alongside ndmsMetricsPoller, which now
	// serves only servers and non-managed system WG tunnels.
	sysfsTrafficPoller := traffic.NewSysfsPoller(
		tunnelService,
		trafficHistory,
		eventBus,
		metricsLogger(loggingService),
	)
	sysfsTrafficPoller.Start()

	orch.SetEventBus(eventBus)
	loggingService.SetEventBus(eventBus)
	tunnelService.SetEventBus(eventBus)

	// Stream geo-file download progress over SSE so the UI can show a
	// real progress bar instead of a guess.
	geoDataStore.SetProgressReporter(func(rawURL, fileType, phase string, downloaded, total int64, errMsg string) {
		eventBus.Publish("hydraroute:geo-progress", events.GeoDownloadProgressEvent{
			URL:        rawURL,
			FileType:   fileType,
			Downloaded: downloaded,
			Total:      total,
			Phase:      phase,
			Error:      errMsg,
		})
	})

	// Start DNS failover listener after event bus is wired
	dnsFailover.SetEventBus(eventBus)
	dnsFailover.StartListener(eventBus)
	defer dnsFailover.StopListener()

	// Traffic publishing is now handled by ndmsMetricsPoller (started by
	// Server.SetMetricsPoller wiring). It feeds trafficHistory and emits
	// tunnel:traffic + server:updated events via one ticker + narrow RCI.

	// Connectivity Monitor — periodically checks tunnel connectivity, publishes via SSE.
	connAdapter := connectivity.NewAdapter(tunnelService, awgStore, testService)
	connMonitor := connectivity.NewMonitor(eventBus, connAdapter, connAdapter, connAdapter)
	connMonitor.Start()
	defer connMonitor.Stop()

	// Sing-box integration
	singboxDir := filepath.Join(*dataDir, "singbox")
	singboxOp := singbox.NewOperator(singbox.OperatorDeps{
		Log:      slog.Default().With("component", "singbox"),
		Queries:  ndmsQueries,
		Commands: ndmsCommands,
		Dir:      singboxDir,
	})
	delayChecker := singbox.NewDelayChecker(singboxOp.Clash(), singboxOp, eventBus)

	// Unified facade: kernel → custom loop, NativeWG → NDMS native, Singbox → delay checker
	pingCheckFacade := pingcheck.NewFacade(pingCheckService, awgStore, settingsStore, nwgOp, singboxDir, delayChecker, loggingService)
	pingCheckFacade.SetNativeWGLatencyProbe(func(ctx context.Context, tunnelID string) int {
		res, err := testService.CheckConnectivity(ctx, tunnelID)
		if err != nil || res == nil || !res.Connected || res.Latency == nil {
			return pingcheck.LatencyNotAvailable
		}
		return *res.Latency
	})
	pingCheckFacade.SetEventBus(eventBus)
	// Удаляем осиротевшие конфиги мониторинга singbox
	if err := pingCheckFacade.CleanupOrphanedConfigs(context.Background()); err != nil {
		bootLog.Warn("pingcheck", "", "Failed to cleanup orphan singbox configs: "+err.Error())
	}
	orch.SetPingCheck(pingCheckFacade)

	singboxHandler := api.NewSingboxHandler(singboxOp, eventBus, delayChecker, testService, loggingService)
	singboxHandler.SetPingCheckService(pingCheckFacade)
	clashProxy := api.NewClashProxy(singboxOp)

	// Watchdog: runs an immediate reconcile (replacing the old one-shot
	// startup reconcile) and keeps checking every 30s. If sing-box crashes
	// while awgm is running, the next tick detects the dead PID and
	// restarts it; the UI is notified via resource:invalidated SSE hints
	// only when the running state actually flips.
	watchdogCtx, watchdogCancel := context.WithCancel(context.Background())
	defer watchdogCancel()
	go singbox.NewWatchdog(singboxOp, eventBus, slog.Default().With("component", "singbox-watchdog")).Run(watchdogCtx)

	trafficCtx, trafficCancel := context.WithCancel(context.Background())
	defer trafficCancel()
	go singbox.NewTrafficAggregator(singboxOp.Clash().Address(), eventBus).Run(trafficCtx)

	delayCtx, delayCancel := context.WithCancel(context.Background())
	defer delayCancel()
	go delayChecker.Run(delayCtx)

	// Forward sing-box runtime logs from clash_api /logs into the app's
	// UI log view (replaces the old file-based log; see process.go).
	logFwdCtx, logFwdCancel := context.WithCancel(context.Background())
	defer logFwdCancel()
	go singbox.NewLogForwarder(singboxOp.Clash().Address(), loggingService).Run(logFwdCtx)

	// Register routing snapshot providers with catalog. Each returns (data, err);
	// errors cause the section to appear in RoutingSnapshot.Missing so the UI can
	// show a "not loaded" state and offer a refresh action.
	catalog.SetSnapshotProvider("dnsRoutes", func(ctx context.Context) (interface{}, error) {
		return dnsRouteService.List(ctx)
	})
	catalog.SetSnapshotProvider("staticRoutes", func(ctx context.Context) (interface{}, error) {
		return staticRouteService.List()
	})
	catalog.SetSnapshotProvider("accessPolicies", func(ctx context.Context) (interface{}, error) {
		return accessPolicySvc.List(ctx)
	})
	catalog.SetSnapshotProvider("policyDevices", func(ctx context.Context) (interface{}, error) {
		return accessPolicySvc.ListDevices(ctx)
	})
	catalog.SetSnapshotProvider("policyInterfaces", func(ctx context.Context) (interface{}, error) {
		return accessPolicySvc.ListGlobalInterfaces(ctx)
	})
	catalog.SetSnapshotProvider("clientRoutes", func(ctx context.Context) (interface{}, error) {
		return clientRouteService.List()
	})
	catalog.SetSnapshotProvider("hydrarouteStatus", func(ctx context.Context) (interface{}, error) {
		return hydraService.GetStatus(), nil
	})

	srv := server.New(
		server.Config{
			Version: version,
			WebRoot: *webRoot,
		},
		log,
		tunnelService,
		externalService,
		testService,
		keeneticClient,
		sessionStore,
		settingsStore,
		awgStore,
		pingCheckFacade,
		loggingService,
		backendImpl,
		kmodLoader,
		updaterService,
		ndmsQueries,
		trafficHistory,
		dnsRouteService,
		staticRouteService,
		systemTunnelSvc,
		managedService,
		nwgOp,
		terminalManager,
		accessPolicySvc,
		clientRouteService,
		catalog,
		orch,
		eventBus,
		hydraService,
		singboxHandler,
		clashProxy,
	)

	srv.SetSingboxOperator(singboxOp)
	singboxOp.SetEventBus(eventBus)

	pingCancels := make(map[string]context.CancelFunc)
	singboxOp.OnTunnelEnabledChanged = func(tag string, enabled bool) {
		bootLog.Info("tunnel-enabled-changed", tag, fmt.Sprintf("enabled=%v", enabled))
		if enabled {
			ctx, cancel := context.WithCancel(context.Background())
			pingCancels[tag] = cancel
			go func() {
				time.Sleep(10 * time.Second)
				select {
				case <-ctx.Done():
					return
				default:
					pingCheckFacade.StartMonitoringByTag(tag, tag) // tag = name for now
				}
			}()
		} else {
			if cancel, ok := pingCancels[tag]; ok {
				cancel()
				delete(pingCancels, tag)
			}
			pingCheckFacade.StopMonitoringByTag(tag)
		}
	}

	// Device-proxy service — LAN-facing SOCKS/HTTP proxy managed through
	// sing-box. See docs/superpowers/specs/2026-04-24-device-proxy-design.md.
	deviceProxyStore := deviceproxy.NewStore(filepath.Join(*dataDir, "deviceproxy.json"))
	deviceProxySvc := deviceproxy.NewService(deviceproxy.Deps{
		Store:         deviceProxyStore,
		Tunnels:       awgStore,
		SystemTunnels: deviceproxy.NewSystemTunnelAdapter(systemTunnelSvc),
		Singbox:       deviceproxy.NewSingboxAdapter(singboxOp),
		NDMSQuery:     deviceproxy.NewNDMSAdapter(ndmsQueries),
		Bus:           eventBus,
	})
	deviceProxySvc.SetTunnelInboundPorts(func() []int {
		cfg, err := singboxOp.LoadCurrentConfig()
		if err != nil {
			return nil
		}
		ports := []int{}
		for _, t := range cfg.Tunnels() {
			if t.ListenPort > 0 {
				ports = append(ports, t.ListenPort)
			}
		}
		return ports
	})
	deviceProxyUnsub := deviceProxySvc.SubscribeBus(context.Background())
	defer deviceProxyUnsub()

	// Initial reconcile on boot — idempotent, brings config.json in sync
	// with storage + current tunnel set.
	if err := deviceProxySvc.Reconcile(context.Background()); err != nil {
		log.Warn("deviceproxy: initial reconcile failed", map[string]interface{}{"err": err})
	}

	srv.SetDeviceProxyService(deviceProxySvc)
	srv.SetNDMSDispatcher(ndmsDispatcher)
	srv.SetNDMSTransport(ndmsTransportClient)
	srv.SetNDMSSaveCoordinator(ndmsSaveCoord)
	srv.SetMetricsPoller(ndmsMetricsPoller)

	// Boot status: 0 = booting, 1 = done. Used by /api/system/info.
	var bootDone int32
	srv.SetBootStatusFunc(func() bool { return atomic.LoadInt32(&bootDone) == 0 })

	// Determine bind IP from settings
	bindIface := settings.Server.Interface
	ip := getInterfaceIP(bindIface)
	if ip == "" {
		fmt.Fprintf(os.Stderr, "Warning: could not get IP for interface %s, binding to all interfaces\n", bindIface)
		ip = "0.0.0.0"
	}

	// Get port from settings, with fallback logic
	selectedPort := settings.Server.Port
	if selectedPort == 0 || !isPortFree(selectedPort) {
		var err error
		selectedPort, err = srv.FindFreePort(settings.Server.Port)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to find free port: %v\n", err)
			os.Exit(1)
		}
	}

	// Persist actual port in settings so postinst / status / hooks show the right URL.
	if selectedPort != settings.Server.Port {
		fmt.Fprintf(os.Stderr, "Warning: port %d occupied, using port %d\n", settings.Server.Port, selectedPort)
		settings.Server.Port = selectedPort
		_ = settingsStore.Save(settings)
	}

	listenAddr := fmt.Sprintf("%s:%d", ip, selectedPort)
	srv.SetListenAddr(listenAddr)

	// Add loopback listener for reverse proxy support (nginx on 127.0.0.1)
	if ip != "0.0.0.0" && ip != "127.0.0.1" {
		srv.SetLoopbackAddr(fmt.Sprintf("127.0.0.1:%d", selectedPort))
	}

	// DNS routing diagnostics
	dnsCheckService := dnscheck.NewService(
		ndmsTransportClient,
		&dnsRouteCountAdapter{store: dnsRouteStore},
		&runningTunnelAdapter{svc: tunnelService},
		log,
	)
	dnsCheckService.EnsureIPHost(context.Background())
	srv.SetDnsCheckService(dnsCheckService)

	logStartup(bootLog, version, string(osdetect.Get()), listenAddr, settings)

	// Shutdown context — cancelled on shutdown
	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
	defer shutdownCancel()

	// Register shutdown hooks for graceful cleanup before syscall.Exec restart.
	srv.AddShutdownHook(shutdownCancel)
	if !ndmsinfo.SupportsWireguardASC() {
		srv.AddShutdownHook(func() {
			nwgOp.KmodManager().RemoveAllTunnels()
		})
	}
	srv.AddShutdownHook(pingCheckService.Stop)
	srv.AddShutdownHook(dnsRefreshScheduler.Stop)
	srv.AddShutdownHook(sessionStore.Stop)
	srv.AddShutdownHook(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := ndmsSaveCoord.Flush(ctx); err != nil {
			log.Warnf("ndms saveCoord flush on shutdown: %v", err)
		}
	})
	srv.AddShutdownHook(ndmsDispatcher.Stop)
	srv.AddShutdownHook(sysfsTrafficPoller.Stop)
	srv.AddShutdownHook(ndmsMetricsPoller.Stop)
	srv.AddShutdownHook(loggingService.Stop)
	srv.AddShutdownHook(trafficHistory.Stop)
	srv.AddShutdownHook(func() { terminalManager.Shutdown(context.Background()) })

	// Фоновая очистка сирот singbox pingcheck каждые 5 минут
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := pingCheckFacade.CleanupOrphanedConfigs(context.Background()); err != nil {
					bootLog.Warn("pingcheck", "", "Periodic orphan cleanup failed: "+err.Error())
				}
			case <-shutdownCtx.Done():
				return
			}
		}
	}()

	// Boot vs restart detection
	uptime = getUptime()
	const bootDetectionMax = 300 // 5 minutes
	isBoot := (uptime > 0 && uptime < bootDetectionMax) || *forceBoot
	if isBoot {
		bootLog.Info("startup", "",
			fmt.Sprintf("Boot detected (uptime %ds), starting tunnels", int(uptime)))

		go func() {

			// Wait for NDMS to fully initialize interface subsystem.
			// Without this delay, tunnels enter start/stop loops because
			// NDMS cycles their conf layer between running/disabled.
			const minBootUptime = 120 // seconds
			if uptime < float64(minBootUptime) {
				waitSec := int(float64(minBootUptime) - uptime)
				bootLog.Info("startup", "",
					fmt.Sprintf("Waiting %ds for NDMS initialization (uptime %ds, target %ds)", waitSec, int(uptime), minBootUptime))
				select {
				case <-time.After(time.Duration(waitSec) * time.Second):
				case <-shutdownCtx.Done():
					return
				}
			}

			// Clean up stale userspace PID files (kernel doesn't need cleanup —
			// bootTunnels handles it via lifecycle-based Start).
			if backendImpl.Type() != backend.TypeKernel {
				cleanupStaleUserspaceState(log)
			}

			// Seed WAN model with current interface state from NDMS.
			// Must happen before tunnel start so ISP resolution works.
			populateWANModel(shutdownCtx, ndmsQueries, wanModel, log)

			// Migrate legacy NDMS ID values to kernel names (one-time after model is populated).
			tunnelService.MigrateISPInterfaceToKernel()

			// Detect actual WAN state.
			if _, err := ndmsQueries.Routes.GetDefaultGatewayInterface(shutdownCtx); err != nil {
				bootLog.Info("startup", "",
					"WAN down at boot — waiting for WAN UP event")
			} else {
				orch.LoadState(shutdownCtx)
				orch.HandleEvent(shutdownCtx, orchestrator.Event{Type: orchestrator.EventBoot})

				// Restore monitoring for all backends (including singbox) after boot
				pingCheckFacade.StartMonitoringAllRunning()
			}

			atomic.StoreInt32(&bootDone, 1)
			bootLog.Info("startup", "", "Boot initialization complete")
		}()
	} else {
		atomic.StoreInt32(&bootDone, 1) // Not booting — mark done immediately.
		// Normal start (daemon restart / upgrade): reconnect to surviving processes.
		// syscall.Exec preserves child processes — amneziawg-go, TUN devices,
		// iptables rules, routes, NDMS config all survive. Only in-memory
		// operator maps (endpointRoutes, resolvedISP) need restoration.
		// PID files are valid (not stale) — do NOT call cleanupStaleUserspaceState.
		populateWANModel(context.Background(), ndmsQueries, wanModel, log)

		// Migrate legacy NDMS ID values to kernel names (one-time after model is populated).
		tunnelService.MigrateISPInterfaceToKernel()

		bootLog.Info("startup", "",
			"Daemon restart detected — reconnecting to running tunnels")

		orch.LoadState(context.Background())
		orch.HandleEvent(context.Background(), orchestrator.Event{Type: orchestrator.EventReconnect})

		// Restore monitoring for all backends (including singbox) after reconnect
		pingCheckFacade.StartMonitoringAllRunning()
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		os.Remove(pidFile)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()

	if err := srv.Start(); err != nil && err.Error() != "http: Server closed" {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}

// configSaver adapts *ndmscommand.SaveCoordinator to the cleanup.ConfigSaver
// interface (Save(ctx) error). Flush forces the debounced NDMS save to run
// synchronously, matching the contract expected by the cleanup service.
type configSaver struct {
	sc *ndmscommand.SaveCoordinator
}

func (c configSaver) Save(ctx context.Context) error {
	return c.sc.Flush(ctx)
}

// tunnelProviderAdapter adapts service.Service to routing.TunnelProvider.
type tunnelProviderAdapter struct {
	svc   service.Service
	store *storage.AWGTunnelStore
}

func (a *tunnelProviderAdapter) ListTunnels(ctx context.Context) ([]routing.TunnelWithStatus, error) {
	tunnels, err := a.svc.List(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]routing.TunnelWithStatus, len(tunnels))
	for i, t := range tunnels {
		entry := routing.TunnelWithStatus{
			ID:      t.ID,
			Name:    t.Name,
			Backend: t.Backend,
			State:   t.State,
		}
		// NativeWG tunnels need NWGIndex from storage.
		if t.Backend == "nativewg" {
			if stored, err := a.store.Get(t.ID); err == nil {
				entry.NWGIndex = stored.NWGIndex
			}
		}
		result[i] = entry
	}
	return result, nil
}

func (a *tunnelProviderAdapter) GetState(ctx context.Context, tunnelID string) tunnel.StateInfo {
	return a.svc.GetState(ctx, tunnelID)
}

func (a *tunnelProviderAdapter) WANModel() *wan.Model {
	return a.svc.WANModel()
}

// dnsRouteCountAdapter adapts dnsroute.Store to dnscheck.DnsRouteProvider.
type dnsRouteCountAdapter struct {
	store *dnsroute.Store
}

func (a *dnsRouteCountAdapter) ListEnabledCount(_ context.Context) (int, int) {
	data := a.store.GetCached()
	if data == nil {
		return 0, 0
	}
	total := len(data.Lists)
	enabled := 0
	for _, l := range data.Lists {
		if l.Enabled {
			enabled++
		}
	}
	return total, enabled
}

// runningTunnelAdapter adapts service.Service to dnscheck.TunnelStateProvider.
type runningTunnelAdapter struct {
	svc service.Service
}

func (a *runningTunnelAdapter) RunningTunnelNames(ctx context.Context) []string {
	list, err := a.svc.List(ctx)
	if err != nil {
		return nil
	}
	var names []string
	for _, t := range list {
		if t.State == tunnel.StateRunning {
			names = append(names, t.Name)
		}
	}
	return names
}

// storeAdapter adapts storage.AWGTunnelStore to routing.StoreClient.
type storeAdapter struct {
	store *storage.AWGTunnelStore
}

func (a *storeAdapter) Get(id string) (routing.StoreEntry, error) {
	t, err := a.store.Get(id)
	if err != nil {
		return routing.StoreEntry{}, err
	}
	return routing.StoreEntry{Backend: t.Backend, NWGIndex: t.NWGIndex}, nil
}

func (a *storeAdapter) Exists(id string) bool {
	return a.store.Exists(id)
}

// logStartup logs system startup information.
func logStartup(appLog *logging.ScopedLogger, version, osVersion, listenAddr string, settings *storage.Settings) {
	appLog.Info("startup", "", fmt.Sprintf("AWG Manager v%s started", version))
	appLog.Info("startup", "", fmt.Sprintf("Keenetic OS: %s", osVersion))
	appLog.Info("startup", "", fmt.Sprintf("Listening on %s", listenAddr))

	// Log feature status
	if settings.PingCheck.Enabled {
		appLog.Info("startup", "", "Ping Check: enabled")
	}
	if settings.Logging.Enabled {
		appLog.Info("startup", "", "Logging: enabled")
	}
}

// populateWANModel queries NDMS for current WAN interfaces and fills the
// unified WAN model so that AnyUp() works before any WAN hooks fire.
func populateWANModel(ctx context.Context, queries *ndmsquery.Queries, model *wan.Model, log *logger.Logger) {
	interfaces, err := queries.Interfaces.ListWAN(ctx)
	if err != nil {
		log.Warn("populateWANModel: failed to get WAN interfaces", map[string]interface{}{"error": err.Error()})
		return
	}
	model.Populate(interfaces)
	log.Info("Boot: WAN model populated", map[string]interface{}{"count": len(interfaces)})
}

// getInterfaceIP returns the first IPv4 address of the given interface.
func getInterfaceIP(ifaceName string) string {
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return ""
	}

	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}

	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok {
			if ip4 := ipnet.IP.To4(); ip4 != nil {
				return ip4.String()
			}
		}
	}
	return ""
}

// isPortFree checks if a port is available for binding.
func isPortFree(port int) bool {
	addr := fmt.Sprintf(":%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

// getUptime reads system uptime in seconds from /proc/uptime.
// Returns 0 on error (treated as non-boot scenario).
func getUptime() float64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0
	}
	uptime, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return uptime
}

// cleanupStaleUserspaceState removes stale PID files and sockets after router reboot.
// After reboot, /tmp (tmpfs) is wiped but PID files in /opt/var/run persist,
// and processes they reference no longer exist.
func cleanupStaleUserspaceState(log *logger.Logger) {
	pidDir := "/opt/var/run/awg-manager"

	entries, err := os.ReadDir(pidDir)
	if err != nil {
		return // Directory doesn't exist yet — nothing to clean
	}

	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".pid" {
			pidPath := filepath.Join(pidDir, e.Name())
			_ = os.Remove(pidPath)
			log.Info("reboot cleanup: removed stale PID file", map[string]interface{}{"file": e.Name()})
		}
	}
}

// runService handles --service flag: start/stop/restart/status.
// This replaces the shell logic that was previously in S99awg-manager.
func runService(action, dataDir, webRoot string) {
	switch action {
	case "start":
		serviceStart(dataDir, webRoot)
	case "stop":
		serviceStop()
	case "restart":
		serviceStop()
		time.Sleep(time.Second)
		serviceStart(dataDir, webRoot)
	case "status":
		serviceStatus(dataDir)
	default:
		fmt.Fprintf(os.Stderr, "Unknown service action: %s\nUsage: --service {start|stop|restart|status}\n", action)
		os.Exit(1)
	}
}

// serviceStart starts the daemon as a background process with PID file management.
func serviceStart(dataDir, webRoot string) {
	// Check if already running
	if pid, running := readPIDFile(); running {
		fmt.Printf("AWG Manager already running (PID %d)\n", pid)
		return
	}

	fmt.Println("Starting AWG Manager...")

	// Ensure directories
	os.MkdirAll("/opt/var/run", 0755)
	os.MkdirAll("/opt/var/log", 0755)
	os.MkdirAll(dataDir, 0755)

	// Resolve executable path
	executable, err := os.Executable()
	if err != nil {
		executable = os.Args[0]
	}

	// Ensure system binaries and libraries are available for child processes
	ensureServiceEnv()

	// Start the daemon without --service flag
	cmd := exec.Command(executable, "-data-dir", dataDir, "-web-root", webRoot)
	setServiceSysProcAttr(cmd)

	devNull, err := os.Open(os.DevNull)
	if err == nil {
		cmd.Stdout = devNull
		cmd.Stderr = devNull
		cmd.Stdin = devNull
		defer devNull.Close()
	}

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start AWG Manager: %v\n", err)
		os.Exit(1)
	}

	childPID := cmd.Process.Pid

	// Write PID file
	_ = os.WriteFile(pidFile, []byte(strconv.Itoa(childPID)+"\n"), 0644)

	// Detach from child — it becomes an orphan re-parented to init
	cmd.Process.Release()

	// Wait for process to start (up to 5 seconds)
	for i := 0; i < 5; i++ {
		time.Sleep(time.Second)
		if isProcessRunning(childPID) {
			host, port := getServiceEndpoint(dataDir)
			fmt.Printf("AWG Manager started: http://%s:%d\n", host, port)
			return
		}
	}

	fmt.Fprintln(os.Stderr, "AWG Manager failed to start")
	os.Remove(pidFile)
	os.Exit(1)
}

// serviceStop stops the running daemon via PID file.
func serviceStop() {
	pid, running := readPIDFile()
	if !running {
		fmt.Println("AWG Manager stopped")
		return
	}

	fmt.Println("Stopping AWG Manager...")

	process, err := os.FindProcess(pid)
	if err != nil {
		os.Remove(pidFile)
		fmt.Println("AWG Manager stopped")
		return
	}

	// Send SIGTERM for graceful shutdown
	_ = process.Signal(syscall.SIGTERM)

	// Wait up to 5 seconds for process to exit
	for i := 0; i < 5; i++ {
		time.Sleep(time.Second)
		if !isProcessRunning(pid) {
			break
		}
	}

	// Force kill if still running
	if isProcessRunning(pid) {
		_ = process.Signal(syscall.SIGKILL)
	}

	os.Remove(pidFile)
	fmt.Println("AWG Manager stopped")
}

// serviceStatus checks if the daemon is running and prints its endpoint.
func serviceStatus(dataDir string) {
	pid, running := readPIDFile()
	if !running {
		fmt.Println("AWG Manager not running")
		os.Exit(1)
	}

	host, port := getServiceEndpoint(dataDir)
	fmt.Printf("AWG Manager running (PID %d): http://%s:%d\n", pid, host, port)
}

// readPIDFile reads the PID file and checks if the process is alive.
// Returns the PID and whether the process is running.
func readPIDFile() (int, bool) {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	if !isProcessRunning(pid) {
		// Stale PID file
		os.Remove(pidFile)
		return 0, false
	}
	return pid, true
}

// isProcessRunning checks if a process with the given PID is an awg-manager instance.
// Reading /proc/<pid>/cmdline avoids false positives from PID reuse after reboot.
func isProcessRunning(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "awg-manager")
}

// getServiceEndpoint reads settings to determine the service host:port for display.
func getServiceEndpoint(dataDir string) (string, int) {
	port := 2222
	settingsFile := filepath.Join(dataDir, "settings.json")
	if data, err := os.ReadFile(settingsFile); err == nil {
		var s struct {
			Server struct {
				Port int `json:"port"`
			} `json:"server"`
		}
		if json.Unmarshal(data, &s) == nil && s.Server.Port > 0 {
			port = s.Server.Port
		}
	}

	// Use br0 (LAN bridge) for display — this is what the user connects from
	host := getInterfaceIP("br0")
	if host == "" {
		host = "192.168.1.1"
	}

	return host, port
}

// ensureCACerts sets SSL_CERT_FILE for entware-based systems (Keenetic) where
// CA certificates live in /opt/etc/ssl/ instead of standard Linux paths.
// Without this, Go's crypto/tls fails to verify GitHub (and other) certificates.
func ensureCACerts() {
	if os.Getenv("SSL_CERT_FILE") != "" {
		return
	}
	const entwareCert = "/opt/etc/ssl/certs/ca-certificates.crt"
	if _, err := os.Stat(entwareCert); err == nil {
		os.Setenv("SSL_CERT_FILE", entwareCert)
	}
}

// ensureServiceEnv ensures PATH contains system directories so child processes
// can find binaries by name. LD_LIBRARY_PATH is intentionally NOT set: forcing
// /lib:/usr/lib first poisons Entware binaries (curl/openssl) by making ld.so
// load incompatible system libraries → SIGSEGV/SIGBUS at runtime.
func ensureServiceEnv() {
	path := os.Getenv("PATH")
	if !strings.Contains(path, "/usr/sbin") {
		os.Setenv("PATH", "/bin:/sbin:/usr/bin:/usr/sbin:/opt/bin:/opt/sbin:"+path)
	}
}

// runCleanup removes all awg-manager resources and config files.
// Called during package uninstall (opkg remove).
func runCleanup(dataDir string) {
	fmt.Println("awg-manager cleanup: removing all managed resources...")

	log := logger.New()
	defer log.Close()

	// Init NDMS info (needed for OS detection)
	initCtx, initCancel := context.WithTimeout(context.Background(), 10*time.Second)
	_ = ndmsinfo.Init(initCtx, 10*time.Second)
	initCancel()

	settingsStore := storage.NewSettingsStore(dataDir)
	settingsStore.Load()

	awgStore := storage.NewAWGTunnelStore(filepath.Join(dataDir, "tunnels"), log)

	// Build minimal NDMS CQRS layer first — state.Manager consumes
	// Queries.Interfaces, and ProxyManager / dnsroute / accesspolicy share
	// the same transport + commands further down.
	cleanupEventBus := events.NewBus()
	cleanupNDMSTransport := ndmstransport.New(ndmstransport.NewSemaphore(4))
	cleanupNDMSQueries := ndmsquery.NewQueries(ndmsquery.Deps{
		Getter: cleanupNDMSTransport,
		Logger: nil,
		IsOS5:  osdetect.Is5,
	})

	// Create service components
	wgClient := wg.New()
	backendImpl := backend.New(log)
	stateMgr := state.New(cleanupNDMSQueries.Interfaces, wgClient, backendImpl, nil)
	firewallMgr := firewall.New(backendImpl.Type() == backend.TypeKernel, osdetect.Is5(), nil)

	// Build NDMS Commands early so the Operator can consume them. HookNotifier
	// is wired below once the orchestrator exists (see SetHookNotifier call).
	cleanupNDMSSave := ndmscommand.NewSaveCoordinator(cleanupNDMSTransport, cleanupEventBus, 3*time.Second, 10*time.Second)
	cleanupNDMSCommands := ndmscommand.NewCommands(ndmscommand.Deps{
		Poster:  cleanupNDMSTransport,
		Save:    cleanupNDMSSave,
		Queries: cleanupNDMSQueries,
		IsOS5:   osdetect.Is5,
	})

	operator := ops.NewOperator(cleanupNDMSQueries, cleanupNDMSCommands, wgClient, backendImpl, firewallMgr, log)
	cleanupRCI := rci.New()
	nwgOp := nwg.NewOperator(log, cleanupNDMSQueries, cleanupNDMSCommands, cleanupNDMSTransport, cleanupRCI, nil)
	tunnelService := service.New(awgStore, nwgOp, operator, stateMgr, log, wan.NewModel(), nil)

	// Wire orchestrator for lifecycle operations (Delete needs it)
	cleanupOrch := orchestrator.New(awgStore, operator, nwgOp, stateMgr, wan.NewModel(), log, nil)
	tunnelService.SetOrchestrator(cleanupOrch)
	nwgOp.SetHookNotifier(cleanupOrch)
	if os5Op, ok := operator.(interface {
		SetHookNotifier(tunnel.HookNotifier)
	}); ok {
		os5Op.SetHookNotifier(cleanupOrch)
	}
	// Wire HookNotifier on NDMS Commands now that the orchestrator exists.
	cleanupNDMSCommands.SetHookNotifier(cleanupOrch)

	// Create auxiliary services
	dnsStore := dnsroute.NewStore(dataDir)
	dnsStore.Load()
	// dnsSvc is constructed later, after cleanup NDMS CQRS layer is built.

	// Client route service for cleanup
	clientRouteStore := storage.NewClientRouteStore(dataDir)
	clientRouteSvc := clientroute.New(clientRouteStore, operator, nil, nil)

	// Managed WireGuard server — wired to the cleanup-path NDMS CQRS layer.
	managedSvc := managed.New(
		cleanupNDMSTransport,
		cleanupNDMSSave,
		cleanupNDMSQueries,
		cleanupNDMSCommands,
		settingsStore,
		slog.Default(),
		nil,
	)

	// DNS route service wired to cleanup NDMS CQRS layer (OS5 only — OS4
	// short-circuits inside reconcile via ErrNotSupportedOnOS4).
	dnsSvc := dnsroute.NewService(dnsStore, cleanupNDMSQueries, cleanupNDMSCommands, nil, log, nil)

	singboxOp := singbox.NewOperator(singbox.OperatorDeps{
		Log:      slog.Default().With("component", "singbox"),
		Queries:  cleanupNDMSQueries,
		Commands: cleanupNDMSCommands,
	})

	accessPolicySvc := accesspolicy.New(cleanupNDMSCommands.Policies, cleanupNDMSCommands.Interfaces, cleanupNDMSQueries, settingsStore, log, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Single cleanup call — all business logic in CleanupService
	cleanupSvc := cleanup.New(tunnelService, awgStore, dnsSvc, managedSvc, accessPolicySvc, clientRouteSvc, singboxOp, configSaver{sc: cleanupNDMSSave})
	if err := cleanupSvc.CleanupAll(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Cleanup error: %v\n", err)
	}

	// Remove all config/runtime files
	fmt.Println("Cleaning up files...")
	os.RemoveAll(filepath.Join(dataDir, "tunnels"))
	files, _ := filepath.Glob(filepath.Join(dataDir, "*.conf"))
	for _, f := range files {
		os.Remove(f)
	}
	os.Remove(filepath.Join(dataDir, "port"))
	os.Remove(filepath.Join(dataDir, "dns-routes.json"))
	os.RemoveAll("/opt/var/run/awg-manager")

	fmt.Println("Done.")
}
