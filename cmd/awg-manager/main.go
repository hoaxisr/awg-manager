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
	"github.com/hoaxisr/awg-manager/internal/auth"
	"github.com/hoaxisr/awg-manager/internal/dnsroute"
	"github.com/hoaxisr/awg-manager/internal/managed"
	"github.com/hoaxisr/awg-manager/internal/staticroute"
	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/pingcheck"
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
	"github.com/hoaxisr/awg-manager/internal/tunnel/netutil"
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

// bootInProgress is set to 1 during boot initialization, 0 when done.
var bootInProgress int32

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
	nwgOp := nwg.NewOperator(log, ndmsClient, loggingService)

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

	// NOTE: RestoreEndpointTracking is called AFTER populateWANModel in both
	// boot and normal-restart paths. It needs WAN model populated so that
	// auto-mode tunnels can resolve ISP interface via NDMS gateway query.

	// DNS route service (OS5 only — routes domains through tunnels via NDMS)
	dnsRouteStore := dnsroute.NewStore(*dataDir)
	if _, err := dnsRouteStore.Load(); err != nil {
		log.Warn("Failed to load dns-routes", map[string]interface{}{"error": err.Error()})
	}
	dnsRouteService := dnsroute.NewService(
		dnsRouteStore,
		ndmsClient,
		&dnsRouteTunnelLister{svc: tunnelService, ndms: ndmsClient, store: awgStore},
		log,
		loggingService,
	)

	// Static route service for IP-based routing through tunnels
	staticRouteStore := storage.NewStaticRouteStore(*dataDir)
	staticRouteService := staticroute.New(staticRouteStore, operator, log, loggingService)

	// DNS route subscription auto-refresh scheduler
	dnsRefreshScheduler := dnsroute.NewScheduler(dnsRouteService, settingsStore, log)
	dnsRefreshScheduler.Start()

	// Create external tunnel service
	externalService := external.NewService(awgStore, settingsStore, tunnelService, log)

	// System WireGuard tunnels (read-only + ASC editing)
	systemTunnelSvc := systemtunnel.New(ndmsClient)

	testService := testing.NewService(awgStore, log)

	// Ping check service
	pingCheckService := pingcheck.NewService(settingsStore, awgStore, log, loggingService)
	pingCheckService.SetMonitorCallback(func(tunnelID string, isDead bool) error {
		ctx := context.Background()
		if isDead {
			return tunnelService.HandleMonitorDead(ctx, tunnelID)
		}
		return tunnelService.HandleMonitorRecovered(ctx, tunnelID)
	})
	pingCheckService.SetForcedRestartCallback(func(tunnelID string) error {
		return tunnelService.HandleForcedRestart(context.Background(), tunnelID)
	})
	pingCheckService.Start()
	defer pingCheckService.Stop()

	// Unified facade: kernel → custom loop, NativeWG → NDMS native
	pingCheckFacade := pingcheck.NewFacade(pingCheckService, awgStore, settingsStore, nwgOp)

	// Wire reconcile hooks so NeedsStop pauses PingCheck, NeedsStart resumes it
	tunnelService.SetReconcileHooks(&pingCheckReconcileHooks{pc: pingCheckFacade})
	tunnelService.SetDnsRouteHooks(dnsRouteService)
	tunnelService.SetStaticRouteHooks(staticRouteService)
	staticRouteService.SetTunnelRunningCheck(func(ctx context.Context, tunnelID string) bool {
		if tunnel.IsSystemTunnel(tunnelID) {
			return true // system tunnels are always considered running
		}
		return tunnelService.GetState(ctx, tunnelID).State == tunnel.StateRunning
	})

	// Resolve tunnelID → kernel interface name for static routes.
	resolveIfaceName := func(ctx context.Context, tunnelID string) string {
		if tunnel.IsSystemTunnel(tunnelID) {
			ndmsName := tunnel.SystemTunnelName(tunnelID)
			if ndmsClient != nil {
				return ndmsClient.GetSystemName(ctx, ndmsName)
			}
			return ndmsName
		}
		// NativeWG tunnels use nwgX interface names, not opkgtunX.
		if stored, err := awgStore.Get(tunnelID); err == nil && stored.Backend == "nativewg" {
			return nwg.NewNWGNames(stored.NWGIndex).IfaceName
		}
		return tunnel.NewNames(tunnelID).IfaceName
	}
	staticRouteService.SetResolveIfaceName(resolveIfaceName)

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
	accessPolicySvc := accesspolicy.New(ndmsClient, log, loggingService)

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
	)

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
	tunnelBootLog := logging.NewScopedLogger(loggingService, logging.GroupTunnel, logging.SubLifecycle)

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
		atomic.StoreInt32(&bootInProgress, 1)
		srv.SetBootStatusFunc(func() bool { return atomic.LoadInt32(&bootInProgress) == 1 })

		bootLog.Info("startup", "",
			fmt.Sprintf("Boot detected (uptime %ds), starting tunnels", int(uptime)))

		go func() {
			defer atomic.StoreInt32(&bootInProgress, 0)

			// Wait for NDMS to fully initialize OpkgTun interface subsystem.
			// Without this delay, kernel tunnels enter start/stop loops because
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
			// forceRestartTunnels handles it via Stop+Start).
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
				tunnelService.HandleWANDown(shutdownCtx, "")
			} else {
				forceRestartTunnels(shutdownCtx, tunnelService, awgStore, nwgOp, tunnelBootLog, log)
				reconcileStaticRoutes(shutdownCtx, staticRouteService, tunnelService, ndmsClient, log)

				if osdetect.Is5() {
					if err := dnsRouteService.Reconcile(shutdownCtx); err != nil {
						log.Warn("boot: dns-route reconcile failed", map[string]interface{}{"error": err.Error()})
					}
				}
			}

			bootLog.Info("startup", "", "Boot initialization complete")
		}()
	} else {
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

		reconnectTunnels(context.Background(), tunnelService, awgStore, nwgOp, pingCheckFacade, tunnelBootLog, log)
		reconcileStaticRoutes(context.Background(), staticRouteService, tunnelService, ndmsClient, log)

		if osdetect.Is5() {
			if err := dnsRouteService.Reconcile(context.Background()); err != nil {
				log.Warn("dns-route reconcile failed", map[string]interface{}{"error": err.Error()})
			}
		}
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

// StartupLogger interface for logging startup events.
// pingCheckReconcileHooks bridges reconcile events to PingCheck facade.
type pingCheckReconcileHooks struct {
	pc *pingcheck.Facade
}

func (h *pingCheckReconcileHooks) OnReconcileStart(tunnelID, tunnelName string) {
	h.pc.StartMonitoring(tunnelID, tunnelName)
}

func (h *pingCheckReconcileHooks) OnReconcileStop(tunnelID string) {
	h.pc.PauseMonitoring(tunnelID)
}

func (h *pingCheckReconcileHooks) OnTunnelDelete(tunnelID string) {
	h.pc.StopMonitoring(tunnelID)
}

// dnsRouteTunnelLister adapts tunnel.service.Service to dnsroute.TunnelLister.
type dnsRouteTunnelLister struct {
	svc   service.Service
	ndms  ndms.Client
	store *storage.AWGTunnelStore
}

func (l *dnsRouteTunnelLister) ListTunnelInfo(ctx context.Context) ([]dnsroute.TunnelInfo, error) {
	tunnels, err := l.svc.List(ctx)
	if err != nil {
		return nil, err
	}

	// Track managed NDMS names to filter them out from system interfaces.
	managed := make(map[string]bool)
	var result []dnsroute.TunnelInfo
	for _, t := range tunnels {
		var ndmsName string
		if t.Backend == "nativewg" {
			// NativeWG tunnels use Wireguard{N} as NDMS name
			if stored, err := l.store.Get(t.ID); err == nil {
				ndmsName = nwg.NewNWGNames(stored.NWGIndex).NDMSName
			}
		} else {
			ndmsName = tunnel.NewNames(t.ID).NDMSName
		}
		if ndmsName == "" {
			continue // skip if we couldn't determine NDMS name
		}
		managed[ndmsName] = true
		result = append(result, dnsroute.TunnelInfo{
			ID:       t.ID,
			Name:     t.Name,
			NDMSName: ndmsName,
			Status:   t.State.String(),
		})
	}

	// Append unmanaged system interfaces: Wireguard, Proxy, OpkgTun (if NDMS is available).
	if l.ndms != nil {
		wgIfaces, err := l.ndms.ListWireguardInterfaces(ctx)
		if err == nil {
			for _, iface := range wgIfaces {
				if managed[iface.Name] {
					continue
				}
				name := iface.Name
				if iface.Description != "" {
					name = iface.Name + " (" + iface.Description + ")"
				}
				result = append(result, dnsroute.TunnelInfo{
					ID:       "system:" + iface.Name,
					Name:     name,
					NDMSName: iface.Name,
					Status:   "system",
					System:   true,
				})
			}
		}
	}

	// Append WAN interfaces (ISP, PPPoE, LTE, etc.)
	wanModel := l.svc.WANModel()
	if wanModel != nil {
		for _, iface := range wanModel.ForUI() {
			label := iface.Label
			if label == "" {
				label = iface.Name
			}
			result = append(result, dnsroute.TunnelInfo{
				ID:       "wan:" + iface.Name,
				Name:     label,
				NDMSName: iface.ID, // NDMS ID (e.g. "ISP", "PPPoE0") for dns-proxy route
				Status:   boolToStatus(iface.Up),
				WAN:      true,
			})
		}
	}

	return result, nil
}

func boolToStatus(up bool) string {
	if up {
		return "up"
	}
	return "down"
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

// reconcileStaticRoutes restores static IP routes for currently running tunnels.
func reconcileStaticRoutes(ctx context.Context, svc *staticroute.ServiceImpl, tunnelSvc service.Service, ndmsClient ndms.Client, log *logger.Logger) {
	tunnels, err := tunnelSvc.List(ctx)
	if err != nil {
		log.Warn("reconcileStaticRoutes: failed to list tunnels", map[string]interface{}{"error": err.Error()})
		return
	}
	running := make(map[string]string)
	for _, t := range tunnels {
		if t.State == tunnel.StateRunning {
			running[t.ID] = t.InterfaceName
		}
	}

	// Include system tunnels that have static routes assigned.
	addSystemTunnels(ctx, running, svc.SystemTunnelIDs(), ndmsClient)

	if len(running) == 0 {
		return
	}
	if err := svc.Reconcile(ctx, running); err != nil {
		log.Warn("reconcileStaticRoutes: failed", map[string]interface{}{"error": err.Error()})
	}
}

// addSystemTunnels resolves system tunnel IDs to kernel names and adds them to the running map.
func addSystemTunnels(ctx context.Context, running map[string]string, systemIDs []string, ndmsClient ndms.Client) {
	if ndmsClient == nil || len(systemIDs) == 0 {
		return
	}
	for _, id := range systemIDs {
		ndmsName := tunnel.SystemTunnelName(id)
		kernelName := ndmsClient.GetSystemName(ctx, ndmsName)
		running[id] = kernelName
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

// forceRestartTunnels does stop+start for all enabled tunnels at boot.
// Stop cleans up stale interfaces/state, Start creates everything fresh.
// NDMS properly initializes firewall and routing on a clean Start cycle.
func forceRestartTunnels(ctx context.Context, tunnelSvc service.Service, store *storage.AWGTunnelStore, nwgOp *nwg.OperatorNativeWG, appLog *logging.ScopedLogger, log *logger.Logger) {
	tunnels, err := tunnelSvc.List(ctx)
	if err != nil {
		return
	}

	for _, t := range tunnels {
		if !t.Enabled {
			continue
		}

		// NativeWG: on firmware >= 5.01.A.4 (native ASC), NDMS restores
		// the interface and peer connect state — no action needed.
		// On older firmware (< 5.01.A.4), proxy ports change after reboot —
		// RestoreKmodTunnel is not enough, need full Stop → Start cycle.
		if t.Backend == "nativewg" && nwgOp != nil {
			if ndmsinfo.SupportsWireguardASC() {
				continue
			}
			// Old firmware with proxy — fall through to Stop → Start below
		}

		// Stop (ignore errors — tunnel may not be running after boot)
		_ = tunnelSvc.Stop(ctx, t.ID)

		log.Info("boot: starting tunnel", map[string]interface{}{"id": t.ID, "name": t.Name})
		if err := tunnelSvc.Start(ctx, t.ID); err != nil {
			appLog.Warn("boot-start", t.Name, "Start failed, retrying in 3s: "+err.Error())

			time.Sleep(3 * time.Second)
			if err := tunnelSvc.Start(ctx, t.ID); err != nil {
				appLog.Warn("boot-start", t.Name, "Retry also failed: "+err.Error())
			} else {
				appLog.Info( "boot-start", t.Name, "Tunnel started (after retry)")
			}
		} else {
			appLog.Info( "boot-start", t.Name, "Tunnel started")
		}
	}
}

// reconnectTunnels restores in-memory tracking for already-running tunnels
// without restarting them. Used on daemon restart (upgrade) where child
// processes survive syscall.Exec — all kernel state (TUN, routes, iptables,
// NDMS config) is intact, only operator in-memory maps need restoration.
// Tunnels that are enabled but not running (process died) get a full Start.
func reconnectTunnels(ctx context.Context, tunnelSvc service.Service, store *storage.AWGTunnelStore, nwgOp *nwg.OperatorNativeWG, pingCheckSvc *pingcheck.Facade, appLog *logging.ScopedLogger, log *logger.Logger) {
	tunnels, err := tunnelSvc.List(ctx)
	if err != nil {
		return
	}

	// Restore endpoint routes and ISP tracking for running tunnels.
	tunnelSvc.RestoreEndpointTracking(ctx)

	for _, t := range tunnels {
		state := t.StateInfo.State

		// NativeWG: restore kmod proxy for running tunnels (only on older firmware)
		if t.Backend == "nativewg" {
			if state == tunnel.StateRunning {
				if !ndmsinfo.SupportsWireguardASC() {
					if stored, err := store.Get(t.ID); err == nil && nwgOp != nil {
						if err := nwgOp.RestoreKmodTunnel(ctx, stored); err != nil {
							appLog.Warn( "reconnect", t.Name, "kmod restore failed: "+err.Error())
						}
					}
				}
				pingCheckSvc.StartMonitoring(t.ID, t.Name)
				appLog.Info( "reconnect", t.Name, "NativeWG tunnel reconnected")
			}
			continue
		}

		if state == tunnel.StateRunning {
			pingCheckSvc.StartMonitoring(t.ID, t.Name)
			appLog.Info( "reconnect", t.Name, "Tunnel reconnected")
			continue
		}

		if !t.Enabled {
			continue
		}

		// Enabled but not running — full start (process died during update)
		_ = tunnelSvc.Stop(ctx, t.ID)

		if err := tunnelSvc.Start(ctx, t.ID); err != nil {
			appLog.Warn( "reconnect", t.Name, "Start failed, retrying in 3s: "+err.Error())

			time.Sleep(3 * time.Second)
			if err := tunnelSvc.Start(ctx, t.ID); err != nil {
				appLog.Warn("reconnect", t.Name, "Retry also failed: "+err.Error())
			} else {
				appLog.Info( "reconnect", t.Name, "Tunnel started (after retry)")
			}
		} else {
			appLog.Info( "reconnect", t.Name, "Tunnel started")
		}
	}
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

// ensureServiceEnv ensures PATH and LD_LIBRARY_PATH contain system directories.
// Required for child processes (ndmc, ip, awg) to find binaries and libraries.
func ensureServiceEnv() {
	path := os.Getenv("PATH")
	if !strings.Contains(path, "/usr/sbin") {
		os.Setenv("PATH", "/bin:/sbin:/usr/bin:/usr/sbin:/opt/bin:/opt/sbin:"+path)
	}
	ldPath := os.Getenv("LD_LIBRARY_PATH")
	if !strings.Contains(ldPath, "/usr/lib") {
		os.Setenv("LD_LIBRARY_PATH", "/lib:/usr/lib:"+ldPath)
	}
}

// runCleanup stops and deletes all tunnels managed by awg-manager.
// This is used during package uninstall to properly clean up resources.
func runCleanup(dataDir string) {
	fmt.Println("awg-manager cleanup: deleting all managed tunnels...")

	log := logger.New()
	defer log.Close()

	awgStore := storage.NewAWGTunnelStore(
		filepath.Join(dataDir, "tunnels"),
		log,
	)

	// List all tunnels from storage
	tunnels, err := awgStore.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to list tunnels: %v\n", err)
		return
	}

	if len(tunnels) == 0 {
		fmt.Println("No tunnels to clean up.")
	}

	// Init NDMS info so osdetect.Is5() works correctly.
	// Without this, cleanup falls back to OS4 operator and skips NDMS interface removal.
	initCtx, initCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := ndmsinfo.Init(initCtx, 10*time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: NDMS not available: %v\n", err)
	}
	initCancel()

	settingsStore := storage.NewSettingsStore(dataDir)
	settingsStore.Load()

	// Create service components for proper cleanup
	ndmsClient := ndms.New()
	wgClient := wg.New()
	backendImpl := backend.New(log)
	stateMgr := state.New(ndmsClient, wgClient, backendImpl, nil)
	firewallMgr := firewall.New(backendImpl.Type() == backend.TypeKernel, osdetect.Is5(), nil)
	operator := ops.NewOperator(ndmsClient, wgClient, backendImpl, firewallMgr, log)
	nwgOp := nwg.NewOperator(log, ndmsClient, nil)
	tunnelService := service.New(awgStore, nwgOp, operator, stateMgr, log, wan.NewModel(), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Remove endpoint routes before deleting tunnels.
	// The operator's in-memory tracking is empty in a fresh cleanup process,
	// so we use the persisted resolved IP (stored by daemon), with DNS fallback.
	for _, t := range tunnels {
		if t.Backend == "nativewg" {
			continue // NDMS manages routing natively via "via" peer property
		}
		if t.Peer.Endpoint == "" {
			continue // no endpoint → no route to clean
		}

		// Prefer stored resolved IP (reliable — same IP that was actually routed)
		ip := t.ResolvedEndpointIP
		if ip == "" && t.Peer.Endpoint != "" {
			// Fallback to DNS resolve (for tunnels from older versions without stored IP)
			var err error
			ip, err = netutil.ResolveEndpointIP(t.Peer.Endpoint)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  Warning: could not resolve endpoint for %s: %v\n", t.ID, err)
				continue
			}
			fmt.Printf("  %s: no stored IP, resolved via DNS: %s\n", t.ID, ip)
		}

		if ip != "" {
			_ = ndmsClient.RemoveHostRoute(ctx, ip)
			fmt.Printf("  Removed endpoint route for %s (%s)\n", t.ID, ip)
		}
	}

	var deleted, failed int
	for _, t := range tunnels {
		fmt.Printf("  Deleting tunnel %s (%s)...\n", t.ID, t.Name)
		if err := tunnelService.Delete(ctx, t.ID); err != nil {
			fmt.Fprintf(os.Stderr, "    Error: %v\n", err)
			failed++
		} else {
			fmt.Printf("    Deleted successfully\n")
			deleted++
		}
	}

	fmt.Printf("\nCleanup complete: %d deleted, %d failed\n", deleted, failed)

	// Clean up DNS route NDMS objects (OS5 only)
	if osdetect.Is5() {
		fmt.Println("Cleaning up DNS routes...")
		dnsStore := dnsroute.NewStore(dataDir)
		dnsStore.Load()
		// Save empty data so reconcile removes all AWG_* objects from the router
		dnsStore.Save(dnsroute.EmptyStoreData())
		dnsSvc := dnsroute.NewService(dnsStore, ndmsClient, nil, log, nil)
		if err := dnsSvc.Reconcile(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: DNS route cleanup failed: %v\n", err)
		} else {
			fmt.Println("  DNS routes cleaned up")
		}
	}

	// Delete managed server (if exists)
	managedSvc := managed.New(ndmsClient, settingsStore, slog.Default(), nil)
	if ms := managedSvc.Get(); ms != nil {
		fmt.Printf("Deleting managed server %s...\n", ms.InterfaceName)
		if err := managedSvc.Delete(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: managed server cleanup failed: %v\n", err)
		} else {
			fmt.Println("  Managed server deleted")
		}
	}

	// Clean up access policies (OS5 only)
	if osdetect.Is5() {
		fmt.Println("Cleaning up access policies...")
		accessPolicySvc := accesspolicy.New(ndmsClient, log, nil)
		policies, err := accessPolicySvc.List(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: failed to list policies: %v\n", err)
		} else if len(policies) > 0 {
			for _, p := range policies {
				fmt.Printf("  Deleting policy %s (%s)...\n", p.Name, p.Description)
				if err := accessPolicySvc.Delete(ctx, p.Name); err != nil {
					fmt.Fprintf(os.Stderr, "    Error: %v\n", err)
				}
			}
			fmt.Printf("  %d policies deleted\n", len(policies))
		} else {
			fmt.Println("  No access policies to clean up")
		}
	}

	// Clean up remaining files
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

