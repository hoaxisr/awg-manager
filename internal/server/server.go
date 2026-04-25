package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hoaxisr/awg-manager/internal/accesspolicy"
	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/deviceproxy"
	"github.com/hoaxisr/awg-manager/internal/auth"
	"github.com/hoaxisr/awg-manager/internal/clientroute"
	"github.com/hoaxisr/awg-manager/internal/connections"
	"github.com/hoaxisr/awg-manager/internal/diagnostics"
	"github.com/hoaxisr/awg-manager/internal/dnscheck"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/hydraroute"
	"github.com/hoaxisr/awg-manager/internal/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/rci"
	"github.com/hoaxisr/awg-manager/internal/routing"
	"github.com/hoaxisr/awg-manager/internal/singbox"

	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/managed"
	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	ndmsmetrics "github.com/hoaxisr/awg-manager/internal/ndms/metrics"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	ndmstransport "github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/kmod"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
	"github.com/hoaxisr/awg-manager/internal/terminal"
	"github.com/hoaxisr/awg-manager/internal/testing"
	"github.com/hoaxisr/awg-manager/internal/traffic"
	"github.com/hoaxisr/awg-manager/internal/tunnel/backend"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
	"github.com/hoaxisr/awg-manager/internal/tunnel/systemtunnel"
	"github.com/hoaxisr/awg-manager/internal/updater"
)

const (
	DefaultPort       = 2222
	FallbackPortStart = 8080
	FallbackPortEnd   = 8090
	DefaultWebRoot    = "/opt/share/www/awg-manager"
)

// Config holds server configuration.
type Config struct {
	ListenAddr         string
	LoopbackListenAddr string // optional: 127.0.0.1:port for reverse proxy support
	WebRoot            string // Path to static files (SPA)
	Version            string
}

// Server is the HTTP server for awg-manager.
type Server struct {
	config              Config
	log                 *logger.Logger
	tunnelService       api.TunnelService
	externalService     api.ExternalTunnelService
	testingService      *testing.Service
	keenetic            *auth.KeeneticClient
	sessions            *auth.SessionStore
	settings            *storage.SettingsStore
	tunnels             *storage.AWGTunnelStore
	pingCheckService    api.PingCheckService
	loggingService      *logging.Service
	activeBackend       backend.Backend
	kmodLoader          *kmod.Loader
	updaterService      *updater.Service
	ndmsQueries         *ndmsquery.Queries
	trafficHistory      *traffic.History
	dnsRouteService     api.DNSRouteService
	staticRouteService  api.StaticRouteService
	systemTunnelService systemtunnel.Service
	managedService      managed.ManagedServerService
	nwgOp               *nwg.OperatorNativeWG
	terminalManager     terminal.Manager
	accessPolicyService accesspolicy.Service
	clientRouteService  clientroute.Service
	catalog             routing.Catalog
	hydraService        *hydraroute.Service
	orch                *orchestrator.Orchestrator
	bus                 *events.Bus
	singboxHandler      *api.SingboxHandler
	clashProxy          *api.ClashProxy
	singboxOp           *singbox.Operator
	deviceProxySvc      *deviceproxy.Service
	dnsCheckService     *dnscheck.Service
	authMiddleware      *auth.Middleware
	httpServer          *http.Server
	loopbackListener    net.Listener // optional loopback listener for reverse proxy

	ndmsDispatcher api.HookDispatcher
	ndmsTransport  *ndmstransport.Client
	ndmsSaveCoord  *ndmscommand.SaveCoordinator
	metricsPoller  *ndmsmetrics.Poller

	instanceID string // unique per process, changes on restart

	bootStatusFn func() bool // returns true if boot still in progress

	// Restart lifecycle
	restartOnce   sync.Once // prevents multiple restart goroutines
	shutdownHooks []func()  // cleanup functions called before syscall.Exec
}

// New creates a new server instance.
func New(cfg Config, log *logger.Logger, tunnelService api.TunnelService, externalService api.ExternalTunnelService, testingService *testing.Service, keenetic *auth.KeeneticClient, sessions *auth.SessionStore, settings *storage.SettingsStore, tunnels *storage.AWGTunnelStore, pingCheckService api.PingCheckService, loggingService *logging.Service, activeBackend backend.Backend, kmodLoader *kmod.Loader, updaterService *updater.Service, ndmsQueries *ndmsquery.Queries, trafficHistory *traffic.History, dnsRouteService api.DNSRouteService, staticRouteService api.StaticRouteService, systemTunnelService systemtunnel.Service, managedService managed.ManagedServerService, nwgOp *nwg.OperatorNativeWG, terminalManager terminal.Manager, accessPolicySvc accesspolicy.Service, clientRouteSvc clientroute.Service, catalog routing.Catalog, orch *orchestrator.Orchestrator, bus *events.Bus, hydraService *hydraroute.Service, singboxHandler *api.SingboxHandler, clashProxy *api.ClashProxy) *Server {
	id := generateInstanceID()
	log.Infof("Server instance: %s", id)

	return &Server{
		config:              cfg,
		log:                 log,
		tunnelService:       tunnelService,
		externalService:     externalService,
		testingService:      testingService,
		keenetic:            keenetic,
		sessions:            sessions,
		settings:            settings,
		tunnels:             tunnels,
		pingCheckService:    pingCheckService,
		loggingService:      loggingService,
		activeBackend:       activeBackend,
		kmodLoader:          kmodLoader,
		updaterService:      updaterService,
		ndmsQueries:         ndmsQueries,
		trafficHistory:      trafficHistory,
		dnsRouteService:     dnsRouteService,
		staticRouteService:  staticRouteService,
		systemTunnelService: systemTunnelService,
		managedService:      managedService,
		nwgOp:               nwgOp,
		terminalManager:     terminalManager,
		accessPolicyService: accessPolicySvc,
		clientRouteService:  clientRouteSvc,
		catalog:             catalog,
		hydraService:        hydraService,
		orch:                orch,
		bus:                 bus,
		singboxHandler:      singboxHandler,
		clashProxy:          clashProxy,
		authMiddleware:      auth.NewMiddleware(sessions, settings, log),
		instanceID:          id,
	}
}

// SetNDMSDispatcher wires the NDMS events.Dispatcher into the hook
// handler so POST /api/hook/ndms invalidates Store caches. Main.go
// calls this after constructing the new layer.
func (s *Server) SetNDMSDispatcher(d api.HookDispatcher) {
	s.ndmsDispatcher = d
}

// SetNDMSTransport wires the new NDMS transport for consumers that need
// ad-hoc raw RCI reads (connections viewer). Main.go calls this after
// constructing the new layer.
func (s *Server) SetNDMSTransport(t *ndmstransport.Client) {
	s.ndmsTransport = t
}

// SetNDMSSaveCoordinator wires the NDMS SaveCoordinator so GET
// /api/ndms/save-status can expose the debounced-save state machine
// snapshot. Main.go calls this after constructing the coordinator.
func (s *Server) SetNDMSSaveCoordinator(sc *ndmscommand.SaveCoordinator) {
	s.ndmsSaveCoord = sc
}

// SetMetricsPoller wires the unified NDMS metrics poller. Once registerRoutes
// builds the ServersHandler, the poller's server-snapshot publisher is
// connected to it and the ticker is started. Main.go calls this before
// srv.Start().
func (s *Server) SetMetricsPoller(p *ndmsmetrics.Poller) {
	s.metricsPoller = p
}

// SetDnsCheckService sets the DNS check service (wired after port selection).
func (s *Server) SetDnsCheckService(svc *dnscheck.Service) {
	s.dnsCheckService = svc
}

// SetSingboxOperator sets the sing-box operator so system info can report install status.
func (s *Server) SetSingboxOperator(op *singbox.Operator) {
	s.singboxOp = op
}

// SetDeviceProxyService wires the device-proxy service into the server
// so the /api/proxy/* routes can be registered.
func (s *Server) SetDeviceProxyService(svc *deviceproxy.Service) {
	s.deviceProxySvc = svc
}

// generateInstanceID creates a random 16-byte hex string (32 chars).
func generateInstanceID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// FindFreePort finds an available port.
// Priority: 1) preferred port from settings, 2) default port (2222), 3) fallback range (8080-8090).
func (s *Server) FindFreePort(preferredPort int) (int, error) {
	// Try preferred port from settings
	if preferredPort > 0 && preferredPort <= 65535 && isPortFree(preferredPort) {
		return preferredPort, nil
	}

	// Try default port (2222)
	if isPortFree(DefaultPort) {
		return DefaultPort, nil
	}

	// Fallback to range 8080-8090
	for port := FallbackPortStart; port <= FallbackPortEnd; port++ {
		if isPortFree(port) {
			return port, nil
		}
	}

	return 0, fmt.Errorf("no free port: %d occupied, fallback range %d-%d also occupied", DefaultPort, FallbackPortStart, FallbackPortEnd)
}

func isPortFree(port int) bool {
	addr := fmt.Sprintf(":%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

// SetListenAddr sets the listen address after port selection.
func (s *Server) SetListenAddr(addr string) {
	s.config.ListenAddr = addr
}

// SetLoopbackAddr sets the loopback listen address for reverse proxy support.
func (s *Server) SetBootStatusFunc(fn func() bool) {
	s.bootStatusFn = fn
}

func (s *Server) SetLoopbackAddr(addr string) {
	s.config.LoopbackListenAddr = addr
}

// Start starts the HTTP server.
func (s *Server) Start() error {
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	handler := s.loggingMiddleware(mux)

	s.httpServer = &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    8192,
		// No ReadTimeout/WriteTimeout — SSE requires long-lived connections.
		// Individual handlers use context timeouts where needed.
	}

	listener, err := net.Listen("tcp", s.config.ListenAddr)
	if err != nil {
		return err
	}

	// Start loopback listener for reverse proxy support (e.g. Keenetic nginx)
	if s.config.LoopbackListenAddr != "" {
		ln, err := net.Listen("tcp", s.config.LoopbackListenAddr)
		if err != nil {
			s.log.Warn("Failed to start loopback listener", map[string]interface{}{
				"addr":  s.config.LoopbackListenAddr,
				"error": err.Error(),
			})
		} else {
			s.loopbackListener = ln
			loopbackSrv := &http.Server{
				Handler:           handler,
				ReadHeaderTimeout: 5 * time.Second,
				IdleTimeout:       120 * time.Second,
				MaxHeaderBytes:    8192,
			}
			go loopbackSrv.Serve(ln)
			s.log.Info("Loopback listener started", map[string]interface{}{
				"addr": s.config.LoopbackListenAddr,
			})
		}
	}

	return s.httpServer.Serve(listener)
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.loopbackListener != nil {
		s.loopbackListener.Close()
	}
	if s.httpServer == nil {
		return nil
	}
	return s.httpServer.Shutdown(ctx)
}

// AddShutdownHook registers a function to call before syscall.Exec restart.
func (s *Server) AddShutdownHook(fn func()) {
	s.shutdownHooks = append(s.shutdownHooks, fn)
}

// ScheduleRestart schedules a self-restart of the daemon after a short delay.
// The delay allows the current HTTP response to be flushed to the client.
// Uses syscall.Exec to replace the process image in-place (same PID).
// sync.Once prevents multiple restart goroutines from racing.
func (s *Server) ScheduleRestart() {
	s.restartOnce.Do(func() {
		go func() {
			// Wait for HTTP response to flush
			time.Sleep(500 * time.Millisecond)

			// Run shutdown hooks (stop PingCheck, sessions, log buffer, etc.)
			for _, fn := range s.shutdownHooks {
				fn()
			}

			executable, err := os.Executable()
			if err != nil {
				s.log.Error("Failed to get executable path for restart", map[string]interface{}{"error": err.Error()})
				return
			}
			s.log.Info("Restarting daemon", map[string]interface{}{"executable": executable})

			if err := syscall.Exec(executable, os.Args, os.Environ()); err != nil {
				s.log.Error("Failed to exec for restart", map[string]interface{}{"error": err.Error()})
			}
		}()
	})
}

func (s *Server) registerRoutes(mux *http.ServeMux) {
	// Create handlers (pass loggingService as AppLogger to constructors)
	appLog := s.loggingService
	authHandler := api.NewAuthHandler(s.keenetic, s.sessions, s.settings, appLog)
	tunnelsHandler := api.NewTunnelsHandler(s.tunnelService, s.tunnels, appLog)
	tunnelsHandler.SetSettingsStore(s.settings)
	tunnelsHandler.SetPingCheckService(s.pingCheckService)
	tunnelsHandler.SetTrafficHistory(s.trafficHistory)
	tunnelsHandler.SetOrchestrator(s.orch)
	controlHandler := api.NewControlHandler(s.tunnelService, appLog)
	controlHandler.SetPingCheckService(s.pingCheckService)
	controlHandler.SetOrchestrator(s.orch)
	controlHandler.SetTunnelsHandler(tunnelsHandler)
	controlHandler.SetEventBus(s.bus)
	controlHandler.SetCatalog(s.catalog)
	testingHandler := api.NewTestingHandler(s.testingService)
	systemHandler := api.NewSystemHandler(s.config.Version)
	systemHandler.SetSettingsStore(s.settings)
	systemHandler.SetActiveBackend(s.activeBackend)
	systemHandler.SetKmodLoader(s.kmodLoader)
	systemHandler.SetSettingsWriter(s.settings)
	systemHandler.SetTunnelService(s.tunnelService)
	systemHandler.SetPingCheckService(s.pingCheckService)
	systemHandler.SetNDMSQueries(s.ndmsQueries)
	systemHandler.SetRestartFunc(s.ScheduleRestart)
	if s.bootStatusFn != nil {
		systemHandler.SetBootStatusFunc(s.bootStatusFn)
	}
	systemHandler.SetHydraRoute(s.hydraService)
	systemHandler.SetSingboxOperator(s.singboxOp)
	systemHandler.SetEventBus(s.bus)
	settingsHandler := api.NewSettingsHandler(s.settings, appLog)
	settingsHandler.SetTunnelStore(s.tunnels)
	settingsHandler.SetPingCheckService(s.pingCheckService)
	importHandler := api.NewImportHandler(s.tunnelService, s.tunnels, appLog)
	importHandler.SetSettingsStore(s.settings)
	importHandler.SetPingCheckService(s.pingCheckService)
	importHandler.SetTunnelsHandler(tunnelsHandler)
	statusHandler := api.NewStatusHandler(s.tunnelService)
	wanHandler := api.NewWANHandler(s.tunnelService, s.log, appLog)
	pingCheckHandler := api.NewPingCheckHandler(s.pingCheckService, s.tunnels, s.nwgOp, appLog)
	pingCheckHandler.SetEventBus(s.bus)
	pingCheckHandler.SetOrchestrator(s.orch)
	tunnelsHandler.SetPingCheckSnapshot(pingCheckHandler.PublishSnapshot)
	settingsHandler.SetPingCheckSnapshot(pingCheckHandler.PublishSnapshot)
	loggingHandler := api.NewLoggingHandler(s.loggingService, appLog)
	loggingHandler.SetEventBus(s.bus)
	settingsHandler.SetLogsSnapshot(loggingHandler.PublishSnapshot)
	externalHandler := api.NewExternalTunnelsHandler(s.externalService, s.tunnelService, s.tunnels, appLog)
	externalHandler.SetTunnelListPublisher(tunnelsHandler.PublishTunnelList)
	updateHandler := api.NewUpdateHandler(s.updaterService, appLog)
	dnsRouteHandler := api.NewDNSRouteHandler(s.dnsRouteService, appLog)
	diagRunner := diagnostics.NewRunner(diagnostics.Deps{
		TunnelService:   s.tunnelService,
		RCI:             rci.New(),
		NDMSQueries:     s.ndmsQueries,
		NDMSTransport:   s.ndmsTransport,
		Backend:         s.activeBackend,
		KmodLoader:      s.kmodLoader,
		TunnelStore:     s.tunnels,
		LogService:      &diagLogAdapter{svc: s.loggingService},
		AppVersion:      s.config.Version,
		PingCheckFacade: s.pingCheckService,
	})
	diagHandler := api.NewDiagnosticsHandler(diagRunner)

	// Connections viewer
	connectionsService := connections.NewService(s.catalog, s.ndmsTransport, s.dnsRouteService)
	connectionsHandler := api.NewConnectionsHandler(connectionsService)

	signatureHandler := api.NewSignatureHandler()
	terminalHandler := api.NewTerminalHandler(s.terminalManager, s.loggingService)

	eventsHandler := api.NewEventsHandler(s.bus)

	// Auth middleware helper
	guarded := s.authMiddleware.RequireAuthFunc

	// Auth endpoints (public)
	mux.HandleFunc("/api/auth/login", authHandler.Login)
	mux.HandleFunc("/api/auth/logout", authHandler.Logout)
	mux.HandleFunc("/api/auth/status", authHandler.Status)

	// Health liveness endpoint (public - used by frontend 5s poller to
	// detect backend offline independently of SSE connection state).
	mux.Handle("/api/health", api.NewHealthHandler(s.config.Version))

	// SSE event stream (protected)
	mux.HandleFunc("/api/events", guarded(eventsHandler.Stream))

	// NDM hooks (public - called from shell scripts). Also carries the
	// former /api/wan/event traffic via iflayerchanged layer=ipv4.
	hookHandler := api.NewHookHandler(s.tunnelService, s.orch, appLog)
	if s.ndmsDispatcher != nil {
		hookHandler.SetDispatcher(s.ndmsDispatcher)
	}
	if s.tunnelService != nil {
		hookHandler.SetWANModel(s.tunnelService.WANModel())
		// Wire the self-create gate so importNativeWG can suppress the
		// ifcreated-driven snapshot republish while its store.Save is
		// still pending.
		s.tunnelService.SetSelfCreateGate(hookHandler)
	}
	mux.HandleFunc("/api/hook/ndms", hookHandler.HandleNDMS)

	// WAN status (protected) — event ingress is now /api/hook/ndms.
	mux.HandleFunc("/api/wan/status", guarded(wanHandler.GetStatus))

	// Tunnels CRUD (protected + boot guarded)
	mux.HandleFunc("/api/tunnels/list", guarded(tunnelsHandler.List))
	mux.HandleFunc("/api/tunnels/all", guarded(tunnelsHandler.GetAll))
	mux.HandleFunc("/api/tunnels/get", guarded(tunnelsHandler.Get))
	mux.HandleFunc("/api/tunnels/create", guarded(tunnelsHandler.Create))
	mux.HandleFunc("/api/tunnels/update", guarded(tunnelsHandler.Update))
	mux.HandleFunc("/api/tunnels/delete", guarded(tunnelsHandler.Delete))
	mux.HandleFunc("/api/tunnels/export", guarded(tunnelsHandler.Export))
	mux.HandleFunc("/api/tunnels/export-all", guarded(tunnelsHandler.ExportAll))
	mux.HandleFunc("/api/tunnels/replace", guarded(tunnelsHandler.ReplaceConf))
	mux.HandleFunc("/api/tunnels/traffic", guarded(tunnelsHandler.Traffic))

	// Control operations (protected + boot guarded)
	mux.HandleFunc("/api/control/start", guarded(controlHandler.Start))
	mux.HandleFunc("/api/control/stop", guarded(controlHandler.Stop))
	mux.HandleFunc("/api/control/restart", guarded(controlHandler.Restart))
	mux.HandleFunc("/api/control/restart-all", guarded(controlHandler.RestartAll))
	mux.HandleFunc("/api/control/toggle-enabled", guarded(controlHandler.ToggleEnabled))
	mux.HandleFunc("/api/control/toggle-default-route", guarded(controlHandler.ToggleDefaultRoute))

	// Status queries (protected + boot guarded)
	mux.HandleFunc("/api/status/get", guarded(statusHandler.Get))
	mux.HandleFunc("/api/status/all", guarded(statusHandler.All))

	// Testing (protected + boot guarded)
	mux.HandleFunc("/api/test/ip", guarded(testingHandler.CheckIP))
	mux.HandleFunc("/api/test/ip/services", guarded(testingHandler.IPCheckServices))
	mux.HandleFunc("/api/test/connectivity", guarded(testingHandler.CheckConnectivity))
	mux.HandleFunc("/api/test/speed/servers", guarded(testingHandler.SpeedTestServers))
	mux.HandleFunc("/api/test/speed/stream", guarded(testingHandler.SpeedTestStream))
	mux.HandleFunc("/api/test/speed", guarded(testingHandler.SpeedTest))

	// System (protected + boot guarded)
	mux.HandleFunc("/api/system/info", guarded(systemHandler.Info))
	mux.HandleFunc("/api/system/restart", guarded(systemHandler.RestartDaemon))
	mux.HandleFunc("/api/system/wan-interfaces", guarded(systemHandler.WANInterfaces))
	mux.HandleFunc("/api/system/all-interfaces", guarded(systemHandler.AllInterfaces))
	mux.HandleFunc("/api/system/hydraroute-status", guarded(systemHandler.HydraRouteStatus))
	mux.HandleFunc("/api/system/hydraroute-control", guarded(systemHandler.HydraRouteControl))

	// HydraRoute settings (protected + boot guarded)
	if s.hydraService != nil {
		hrHandler := api.NewHydraRouteHandler(s.hydraService)
		hrHandler.SetEventBus(s.bus)
		mux.HandleFunc("/api/hydraroute/config", guarded(hrHandler.GetConfig))
		mux.HandleFunc("/api/hydraroute/config/update", guarded(hrHandler.UpdateConfig))
		mux.HandleFunc("/api/hydraroute/geo-files", guarded(hrHandler.ListGeoFiles))
		mux.HandleFunc("/api/hydraroute/geo-files/add", guarded(hrHandler.AddGeoFile))
		mux.HandleFunc("/api/hydraroute/geo-files/delete", guarded(hrHandler.DeleteGeoFile))
		mux.HandleFunc("/api/hydraroute/geo-files/update", guarded(hrHandler.UpdateGeoFile))
		mux.HandleFunc("/api/hydraroute/geo-tags", guarded(hrHandler.GetGeoTags))
		mux.HandleFunc("/api/hydraroute/ipset-usage", guarded(hrHandler.GetIpsetUsage))
		mux.HandleFunc("/api/hydraroute/oversized-tags", guarded(hrHandler.GetOversizedTags))
		mux.HandleFunc("/api/hydraroute/policy-order", guarded(hrHandler.SetPolicyOrder))
	}

	// Update endpoints (protected + boot guarded)
	mux.HandleFunc("/api/system/update/check", guarded(updateHandler.Check))
	mux.HandleFunc("/api/system/update/apply", guarded(updateHandler.Apply))
	mux.HandleFunc("/api/system/update/changelog", guarded(updateHandler.Changelog))

	// DNS routes (NDMS backend on OS5, HydraRoute on any OS)
	mux.HandleFunc("/api/dns-routes/list", guarded(dnsRouteHandler.List))
	mux.HandleFunc("/api/dns-routes/get", guarded(dnsRouteHandler.Get))
	mux.HandleFunc("/api/dns-routes/create", guarded(dnsRouteHandler.Create))
	mux.HandleFunc("/api/dns-routes/update", guarded(dnsRouteHandler.Update))
	mux.HandleFunc("/api/dns-routes/delete", guarded(dnsRouteHandler.Delete))
	mux.HandleFunc("/api/dns-routes/delete-batch", guarded(dnsRouteHandler.DeleteBatch))
	mux.HandleFunc("/api/dns-routes/create-batch", guarded(dnsRouteHandler.CreateBatch))
	mux.HandleFunc("/api/dns-routes/set-enabled", guarded(dnsRouteHandler.SetEnabled))
	mux.HandleFunc("/api/dns-routes/refresh", guarded(dnsRouteHandler.Refresh))
	mux.HandleFunc("/api/dns-routes/bulk-backend", guarded(dnsRouteHandler.BulkBackend))

	// Static IP routes (protected + boot guarded)
	staticRouteHandler := api.NewStaticRouteHandler(s.staticRouteService, appLog)
	mux.HandleFunc("/api/static-routes/list", guarded(staticRouteHandler.List))
	mux.HandleFunc("/api/static-routes/create", guarded(staticRouteHandler.Create))
	mux.HandleFunc("/api/static-routes/update", guarded(staticRouteHandler.Update))
	mux.HandleFunc("/api/static-routes/delete", guarded(staticRouteHandler.Delete))
	mux.HandleFunc("/api/static-routes/set-enabled", guarded(staticRouteHandler.SetEnabled))
	mux.HandleFunc("/api/static-routes/import", guarded(staticRouteHandler.Import))

	// Routing: unified tunnel listing for all routing subsystems
	routingHandler := api.NewRoutingHandler(s.catalog, s.ndmsQueries)
	routingHandler.SetEventBus(s.bus)
	mux.HandleFunc("/api/routing/tunnels", guarded(routingHandler.Tunnels))
	mux.HandleFunc("/api/routing/refresh", guarded(routingHandler.Refresh))

	// Routing: per-section GET endpoints (Task 11 — polling stores).
	// Reuse the dedicated service handlers so there is a single source of
	// truth per section. These URLs mirror the frontend store paths
	// (frontend/src/lib/stores/routing.ts).
	mux.HandleFunc("/api/routing/dns-routes", guarded(dnsRouteHandler.List))
	mux.HandleFunc("/api/routing/static-routes", guarded(staticRouteHandler.List))
	// Access policies + client routes + policy interfaces are registered
	// further below once their handlers exist (see "Routing polling GET
	// aliases" below).

	// DNS resolve for routing search
	resolveHandler := api.NewResolveHandler()
	mux.HandleFunc("/api/routing/resolve", guarded(resolveHandler.Resolve))

	// Settings (protected + boot guarded)
	mux.HandleFunc("/api/settings/get", guarded(settingsHandler.Get))
	mux.HandleFunc("/api/settings/update", guarded(settingsHandler.Update))

	// Ping check (protected + boot guarded)
	mux.HandleFunc("/api/pingcheck/status", guarded(pingCheckHandler.GetStatus))
	mux.HandleFunc("/api/pingcheck/logs", guarded(pingCheckHandler.GetLogs))
	mux.HandleFunc("/api/pingcheck/check-now", guarded(pingCheckHandler.CheckNow))
	mux.HandleFunc("/api/pingcheck/logs/clear", guarded(pingCheckHandler.ClearLogs))

	// Per-tunnel NDMS ping-check (nativewg)
	mux.HandleFunc("/api/tunnels/pingcheck", guarded(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			pingCheckHandler.GetTunnelPingCheckStatus(w, r)
		case http.MethodPost:
			pingCheckHandler.ConfigureTunnelPingCheck(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	mux.HandleFunc("/api/tunnels/pingcheck/remove", guarded(pingCheckHandler.RemoveTunnelPingCheck))

	// Device proxy (protected + boot guarded)
	deviceProxyHandler := api.NewDeviceProxyHandler(s.deviceProxySvc, appLog)
	mux.HandleFunc("/api/proxy/config", guarded(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			deviceProxyHandler.GetConfig(w, r)
		case http.MethodPut:
			deviceProxyHandler.SaveConfig(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	mux.HandleFunc("/api/proxy/runtime", guarded(deviceProxyHandler.GetRuntime))
	mux.HandleFunc("/api/proxy/runtime/select", guarded(deviceProxyHandler.SelectRuntime))
	mux.HandleFunc("/api/proxy/apply", guarded(deviceProxyHandler.ForceApply))
	mux.HandleFunc("/api/proxy/outbounds", guarded(deviceProxyHandler.ListOutbounds))
	mux.HandleFunc("/api/proxy/listen-choices", guarded(deviceProxyHandler.ListenChoices))

	// Logging (protected + boot guarded)
	mux.HandleFunc("/api/logs", guarded(loggingHandler.GetLogs))
	mux.HandleFunc("/api/logs/clear", guarded(loggingHandler.ClearLogs))

	// Import (protected + boot guarded)
	mux.HandleFunc("/api/import/conf", guarded(importHandler.ImportConf))

	// External tunnels (protected + boot guarded)
	mux.HandleFunc("/api/external-tunnels", guarded(externalHandler.List))
	mux.HandleFunc("/api/external-tunnels/adopt", guarded(externalHandler.Adopt))

	// System WireGuard tunnels (protected + boot guarded)
	systemTunnelHandler := api.NewSystemTunnelsHandler(s.systemTunnelService, s.settings, s.tunnels, s.loggingService)
	mux.HandleFunc("/api/system-tunnels", guarded(systemTunnelHandler.List))
	mux.HandleFunc("/api/system-tunnels/get", guarded(systemTunnelHandler.Get))
	mux.HandleFunc("/api/system-tunnels/asc", guarded(systemTunnelHandler.ASC))
	mux.HandleFunc("/api/system-tunnels/hide", guarded(systemTunnelHandler.Hide))
	mux.HandleFunc("/api/system-tunnels/hidden", guarded(systemTunnelHandler.Hidden))
	mux.HandleFunc("/api/system-tunnels/test-connectivity", guarded(systemTunnelHandler.CheckConnectivity))
	mux.HandleFunc("/api/system-tunnels/test-ip", guarded(systemTunnelHandler.CheckIP))
	mux.HandleFunc("/api/system-tunnels/test-speed", guarded(systemTunnelHandler.SpeedTestStream))

	// System tunnel traffic is now gathered by ndmsMetricsPoller via the
	// runningInterfacesAdapter (wired in main.go).

	// VPN Servers (protected + boot guarded)
	serverHandler := api.NewServersHandler(s.ndmsQueries, s.settings, s.tunnels)
	mux.HandleFunc("/api/servers", guarded(serverHandler.List))
	mux.HandleFunc("/api/servers/all", guarded(serverHandler.GetAll))
	mux.HandleFunc("/api/servers/get", guarded(serverHandler.Get))
	mux.HandleFunc("/api/servers/config", guarded(serverHandler.Config))
	mux.HandleFunc("/api/servers/mark", guarded(serverHandler.Mark))
	mux.HandleFunc("/api/servers/marked", guarded(serverHandler.Marked))
	mux.HandleFunc("/api/servers/wan-ip", guarded(serverHandler.WANIP))

	// Managed WireGuard Server (protected + boot guarded)
	managedHandler := api.NewManagedServerHandler(s.managedService)
	mux.HandleFunc("/api/managed-server", guarded(managedHandler.Get))
	mux.HandleFunc("/api/managed-server/stats", guarded(managedHandler.Stats))
	mux.HandleFunc("/api/managed-server/create", guarded(managedHandler.Create))
	mux.HandleFunc("/api/managed-server/suggest-address", guarded(managedHandler.SuggestAddress))
	mux.HandleFunc("/api/managed-server/update", guarded(managedHandler.Update))
	mux.HandleFunc("/api/managed-server/delete", guarded(managedHandler.Delete))
	mux.HandleFunc("/api/managed-server/peers", guarded(managedHandler.AddPeer))
	mux.HandleFunc("/api/managed-server/peers/update", guarded(managedHandler.UpdatePeer))
	mux.HandleFunc("/api/managed-server/peers/delete", guarded(managedHandler.DeletePeer))
	mux.HandleFunc("/api/managed-server/peers/toggle", guarded(managedHandler.TogglePeer))
	mux.HandleFunc("/api/managed-server/peers/conf", guarded(managedHandler.PeerConf))
	mux.HandleFunc("/api/managed-server/enabled", guarded(managedHandler.SetEnabled))
	mux.HandleFunc("/api/managed-server/nat", guarded(managedHandler.NAT))
	mux.HandleFunc("/api/managed-server/asc", guarded(managedHandler.ASC))

	// Signature capture (protected + boot guarded)
	mux.HandleFunc("/api/signature/capture", guarded(signatureHandler.Capture))

	// Terminal
	mux.HandleFunc("/api/terminal/status", guarded(terminalHandler.Status))
	mux.HandleFunc("/api/terminal/install", guarded(terminalHandler.Install))
	mux.HandleFunc("/api/terminal/start", guarded(terminalHandler.Start))
	mux.HandleFunc("/api/terminal/stop", guarded(terminalHandler.Stop))
	mux.HandleFunc("/api/terminal/ws", guarded(terminalHandler.WebSocket))

	// Access policies — handler created outside block for shared endpoints
	accessPolicyHandler := api.NewAccessPolicyHandler(s.accessPolicyService)

	// Devices endpoint uses hotspot RCI — works on both OS4 and OS5
	mux.HandleFunc("/api/access-policies/devices", guarded(accessPolicyHandler.ListDevices))

	// Access policies (protected + boot guarded) — OS5 only
	if osdetect.Is5() {
		mux.HandleFunc("/api/access-policies", guarded(accessPolicyHandler.List))
		mux.HandleFunc("/api/access-policies/create", guarded(accessPolicyHandler.Create))
		mux.HandleFunc("/api/access-policies/delete", guarded(accessPolicyHandler.Delete))
		mux.HandleFunc("/api/access-policies/description", guarded(accessPolicyHandler.SetDescription))
		mux.HandleFunc("/api/access-policies/standalone", guarded(accessPolicyHandler.SetStandalone))
		mux.HandleFunc("/api/access-policies/permit", guarded(accessPolicyHandler.PermitInterface))
		mux.HandleFunc("/api/access-policies/assign", guarded(accessPolicyHandler.AssignDevice))
		mux.HandleFunc("/api/access-policies/interfaces", guarded(accessPolicyHandler.ListGlobalInterfaces))
		mux.HandleFunc("/api/access-policies/interface-up", guarded(accessPolicyHandler.SetInterfaceUp))
	}

	// Client routes (per-device VPN routing) — works on both OS4 and OS5
	crHandler := api.NewClientRouteHandler(s.clientRouteService)
	mux.HandleFunc("/api/client-routes", guarded(crHandler.HandleList))
	mux.HandleFunc("/api/client-routes/create", guarded(crHandler.HandleCreate))
	mux.HandleFunc("/api/client-routes/update", guarded(crHandler.HandleUpdate))
	mux.HandleFunc("/api/client-routes/delete", guarded(crHandler.HandleDelete))
	mux.HandleFunc("/api/client-routes/toggle", guarded(crHandler.HandleToggle))

	// Routing polling GET aliases for sections whose handlers live later
	// in this function (accesspolicy + clientroute). See Task 11.
	mux.HandleFunc("/api/routing/client-routes", guarded(crHandler.HandleList))
	// Policy devices endpoint is unconditional (hotspot RCI works on both OSes).
	mux.HandleFunc("/api/routing/policy-devices", guarded(accessPolicyHandler.ListDevices))
	if osdetect.Is5() {
		mux.HandleFunc("/api/routing/access-policies", guarded(accessPolicyHandler.List))
		mux.HandleFunc("/api/routing/policy-interfaces", guarded(accessPolicyHandler.ListGlobalInterfaces))
	} else {
		// OS4: access policies + global-interfaces are NOT available.
		// Return empty arrays so the polling stores stay in the 'fresh'
		// state instead of erroring and showing a badge.
		emptyArr := func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[]}`))
		}
		mux.HandleFunc("/api/routing/access-policies", guarded(emptyArr))
		mux.HandleFunc("/api/routing/policy-interfaces", guarded(emptyArr))
	}

	// Diagnostics (protected + boot guarded)
	mux.HandleFunc("/api/diagnostics/run", guarded(diagHandler.Run))
	mux.HandleFunc("/api/diagnostics/status", guarded(diagHandler.Status))
	mux.HandleFunc("/api/diagnostics/result", guarded(diagHandler.Result))
	mux.HandleFunc("/api/diagnostics/stream", guarded(diagHandler.Stream))

	// Connections viewer (protected)
	mux.HandleFunc("/api/connections", guarded(connectionsHandler.List))

	// NDMS save-coordinator status (protected) — polled by the header
	// save indicator. SaveCoordinator emits a resource:invalidated hint
	// on every state transition so clients refetch this endpoint.
	ndmsHandler := api.NewNDMSHandler(s.ndmsSaveCoord)
	mux.HandleFunc("/api/ndms/save-status", guarded(ndmsHandler.GetSaveStatus))

	// Boot status (public - frontend uses instanceId for restart detection)
	mux.HandleFunc("/api/boot-status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"initializing":     false,
			"remainingSeconds": 0,
			"phase":            "ready",
			"instanceId":       s.instanceID,
		})
	})

	// Wire event bus to CRUD handlers for SSE publishing
	tunnelsHandler.SetEventBus(s.bus)
	tunnelsHandler.SetCatalog(s.catalog)
	dnsRouteHandler.SetEventBus(s.bus)
	staticRouteHandler.SetEventBus(s.bus)
	accessPolicyHandler.SetEventBus(s.bus)
	crHandler.SetEventBus(s.bus)
	serverHandler.SetEventBus(s.bus)

	// Cross-wire servers <-> managed for unified server:updated event
	serverHandler.SetManagedHandler(managedHandler)
	managedHandler.SetServersHandler(serverHandler)

	// Plug MetricsPoller into the handler now that ServersHandler is fully
	// wired (bus + managed). The poller re-broadcasts the full server snapshot
	// via serverHandler whenever any server's peer metrics change.
	if s.metricsPoller != nil {
		s.metricsPoller.SetServerSnapshotPublisher(serverHandler)
		s.metricsPoller.Start()
	}

	// Composite tunnels snapshot builder — used by GET /api/tunnels/all
	// and by the hook-driven resource:invalidated refresher to assemble
	// the {tunnels, external, system} payload the polling store reads.
	tsb := api.NewTunnelsSnapshotBuilder()
	tsb.SetTunnelsHandler(tunnelsHandler)
	tsb.SetExternalHandler(externalHandler)
	tsb.SetSystemTunnelsHandler(systemTunnelHandler)

	// Wire hook-driven tunnel invalidation so the UI drops destroyed
	// tunnel cards (including system tunnels) without a browser refresh.
	// The closure invalidates the in-memory NDMS caches so the next
	// poll reads fresh data, then publishes resource:invalidated; the
	// frontend tunnels store responds by refetching /api/tunnels/all.
	invalidateTunnelsOnHook := func(ctx context.Context) {
		_ = ctx
		// NDMS cache invalidation stays — hook events signal that the
		// system view has changed, so our in-memory caches must drop
		// their entries before the next poll.
		if s.ndmsQueries != nil {
			if s.ndmsQueries.WGServers != nil {
				s.ndmsQueries.WGServers.InvalidateAll()
			}
			if s.ndmsQueries.Interfaces != nil {
				s.ndmsQueries.Interfaces.InvalidateAll()
			}
		}
		if s.bus != nil {
			s.bus.Publish("resource:invalidated", events.ResourceInvalidatedEvent{
				Resource: api.ResourceTunnels,
				Reason:   "ndms-hook",
			})
		}
	}
	hookHandler.SetTunnelRefresher(invalidateTunnelsOnHook)
	// Injects the composite {tunnels, external, system} builder used by
	// GetAll so /api/tunnels/all returns the exact shape the polling
	// store expects.
	tunnelsHandler.SetTunnelsSnapshotBuilder(func(ctx context.Context) map[string]interface{} {
		return tsb.Build(ctx)
	})
	tunnelsHandler.SetSelfCreateGate(hookHandler)

	// DNS routing diagnostics
	if s.dnsCheckService != nil {
		dnsCheckHandler := api.NewDnsCheckHandler(s.dnsCheckService)
		mux.HandleFunc("/api/dns-check/start", guarded(dnsCheckHandler.Start))
		mux.HandleFunc("/api/dns-check/probe", dnsCheckHandler.Probe) // NO auth — cross-origin
	}

	// Sing-box integration (protected + boot guarded)
	if s.singboxHandler != nil {
		mux.HandleFunc("/api/singbox/status", guarded(s.singboxHandler.Status))
		mux.HandleFunc("/api/singbox/install", guarded(s.singboxHandler.Install))
		mux.HandleFunc("/api/singbox/tunnels/delay-check", guarded(s.singboxHandler.DelayCheck))
		mux.HandleFunc("/api/singbox/tunnels/test/speed/stream", guarded(s.singboxHandler.SpeedTestStream))
		mux.HandleFunc("/api/singbox/tunnels", guarded(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				if r.URL.Query().Has("tag") {
					s.singboxHandler.GetTunnel(w, r)
				} else {
					s.singboxHandler.ListTunnels(w, r)
				}
			case http.MethodPost:
				s.singboxHandler.AddTunnels(w, r)
			case http.MethodPut:
				s.singboxHandler.UpdateTunnel(w, r)
			case http.MethodDelete:
				s.singboxHandler.DeleteTunnel(w, r)
			default:
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}
		}))
	}
	if s.clashProxy != nil {
		mux.HandleFunc("/api/singbox/clash/", guarded(s.clashProxy.ServeHTTP))
		mux.HandleFunc("/api/singbox/clash", guarded(s.clashProxy.ServeHTTP))
	}

	// Static files (SPA) - must be last
	if s.config.WebRoot != "" {
		mux.Handle("/", s.spaHandler())
	}
}

// spaHandler serves static files with SPA fallback to index.html
func (s *Server) spaHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Clean and join path
		path := filepath.Join(s.config.WebRoot, filepath.Clean(r.URL.Path))

		// Check if file exists
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			// File doesn't exist or is directory - serve index.html (SPA fallback)
			path = filepath.Join(s.config.WebRoot, "index.html")
		}

		// Set content type based on extension
		ext := filepath.Ext(path)
		switch ext {
		case ".html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		case ".css":
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		case ".js":
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		case ".json":
			w.Header().Set("Content-Type", "application/json")
		case ".svg":
			w.Header().Set("Content-Type", "image/svg+xml")
		case ".png":
			w.Header().Set("Content-Type", "image/png")
		case ".ico":
			w.Header().Set("Content-Type", "image/x-icon")
		case ".woff":
			w.Header().Set("Content-Type", "font/woff")
		case ".woff2":
			w.Header().Set("Content-Type", "font/woff2")
		}

		// Cache control: immutable files (content-hashed by vite) cache forever,
		// everything else must revalidate to pick up new builds after upgrade.
		if strings.Contains(r.URL.Path, "/immutable/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		}

		http.ServeFile(w, r, path)
	})
}

// diagLogAdapter adapts logging.Service to diagnostics.LogServiceForDiag.
// Diagnostics expects GetLogs(category, level) but new service uses GetLogs(group, subgroup, level).
type diagLogAdapter struct {
	svc *logging.Service
}

func (a *diagLogAdapter) GetLogs(category, level string) []logging.LogEntry {
	// For diagnostics, category maps to group (empty = all); return all entries (no pagination)
	logs, _ := a.svc.GetLogs(category, "", level, 10000, 0)
	return logs
}

func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Panic recovery
		defer func() {
			if err := recover(); err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`{"error":true,"message":"internal server error","code":"PANIC"}`))
			}
		}()

		next.ServeHTTP(w, r)
	})
}
