package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"log/slog"
	"runtime"

	"github.com/hoaxisr/awg-manager/frontend"
	"github.com/hoaxisr/awg-manager/internal/accesspolicy"
	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/auth"
	"github.com/hoaxisr/awg-manager/internal/cleanup"
	"github.com/hoaxisr/awg-manager/internal/clientroute"
	"github.com/hoaxisr/awg-manager/internal/connectivity"
	"github.com/hoaxisr/awg-manager/internal/deviceproxy"
	"github.com/hoaxisr/awg-manager/internal/diagnostics"
	"github.com/hoaxisr/awg-manager/internal/dnscheck"
	"github.com/hoaxisr/awg-manager/internal/dnsroute"
	"github.com/hoaxisr/awg-manager/internal/downloader"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/hydraroute"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/managed"
	"github.com/hoaxisr/awg-manager/internal/monitoring"
	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	ndmsevents "github.com/hoaxisr/awg-manager/internal/ndms/events"
	ndmsmetrics "github.com/hoaxisr/awg-manager/internal/ndms/metrics"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	ndmstransport "github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/pingcheck"
	"github.com/hoaxisr/awg-manager/internal/presets"
	"github.com/hoaxisr/awg-manager/internal/routing"
	"github.com/hoaxisr/awg-manager/internal/server"
	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/singbox/awgoutbounds"
	singboxcfg "github.com/hoaxisr/awg-manager/internal/singbox/configmerge"
	"github.com/hoaxisr/awg-manager/internal/singbox/dnsrewrite"
	"github.com/hoaxisr/awg-manager/internal/singbox/installer"
	singboxorch "github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/singbox/subscription"
	"github.com/hoaxisr/awg-manager/internal/staticroute"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/env"
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
	// pidFile lives on the system tmpfs (cleared on every boot) so an
	// unclean reboot can never leave a stale PID pointing at whatever
	// process eventually inherits that PID slot on the next uptime.
	// /var/run is FHS-canonical and always tmpfs on Keenetic; /opt/var/run
	// is Entware-persistent storage and was the source of the stale-PID
	// startup-block bug.
	pidFile = "/var/run/awg-manager.pid"
	// legacyPidFile is the pre-move location; one-shot cleanup on startup
	// removes it after an upgrade so the old file does not linger.
	legacyPidFile = "/opt/var/run/awg-manager.pid"
)

// version is set via ldflags at build time
var version = "dev"

// buildArch is set via ldflags at build time to one of the awg-manager
// architecture keys: "mipsel-3.4" | "mips-3.4" | "aarch64-3.10".
// Empty when running `go run` / `go build ./cmd/awg-manager` directly —
// detectArch() falls back to runtime.GOARCH-based mapping.
var buildArch string

// detectArch returns the awg-manager arch key for installer.EmbeddedBinaries.
// Prefers the build-time -X main.buildArch override; falls back to
// runtime.GOARCH for dev builds.
func detectArch() string {
	if buildArch != "" {
		return buildArch
	}
	switch runtime.GOARCH {
	case "mipsle":
		return "mipsel-3.4"
	case "mips":
		return "mips-3.4"
	case "arm64":
		return "aarch64-3.10"
	}
	return ""
}

// deviceproxySubscriptionOutboundsAdapter adapts *subscription.Service to
// the deviceproxy.SubscriptionOutboundsCatalog interface, exposing enabled
// subscription selector/urltest outbounds as device-proxy targets without
// creating a direct dependency from the subscription package on deviceproxy.
type deviceproxySubscriptionOutboundsAdapter struct {
	src *subscription.Service
}

func (a *deviceproxySubscriptionOutboundsAdapter) ListDeviceProxyOutbounds() []deviceproxy.SubscriptionOutboundInfo {
	if a == nil || a.src == nil {
		return nil
	}
	subs := a.src.List()
	out := make([]deviceproxy.SubscriptionOutboundInfo, 0, len(subs))

	for _, sub := range subs {
		if !sub.Enabled || sub.SelectorTag == "" || len(sub.MemberTags) == 0 {
			continue
		}
		label := strings.TrimSpace(sub.Label)
		if label == "" {
			label = sub.ID
		}
		active := sub.ActiveMember
		if active == "" && len(sub.MemberTags) > 0 {
			active = sub.MemberTags[0]
		}
		detail := active
		for _, m := range sub.Members {
			if m.Tag != active {
				continue
			}
			parts := []string{}
			if strings.TrimSpace(m.Label) != "" {
				parts = append(parts, strings.TrimSpace(m.Label))
			}
			if m.Protocol != "" {
				parts = append(parts, strings.ToUpper(m.Protocol))
			}
			if m.Server != "" {
				parts = append(parts, fmt.Sprintf("%s:%d", m.Server, m.Port))
			}
			if len(parts) > 0 {
				detail = strings.Join(parts, " · ")
			}
			break
		}
		out = append(out, deviceproxy.SubscriptionOutboundInfo{
			Tag:    sub.SelectorTag,
			Label:  label,
			Detail: detail,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Label < out[j].Label
	})
	return out
}

func main() {
	dataDir := flag.String("data-dir", defaultDataDir, "Data directory path")
	showVersion := flag.Bool("version", false, "Show version and exit")
	cleanup := flag.Bool("cleanup", false, "Stop and delete all tunnels, then exit (for uninstall)")
	serviceAction := flag.String("service", "", "Service management (start|stop|restart|status)")
	forceBoot := flag.Bool("force-boot", false, "Simulate boot mode (for testing boot path on running router)")
	pprofListen := flag.String("pprof-listen", "", "Dedicated TCP address for Go /debug/pprof only (recommended: 127.0.0.1:6060); empty disables standalone pprof")
	pprofOnMain := flag.Bool("pprof-on-main", false, "Also mount /debug/pprof/* on the main HTTP server (LAN/loopback listeners)")
	slowReqMS := flag.Int("slow-request-ms", 0, "Log HTTP handlers slower than this (ms) to stderr via slog (0 disables); long-lived SSE/WS routes are excluded")
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
		runService(*serviceAction, *dataDir)
		os.Exit(0)
	}

	if err := os.MkdirAll(*dataDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create data dir: %v\n", err)
		os.Exit(1)
	}

	// One-shot cleanup of the pre-move PID file. Older awgm wrote it to
	// /opt/var/run/awg-manager.pid (persistent Entware storage); after
	// the move to /var/run we never reference that path again, so remove
	// it so a stale upgrade artifact does not linger.
	_ = os.Remove(legacyPidFile)

	// Record the exact moment main() enters the daemon path so BootHealth
	// can compute uptime accurately. Must happen before any goroutines start.
	diagnostics.SetProcessStartedAt(time.Now())

	uptime := getUptime()

	// Settings (load first to get server config)
	settingsStore := storage.NewSettingsStore(*dataDir)
	settings, err := settingsStore.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load settings: %v\n", err)
		os.Exit(1)
	}

	awgStore := storage.NewAWGTunnelStore(
		filepath.Join(*dataDir, "tunnels"),
	)

	// Logging service (created early — injected into tunnel service, pingcheck, dnsroute, operator, state, firewall, nwg)
	loggingService := logging.NewService(settingsStore)
	defer loggingService.Stop()

	// bootLog: UI-visible scoped logger for all bootstrap diagnostics. Replaces
	// the legacy no-op *logger.Logger that used to silently drop everything.
	bootLog := logging.NewScopedLogger(loggingService, logging.GroupSystem, logging.SubBoot)

	// === NEW NDMS LAYER (CQRS: query.Queries + command.Commands) ===
	// Transport + Queries are constructed early so downstream consumers
	// (state.Manager, routing.Catalog, etc.) can depend on them. Commands
	// + SaveCoordinator are constructed later (they depend on eventBus +
	// orchestrator).
	ndmsSem := ndmstransport.NewSemaphore(env.IntDefault("AWG_NDMS_CAP", 30))
	ndmsTransportClient := ndmstransport.New(ndmsSem)
	ndmsTransportClient.SetAppLogger(loggingService)
	defer ndmsTransportClient.Close() // graceful batcher shutdown — финальный flush pending'а
	// ВРЕМЕННЫЙ perf-dumper: раз в минуту печатает RCI counters в app-log.
	// Удалить после анализа perf-сессии 2026-05-23.
	ndmsTransportClient.StartPerfDumper(context.Background(), time.Minute)

	ndmsQueries := ndmsquery.NewQueries(ndmsquery.Deps{
		Getter: ndmsTransportClient,
		Logger: queryLogger(loggingService),
		IsOS5:  osdetect.Is5,
	})

	// Initialize SystemInfoStore at boot — one-shot fetch of /show/version.
	// Compute timeout based on system uptime (wait longer at early boot).
	ndmsTimeout := time.Second // normal restart: single attempt
	if uptime > 0 && uptime < 120 {
		ndmsTimeout = 30 * time.Second // boot: wait for NDMS
	}
	// Wire ndmsinfo to the SystemInfoStore, then initialize with retry.
	// MUST run before kmod.New(): the kmod loader reads model/SoC from
	// ndmsinfo.Get() at construction time.
	if err := ndmsinfo.Init(context.Background(), ndmsQueries.SystemInfo, ndmsTimeout); err != nil {
		bootLog.Warn("ndms-version", "", err.Error())
	}

	// Load kernel module if available (before backend detection).
	// kmod.New() reads model/SoC from ndmsinfo, so it must run after Init above.
	kmodLoader := kmod.New()

	// Clean up old SoC-based module directories from previous IPK versions
	kmodLoader.CleanupLegacyModules()
	// EnsureModule: select bundled .ko if available → insmod
	if err := kmodLoader.EnsureModule(context.Background()); err != nil {
		bootLog.Warn("kmod", "", "kernel module not available: "+err.Error())
	}

	// Warm NDMS list caches before accepting clients so the first SSE snapshot
	// is not empty while the caches populate lazily. Failures are non-fatal:
	// the corresponding sections will appear in RoutingSnapshot.Missing and
	// the UI will prompt the user to retry.
	{
		warmCtx, warmCancel := context.WithTimeout(context.Background(), 15*time.Second)
		if _, err := ndmsQueries.Policies.List(warmCtx); err != nil {
			bootLog.Warn("ndms-prewarm", "policies", err.Error())
		}
		if _, err := ndmsQueries.Hotspot.List(warmCtx); err != nil {
			bootLog.Warn("ndms-prewarm", "hotspot", err.Error())
		}
		if _, err := ndmsQueries.Interfaces.List(warmCtx); err != nil {
			bootLog.Warn("ndms-prewarm", "interfaces", err.Error())
		}
		if _, err := ndmsQueries.RunningConfig.Lines(warmCtx); err != nil {
			bootLog.Warn("ndms-prewarm", "running-config", err.Error())
		}
		warmCancel()
	}

	// Create tunnel service components
	wgClient := wg.New()
	backendImpl := backend.New(bootLog)
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
		env.DurationDefault("AWG_NDMS_SAVE_SETTLE_DELAY", 2*time.Second),
		ndmsQueries.RunningConfig,
	)
	ndmsCommands := ndmscommand.NewCommands(ndmscommand.Deps{
		Poster:       ndmsTransportClient,
		Save:         ndmsSaveCoord,
		Queries:      ndmsQueries,
		HookNotifier: nil, // wired after orchestrator construction below
		IsOS5:        osdetect.Is5,
	})

	operator := ops.NewOperator(ndmsQueries, ndmsCommands, wgClient, backendImpl, firewallMgr)

	// Create NativeWG operator
	nwgOp := nwg.NewOperator(ndmsQueries, ndmsCommands, ndmsTransportClient, loggingService)

	// Load awg_proxy.ko if firmware < 5.1 Alpha 4
	if !ndmsinfo.SupportsWireguardASC() {
		if err := nwgOp.EnsureKmodLoaded(); err != nil {
			bootLog.Warn("kmod", "awg_proxy.ko", "not available: "+err.Error())
		}
	}

	// Create WAN state model (populated at boot, updated by hooks).
	// Re-populate callback fires when a hook reports an unknown interface
	// (USB hotplug, new PPPoE configured after boot, etc.).
	wanModel := wan.NewModel()
	wanModel.SetRepopulateFn(func() {
		populateWANModel(context.Background(), ndmsQueries, wanModel, bootLog)
	})

	// Create the main tunnel service
	tunnelService := service.New(awgStore, nwgOp, operator, stateMgr, wanModel, loggingService)

	// Migrate legacy ISPInterface="none" to "" (auto) for tunnels from older versions.
	tunnelService.MigrateISPInterfaceNone()
	tunnelService.MigrateEmptyBackend()

	// Routing catalog — unified tunnel listing for all routing subsystems
	catalog := routing.NewCatalog(
		&tunnelProviderAdapter{svc: tunnelService, store: awgStore},
		ndmsQueries.Interfaces,
		&storeAdapter{store: awgStore},
		loggingService,
	)

	// HydraRoute Neo integration (optional — detected at startup)
	hydraService := hydraroute.NewService(catalog, loggingService)
	geoDataStore := hydraroute.NewGeoDataStore(*dataDir)
	geoDataStore.SetAppLogger(loggingService)
	hydraService.SetGeoDataStore(geoDataStore)
	// Adopt any geo files already listed in hrneo.conf (e.g. added manually
	// before awg-manager was installed) so they show up in the UI. Adoption
	// is stat-only — TagCount is populated lazily in the background.
	if cfg, err := hydraroute.ReadConfig(); err == nil {
		if n, err := geoDataStore.AdoptExternalFiles(cfg); err != nil {
			bootLog.Warn("hrneo-adopt-geo", "", err.Error())
		} else if n > 0 {
			bootLog.Info("hrneo-adopt-geo", "", fmt.Sprintf("adopted %d files from hrneo.conf", n))
		}
		if err := hydraService.SyncGeoFilesToConfig(); err != nil {
			bootLog.Warn("hrneo-sync-geo", "", "failed after adopt: "+err.Error())
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
				bootLog.Warn("subscription-tag-parse", e.Path, err.Error())
			}
		}
	}()
	// NDMS wiring (SetQueries + SetPolicies) happens after ndmsCommands is
	// constructed — see below.

	// DNS route service (OS5 only — routes domains through tunnels via NDMS)
	// (constructed later, after ndmsCommands is available.)
	dnsRouteStore := dnsroute.NewStore(*dataDir)
	if _, err := dnsRouteStore.Load(); err != nil {
		bootLog.Warn("dns-routes-load", "", err.Error())
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

	// Unified preset catalog (U0: read-only, no CRUD yet)
	presetStore := presets.NewStore(*dataDir)
	presetCatalog := presets.NewCatalog(presetStore)

	// Create external tunnel service
	externalService := external.NewService(awgStore, settingsStore, tunnelService, loggingService)

	// System WireGuard tunnels (read-only + ASC editing) — constructed later,
	// after ndmsQueries/ndmsCommands are available.
	var systemTunnelSvc *systemtunnel.ServiceImpl

	testService := testing.NewService(awgStore, loggingService)
	testService.SetSettingsStore(settingsStore)

	// Ping check service
	pingCheckService := pingcheck.NewService(settingsStore, awgStore, wgClient, loggingService)
	pingCheckService.Start()
	defer pingCheckService.Stop()

	// Unified facade: kernel → custom loop, NativeWG → NDMS native
	pingCheckFacade := pingcheck.NewFacade(pingCheckService, awgStore, settingsStore, nwgOp)
	pingCheckFacade.SetNativeWGLatencyProbe(func(ctx context.Context, tunnelID string) int {
		res, err := testService.CheckConnectivity(ctx, tunnelID)
		if err != nil || res == nil || !res.Connected || res.Latency == nil {
			return pingcheck.LatencyNotAvailable
		}
		return *res.Latency
	})

	// monitoringService is constructed below after systemTunnelSvc is wired,
	// so the matrix can include Keenetic-native tunnels.
	var monitoringService *monitoring.Service

	// Auth components
	keeneticClient := auth.NewKeeneticClient()
	sessionStore := auth.NewSessionStore()
	defer sessionStore.Stop()

	operator.SetAppLogger(loggingService)

	// Traffic history (in-memory, 48h)
	trafficHistory := traffic.New()
	defer trafficHistory.Stop()

	// Updater service
	updaterService := updater.New(version, settingsStore, loggingService)
	updaterService.Start()
	defer updaterService.Stop()

	// Managed WireGuard server service — constructed after ndmsCommands is built.
	var managedService *managed.Service

	// Terminal manager (ttyd lifecycle)
	terminalManager := terminal.New(loggingService)

	// Autostart ttyd if already installed — silent, non-blocking.
	if terminalManager.IsInstalled(context.Background()) {
		if port, err := terminalManager.Start(context.Background()); err != nil {
			bootLog.Warn("ttyd-autostart", "", err.Error())
		} else {
			bootLog.Info("ttyd-autostart", "", fmt.Sprintf("started on port %d", port))
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
	orch := orchestrator.New(awgStore, operator, nwgOp, stateMgr, wanModel, loggingService)
	tunnelService.SetOrchestrator(orch)
	nwgOp.SetHookNotifier(orch) // operators register expected hooks before InterfaceUp/Down
	// OS5 kernel operator also uses ExpectHook (via OpkgTun two-layer arch).
	if os5Op, ok := operator.(interface {
		SetHookNotifier(tunnel.HookNotifier)
	}); ok {
		os5Op.SetHookNotifier(orch)
	}
	orch.SetSupportsASC(ndmsinfo.SupportsWireguardASC)
	orch.SetPingCheck(pingCheckFacade)
	// dnsRouteService wiring to orchestrator happens later, after ndmsCommands is built.
	orch.SetClientRoute(clientRouteService)

	// Wire HookNotifier for NDMS Commands — orchestrator exists now.
	ndmsCommands.SetHookNotifier(orch)

	// System WireGuard tunnels (read-only + ASC editing) — wired to NDMS CQRS layer.
	systemTunnelSvc = systemtunnel.New(ndmsQueries, ndmsCommands)

	// Monitoring service (target × tunnel matrix probing). Constructed here so
	// it can include Keenetic-native (system) tunnels in the matrix via the
	// systemTunnelLister adapter.
	monitoringService = monitoring.NewService(monitoring.SchedulerDeps{
		TunnelLister:  tunnelService,
		TunnelStore:   awgStore,
		SettingsStore: settingsStore,
		SystemTunnels: &monitoringSystemTunnelAdapter{svc: systemTunnelSvc},
		Prober:        monitoring.NewHTTPProber(),
		ICMPProber:    monitoring.NewICMPProber(),
		Log:           loggingService,
	})
	defer monitoringService.Stop()

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
	staticRouteService := staticroute.New(staticRouteStore, ndmsCommands.Routes, catalog, loggingService)
	orch.SetStaticRoute(staticRouteService)

	// DNS route service — wired to NDMS CQRS layer.
	dnsRouteService := dnsroute.NewService(dnsRouteStore, ndmsQueries, ndmsCommands, catalog, loggingService)

	// DNS route failover — switches DNS targets when pingcheck detects tunnel failure.
	dnsFailover := dnsroute.NewFailoverManager(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := dnsRouteService.Reconcile(ctx); err != nil {
			bootLog.Warn("dns-failover-reconcile", "", err.Error())
			return err
		}
		return nil
	})
	dnsRouteService.SetHydraRoute(hydraService)
	dnsRouteService.SetFailoverManager(dnsFailover)
	dnsFailover.SetAffectedListsLookup(dnsRouteService.LookupAffectedLists)

	if osdetect.Is5() {
		orch.SetDNSRoute(dnsRouteService)
	}

	// DNS route subscription auto-refresh scheduler
	dnsRefreshScheduler := dnsroute.NewScheduler(dnsRouteService, settingsStore, loggingService)

	// Access policy service (NDMS ip policy management) — wired to CQRS layer.
	accessPolicySvc := accesspolicy.New(ndmsCommands.Policies, ndmsCommands.Interfaces, ndmsQueries, settingsStore, loggingService, ndmsquery.NewPolicyMarkStore(ndmsTransportClient, nil))
	// Route SetInterfaceUp for managed tunnels through the orchestrator
	// lifecycle (NativeWG needs kmod proxy + endpoint rewrite, not a raw NDMS
	// flip — issue #183). System interfaces keep the raw flip.
	accessPolicySvc.SetTunnelLifecycle(orchLifecycleAdapter{orch}, storeManagedTunnelResolver{awgStore})

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
		bootLog.Warn("ndms-hook-installer", "", err.Error())
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
		loggingService,
	)
	sysfsTrafficPoller.Start()

	orch.SetEventBus(eventBus)
	loggingService.SetEventBus(eventBus)
	tunnelService.SetEventBus(eventBus)
	pingCheckFacade.SetEventBus(eventBus)
	monitoringService.SetEventBus(eventBus)

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

	// Connectivity Monitor — handshake-trigger only. After a tunnel reaches
	// "running" + handshake we ask the monitoring scheduler for an immediate
	// matrix tick so cards show fresh latency without waiting up to 60s.
	// All actual probing happens inside monitoring.Scheduler.
	connAdapter := connectivity.NewAdapter(tunnelService)
	connMonitor := connectivity.NewMonitor(eventBus, monitoringService.Scheduler(), connAdapter, loggingService)
	connMonitor.Start()
	defer connMonitor.Stop()

	// Sing-box integration
	singboxOp := singbox.NewOperator(singbox.OperatorDeps{
		Log:             slog.Default().With("component", "singbox"),
		Queries:         ndmsQueries,
		Commands:        ndmsCommands,
		AppLogger:       loggingService,
		SingboxLogLevel: settingsStore.GetSingboxLogLevel,
		// Seed the sticky-stop flag from disk so the watchdog respects
		// a user-pressed Stop across awgm restarts. SetManuallyStopped
		// writes the new intent back through a single-field updater so
		// concurrent writers on other Settings fields (e.g. router
		// service toggling SingboxRouter) cannot silently overwrite it.
		InitialManuallyStopped: settings.SingboxManuallyStopped,
		SetManuallyStopped:     settingsStore.SetSingboxManuallyStopped,
		IsNDMSProxyEnabled:     settingsStore.IsSingboxNDMSProxyEnabled,
	})
	// Если на старте флаг disabled — orphan-cleanup (после возможного
	// обрыва прошлой MigrateOff в любой момент). Reconcile подберёт
	// сигнал на первом тике watchdog'а.
	if !settingsStore.IsSingboxNDMSProxyEnabled() {
		singboxOp.MarkNeedsOrphanCleanup()
	}

	// config.d orchestrator — the single writer of slot files (00-base /
	// 10-tunnels / 15-awg / 20-router / 30-deviceproxy). Producers route
	// their writes through Save / SetEnabled so a "disabled" domain
	// actually moves the file out of sing-box's view (config.d/disabled/)
	// instead of leaving stale content behind.
	singboxConfigDir := singboxOp.ConfigDir()
	if err := singbox.MigrateDeviceProxyOutOfTunnels(singboxConfigDir); err != nil {
		bootLog.Warn("deviceproxy-migration", "", err.Error())
	}
	sbOrch := singboxorch.New(singboxConfigDir, singboxOp.Process())
	sbOrch.SetLogger(func(level, msg string) {
		switch level {
		case "warn":
			loggingService.AppLog(logging.LevelWarn, logging.GroupSingbox, logging.SubSBProcess, "orchestrator", "", msg)
		case "error":
			loggingService.AppLog(logging.LevelError, logging.GroupSingbox, logging.SubSBProcess, "orchestrator", "", msg)
		default:
			loggingService.AppLog(logging.LevelInfo, logging.GroupSingbox, logging.SubSBProcess, "orchestrator", "", msg)
		}
	})
	sbOrch.SetValidator(&orchValidatorAdapter{v: singbox.NewValidator(installer.DefaultBinaryPath)})
	// Propagate the sticky-stop intent so reload-triggered cold-starts
	// (slot-file writes from router/deviceproxy/subscriptions) respect a
	// user-pressed Stop in the same way the watchdog does.
	sbOrch.SetShouldRun(func() bool { return !singboxOp.IsManuallyStopped() })
	for _, meta := range singboxorch.KnownSlots() {
		// SlotTunnels is AlwaysOn but only counts as "active work" when
		// the user has defined sing-box tunnels — wire HasContent so
		// the daemon stops running for an empty 10-tunnels.json.
		if meta.Slot == singboxorch.SlotTunnels {
			meta.HasContent = func() bool {
				return singboxOp.HasUserTunnels()
			}
		}
		if err := sbOrch.Register(meta); err != nil {
			bootLog.Error("singbox-orchestrator", string(meta.Slot), "register failed: "+err.Error())
		}
	}
	if err := sbOrch.Bootstrap(); err != nil {
		bootLog.Error("singbox-orchestrator", "bootstrap", err.Error())
	}
	// Download proxy is an ephemeral per-operation slot. If a previous awgm
	// process crashed during geo download, Bootstrap may discover an active
	// 35-download-proxy.json. Always disable it on boot.
	if err := sbOrch.SetEnabledSilent(singboxorch.SlotDownloadProxy, false); err != nil {
		bootLog.Warn("singbox-orchestrator", "downloadproxy-disable", err.Error())
	}
	// Reflect Settings into orchestrator slot enabled-state. router /
	// deviceproxy / subscriptions are content-driven; tunnels / awg
	// are AlwaysOn (registered as such above) and cannot be toggled
	// here — Register already marked them enabled. deviceproxy is
	// reflected after deviceProxySvc is constructed below.
	if curSettings, err := settingsStore.Load(); err == nil && curSettings != nil {
		_ = sbOrch.SetEnabled(singboxorch.SlotRouter, curSettings.SingboxRouter.Enabled)
	}

	// Subscription service — owns 40-subscriptions.json in config.d.
	// NewOperatorAdapter registers the slot into sbOrch (must happen before
	// Bootstrap so Bootstrap can scan the file). LoadFromDisk reads any
	// existing 40-subscriptions.json so the in-memory state is consistent.
	subStorePath := filepath.Join(*dataDir, "subscriptions.json")
	subStore, err := subscription.NewStore(subStorePath)
	if err != nil {
		bootLog.Error("subscription-store", "", err.Error())
	}
	subProxyMgr := singbox.NewProxyManager(ndmsQueries, ndmsCommands)
	subAdapter := subscription.NewOperatorAdapter(sbOrch, subProxyMgr, singboxOp.Clash())
	if err := subAdapter.LoadFromDisk(singboxConfigDir); err != nil {
		bootLog.Warn("subscription-adapter", "load-from-disk", err.Error())
	}
	subSvc := subscription.NewService(subStore, subAdapter)
	subSvc.SetAppLogger(loggingService)

	// Let NDMS-proxy enable/disable + orphan cleanup manage subscription
	// composite proxies (a set separate from Tunnels()).
	singboxOp.SetSubscriptionProxySet(subProxySet{store: subStore})

	// Wire orchestrator into Operator so ApplyConfig writes 10-tunnels.json
	// through SlotTunnels rather than an in-place write that bypasses
	// the orchestrator's validate / debounced reload.
	singboxOp.SetOrch(sbOrch)

	// Wire managed-binary installer into Operator. The installer is keyed
	// by the build-time arch string (e.g. "mipsel-3.4") so it can resolve
	// the correct download URL and SHA256 from EmbeddedBinaries.
	var singboxInstaller *installer.Installer
	arch := detectArch()
	if arch == "" {
		bootLog.Warn("managed-singbox", runtime.GOARCH, "could not derive arch — install/update disabled")
	} else {
		spec, ok := installer.EmbeddedBinaries[arch]
		if !ok {
			bootLog.Warn("managed-singbox", arch, "no embedded BinarySpec — install/update disabled")
		} else {
			singboxInstaller = installer.New(installer.DefaultBinaryPath, arch, spec, loggingService)
			singboxOp.SetInstaller(singboxInstaller)

			// Stream sing-box install/update lifecycle over SSE so the UI
			// can render a live progress bar instead of a blocking spinner.
			singboxOp.SetInstallProgressReporter(func(op, phase string, downloaded, total int64, errMsg string) {
				eventBus.Publish("singbox:install-progress", events.SingboxInstallProgressEvent{
					Op:         op,
					Phase:      phase,
					Downloaded: downloaded,
					Total:      total,
					Error:      errMsg,
				})
			})

		}
	}

	delayChecker := singbox.NewDelayChecker(
		singboxOp.Clash(),
		&singboxAndSubLister{op: singboxOp, sub: subSvc},
		eventBus,
	)
	singboxHandler := api.NewSingboxHandler(singboxOp, eventBus, delayChecker, testService, loggingService)
	singboxMigrator := singbox.NewMigrator(singboxOp, settingsStore)
	singboxHandler.SetNDMSProxyMigrator(singboxMigrator, settingsStore)
	clashProxy := api.NewClashProxy(singboxOp)
	singboxConnsHandler := api.NewSingboxConnectionsHandler(ndmsQueries.Hotspot)

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
	go singbox.NewTrafficAggregator(singboxOp.Clash().Address(), eventBus, trafficHistory).Run(trafficCtx)

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

	var slowHTTPThreshold time.Duration
	if *slowReqMS > 0 {
		slowHTTPThreshold = time.Duration(*slowReqMS) * time.Millisecond
	}
	frontendFS, err := frontend.FS()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load embedded frontend: %v\n", err)
		os.Exit(1)
	}

	srv := server.New(
		server.Config{
			Version:              version,
			FrontendFS:           frontendFS,
			PprofStandaloneAddr:  strings.TrimSpace(*pprofListen),
			PprofOnMain:          *pprofOnMain,
			SlowRequestThreshold: slowHTTPThreshold,
		},
		server.Deps{
			TunnelService:       tunnelService,
			ExternalService:     externalService,
			TestingService:      testService,
			Keenetic:            keeneticClient,
			Sessions:            sessionStore,
			Settings:            settingsStore,
			Tunnels:             awgStore,
			PingCheckService:    pingCheckFacade,
			LoggingService:      loggingService,
			ActiveBackend:       backendImpl,
			KmodLoader:          kmodLoader,
			UpdaterService:      updaterService,
			NdmsQueries:         ndmsQueries,
			NdmsCommands:        ndmsCommands,
			TrafficHistory:      trafficHistory,
			DnsRouteService:     dnsRouteService,
			StaticRouteService:  staticRouteService,
			SystemTunnelService: systemTunnelSvc,
			ManagedService:      managedService,
			ManagedServiceImpl:  managedService,
			NwgOp:               nwgOp,
			TerminalManager:     terminalManager,
			AccessPolicySvc:     accessPolicySvc,
			ClientRouteSvc:      clientRouteService,
			Catalog:             catalog,
			Orch:                orch,
			Bus:                 eventBus,
			HydraService:        hydraService,
			SingboxHandler:      singboxHandler,
			SingboxOrch:         sbOrch,
			ClashProxy:          clashProxy,
			SingboxConnsHandler: singboxConnsHandler,
			MonitoringService:   monitoringService,
			SingboxSubMembers: func() []diagnostics.SingboxSubMember {
				subs := subSvc.List()
				out := make([]diagnostics.SingboxSubMember, 0, len(subs)*2)
				for _, sub := range subs {
					activeKnown := sub.ActiveMember != ""
					for _, tag := range sub.MemberTags {
						out = append(out, diagnostics.SingboxSubMember{
							Tag:         tag,
							ListenPort:  int(sub.ListenPort),
							Enabled:     sub.Enabled,
							Active:      activeKnown && sub.ActiveMember == tag,
							ActiveKnown: activeKnown,
						})
					}
				}
				return out
			},
			SingboxConfigPreview: func() (string, error) {
				return singboxcfg.MergeDir(sbOrch.ConfigDir())
			},
		},
	)

	srv.SetSingboxOperator(singboxOp)
	singboxOp.SetEventBus(eventBus)

	// systemTunnelDPAdapter bridges Keenetic NativeWG tunnels (from NDMS)
	// into both awgoutbounds (canonical tag writer) and deviceproxy
	// (SystemTunnelQuery — kept for its List interface in adapters).
	systemTunnelDPAdapter := deviceproxy.NewSystemTunnelAdapter(systemTunnelSvc)

	// awgoutbounds — canonical writer of AWG-direct outbounds in
	// config.d/15-awg.json. Sources managed AWG tunnels from storage
	// and system (NativeWG) tunnels via the deviceproxy adapter.
	// Must be constructed before deviceProxySvc so we can pass it as
	// AWGOutbounds dep (deviceproxy now queries tags instead of enumerating).
	awgoutboundsSvc := awgoutbounds.NewService(awgoutbounds.Deps{
		AWGTunnels:     newAWGStoreAdapter(awgStore),
		SystemTunnels:  newSystemTunnelStoreAdapter(systemTunnelDPAdapter),
		ManagedServers: newSettingsManagedServersAdapter(settingsStore),
		Singbox:        newAwgoutboundsSingboxAdapter(singboxOp),
		AppLog:         logging.NewScopedLogger(loggingService, logging.GroupRouting, logging.SubAWGOutbounds),
		Bus:            eventBus,
		Orch:           sbOrch,
	})
	awgoutboundsUnsub := awgoutboundsSvc.SubscribeBus(context.Background())
	defer awgoutboundsUnsub()
	// Boot reconcile — populates 15-awg.json before sing-box starts so
	// the merged config.d is consistent on first read. Reload-free.
	if err := awgoutboundsSvc.Reconcile(context.Background()); err != nil {
		bootLog.Warn("awgoutbounds-reconcile", "", err.Error())
	}

	// Device-proxy service — LAN-facing SOCKS/HTTP proxy managed through
	// sing-box. See docs/superpowers/specs/2026-04-24-device-proxy-design.md.
	deviceProxyStore := deviceproxy.NewStore(filepath.Join(*dataDir, "deviceproxy.json"))
	deviceProxySingboxAdapter := deviceproxy.NewSingboxAdapter(singboxOp)
	deviceProxySingboxAdapter.SetOrch(sbOrch)

	subOutboundsAdapter := &deviceproxySubscriptionOutboundsAdapter{src: subSvc}

	deviceProxySvc := deviceproxy.NewService(deviceproxy.Deps{
		Store:                 deviceProxyStore,
		Singbox:               deviceProxySingboxAdapter,
		SubscriptionOutbounds: subOutboundsAdapter,
		NDMSQuery:             deviceproxy.NewNDMSAdapter(ndmsQueries),
		Bus:                   eventBus,
		AWGOutbounds:          &deviceproxyAWGOutboundsAdapter{src: awgoutboundsSvc},
		AppLogger:             loggingService,
	})
	// Reflect deviceproxy storage state into the orchestrator slot so
	// the saved Enabled flag matches the on-disk active/disabled
	// location of 30-deviceproxy.json from boot.
	_ = sbOrch.SetEnabled(singboxorch.SlotDeviceProxy, deviceProxyStore.Get().Enabled)
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
		bootLog.Warn("deviceproxy-reconcile", "", err.Error())
	}
	sharedDownloadSvc := downloader.NewSettingsBackedService(
		deviceProxySvc,
		singboxOp,
		sbOrch,
		settingsStore,
	)
	dnsRouteService.SetDownloader(&dnsRouteDownloaderAdapter{svc: sharedDownloadSvc})
	dnsRefreshScheduler.Start()
	updaterService.SetDownloader(sharedDownloadSvc)
	if singboxInstaller != nil {
		singboxInstaller.SetDownloader(&installerDownloaderAdapter{svc: sharedDownloadSvc})
		// Auto-migration goroutine: when legacy sing-box-naive opkg
		// package is present but managed binary is missing, run the
		// jump from opkg → managed in the background. Failures keep
		// awg-manager on the legacy install — retry happens on next boot.
		go func() {
			ctx := context.Background()
			if singboxInstaller.CurrentVersion(ctx) != "" {
				return // managed binary already in place
			}
			if !singboxInstaller.IsLegacyOpkgInstalled(ctx) {
				return // nothing to migrate
			}
			// Skip migration if there is not enough disk space — GetStatus
			// will surface InstallStateMissingNoSpace automatically; no
			// point burning bandwidth on a download that will fail.
			if singboxInstaller.EvaluateInstallState() == installer.InstallStateMissingNoSpace {
				bootLog.Warn("singbox-auto-migration", "", "skipped: not enough disk space")
				return
			}
			lc := &operatorLifecycle{op: singboxOp}
			if err := singboxInstaller.Migrate(ctx, lc); err != nil {
				bootLog.Warn("singbox-auto-migration", "", "deferred: "+err.Error())
			}
		}()
	}

	srv.SetPresetCatalog(presetCatalog)
	srv.SetDeviceProxyService(deviceProxySvc)
	srv.SetDownloadService(sharedDownloadSvc)
	// Note: legacy awg-* outbound cleanup happens lazily on first
	// deviceproxy CRUD via pruneAWGOutbounds(nil) inside EnsureDeviceProxy.
	// We deliberately do NOT call ForceApply on boot because it triggers
	// ApplyConfig → startAndWait, which spuriously starts sing-box even
	// when both the deviceproxy and the router engine are disabled —
	// once started, only an explicit Stop call (no UI today) brings it
	// back down. Reconcile already handles cleanup on the next legitimate
	// trigger; the migration tax is at most one stale file fragment that
	// gets stripped on the next Save/Enable.
	srv.SetNDMSDispatcher(ndmsDispatcher)
	srv.SetNDMSTransport(ndmsTransportClient)
	srv.SetNDMSSaveCoordinator(ndmsSaveCoord)
	srv.SetMetricsPoller(ndmsMetricsPoller)

	routerSvc := router.NewService(router.Deps{
		AppLog:                 loggingService,
		Settings:               settingsStore,
		Singbox:                singboxOp,
		Policies:               &routerAccessPolicyAdapter{svc: accessPolicySvc, wan: wanModel},
		Events:                 eventBus,
		Bus:                    eventBus,
		AWGTags:                &routerAWGTagAdapter{src: awgoutboundsSvc},
		SingboxTunnels:         &routerSingboxTunnelAdapter{src: singboxOp},
		SubscriptionComposites: router.NewSubscriptionCompositesAdapter(subAdapter),
		Orch:                   sbOrch,
		WANInterfaces:          &routerWANInterfaceAdapter{store: ndmsQueries.Interfaces},
		BindableInterfaces:     &routerWANInterfaceAdapter{store: ndmsQueries.Interfaces},
		IngressResolver:        &routerIngressResolverAdapter{store: ndmsQueries.Interfaces},
		PresetCatalog:          presetCatalog,
		GeoData:                geoDataStore,
	})
	singboxOp.SetOutboundReferenceRenamer(routerSvc)
	tunnelService.SetAWGSyncer(awgoutboundsSvc)
	tunnelService.SetDeviceProxyRefChecker(deviceProxySvc)
	tunnelService.SetRouterRefChecker(routerSvc)
	routerStartupLog := logging.NewScopedLogger(loggingService, logging.GroupRouting, logging.SubSingboxRouter)
	go func() {
		if err := routerSvc.Reconcile(context.Background()); err != nil {
			routerStartupLog.Error("reconcile", "startup", err.Error())
		}
	}()
	routerScheduler := router.NewScheduler(routerSvc, settingsStore)
	routerScheduler.Start()

	// Late-bind sing-box / router / Clash deps into the monitoring scheduler.
	// monitoringService is constructed early (line ~421) so the matrix can
	// include Keenetic-native tunnels; singboxOp + routerSvc + clashProxy
	// are constructed later in the bootstrap, hence the deferred wiring.
	monitoringService.SetSingboxTunnels(&monitoringSingboxTunnelAdapter{op: singboxOp, sub: subSvc})
	monitoringService.SetComposites(&monitoringCompositesAdapter{svc: routerSvc})
	monitoringService.SetClashState(monitoring.NewClashState(clashProxy.ClashBaseURL, nil))
	monitoringService.SetSingboxDelay(singboxOp.Clash())

	srv.SetSingboxRouterHandler(api.NewSingboxRouterHandler(routerSvc, loggingService))
	srv.SetAWGOutboundsHandler(api.NewAWGOutboundsHandler(awgoutboundsSvc))
	srv.SetSingboxConfigHandler(api.NewSingboxConfigHandler(sbOrch.ConfigDir))

	proxiesHandler := api.NewSingboxProxiesHandler(
		clashProxy.ClashBaseURL,
		func() map[string]struct{} {
			out, _ := routerSvc.ListCompositeOutbounds(context.Background())
			set := make(map[string]struct{}, len(out))
			for _, o := range out {
				set[o.Tag] = struct{}{}
			}
			return set
		},
		nil,
	)
	srv.SetSingboxProxiesHandler(proxiesHandler)

	// Wire subscription handler + start refresh scheduler.
	// subSvc and subAdapter are constructed earlier (after sbOrch.Bootstrap).
	subSched := subscription.NewScheduler(subStore, func(ctx context.Context, id string) error {
		_, err := subSvc.Refresh(ctx, id)
		return err
	})
	subSched.SetAppLogger(loggingService)
	subSched.Start(context.Background())
	subHandler := api.NewSubscriptionHandler(subSvc, singboxOp, loggingService)
	subHandler.SetNDMSProxyToggler(settingsStore)
	srv.SetSubscriptionHandler(subHandler)
	srv.AddShutdownHook(subSched.Stop)

	// DNS Rewrites — sing-box slot 17-dns-rewrites.json.
	dnsRewriteStorePath := filepath.Join(*dataDir, "dns_rewrites.json")
	dnsRewriteStore := storage.NewDNSRewriteStore(dnsRewriteStorePath)
	dnsRewriteSvc := dnsrewrite.NewService(dnsRewriteStore, &dnsRewriteOrchAdapter{orch: sbOrch}, eventBus)
	if err := dnsRewriteSvc.Resync(); err != nil {
		bootLog.Warn("dnsrewrite-resync", "", err.Error())
	}
	srv.SetDNSRewritesHandler(api.NewDNSRewritesHandler(dnsRewriteSvc))

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
		ndmsQueries.Hotspot,
		ndmsQueries.IPHost,
		ndmsQueries.DNSProxyConfig,
		&dnsRouteCountAdapter{store: dnsRouteStore},
		&runningTunnelAdapter{svc: tunnelService},
		loggingService,
	)
	dnsCheckService.EnsureIPHost(context.Background())
	srv.SetDnsCheckService(dnsCheckService)

	logStartup(bootLog, version, string(osdetect.Get()), listenAddr, settings)

	// Shutdown context — cancelled on shutdown
	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
	defer shutdownCancel()

	// Start the monitoring scheduler now that shutdownCtx exists.
	monitoringService.Start(shutdownCtx)

	// Register shutdown hooks for graceful cleanup before syscall.Exec restart.
	srv.AddShutdownHook(shutdownCancel)
	// Intentionally NOT removing kmod proxy slots on restart: the
	// reconnect path (EventReconnect → ActionRestoreKmod →
	// KmodManager.RestoreTunnel) adopts each existing slot without
	// touching /proc/awg_proxy/del, so kernel WG keeps forwarding
	// through the live slot across syscall.Exec. This is what makes
	// "Перезапуск AWGM, туннели продолжат работать" actually true on
	// proxy-firmware. Touching /proc/del here also opened a kernel-side
	// race on awg_proxy < 1.1.10 (issue #234) — slots are now left
	// to the reconnect path.
	srv.AddShutdownHook(pingCheckService.Stop)
	srv.AddShutdownHook(monitoringService.Stop)
	srv.AddShutdownHook(dnsRefreshScheduler.Stop)
	srv.AddShutdownHook(routerScheduler.Stop)
	srv.AddShutdownHook(sessionStore.Stop)
	srv.AddShutdownHook(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := ndmsSaveCoord.Flush(ctx); err != nil {
			bootLog.Warn("ndms-savecoord-flush", "shutdown", err.Error())
		}
	})
	srv.AddShutdownHook(ndmsDispatcher.Stop)
	srv.AddShutdownHook(sysfsTrafficPoller.Stop)
	srv.AddShutdownHook(ndmsMetricsPoller.Stop)
	srv.AddShutdownHook(loggingService.Stop)
	srv.AddShutdownHook(trafficHistory.Stop)
	srv.AddShutdownHook(func() { terminalManager.Shutdown(context.Background()) })

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

			// Seed WAN model with current interface state from NDMS.
			// Must happen before tunnel start so ISP resolution works.
			populateWANModel(shutdownCtx, ndmsQueries, wanModel, bootLog)

			// Back-fill ManagedServer.PrivateKey for entries created before
			// the field existed in storage. Best-effort, idempotent — already
			// populated entries are skipped. Must run AFTER the NDMS interface
			// cache is ready so kernel-name resolution works.
			managedService.MigratePrivateKeys(shutdownCtx)

			// One-time sweep: strip the legacy default 0.0.0.0/0 from
			// managed-server peers' allow-ips (per-peer /32 only). New
			// firmware rejects multiple peers sharing 0.0.0.0/0. Gated by a
			// persisted flag; best-effort, retries next boot if NDMS is down.
			managedService.MigratePeerAllowIPs(shutdownCtx)

			// Migrate legacy NDMS ID values to kernel names (one-time after model is populated).
			tunnelService.MigrateISPInterfaceToKernel()
			// Clear stored.ActiveWAN entries that don't name a real kernel iface
			// (legacy garbage from the pre-hardened resolver, e.g. "ISP").
			tunnelService.HealStaleActiveWAN()

			// Detect actual WAN state.
			if _, err := ndmsQueries.Routes.GetDefaultGatewayInterface(shutdownCtx); err != nil {
				bootLog.Info("startup", "",
					"WAN down at boot — waiting for WAN UP event")
			} else {
				orch.LoadState(shutdownCtx)
				orch.HandleEvent(shutdownCtx, orchestrator.Event{Type: orchestrator.EventBoot})
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
		populateWANModel(context.Background(), ndmsQueries, wanModel, bootLog)

		// Migrate legacy NDMS ID values to kernel names (one-time after model is populated).
		tunnelService.MigrateISPInterfaceToKernel()
		// Clear stored.ActiveWAN entries that don't name a real kernel iface
		// (legacy garbage from the pre-hardened resolver, e.g. "ISP").
		tunnelService.HealStaleActiveWAN()

		// One-time sweep on daemon restart/upgrade too (NDMS already up):
		// strip the legacy default 0.0.0.0/0 from managed-server peers'
		// allow-ips. Flag-gated, best-effort. Without this the migration
		// would only fire on a cold router boot (isBoot branch above).
		managedService.MigratePeerAllowIPs(context.Background())

		bootLog.Info("startup", "",
			"Daemon restart detected — reconnecting to running tunnels")

		orch.LoadState(context.Background())
		orch.HandleEvent(context.Background(), orchestrator.Event{Type: orchestrator.EventReconnect})
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
func populateWANModel(ctx context.Context, queries *ndmsquery.Queries, model *wan.Model, appLog *logging.ScopedLogger) {
	interfaces, err := queries.Interfaces.ListWAN(ctx)
	if err != nil {
		appLog.Warn("populate-wan", "", "failed to get WAN interfaces: "+err.Error())
		return
	}
	model.Populate(interfaces)
	appLog.Info("populate-wan", "", fmt.Sprintf("WAN model populated, count=%d", len(interfaces)))
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

// runService handles --service flag: start/stop/restart/status.
// This replaces the shell logic that was previously in S99awg-manager.
func runService(action, dataDir string) {
	switch action {
	case "start":
		serviceStart(dataDir)
	case "stop":
		serviceStop()
	case "restart":
		serviceStop()
		time.Sleep(time.Second)
		serviceStart(dataDir)
	case "status":
		serviceStatus(dataDir)
	default:
		fmt.Fprintf(os.Stderr, "Unknown service action: %s\nUsage: --service {start|stop|restart|status}\n", action)
		os.Exit(1)
	}
}

// serviceStart starts the daemon as a background process with PID file management.
func serviceStart(dataDir string) {
	// Check if already running
	if pid, running := readPIDFile(); running {
		fmt.Printf("AWG Manager already running (PID %d)\n", pid)
		return
	}

	fmt.Println("Starting AWG Manager...")

	// Ensure directories. /var/run is system tmpfs and always exists,
	// but MkdirAll is idempotent so harmless to call.
	os.MkdirAll("/var/run", 0755)
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
	cmd := exec.Command(executable, "-data-dir", dataDir)
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

// isProcessRunning checks if a process with the given PID is an awg-manager
// instance. /proc/<pid>/cmdline is the NUL-separated argv. We match on the
// basename of argv[0] rather than on the whole buffer so an argument that
// happens to contain "awg-manager" (e.g. "-data-dir /opt/etc/awg-manager")
// for an unrelated process that inherited the recycled PID does not
// produce a false positive.
func isProcessRunning(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false
	}
	argv0 := string(data)
	if i := strings.IndexByte(argv0, 0); i >= 0 {
		argv0 = argv0[:i]
	}
	return filepath.Base(argv0) == "awg-manager"
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

	settingsStore := storage.NewSettingsStore(dataDir)
	settingsStore.Load()

	loggingService := logging.NewService(settingsStore)
	defer loggingService.Stop()
	bootLog := logging.NewScopedLogger(loggingService, logging.GroupSystem, logging.SubCleanup)

	awgStore := storage.NewAWGTunnelStore(filepath.Join(dataDir, "tunnels"))

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

	// Init NDMS info (needed for OS detection). Wire ndmsinfo to the
	// SystemInfoStore, then initialize with retry.
	if err := ndmsinfo.Init(context.Background(), cleanupNDMSQueries.SystemInfo, 10*time.Second); err != nil {
		bootLog.Warn("ndms-version", "", err.Error())
	}

	// Create service components
	wgClient := wg.New()
	backendImpl := backend.New(bootLog)
	stateMgr := state.New(cleanupNDMSQueries.Interfaces, wgClient, backendImpl, nil)
	firewallMgr := firewall.New(backendImpl.Type() == backend.TypeKernel, osdetect.Is5(), nil)

	// Build NDMS Commands early so the Operator can consume them. HookNotifier
	// is wired below once the orchestrator exists (see SetHookNotifier call).
	cleanupNDMSSave := ndmscommand.NewSaveCoordinator(
		cleanupNDMSTransport,
		cleanupEventBus,
		3*time.Second,
		10*time.Second,
		env.DurationDefault("AWG_NDMS_SAVE_SETTLE_DELAY", 2*time.Second),
		cleanupNDMSQueries.RunningConfig,
	)
	cleanupNDMSCommands := ndmscommand.NewCommands(ndmscommand.Deps{
		Poster:  cleanupNDMSTransport,
		Save:    cleanupNDMSSave,
		Queries: cleanupNDMSQueries,
		IsOS5:   osdetect.Is5,
	})

	operator := ops.NewOperator(cleanupNDMSQueries, cleanupNDMSCommands, wgClient, backendImpl, firewallMgr)

	nwgOp := nwg.NewOperator(cleanupNDMSQueries, cleanupNDMSCommands, cleanupNDMSTransport, nil)
	tunnelService := service.New(awgStore, nwgOp, operator, stateMgr, wan.NewModel(), nil)

	// Wire orchestrator for lifecycle operations (Delete needs it)
	cleanupOrch := orchestrator.New(awgStore, operator, nwgOp, stateMgr, wan.NewModel(), nil)
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
	dnsSvc := dnsroute.NewService(dnsStore, cleanupNDMSQueries, cleanupNDMSCommands, nil, nil)

	singboxOp := singbox.NewOperator(singbox.OperatorDeps{
		Log:                slog.Default().With("component", "singbox"),
		Queries:            cleanupNDMSQueries,
		Commands:           cleanupNDMSCommands,
		SingboxLogLevel:    settingsStore.GetSingboxLogLevel,
		IsNDMSProxyEnabled: settingsStore.IsSingboxNDMSProxyEnabled,
	})

	// Cleanup mode: bootstrap the orchestrator so any subsequent
	// operator call that goes through ApplyConfig writes the slot
	// file rather than the legacy in-place tunnels.json. Cleanup
	// itself only invokes singboxOp.Cleanup, but we keep the wiring
	// symmetrical to the daemon path so future cleanup steps have it
	// available.
	cleanupSingboxConfigDir := singboxOp.ConfigDir()
	if err := singbox.MigrateDeviceProxyOutOfTunnels(cleanupSingboxConfigDir); err != nil {
		bootLog.Warn("deviceproxy-migration", "", err.Error())
	}
	cleanupSbOrch := singboxorch.New(cleanupSingboxConfigDir, singboxOp.Process())
	for _, meta := range singboxorch.KnownSlots() {
		if err := cleanupSbOrch.Register(meta); err != nil {
			bootLog.Error("singbox-orchestrator", string(meta.Slot), "register failed: "+err.Error())
		}
	}
	if err := cleanupSbOrch.Bootstrap(); err != nil {
		bootLog.Error("singbox-orchestrator", "bootstrap", err.Error())
	}
	singboxOp.SetOrch(cleanupSbOrch)

	accessPolicySvc := accesspolicy.New(cleanupNDMSCommands.Policies, cleanupNDMSCommands.Interfaces, cleanupNDMSQueries, settingsStore, nil, ndmsquery.NewPolicyMarkStore(cleanupNDMSTransport, nil))

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

	fmt.Println("Done.")
}

// monitoringSystemTunnelAdapter adapts systemtunnel.Service to
// monitoring.SystemTunnelLister (a small typed view of just the fields the
// monitoring scheduler needs).
type monitoringSystemTunnelAdapter struct {
	svc *systemtunnel.ServiceImpl
}

func (s *monitoringSystemTunnelAdapter) List(ctx context.Context) ([]monitoring.SystemTunnelInfo, error) {
	if s == nil || s.svc == nil {
		return nil, nil
	}
	list, err := s.svc.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]monitoring.SystemTunnelInfo, 0, len(list))
	for _, t := range list {
		// Skip awg-manager's own server interface (tagged with the
		// ManagedServerDescription prefix "AWGM ..."; see
		// internal/managed/types.go::ManagedServerDescription). Server-side
		// WG is not a client tunnel and must not appear in monitoring.
		if strings.HasPrefix(t.Description, "AWGM") {
			continue
		}
		out = append(out, monitoring.SystemTunnelInfo{
			ID:            t.ID,
			InterfaceName: t.InterfaceName,
			Description:   t.Description,
			Connected:     t.Connected,
		})
	}
	return out, nil
}

// orchLifecycleAdapter routes accesspolicy.SetInterfaceUp for managed tunnels
// to the orchestrator lifecycle (full start/stop incl. NativeWG kmod proxy).
type orchLifecycleAdapter struct{ orch *orchestrator.Orchestrator }

func (a orchLifecycleAdapter) Start(ctx context.Context, id string) error {
	err := a.orch.HandleEvent(ctx, orchestrator.Event{Type: orchestrator.EventStart, Tunnel: id})
	if errors.Is(err, tunnel.ErrAlreadyRunning) {
		return nil // already running — user's "on" intent fulfilled
	}
	return err
}

func (a orchLifecycleAdapter) Stop(ctx context.Context, id string) error {
	return a.orch.HandleEvent(ctx, orchestrator.Event{Type: orchestrator.EventStop, Tunnel: id})
}

// storeManagedTunnelResolver maps an NDMS interface name to a managed tunnel
// ID by scanning the AWG tunnel store. ok=false for plain system interfaces.
type storeManagedTunnelResolver struct{ store *storage.AWGTunnelStore }

func (r storeManagedTunnelResolver) ManagedTunnelByNDMSName(_ context.Context, ndmsName string) (string, bool) {
	tunnels, err := r.store.List()
	if err != nil {
		return "", false
	}
	for _, t := range tunnels {
		var nm string
		if t.Backend == "nativewg" {
			nm = nwg.NewNWGNames(t.NWGIndex).NDMSName
		} else {
			nm = tunnel.NewNames(t.ID).NDMSName
		}
		if nm != "" && nm == ndmsName {
			return t.ID, true
		}
	}
	return "", false
}

// operatorLifecycle adapts *singbox.Operator to installer.Lifecycle so
// the installer can stop/start the daemon during migration without the
// installer package taking a circular dependency on singbox.
type operatorLifecycle struct {
	op *singbox.Operator
}

func (l *operatorLifecycle) Stop(ctx context.Context) error {
	return l.op.Control(ctx, "stop")
}

func (l *operatorLifecycle) Start(ctx context.Context) error {
	return l.op.Control(ctx, "start")
}

type installerDownloaderAdapter struct {
	svc *downloader.Service
}

type dnsRouteDownloaderAdapter struct {
	svc *downloader.Service
}

func (a *dnsRouteDownloaderAdapter) ReadAll(
	ctx context.Context,
	req dnsroute.SubscriptionDownloadRequest,
) ([]byte, dnsroute.SubscriptionDownloadMeta, error) {
	if a == nil || a.svc == nil {
		return nil, dnsroute.SubscriptionDownloadMeta{}, fmt.Errorf("downloader service is not configured")
	}
	body, meta, err := a.svc.ReadAll(ctx, downloader.Request{
		Purpose:       "dnsroute-subscription",
		URL:           req.URL,
		Timeout:       req.Timeout,
		MaxBodyBytes:  req.MaxBodyBytes,
		AllowedStatus: req.AllowedStatus,
	})
	if err != nil {
		return nil, dnsroute.SubscriptionDownloadMeta{}, err
	}
	return body, dnsroute.SubscriptionDownloadMeta{ContentType: meta.ContentType}, nil
}

func (a *installerDownloaderAdapter) DownloadFile(ctx context.Context, req installer.DownloadFileRequest) (installer.DownloadFileResult, error) {
	if a == nil || a.svc == nil {
		return installer.DownloadFileResult{}, fmt.Errorf("downloader service is not configured")
	}

	res, err := a.svc.DownloadFile(ctx, downloader.FileRequest{
		Request: downloader.Request{
			Purpose: "singbox-binary",
			URL:     req.URL,
			Timeout: req.Timeout,
		},
		// Intentional: req.DestPath is already "<binary>.tmp" from installer.
		// We keep temp == dest here, and activation of live binary is done
		// separately in Installer.Activate().
		DestPath:     req.DestPath,
		TempPath:     req.DestPath,
		MaxFileBytes: req.MaxFileBytes,
		Mode:         req.Mode,
		// Intentional: do not atomically move to final binary here.
		// Installer.Activate() performs chmod + final atomic rename.
		Atomic:   false,
		Progress: req.Progress,
	})
	if err != nil {
		return installer.DownloadFileResult{}, err
	}
	return installer.DownloadFileResult{
		Path: res.Path,
		Size: res.Size,
	}, nil
}

// singboxAndSubLister satisfies singbox.tunnelLister by combining the regular
// sing-box tunnel list with the active outbound tags of enabled subscriptions.
// This lets DelayChecker probe subscription active members with the same
// periodic clash latency test it runs for regular sing-box tunnels.
type singboxAndSubLister struct {
	op  *singbox.Operator
	sub *subscription.Service
}

func (l *singboxAndSubLister) ListTunnels(ctx context.Context) ([]singbox.TunnelInfo, error) {
	return l.op.ListTunnels(ctx)
}

func (l *singboxAndSubLister) ListSubActiveTags() []string {
	return l.sub.ListActiveMemberTags()
}

// orchValidatorAdapter bridges singbox.Validator (no context) to the
// singboxorch.DraftValidator interface (with context).
type orchValidatorAdapter struct {
	v *singbox.Validator
}

func (a *orchValidatorAdapter) Validate(ctx context.Context, configDir string) error {
	return a.v.Validate(configDir)
}

// subProxySet adapts the subscription store to singbox.SubscriptionProxySet,
// exposing each subscription's allocated NDMS composite proxy (index/port/label)
// so the NDMS-proxy migration and orphan cleanup manage them.
type subProxySet struct {
	store *subscription.Store
}

func (a subProxySet) SubscriptionProxies() []singbox.SubscriptionProxy {
	if a.store == nil {
		return nil
	}
	var out []singbox.SubscriptionProxy
	for _, sub := range a.store.List() {
		if sub.ProxyIndex < 0 || sub.ListenPort == 0 {
			continue
		}
		out = append(out, singbox.SubscriptionProxy{
			Index: sub.ProxyIndex,
			Port:  int(sub.ListenPort),
			Label: sub.Label,
		})
	}
	return out
}

// dnsRewriteOrchAdapter adapts *singboxorch.Orchestrator to the
// dnsrewrite.Orchestrator interface (which uses plain string so the
// package stays decoupled from singboxorch.Slot).
type dnsRewriteOrchAdapter struct {
	orch *singboxorch.Orchestrator
}

func (a *dnsRewriteOrchAdapter) Save(slot string, data []byte) error {
	return a.orch.Save(singboxorch.Slot(slot), data)
}

func (a *dnsRewriteOrchAdapter) SetEnabled(slot string, on bool) error {
	return a.orch.SetEnabled(singboxorch.Slot(slot), on)
}
