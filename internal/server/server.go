package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hoaxisr/awg-manager/internal/accesspolicy"
	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/auth"
	"github.com/hoaxisr/awg-manager/internal/clientroute"
	"github.com/hoaxisr/awg-manager/internal/deviceproxy"
	"github.com/hoaxisr/awg-manager/internal/diagnostics"
	"github.com/hoaxisr/awg-manager/internal/dnscheck"
	"github.com/hoaxisr/awg-manager/internal/downloader"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/hydraroute"
	"github.com/hoaxisr/awg-manager/internal/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/presets"
	"github.com/hoaxisr/awg-manager/internal/routing"
	"github.com/hoaxisr/awg-manager/internal/singbox"
	singboxorch "github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/managed"
	"github.com/hoaxisr/awg-manager/internal/monitoring"
	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	ndmsmetrics "github.com/hoaxisr/awg-manager/internal/ndms/metrics"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	ndmstransport "github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/kmod"
	"github.com/hoaxisr/awg-manager/internal/terminal"
	"github.com/hoaxisr/awg-manager/internal/testing"
	"github.com/hoaxisr/awg-manager/internal/traffic"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
	"github.com/hoaxisr/awg-manager/internal/tunnel/systemtunnel"
	"github.com/hoaxisr/awg-manager/internal/updater"
)

const (
	DefaultPort       = 2222
	FallbackPortStart = 8080
	FallbackPortEnd   = 8090
)

// Config holds server configuration.
type Config struct {
	FrontendFS fs.FS
	Version    string

	// PprofStandaloneAddr, if non-empty, starts an additional listener that
	// serves only Go's /debug/pprof/* endpoints (recommended: 127.0.0.1:6060).
	PprofStandaloneAddr string
	// PprofOnMain mounts the same endpoints on the primary HTTP mux (reachable on
	// every listen addr — LAN and loopback). Use sparingly when the API is exposed.
	PprofOnMain bool
	// SlowRequestThreshold, if positive, logs requests whose handler runs longer than
	// this duration to stderr (via slog); long-lived SSE/WebSocket routes are skipped.
	SlowRequestThreshold time.Duration
}

// Server is the HTTP server for awg-manager.
type Server struct {
	config                     Config
	appLog                     *logging.ScopedLogger
	tunnelService              api.TunnelService
	externalService            api.ExternalTunnelService
	testingService             *testing.Service
	keenetic                   *auth.KeeneticClient
	sessions                   *auth.SessionStore
	settings                   *storage.SettingsStore
	tunnels                    *storage.AWGTunnelStore
	pingCheckService           api.PingCheckService
	proxyRecords               api.ProxyRecordLister
	loggingService             *logging.Service
	kmodLoader                 *kmod.Loader
	opkgTunOccupancy           storage.OpkgTunPins
	updaterService             *updater.Service
	ndmsQueries                *ndmsquery.Queries
	ndmsCommands               *ndmscommand.Commands
	trafficHistory             *traffic.History
	dnsRouteService            api.DNSRouteService
	staticRouteService         api.StaticRouteService
	systemTunnelService        systemtunnel.Service
	managedService             managed.ManagedServerService
	managedServiceImpl         *managed.Service
	nwgOp                      *nwg.OperatorNativeWG
	terminalManager            terminal.Manager
	accessPolicyService        accesspolicy.Service
	clientRouteService         clientroute.Service
	catalog                    routing.Catalog
	hydraService               *hydraroute.Service
	orch                       *orchestrator.Orchestrator
	bus                        *events.Bus
	exposureGuard              *api.ExposureGuard
	exposureGuardStop          context.CancelFunc
	singboxHandler             *api.SingboxHandler
	singboxConnsHandler        *api.SingboxConnectionsHandler
	singboxRouterHandler       *api.SingboxRouterHandler
	connectionsMarkProvider    func(context.Context) (string, bool)
	singboxFakeIPConfigHandler *api.SingboxFakeIPConfigHandler
	singboxConfigHandler       *api.SingboxConfigHandler
	singboxConfigEditorHandler *api.SingboxConfigEditorHandler
	singboxInboundsHandler     *api.SingboxInboundsHandler
	singboxProxiesHandler      *api.SingboxProxiesHandler
	bypassSetHandler           *api.BypassSetHandler
	awgOutboundsHandler        *api.AWGOutboundsHandler
	subscriptionHandler        *api.SubscriptionHandler
	dnsRewritesHandler         *api.DNSRewritesHandler
	awg3Handler                *api.Awg3Handler
	clashProxy                 *api.ClashProxy
	singboxOp                  *singbox.Operator
	singboxOrch                *singboxorch.Orchestrator
	presetCatalog              *presets.Catalog
	deviceProxySvc             *deviceproxy.Service
	downloadSvc                *downloader.Service
	monitoringService          *monitoring.Service
	singboxSubMembersFn        func() []diagnostics.SingboxSubMember
	singboxConfigPreviewFn     func() (string, error)
	dnsCheckService            *dnscheck.Service
	authMiddleware             *auth.Middleware
	mcpKeys                    *storage.McpKeyStore
	// mcpCalls is cancelled by Shutdown so in-flight MCP tool calls stop
	// instead of holding httpServer.Shutdown for their full grace period.
	mcpCalls       context.Context
	mcpCallsCancel context.CancelFunc
	httpServer     *http.Server

	// listen владеет всеми HTTP-листенерами (по адресам из ListenSpec +
	// безусловный loopback) и confirm-окном живой смены адреса. См. listen.go.
	listen         listenState
	listenDone     chan struct{} // закрывается в Shutdown — отпускает Start
	listenDoneOnce sync.Once

	ndmsDispatcher api.HookDispatcher
	ndmsTransport  *ndmstransport.Client
	ndmsSaveCoord  *ndmscommand.SaveCoordinator
	metricsPoller  *ndmsmetrics.Poller
	pprofServer    *http.Server // optional standalone pprof-only listener

	instanceID string // unique per process, changes on restart

	bootStatusFn func() bool // returns true if boot still in progress

	proxyRuntimeNudge api.ProxyRuntimeNudge

	// proxyRuntime — менеджер прокси-рантайма за узким срезом: тумблер
	// намерения инстанса (карточка зеркальной записи wdtt-raw) и глушение
	// детей на время бэкапа. Проводка ставит его ПОСЛЕ server.New — менеджер
	// строится позже.
	proxyRuntime ProxyRuntime

	// proxyRt — ручки прокси-рантайма; собирает проводка (cmd/awg-manager).
	// Нулевое значение означает «рантайм не проведён»: маршруты не
	// регистрируются вовсе, а не отвечают пустотой.
	proxyRt ProxyRtSurface

	// Restart lifecycle
	restartOnce   sync.Once // prevents multiple restart goroutines
	shutdownHooks []func()  // cleanup functions called before syscall.Exec
}

// Deps groups all New() construction-time dependencies into a named
// struct so call sites and signature edits are not positional. Adding
// a new dependency: append a field here AND set it in main.go.
//
// Optional handlers and operators that must be constructed AFTER
// server.New (because they consume *Server or each other) stay wired
// via the existing post-construction Set*Handler() / SetSingboxOperator()
// setters — see SetSingboxRouterHandler etc. below in this file.
type Deps struct {
	TunnelService    api.TunnelService
	ExternalService  api.ExternalTunnelService
	TestingService   *testing.Service
	Keenetic         *auth.KeeneticClient
	Sessions         *auth.SessionStore
	Settings         *storage.SettingsStore
	Tunnels          *storage.AWGTunnelStore
	PingCheckService api.PingCheckService
	ProxyRecords     api.ProxyRecordLister
	LoggingService   *logging.Service
	KmodLoader       *kmod.Loader
	// OpkgTunOccupancy — занятость номеров OpkgTun: живые интерфейсы плюс пины
	// чужих подсистем. Нужна выдаче идентификатора kernel-туннеля.
	OpkgTunOccupancy     storage.OpkgTunPins
	UpdaterService       *updater.Service
	NdmsQueries          *ndmsquery.Queries
	NdmsCommands         *ndmscommand.Commands
	TrafficHistory       *traffic.History
	DnsRouteService      api.DNSRouteService
	StaticRouteService   api.StaticRouteService
	SystemTunnelService  systemtunnel.Service
	ManagedService       managed.ManagedServerService
	ManagedServiceImpl   *managed.Service
	NwgOp                *nwg.OperatorNativeWG
	TerminalManager      terminal.Manager
	AccessPolicySvc      accesspolicy.Service
	ClientRouteSvc       clientroute.Service
	Catalog              routing.Catalog
	Orch                 *orchestrator.Orchestrator
	Bus                  *events.Bus
	HydraService         *hydraroute.Service
	SingboxHandler       *api.SingboxHandler
	SingboxOrch          *singboxorch.Orchestrator
	ClashProxy           *api.ClashProxy
	SingboxConnsHandler  *api.SingboxConnectionsHandler
	MonitoringService    *monitoring.Service
	SingboxSubMembers    func() []diagnostics.SingboxSubMember
	SingboxConfigPreview func() (string, error)

	// McpKeys holds the MCP API keys. Nil disables the /mcp endpoint and
	// its key-management routes entirely (they are never registered).
	McpKeys *storage.McpKeyStore
}

// authLoggerAdapter narrows ScopedLogger to the AuthLogger interface
// (Warnf) required by auth.NewMiddleware.
type authLoggerAdapter struct {
	log *logging.ScopedLogger
}

func (a *authLoggerAdapter) Warnf(format string, args ...interface{}) {
	if a.log == nil {
		return
	}
	a.log.Warn("auth", "", fmt.Sprintf(format, args...))
}

// New creates a new server instance.
func New(cfg Config, deps Deps) *Server {
	id := generateInstanceID()
	appLog := logging.NewScopedLogger(deps.LoggingService, logging.GroupServer, logging.SubHTTP)
	appLog.Info("startup", "", "Server instance: "+id)

	return &Server{
		config:                 cfg,
		appLog:                 appLog,
		tunnelService:          deps.TunnelService,
		externalService:        deps.ExternalService,
		testingService:         deps.TestingService,
		keenetic:               deps.Keenetic,
		sessions:               deps.Sessions,
		settings:               deps.Settings,
		tunnels:                deps.Tunnels,
		pingCheckService:       deps.PingCheckService,
		proxyRecords:           deps.ProxyRecords,
		loggingService:         deps.LoggingService,
		kmodLoader:             deps.KmodLoader,
		opkgTunOccupancy:       deps.OpkgTunOccupancy,
		updaterService:         deps.UpdaterService,
		ndmsQueries:            deps.NdmsQueries,
		ndmsCommands:           deps.NdmsCommands,
		trafficHistory:         deps.TrafficHistory,
		dnsRouteService:        deps.DnsRouteService,
		staticRouteService:     deps.StaticRouteService,
		systemTunnelService:    deps.SystemTunnelService,
		managedService:         deps.ManagedService,
		managedServiceImpl:     deps.ManagedServiceImpl,
		nwgOp:                  deps.NwgOp,
		terminalManager:        deps.TerminalManager,
		accessPolicyService:    deps.AccessPolicySvc,
		clientRouteService:     deps.ClientRouteSvc,
		catalog:                deps.Catalog,
		hydraService:           deps.HydraService,
		orch:                   deps.Orch,
		bus:                    deps.Bus,
		singboxHandler:         deps.SingboxHandler,
		singboxOrch:            deps.SingboxOrch,
		singboxConnsHandler:    deps.SingboxConnsHandler,
		clashProxy:             deps.ClashProxy,
		monitoringService:      deps.MonitoringService,
		singboxSubMembersFn:    deps.SingboxSubMembers,
		singboxConfigPreviewFn: deps.SingboxConfigPreview,
		authMiddleware:         auth.NewMiddleware(deps.Sessions, deps.Settings, &authLoggerAdapter{log: appLog}),
		mcpKeys:                deps.McpKeys,
		instanceID:             id,
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

// SetPresetCatalog wires the unified preset catalog into the server.
func (s *Server) SetPresetCatalog(c *presets.Catalog) {
	s.presetCatalog = c
}

// SetDeviceProxyService wires the device-proxy service into the server
// so the /api/proxy/* routes can be registered.
func (s *Server) SetDeviceProxyService(svc *deviceproxy.Service) {
	s.deviceProxySvc = svc
}

// SetDownloadService wires the shared downloader service.
func (s *Server) SetDownloadService(svc *downloader.Service) {
	s.downloadSvc = svc
}

// SetSingboxRouterHandler wires the sing-box router HTTP handler so the
// /api/singbox/router/* routes can be registered.
func (s *Server) SetSingboxRouterHandler(h *api.SingboxRouterHandler) {
	s.singboxRouterHandler = h
}

// SetConnectionsMarkProvider wires the sb-router policy-mark source into the
// connections service (атрибуция tproxy-потоков без ifw). Вызывается из
// wiring до Start(): buildRouteHandlers читает поле при регистрации роутов.
func (s *Server) SetConnectionsMarkProvider(fn func(ctx context.Context) (string, bool)) {
	s.connectionsMarkProvider = fn
}

// SetSingboxFakeIPConfigHandler wires the fakeip-tun config CRUD handler so
// the /api/singbox/fakeip/config/* routes can be registered.
func (s *Server) SetSingboxFakeIPConfigHandler(h *api.SingboxFakeIPConfigHandler) {
	s.singboxFakeIPConfigHandler = h
}

// SetAWGOutboundsHandler wires the AWG outbounds tag catalog handler
// so /api/singbox/awg-outbounds/tags can be registered.
func (s *Server) SetAWGOutboundsHandler(h *api.AWGOutboundsHandler) {
	s.awgOutboundsHandler = h
}

// SetSingboxConfigHandler injects the read-only config-preview handler.
// Wired post-construction because the orchestrator's ConfigDir is only
// available after main wires everything up.
func (s *Server) SetSingboxConfigHandler(h *api.SingboxConfigHandler) {
	s.singboxConfigHandler = h
}

// SetSingboxConfigEditorHandler injects the expert config-editor handler
// (slots browser + user-slot draft pipeline). Wired post-construction like
// the config-preview handler — it needs the fully-registered orchestrator.
func (s *Server) SetSingboxConfigEditorHandler(h *api.SingboxConfigEditorHandler) {
	s.singboxConfigEditorHandler = h
}

// SetSingboxInboundsHandler injects the read-only inbounds-mirror handler
// (/api/singbox/inbounds). Wired post-construction like the config-preview
// handler — its resolvers (stores, settings toggle) are built in main.
func (s *Server) SetSingboxInboundsHandler(h *api.SingboxInboundsHandler) {
	s.singboxInboundsHandler = h
}

// SetSingboxProxiesHandler injects the runtime-controls handler that
// wraps the sing-box clash API. Wired post-construction since both the
// router service (for composite-tag enumeration) and the clash proxy
// (for the upstream URL) are constructed late.
func (s *Server) SetSingboxProxiesHandler(h *api.SingboxProxiesHandler) {
	s.singboxProxiesHandler = h
}

// ProxyRtSurface — поверхность прокси-рантайма под /api/proxyrt/*.
//
// Функциями, а не хендлерами: собирают её пакеты internal/proxyapp/* и
// internal/api, и тянуть их сюда значило бы завести шестую зависимость ради
// одной строки регистрации у каждой. Instances обслуживает ВЕСЬ подпуть
// /api/proxyrt/instances[/...] — хвост разбирает диспетчер проводки, потому
// что подпути инстанса (users, link, captcha, allowlist) в http.ServeMux без
// wildcard-паттернов не выразимы.
type ProxyRtSurface struct {
	Instances http.HandlerFunc
	// ListenMoves — снятие уведомлений о переезде listen-порта при посеве.
	ListenMoves        http.HandlerFunc
	WdttLinkDecode     http.HandlerFunc
	WdttLinkImport     http.HandlerFunc
	FreeTurnLinkDecode http.HandlerFunc
	CaptchaStatus      http.HandlerFunc
	InstallStatus      http.HandlerFunc
	Install            http.HandlerFunc
	Uninstall          http.HandlerFunc
}

// SetProxyRtSurface wires the proxy-runtime handlers so /api/proxyrt/*
// routes can be registered.
func (s *Server) SetProxyRtSurface(h ProxyRtSurface) {
	s.proxyRt = h
}

// SetBypassSetHandler wires the geoip bypass-set handler so
// /api/singbox/router/bypass-set/* routes can be registered.
func (s *Server) SetBypassSetHandler(h *api.BypassSetHandler) {
	s.bypassSetHandler = h
}

// SetSubscriptionHandler wires the VPN subscription CRUD handler so the
// /api/singbox/subscriptions/* routes can be registered.
func (s *Server) SetSubscriptionHandler(h *api.SubscriptionHandler) {
	s.subscriptionHandler = h
}

// SetDNSRewritesHandler wires the DNS rewrites CRUD handler so the
// /api/singbox/router/dns/rewrites/* routes can be registered.
func (s *Server) SetDNSRewritesHandler(h *api.DNSRewritesHandler) {
	s.dnsRewritesHandler = h
}

// SetAwg3Handler wires the AWG3 endpoint import/CRUD handler so the
// /api/awg3-endpoints[/{id}] routes can be registered.
func (s *Server) SetAwg3Handler(h *api.Awg3Handler) {
	s.awg3Handler = h
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
	if preferredPort > 0 && preferredPort <= 65535 && IsPortFree(preferredPort) {
		return preferredPort, nil
	}

	// Try default port (2222)
	if IsPortFree(DefaultPort) {
		return DefaultPort, nil
	}

	// Fallback to range 8080-8090
	for port := FallbackPortStart; port <= FallbackPortEnd; port++ {
		if IsPortFree(port) {
			return port, nil
		}
	}

	return 0, fmt.Errorf("no free port: %d occupied, fallback range %d-%d also occupied", DefaultPort, FallbackPortStart, FallbackPortEnd)
}

// IsPortFree reports whether a TCP port is available for binding.
func IsPortFree(port int) bool {
	addr := fmt.Sprintf(":%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

func (s *Server) SetBootStatusFunc(fn func() bool) {
	s.bootStatusFn = fn
}

// SetProxyRuntimeNudge wires the WAN-up callback of the proxy runtime
// (seed retry plus worker wake-up).
func (s *Server) SetProxyRuntimeNudge(fn api.ProxyRuntimeNudge) {
	s.proxyRuntimeNudge = fn
}

// ProxyRuntime — то, что серверу нужно от менеджера прокси-рантайма:
// тумблер намерения одного инстанса и пара «погасить/поднять» для бэкапа.
// *manager.Manager удовлетворяет как есть.
type ProxyRuntime interface {
	SetEnabled(ctx context.Context, key string, on bool) error
	Boot(ctx context.Context) error
	Shutdown()
}

// SetProxyRuntime wires the proxy-runtime manager. Called from the wiring
// after the manager is built (server.New runs earlier).
func (s *Server) SetProxyRuntime(rt ProxyRuntime) {
	s.proxyRuntime = rt
}

// Start starts the HTTP server.
func (s *Server) Start() error {
	if addr := strings.TrimSpace(s.config.PprofStandaloneAddr); addr != "" {
		pm := http.NewServeMux()
		registerPprofRoutes(pm)
		s.pprofServer = &http.Server{
			Addr:              addr,
			Handler:           pm,
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       120 * time.Second,
		}
		go func() {
			if err := s.pprofServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				s.appLog.Warn("pprof", addr, "listener stopped: "+err.Error())
			}
		}()
		fmt.Fprintf(os.Stderr, "awg-manager: pprof (standalone): http://%s/debug/pprof/\n", addr)
	}

	mux := http.NewServeMux()
	if s.config.PprofOnMain {
		registerPprofRoutes(mux)
	}
	s.registerRoutes(mux)

	// Фоновая проверка «не открыты ли мы наружу без пароля» — гвард
	// конструируется в registerRoutes, поэтому запускается сразу после.
	if s.exposureGuard != nil {
		guardCtx, cancel := context.WithCancel(context.Background())
		s.exposureGuardStop = cancel
		s.exposureGuard.Start(guardCtx)
	}

	core := http.Handler(mux)
	if s.config.SlowRequestThreshold > 0 {
		core = s.slowRequestMiddleware(s.config.SlowRequestThreshold, core)
	}
	handler := s.loggingMiddleware(core)

	s.httpServer = &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    8192,
		// No ReadTimeout/WriteTimeout — SSE requires long-lived connections.
		// Individual handlers use context timeouts where needed.
	}

	// Мультилистенеры (listen.go): по одному на IPv4 каждого выбранного
	// интерфейса + безусловный loopback. Boot — best-effort: интерфейс без
	// IP пропускается (heal-тикер добиндит, когда IP появится); фатально
	// только «не открылся ни один листенер».
	s.listenDone = make(chan struct{})
	s.listen.mu.Lock()
	addrs, _ := s.resolveListenAddrs(s.listen.spec, false)
	n, _ := s.applyListenLocked(addrs, true)
	s.listen.mu.Unlock()
	if n == 0 {
		return fmt.Errorf("не удалось открыть ни одного HTTP-листенера (порт %d)", s.listen.spec.Port)
	}
	s.startListenHeal()

	// Блокируемся до Shutdown — контракт прежнего Serve-вызова для main.go.
	<-s.listenDone
	return http.ErrServerClosed
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.listen.mu.Lock()
	if s.listen.healStop != nil {
		close(s.listen.healStop)
		s.listen.healStop = nil
	}
	if s.listen.pendingTimer != nil {
		s.listen.pendingTimer.Stop()
	}
	s.clearPendingLocked()
	s.listen.mu.Unlock()

	if s.exposureGuardStop != nil {
		s.exposureGuardStop()
		s.exposureGuardStop = nil
	}

	if s.mcpCallsCancel != nil {
		s.mcpCallsCancel()
	}

	if s.pprofServer != nil {
		shutdownCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_ = s.pprofServer.Shutdown(shutdownCtx)
		cancel()
		s.pprofServer = nil
	}
	if s.httpServer == nil {
		return nil
	}
	// httpServer.Shutdown закрывает все листенеры (Serve-горутины выходят)
	// и дожидается соединений; после него отпускаем заблокированный Start.
	err := s.httpServer.Shutdown(ctx)
	if s.listenDone != nil {
		s.listenDoneOnce.Do(func() { close(s.listenDone) })
	}
	return err
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
			s.appLog.Info("restart", "", "restart requested")

			// Wait for HTTP response to flush
			time.Sleep(500 * time.Millisecond)

			executable, err := os.Executable()
			if err != nil {
				s.appLog.Error("restart", "", "failed to get executable path: "+err.Error())
				return
			}
			s.appLog.Info("restart", executable, "restarting daemon")

			// Run shutdown hooks (stop PingCheck, sessions, log buffer, etc.)
			for _, fn := range s.shutdownHooks {
				fn()
			}

			if err := syscall.Exec(executable, os.Args, os.Environ()); err != nil {
				s.appLog.Error("restart", "", "exec failed: "+err.Error())
			}
		}()
	})
}

func (s *Server) registerRoutes(mux *http.ServeMux) {
	h := s.buildRouteHandlers()
	s.registerCoreRoutes(mux, h)
	s.registerTunnelRoutes(mux, h)
	s.registerSystemRoutes(mux, h)
	s.registerRoutingRoutes(mux, h)
	s.registerSettingsRoutes(mux, h)
	s.registerDeviceProxyRoutes(mux, h)
	s.registerLogsImportRoutes(mux, h)
	s.registerServerRoutes(mux, h)
	s.registerPolicyRoutes(mux, h)
	s.registerDiagnosticsRoutes(mux, h)
	s.wireCrossHandlers(mux, h)
	s.registerSingboxRoutes(mux, h)
	s.registerProxyRtRoutes(mux, h)
	s.registerMcpRoutes(mux, h)
	s.registerStaticRoutes(mux, h)
}

// spaHandler serves static files with SPA fallback to index.html.
func spaHandler(staticFS fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Незарегистрированная ручка под /api/ — это ошибка маршрутизации, а
		// не путь SPA. Отдав index.html с кодом 200, мы заставляли клиент
		// разбирать HTML как JSON, и он показывал «Некорректный ответ сервера
		// (200)» вместо внятного сообщения.
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":true,"message":"эндпоинт не найден","code":"NOT_FOUND"}`))
			return
		}

		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}

		// A path resolves if either the raw file or its precompressed .gz
		// twin exists. Build-time gzip (frontend postbuild) drops the raw
		// original for compressed assets, so the .gz is often the only copy.
		if !resolves(staticFS, name) {
			name = "index.html"
		}

		contentType := mime.TypeByExtension(path.Ext(name))
		switch {
		case strings.HasSuffix(name, ".html"):
			contentType = "text/html; charset=utf-8"
		case strings.HasSuffix(name, ".js"):
			contentType = "application/javascript; charset=utf-8"
		case strings.HasSuffix(name, ".json"):
			contentType = "application/json"
		case strings.HasSuffix(name, ".webmanifest"):
			contentType = "application/manifest+json"
		}
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}

		// Cache control: immutable files (content-hashed by vite) cache forever,
		// everything else must revalidate to pick up new builds after upgrade.
		if strings.Contains(r.URL.Path, "/immutable/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		}

		// Prefer the precompressed twin when present. Compression happens at
		// build time, so the hot path is a plain copy — no per-request gzip.
		// The rare non-gzip client (curl/wget without --compressed, probes)
		// gets the asset gunzipped on the fly.
		if gzData, err := fs.ReadFile(staticFS, name+".gz"); err == nil {
			w.Header().Set("Vary", "Accept-Encoding")
			if acceptsGzip(r) {
				w.Header().Set("Content-Encoding", "gzip")
				http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(gzData))
				return
			}
			if zr, err := gzip.NewReader(bytes.NewReader(gzData)); err == nil {
				raw, rerr := io.ReadAll(zr)
				_ = zr.Close()
				if rerr == nil {
					http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(raw))
					return
				}
			}
		}

		http.ServeFileFS(w, r, staticFS, name)
	})
}

// resolves reports whether name maps to a servable asset — either the raw
// file or its precompressed .gz twin.
func resolves(staticFS fs.FS, name string) bool {
	if info, err := fs.Stat(staticFS, name); err == nil && !info.IsDir() {
		return true
	}
	if _, err := fs.Stat(staticFS, name+".gz"); err == nil {
		return true
	}
	return false
}

// acceptsGzip reports whether the client advertised gzip support.
func acceptsGzip(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")
}

// diagLogAdapter adapts logging.Service to diagnostics.LogServiceForDiag.
// The structured journalWarnings report section uses GetBucketLogs/
// GetBucketStats to collect both app and sing-box buckets explicitly.
type diagLogAdapter struct {
	svc *logging.Service
}

func (a *diagLogAdapter) GetBucketLogs(bucket logging.Bucket, group, subgroup, level string, limit, offset int) ([]logging.LogEntry, int) {
	if a == nil || a.svc == nil {
		return []logging.LogEntry{}, 0
	}
	logs, total := a.svc.GetLogs(bucket, group, subgroup, level, time.Time{}, limit, offset)
	if logs == nil {
		logs = []logging.LogEntry{}
	}
	return logs, total
}

func (a *diagLogAdapter) GetBucketStats(bucket logging.Bucket) logging.BufferStats {
	if a == nil || a.svc == nil {
		return logging.BufferStats{Bucket: bucket}
	}
	return a.svc.Stats(bucket)
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
