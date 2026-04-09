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
	"github.com/hoaxisr/awg-manager/internal/clientroute"
	"github.com/hoaxisr/awg-manager/internal/connectivity"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/auth"
	"github.com/hoaxisr/awg-manager/internal/cleanup"
	"github.com/hoaxisr/awg-manager/internal/dnsroute"
	"github.com/hoaxisr/awg-manager/internal/managed"
	"github.com/hoaxisr/awg-manager/internal/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/routing"
	"github.com/hoaxisr/awg-manager/internal/staticroute"
	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/pingcheck"
	"github.com/hoaxisr/awg-manager/internal/rci"
	"github.com/hoaxisr/awg-manager/internal/server"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
	"github.com/hoaxisr/awg-manager/internal/terminal"
	"github.com/hoaxisr/awg-manager/internal/testing"
	"github.com/hoaxisr/awg-manager/internal/traffic"
	"github.com/hoaxisr/awg-manager/internal/sys/kmod"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/backend"
	"github.com/hoaxisr/awg-manager/internal/tunnel/external"
	"github.com/hoaxisr/awg-manager/internal/tunnel/systemtunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/firewall"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ops"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
	"github.com/hoaxisr/awg-manager/internal/tunnel/state"
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
	defer loggingService.Stop()

	// Create tunnel service components
	ndmsClient := ndms.New()
	wgClient := wg.New()
	backendImpl := backend.New(log)
	stateMgr := state.New(ndmsClient, wgClient, backendImpl, loggingService)
	firewallMgr := firewall.New(backendImpl.Type() == backend.TypeKernel, osdetect.Is5(), loggingService)
	operator := ops.NewOperator(ndmsClient, wgClient, backendImpl, firewallMgr, log)

	// Create NativeWG operator
	rciClient := rci.New()
	nwgOp := nwg.NewOperator(log, ndmsClient, rciClient, loggingService)

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
		populateWANModel(context.Background(), ndmsClient, wanModel, log)
	})

	// Create the main tunnel service
	tunnelService := service.New(awgStore, nwgOp, operator, stateMgr, log, wanModel, loggingService)

	// Migrate legacy ISPInterface="none" to "" (auto) for tunnels from older versions.
	tunnelService.MigrateISPInterfaceNone()
	tunnelService.MigrateEmptyBackend()

	// Routing catalog — unified tunnel listing for all routing subsystems
	catalog := routing.NewCatalog(
		&tunnelProviderAdapter{svc: tunnelService, store: awgStore},
		ndmsClient,
		&storeAdapter{store: awgStore},
	)

	// DNS route service (OS5 only — routes domains through tunnels via NDMS)
	dnsRouteStore := dnsroute.NewStore(*dataDir)
	if _, err := dnsRouteStore.Load(); err != nil {
		log.Warn("Failed to load dns-routes", map[string]interface{}{"error": err.Error()})
	}
	dnsRouteService := dnsroute.NewService(dnsRouteStore, ndmsClient, catalog, log, loggingService)

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
	dnsRouteService.SetFailoverManager(dnsFailover)
	dnsFailover.SetLogger(log)
	dnsFailover.SetAffectedListsLookup(dnsRouteService.LookupAffectedLists)

	// Static route service for IP-based routing through tunnels
	staticRouteStore := storage.NewStaticRouteStore(*dataDir)
	staticRouteService := staticroute.New(staticRouteStore, ndmsClient, catalog, log, loggingService)

	// DNS route subscription auto-refresh scheduler
	dnsRefreshScheduler := dnsroute.NewScheduler(dnsRouteService, settingsStore, log)
	dnsRefreshScheduler.Start()

	// Create external tunnel service
	externalService := external.NewService(awgStore, settingsStore, tunnelService, log)

	// System WireGuard tunnels (read-only + ASC editing)
	systemTunnelSvc := systemtunnel.New(ndmsClient)

	testService := testing.NewService(awgStore, log, loggingService)

	// Ping check service
	pingCheckService := pingcheck.NewService(settingsStore, awgStore, wgClient, log, loggingService)
	pingCheckService.Start()
	defer pingCheckService.Stop()

	// Unified facade: kernel → custom loop, NativeWG → NDMS native
	pingCheckFacade := pingcheck.NewFacade(pingCheckService, awgStore, settingsStore, nwgOp)

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

	// Managed WireGuard server service
	managedService := managed.New(ndmsClient, settingsStore, slog.Default().With("component", "managed"), loggingService)

	// Terminal manager (ttyd lifecycle)
	terminalManager := terminal.New(log)

	// Access policy service (NDMS ip policy management)
	accessPolicySvc := accesspolicy.New(ndmsClient, settingsStore, log, loggingService)

	// Client route service (per-device VPN routing)
	clientRouteStore := storage.NewClientRouteStore(*dataDir)
	clientRouteService := clientroute.New(
		clientRouteStore,
		operator,
		catalog,
		loggingService,
	)
	// Create orchestrator — single brain for all lifecycle decisions.
	orch := orchestrator.New(awgStore, operator, nwgOp, stateMgr, wanModel, ndmsClient, log, loggingService)
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
	if osdetect.Is5() {
		orch.SetDNSRoute(dnsRouteService)
	}
	orch.SetStaticRoute(staticRouteService)
	orch.SetClientRoute(clientRouteService)

	eventBus := events.NewBus()
	orch.SetEventBus(eventBus)
	loggingService.SetEventBus(eventBus)
	tunnelService.SetEventBus(eventBus)
	pingCheckFacade.SetEventBus(eventBus)

	// Start DNS failover listener after event bus is wired
	dnsFailover.SetEventBus(eventBus)
	dnsFailover.StartListener(eventBus)
	defer dnsFailover.StopListener()

	// Traffic Collector — periodically collects traffic metrics, publishes via SSE.
	trafficCollector := traffic.NewCollector(eventBus, trafficHistory, tunnelService)
	trafficCollector.Start()
	defer trafficCollector.Stop()

	// Connectivity Monitor — periodically checks tunnel connectivity, publishes via SSE.
	connAdapter := connectivity.NewAdapter(tunnelService, awgStore, testService)
	connMonitor := connectivity.NewMonitor(eventBus, connAdapter, connAdapter, connAdapter)
	connMonitor.Start()
	defer connMonitor.Stop()

	// Register routing snapshot providers with catalog.
	catalog.SetSnapshotProvider("dnsRoutes", func(ctx context.Context) interface{} {
		routes, _ := dnsRouteService.List(ctx)
		return routes
	})
	catalog.SetSnapshotProvider("staticRoutes", func(ctx context.Context) interface{} {
		routes, _ := staticRouteService.List()
		return routes
	})
	catalog.SetSnapshotProvider("accessPolicies", func(ctx context.Context) interface{} {
		policies, _ := accessPolicySvc.List(ctx)
		return policies
	})
	catalog.SetSnapshotProvider("policyDevices", func(ctx context.Context) interface{} {
		devices, _ := accessPolicySvc.ListDevices(ctx)
		return devices
	})
	catalog.SetSnapshotProvider("policyInterfaces", func(ctx context.Context) interface{} {
		ifaces, _ := accessPolicySvc.ListGlobalInterfaces(ctx)
		return ifaces
	})
	catalog.SetSnapshotProvider("clientRoutes", func(ctx context.Context) interface{} {
		routes, _ := clientRouteService.List()
		return routes
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
		ndmsClient,
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
	)

	srv.SetTrafficCollector(trafficCollector)

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

	bootLog := logging.NewScopedLogger(loggingService, logging.GroupSystem, logging.SubBoot)

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

			// Clean up stale userspace PID files (kernel doesn't need cleanup —
			// bootTunnels handles it via lifecycle-based Start).
			if backendImpl.Type() != backend.TypeKernel {
				cleanupStaleUserspaceState(log)
			}

			// Seed WAN model with current interface state from NDMS.
			// Must happen before tunnel start so ISP resolution works.
			populateWANModel(shutdownCtx, ndmsClient, wanModel, log)

			// Migrate legacy NDMS ID values to kernel names (one-time after model is populated).
			tunnelService.MigrateISPInterfaceToKernel()

			// Detect actual WAN state.
			if _, err := ndmsClient.GetDefaultGatewayInterface(shutdownCtx); err != nil {
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
		// PID files are valid (not stale) — do NOT call cleanupStaleUserspaceState.
		populateWANModel(context.Background(), ndmsClient, wanModel, log)

		// Migrate legacy NDMS ID values to kernel names (one-time after model is populated).
		tunnelService.MigrateISPInterfaceToKernel()

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
func populateWANModel(ctx context.Context, ndmsClient ndms.Client, model *wan.Model, log *logger.Logger) {
	interfaces, err := ndmsClient.QueryAllWANInterfaces(ctx)
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
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

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

	// Create service components
	ndmsClient := ndms.New()
	wgClient := wg.New()
	backendImpl := backend.New(log)
	stateMgr := state.New(ndmsClient, wgClient, backendImpl, nil)
	firewallMgr := firewall.New(backendImpl.Type() == backend.TypeKernel, osdetect.Is5(), nil)
	operator := ops.NewOperator(ndmsClient, wgClient, backendImpl, firewallMgr, log)
	cleanupRCI := rci.New()
	nwgOp := nwg.NewOperator(log, ndmsClient, cleanupRCI, nil)
	tunnelService := service.New(awgStore, nwgOp, operator, stateMgr, log, wan.NewModel(), nil)

	// Wire orchestrator for lifecycle operations (Delete needs it)
	cleanupOrch := orchestrator.New(awgStore, operator, nwgOp, stateMgr, wan.NewModel(), ndmsClient, log, nil)
	tunnelService.SetOrchestrator(cleanupOrch)
	nwgOp.SetHookNotifier(cleanupOrch)
	if os5Op, ok := operator.(interface {
		SetHookNotifier(tunnel.HookNotifier)
	}); ok {
		os5Op.SetHookNotifier(cleanupOrch)
	}

	// Create auxiliary services
	dnsStore := dnsroute.NewStore(dataDir)
	dnsStore.Load()
	dnsSvc := dnsroute.NewService(dnsStore, ndmsClient, nil, log, nil)

	managedSvc := managed.New(ndmsClient, settingsStore, slog.Default(), nil)
	accessPolicySvc := accesspolicy.New(ndmsClient, settingsStore, log, nil)

	// Client route service for cleanup
	clientRouteStore := storage.NewClientRouteStore(dataDir)
	clientRouteSvc := clientroute.New(clientRouteStore, operator, nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Single cleanup call — all business logic in CleanupService
	cleanupSvc := cleanup.New(tunnelService, awgStore, dnsSvc, managedSvc, accessPolicySvc, clientRouteSvc, ndmsClient)
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

