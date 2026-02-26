package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hoaxisr/awg-manager/internal/auth"
	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/pingcheck"
	"github.com/hoaxisr/awg-manager/internal/policy"
	"github.com/hoaxisr/awg-manager/internal/server"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
	"github.com/hoaxisr/awg-manager/internal/testing"
	"github.com/hoaxisr/awg-manager/internal/traffic"
	"github.com/hoaxisr/awg-manager/internal/sys/kmod"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	sysexec "github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/tunnel/backend"
	"github.com/hoaxisr/awg-manager/internal/tunnel/external"
	"github.com/hoaxisr/awg-manager/internal/tunnel/firewall"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
	"github.com/hoaxisr/awg-manager/internal/tunnel/netutil"
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
	changeBackend := flag.String("change-backend", "", "Change backend mode and restart (auto|kernel|userspace)")
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

	// Change backend mode via running daemon or direct settings save
	if *changeBackend != "" {
		runChangeBackend(*dataDir, *changeBackend)
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

	// Redirect stderr to file so unrecovered panics are captured.
	// start-stop-daemon -b closes stderr → panic stack traces are lost.
	if f, err := os.OpenFile(filepath.Join(*dataDir, "panic.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644); err == nil {
		os.Stderr = f
		defer f.Close()
	}

	// Startup log — overwritten each start, persists for diagnostics
	slog := newStartupLog(filepath.Join(*dataDir, "startup.log"))
	defer slog.close()

	uptime := getUptime()
	slog.logf("awg-manager %s starting", version)
	slog.logf("uptime: %.1fs", uptime)

	log, _ := logger.New(logger.Config{})
	defer log.Close()

	// Settings (load first to get server config)
	settingsStore := storage.NewSettingsStore(*dataDir)
	settings, err := settingsStore.Load()
	if err != nil {
		slog.logf("FATAL: failed to load settings: %v", err)
		log.Error("Failed to load settings", map[string]interface{}{"error": err.Error()})
		fmt.Fprintf(os.Stderr, "Failed to load settings: %v\n", err)
		os.Exit(1)
	}
	slog.logf("settings loaded (backend=%s, bootDelay=%d)", settings.BackendMode, settings.BootDelaySeconds)

	awgStore := storage.NewAWGTunnelStore(
		filepath.Join(*dataDir, "tunnels"),
		log,
	)

	// Fetch NDMS version info via RCI (single HTTP call, cached for all consumers).
	// At early boot, retry until NDMS responds (up to 30s).
	ndmsTimeout := time.Second // normal restart: single attempt
	if uptime > 0 && uptime < 120 {
		ndmsTimeout = 30 * time.Second // boot: wait for NDMS
		slog.logf("boot detected, waiting up to %s for NDMS", ndmsTimeout)
	}
	ndmsStart := time.Now()
	if err := ndmsinfo.Init(context.Background(), ndmsTimeout); err != nil {
		slog.logf("NDMS init failed after %s: %v", time.Since(ndmsStart).Round(time.Millisecond), err)
		log.Warn("NDMS version info not available", map[string]interface{}{"error": err.Error()})
	} else {
		info := ndmsinfo.Get()
		slog.logf("NDMS ready in %s: hw_id=%s release=%s model=%s",
			time.Since(ndmsStart).Round(time.Millisecond), info.HwID, info.Release, info.Model)
	}

	// Load kernel module if available (before backend detection)
	kmodLoader := kmod.New()
	slog.logf("kmod: model=%s soc=%s", kmodLoader.Model(), kmodLoader.SoC())

	if model := kmodLoader.Model(); model != "" {
		log.Info("Detected router model", map[string]interface{}{"model": model})
	}
	// Clean up old SoC-based module directories from previous IPK versions
	if removed := kmodLoader.CleanupLegacyModules(); removed > 0 {
		slog.logf("kmod: cleaned %d legacy SoC directories", removed)
		log.Info("Cleaned up legacy module directories", map[string]interface{}{"removed": removed})
	}
	// Set target kmod version from settings (for user-selected version pinning)
	if settings.KmodVersion != "" {
		kmodLoader.SetTargetVersion(settings.KmodVersion)
		log.Info("Kernel module target version", map[string]interface{}{"version": settings.KmodVersion})
	}
	// EnsureModule: select bundled .ko if available → insmod
	if err := kmodLoader.EnsureModule(context.Background()); err != nil {
		slog.logf("kmod: EnsureModule failed: %v", err)
		slog.logf("kmod: dmesg: %s", kmodLoader.GetLoadError())
		log.Warn("Kernel module not available", map[string]interface{}{
			"error": err.Error(),
			"dmesg": kmodLoader.GetLoadError(),
		})
	} else if kmodLoader.IsLoaded() {
		slog.logf("kmod: loaded %s (version=%s)", kmodLoader.ModulePath(), kmodLoader.OnDiskVersion())
		log.Info("Kernel module loaded", map[string]interface{}{
			"path": kmodLoader.ModulePath(),
		})
	}

	// Create tunnel service components
	ndmsClient := ndms.New()
	wgClient := wg.New()
	backendImpl := backend.NewWithMode(settings.BackendMode, log)
	slog.logf("backend: %s", backendImpl.Type())
	stateMgr := state.New(ndmsClient, wgClient, backendImpl)
	firewallMgr := firewall.New(backendImpl.Type() == backend.TypeKernel, osdetect.Is5())
	operator := ops.NewOperator(ndmsClient, wgClient, backendImpl, firewallMgr, log)

	// Create WAN state model (populated at boot, updated by hooks).
	// Re-populate callback fires when a hook reports an unknown interface
	// (USB hotplug, new PPPoE configured after boot, etc.).
	wanModel := wan.NewModel()
	wanModel.SetRepopulateFn(func() {
		populateWANModel(context.Background(), ndmsClient, wanModel, log)
	})

	// Create the main tunnel service
	tunnelService := service.New(awgStore, stateMgr, operator, log, wanModel)

	// Migrate legacy ISPInterface="none" to "" (auto) for tunnels from older versions.
	tunnelService.MigrateISPInterfaceNone()

	// NOTE: RestoreEndpointTracking is called AFTER populateWANModel in both
	// boot and normal-restart paths. It needs WAN model populated so that
	// auto-mode tunnels can resolve ISP interface via NDMS gateway query.

	// Policy service for per-client routing
	policyStore := storage.NewPolicyStore(*dataDir)
	policyService := policy.New(policyStore, operator, log)

	// Create external tunnel service
	externalService := external.NewService(awgStore, settingsStore, tunnelService, log)

	testService := testing.NewService(awgStore, log)

	// Ping check service
	pingCheckService := pingcheck.NewService(settingsStore, awgStore, log)
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

	// Wire reconcile hooks so NeedsStop pauses PingCheck, NeedsStart resumes it
	tunnelService.SetReconcileHooks(&pingCheckReconcileHooks{pc: pingCheckService})
	tunnelService.SetPolicyHooks(policyService)
	policyService.SetTunnelRunningCheck(func(ctx context.Context, tunnelID string) bool {
		return tunnelService.GetState(ctx, tunnelID).State == tunnel.StateRunning
	})

	// Auth components
	keeneticClient := auth.NewKeeneticClient()
	sessionStore := auth.NewSessionStore()
	sessionStore.SetLogger(log)
	defer sessionStore.Stop()

	// Logging service
	loggingService := logging.NewService(settingsStore)
	defer loggingService.Stop()
	pingCheckService.SetLoggingService(loggingService)
	operator.SetAppLogger(loggingService)

	// Traffic history (in-memory, 24h)
	trafficHistory := traffic.New()
	defer trafficHistory.Stop()

	// Updater service
	updaterService := updater.New(version, settingsStore, log)
	updaterService.Start()
	defer updaterService.Stop()

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
		pingCheckService,
		loggingService,
		backendImpl,
		kmodLoader,
		updaterService,
		policyService,
		ndmsClient,
		trafficHistory,
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

	listenAddr := fmt.Sprintf("%s:%d", ip, selectedPort)
	srv.SetListenAddr(listenAddr)

	// Add loopback listener for reverse proxy support (nginx on 127.0.0.1)
	if ip != "0.0.0.0" && ip != "127.0.0.1" {
		srv.SetLoopbackAddr(fmt.Sprintf("127.0.0.1:%d", selectedPort))
	}

	slog.logf("OS: %s, listen: %s", osdetect.Get(), listenAddr)

	// Log startup information
	slog.logf("debug: logStartup")
	logStartup(loggingService, version, string(osdetect.Get()), listenAddr, settings)

	// Shutdown context — cancelled on shutdown
	slog.logf("debug: shutdown hooks")
	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
	defer shutdownCancel()

	// Register shutdown hooks for graceful cleanup before syscall.Exec restart.
	srv.AddShutdownHook(shutdownCancel)
	srv.AddShutdownHook(pingCheckService.Stop)
	srv.AddShutdownHook(sessionStore.Stop)
	srv.AddShutdownHook(loggingService.Stop)
	srv.AddShutdownHook(trafficHistory.Stop)

	slog.logf("debug: boot threshold")
	// Boot-wait: if uptime < threshold, defer boot tasks until NDMS is ready
	bootThreshold := 180
	if settings.BootDelaySeconds >= 120 {
		bootThreshold = settings.BootDelaySeconds
	}
	uptime = getUptime()
	// Boot detection: uptime < 5 min = always boot path (regardless of threshold).
	// Routers with slow init.d can exceed bootThreshold before awg-manager starts,
	// but WAN detection and deferred startup are still needed at boot.
	const bootDetectionMax = 300 // 5 minutes
	isBoot := (uptime > 0 && uptime < bootDetectionMax) || *forceBoot
	slog.logf("debug: uptime=%.0f threshold=%d bootDetectionMax=%d forceBoot=%v isBoot=%v",
		uptime, bootThreshold, bootDetectionMax, *forceBoot, isBoot)
	if isBoot {
		waitSec := 5 // immediate when forced
		if !*forceBoot {
			waitSec = bootThreshold - int(uptime)
			if waitSec < 0 {
				waitSec = 0
			}
		}
		readyAt := time.Now().Add(time.Duration(waitSec) * time.Second)
		srv.SetBootWait(readyAt)
		slog.logf("boot-wait: uptime=%.0fs < threshold=%ds, deferring %ds", uptime, bootThreshold, waitSec)
		log.Info("Boot wait: deferring initialization", map[string]interface{}{
			"uptimeSec": int(uptime),
			"waitSec":   waitSec,
		})
		loggingService.Log(logging.CategorySystem, "startup", "",
			fmt.Sprintf("Boot detected (uptime %ds), waiting %ds for NDMS initialization", int(uptime), waitSec))

		go func() {
			// Respect shutdown: if context is cancelled during boot wait, exit.
			select {
			case <-time.After(time.Until(readyAt)):
			case <-shutdownCtx.Done():
				slog.logf("boot-wait: shutdown during wait, aborting")
				return
			}
			slog.logf("boot-wait: timer expired, starting boot tasks")
			srv.SetBootPhaseStarting()

			// Clean up stale userspace PID files (kernel doesn't need cleanup —
			// forceRestartTunnels handles it via Stop+Start).
			if backendImpl.Type() != backend.TypeKernel {
				slog.logf("boot: cleanup stale userspace state")
				cleanupStaleUserspaceState(log)
			}

			// Seed WAN model with current interface state from NDMS.
			// Must happen before tunnel start so ISP resolution works.
			slog.logf("boot: populateWANModel")
			populateWANModel(shutdownCtx, ndmsClient, wanModel, log)
			slog.logf("boot: populateWANModel done")

			// Detect actual WAN state.
			slog.logf("boot: detecting WAN state")
			if _, err := ndmsClient.GetDefaultGatewayInterface(shutdownCtx); err != nil {
				slog.logf("boot: WAN down (no default route: %v)", err)
				log.Info("Boot: no default route, WAN is down", nil)
				loggingService.Log(logging.CategorySystem, "startup", "",
					"WAN down at boot — waiting for WAN UP event")
				tunnelService.HandleWANDown(shutdownCtx, "")
			} else {
				slog.logf("boot: WAN up, starting tunnels")
				log.Info("Boot: default route found, WAN is up", nil)

				slog.logf("boot: forceRestartTunnels")
				forceRestartTunnels(shutdownCtx, tunnelService, loggingService, log)
				slog.logf("boot: forceRestartTunnels done")

				slog.logf("boot: reconcilePolicies")
				reconcilePolicies(shutdownCtx, policyService, tunnelService, log)
			}

			slog.logf("boot: complete (%.1fs since boot)", getUptime())
			srv.SetBootReady()
			log.Info("Boot initialization complete", nil)
			loggingService.Log(logging.CategorySystem, "startup", "", "Boot initialization complete")
		}()
	} else {
		// Normal start (daemon restart / upgrade): same flow as boot.
		slog.logf("normal start (uptime=%.0fs >= threshold=%ds)", uptime, bootThreshold)

		if backendImpl.Type() != backend.TypeKernel {
			slog.logf("cleanup stale userspace state")
			cleanupStaleUserspaceState(log)
		}

		slog.logf("populateWANModel")
		populateWANModel(context.Background(), ndmsClient, wanModel, log)
		slog.logf("populateWANModel done")

		slog.logf("forceRestartTunnels")
		forceRestartTunnels(context.Background(), tunnelService, loggingService, log)
		slog.logf("forceRestartTunnels done")

		slog.logf("reconcilePolicies")
		reconcilePolicies(context.Background(), policyService, tunnelService, log)
		slog.logf("reconcilePolicies done")
	}

	slog.logf("starting HTTP server")

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
		slog.logf("FATAL: server error: %v", err)
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}

// StartupLogger interface for logging startup events.
// pingCheckReconcileHooks bridges reconcile events to PingCheck service.
type pingCheckReconcileHooks struct {
	pc *pingcheck.Service
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

type StartupLogger interface {
	Log(category, action, target, message string)
	LogWarn(category, action, target, message string)
	LogError(category, action, target, message, errorMsg string)
}

// logStartup logs system startup information.
func logStartup(appLog StartupLogger, version, osVersion, listenAddr string, settings *storage.Settings) {
	appLog.Log(logging.CategorySystem, "startup", "", fmt.Sprintf("AWG Manager v%s started", version))
	appLog.Log(logging.CategorySystem, "startup", "", fmt.Sprintf("Keenetic OS: %s", osVersion))
	appLog.Log(logging.CategorySystem, "startup", "", fmt.Sprintf("Listening on %s", listenAddr))

	// Log feature status
	if settings.PingCheck.Enabled {
		appLog.Log(logging.CategorySystem, "startup", "", "Ping Check: enabled")
	}
	if settings.Logging.Enabled {
		appLog.Log(logging.CategorySystem, "startup", "", "Logging: enabled")
	}
}

// reconcilePolicies restores policy routing for currently running tunnels.
func reconcilePolicies(ctx context.Context, policySvc *policy.ServiceImpl, tunnelSvc service.Service, log *logger.Logger) {
	tunnels, err := tunnelSvc.List(ctx)
	if err != nil {
		log.Warn("reconcilePolicies: failed to list tunnels", map[string]interface{}{"error": err.Error()})
		return
	}
	running := make(map[string]string)
	for _, t := range tunnels {
		if t.State == tunnel.StateRunning {
			running[t.ID] = t.InterfaceName
		}
	}
	if len(running) == 0 {
		return
	}
	if err := policySvc.Reconcile(ctx, running); err != nil {
		log.Warn("reconcilePolicies: failed", map[string]interface{}{"error": err.Error()})
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
func forceRestartTunnels(ctx context.Context, tunnelSvc service.Service, appLog StartupLogger, log *logger.Logger) {
	tunnels, err := tunnelSvc.List(ctx)
	if err != nil {
		return
	}

	for _, t := range tunnels {
		if !t.Enabled {
			continue
		}

		// Stop (ignore errors — tunnel may not be running after boot)
		_ = tunnelSvc.Stop(ctx, t.ID)

		log.Info("boot: starting tunnel", map[string]interface{}{"id": t.ID, "name": t.Name})
		if err := tunnelSvc.Start(ctx, t.ID); err != nil {
			appLog.LogWarn(logging.CategoryTunnel, "boot-start", t.Name, "Start failed, retrying in 3s: "+err.Error())

			time.Sleep(3 * time.Second)
			if err := tunnelSvc.Start(ctx, t.ID); err != nil {
				appLog.LogError(logging.CategoryTunnel, "boot-start", t.Name, "Retry also failed", err.Error())
			} else {
				appLog.Log(logging.CategoryTunnel, "boot-start", t.Name, "Tunnel started (after retry)")
			}
		} else {
			appLog.Log(logging.CategoryTunnel, "boot-start", t.Name, "Tunnel started")
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

// cleanupStaleKernelInterfaces removes stale kernel interfaces after router reboot.
// After reboot, kernel interfaces from previous session may partially exist,
// causing NDMS conflicts when trying to recreate them.
func cleanupStaleKernelInterfaces(ctx context.Context, store *storage.AWGTunnelStore, log *logger.Logger) {
	tunnels, err := store.List()
	if err != nil {
		log.Warn("reboot cleanup: failed to list tunnels", map[string]interface{}{"error": err.Error()})
		return
	}

	for _, t := range tunnels {
		names := tunnel.NewNames(t.ID)
		// Remove stale kernel interface (ignore errors - may not exist)
		_, _ = sysexec.Run(ctx, "/opt/sbin/ip", "link", "del", "dev", names.IfaceName)
		log.Info("reboot cleanup: removed kernel interface", map[string]interface{}{"iface": names.IfaceName})
	}
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

// runChangeBackend sends a change-backend request to the running daemon,
// or saves the mode directly to settings.json if the daemon is not running.
func runChangeBackend(dataDir, mode string) {
	if mode != "auto" && mode != "kernel" && mode != "userspace" {
		fmt.Fprintf(os.Stderr, "Invalid mode: %s (must be auto, kernel, or userspace)\n", mode)
		os.Exit(1)
	}

	// Read settings to get the server port
	settingsStore := storage.NewSettingsStore(dataDir)
	settings, err := settingsStore.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load settings: %v\n", err)
		os.Exit(1)
	}

	// Get actual bind address (same logic as getServiceEndpoint)
	host, port := getServiceEndpoint(dataDir)

	// Try to send request to running daemon
	body, _ := json.Marshal(map[string]string{"mode": mode})
	url := fmt.Sprintf("http://%s:%d/api/system/change-backend", host, port)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		// Daemon not running — save directly
		fmt.Printf("Daemon not running, saving mode directly.\n")
		settings.BackendMode = mode
		if err := settingsStore.Save(settings); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to save settings: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Backend mode set to '%s'. Start the daemon to apply.\n", mode)
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error from daemon: %s\n", string(respBody))
		os.Exit(1)
	}

	fmt.Printf("Backend mode changed to '%s'. Daemon is restarting...\n", mode)
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

	log, _ := logger.New(logger.Config{})
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
		return
	}

	// Init NDMS info so osdetect.Is5() works correctly.
	// Without this, cleanup falls back to OS4 operator and skips NDMS interface removal.
	initCtx, initCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := ndmsinfo.Init(initCtx, 10*time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: NDMS not available: %v\n", err)
	}
	initCancel()

	// Read backend mode from settings to match the running configuration.
	// NewAuto() would miss kernel-specific cleanup if the module happened to
	// not be loaded at uninstall time.
	settingsStore := storage.NewSettingsStore(dataDir)
	settings, _ := settingsStore.Load()
	backendMode := ""
	if settings != nil {
		backendMode = settings.BackendMode
	}

	// Create service components for proper cleanup
	ndmsClient := ndms.New()
	wgClient := wg.New()
	backendImpl := backend.NewWithMode(backendMode, log)
	stateMgr := state.New(ndmsClient, wgClient, backendImpl)
	firewallMgr := firewall.New(backendImpl.Type() == backend.TypeKernel, osdetect.Is5())
	operator := ops.NewOperator(ndmsClient, wgClient, backendImpl, firewallMgr, log)
	tunnelService := service.New(awgStore, stateMgr, operator, log, wan.NewModel())

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Remove endpoint routes before deleting tunnels.
	// The operator's in-memory tracking is empty in a fresh cleanup process,
	// so we use the persisted resolved IP (stored by daemon), with DNS fallback.
	for _, t := range tunnels {
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

	// Clean up remaining files
	fmt.Println("Cleaning up files...")
	os.RemoveAll(filepath.Join(dataDir, "tunnels"))
	files, _ := filepath.Glob(filepath.Join(dataDir, "*.conf"))
	for _, f := range files {
		os.Remove(f)
	}
	os.Remove(filepath.Join(dataDir, "port"))
	os.RemoveAll("/opt/var/run/awg-manager")

	fmt.Println("Done.")
}

// startupLog writes timestamped lines to a file for boot diagnostics.
type startupLog struct {
	f *os.File
}

func newStartupLog(path string) *startupLog {
	f, err := os.Create(path)
	if err != nil {
		return &startupLog{} // noop if can't create
	}
	return &startupLog{f: f}
}

func (s *startupLog) logf(format string, args ...interface{}) {
	if s.f == nil {
		return
	}
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(s.f, "%s %s\n", time.Now().Format("15:04:05.000"), msg)
}

func (s *startupLog) close() {
	if s.f != nil {
		s.f.Close()
	}
}
