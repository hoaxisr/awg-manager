package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/connections"
	"github.com/hoaxisr/awg-manager/internal/diagnostics"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/openapi"
	"github.com/hoaxisr/awg-manager/internal/response"

	"github.com/hoaxisr/awg-manager/internal/logging"
)

// routeHandlers держит handlers, разделяемые секциями registerRoutes.
// Конструирование и перекрёстная проводка — в buildRouteHandlers; секционные
// register*-методы только вешают маршруты (и локально строят handlers,
// нужные единственной секции). Порядок вызова секций в registerRoutes
// сохраняет исходный порядок регистрации.
type routeHandlers struct {
	appLog               *logging.Service
	authHandler          *api.AuthHandler
	tunnelsHandler       *api.TunnelsHandler
	controlHandler       *api.ControlHandler
	testingHandler       *api.TestingHandler
	systemHandler        *api.SystemHandler
	settingsHandler      *api.SettingsHandler
	importHandler        *api.ImportHandler
	wanHandler           *api.WANHandler
	pingCheckHandler     *api.PingCheckHandler
	freeturnHandler      *api.FreeTurnHandler
	wdttHandler          *api.WdttHandler
	proxyListenerHandler *api.ProxyListenerHandler
	loggingHandler       *api.LoggingHandler
	externalHandler      *api.ExternalTunnelsHandler
	updateHandler        *api.UpdateHandler
	dnsRouteHandler      *api.DNSRouteHandler
	diagRunner           *diagnostics.Runner
	diagHandler          *api.DiagnosticsHandler
	connectionsService   *connections.Service
	connectionsHandler   *api.ConnectionsHandler
	signatureHandler     *api.SignatureHandler
	terminalHandler      *api.TerminalHandler
	eventsHandler        *api.EventsHandler
	hookHandler          *api.HookHandler
	staticRouteHandler   *api.StaticRouteHandler
	systemTunnelHandler  *api.SystemTunnelsHandler
	serverHandler        *api.ServersHandler
	managedHandler       *api.ManagedServerHandler
	accessPolicyHandler  *api.AccessPolicyHandler
	crHandler            *api.ClientRouteHandler
	systemToolsHandler   *api.SystemToolsHandler

	// guarded оборачивает handler в auth-middleware (RequireAuthFunc).
	guarded func(http.HandlerFunc) http.HandlerFunc
}

// expertGuarded — auth плюс проверка usage level: раздел «Система» даёт
// root-доступ к файлам, процессам и opkg, поэтому гейт стоит на регистрации
// маршрута, а не копией в начале каждого хендлера.
func (h *routeHandlers) expertGuarded(fn http.HandlerFunc) http.HandlerFunc {
	return h.guarded(h.systemToolsHandler.ExpertOnly(fn))
}

// buildRouteHandlers конструирует и перекрёстно связывает handlers,
// используемые секциями registerRoutes. Тело перенесено из начала
// registerRoutes дословно.
func (s *Server) buildRouteHandlers() *routeHandlers {
	h := &routeHandlers{}
	// Create handlers (pass loggingService as AppLogger to constructors)
	h.appLog = s.loggingService
	h.authHandler = api.NewAuthHandler(s.keenetic, s.sessions, s.settings, h.appLog)
	h.tunnelsHandler = api.NewTunnelsHandler(s.tunnelService, s.tunnels, h.appLog)
	h.tunnelsHandler.SetSettingsStore(s.settings)
	h.tunnelsHandler.SetPingCheckService(s.pingCheckService)
	h.tunnelsHandler.SetTrafficHistory(s.trafficHistory)
	h.tunnelsHandler.SetOrchestrator(s.orch)
	h.controlHandler = api.NewControlHandler(s.tunnelService, h.appLog)
	h.controlHandler.SetPingCheckService(s.pingCheckService)
	h.controlHandler.SetOrchestrator(s.orch)
	h.controlHandler.SetTunnelsHandler(h.tunnelsHandler)
	h.controlHandler.SetEventBus(s.bus)
	h.testingHandler = api.NewTestingHandler(s.testingService)
	h.systemHandler = api.NewSystemHandler(s.config.Version)
	h.systemHandler.SetSettingsStore(s.settings)
	h.systemHandler.SetKmodLoader(s.kmodLoader)
	h.systemHandler.SetSettingsWriter(s.settings)
	h.systemHandler.SetTunnelService(s.tunnelService)
	h.systemHandler.SetPingCheckService(s.pingCheckService)
	h.systemHandler.SetNDMSQueries(s.ndmsQueries)
	h.systemHandler.SetRestartFunc(s.ScheduleRestart)
	if s.bootStatusFn != nil {
		h.systemHandler.SetBootStatusFunc(s.bootStatusFn)
	}
	h.systemHandler.SetHydraRoute(s.hydraService)
	h.systemHandler.SetSingboxOperator(s.singboxOp)
	h.systemHandler.SetEventBus(s.bus)
	if ms := int(s.config.SlowRequestThreshold / time.Millisecond); ms > 0 {
		h.systemHandler.SetSlowRequestThresholdMs(ms)
	}
	h.settingsHandler = api.NewSettingsHandler(s.settings, h.appLog)
	h.settingsHandler.SetDownloadService(s.downloadSvc)
	h.settingsHandler.SetTunnelStore(s.tunnels)
	h.settingsHandler.SetPingCheckService(s.pingCheckService)
	h.settingsHandler.SetMonitoringService(s.monitoringService)
	h.settingsHandler.SetEventBus(s.bus)
	if s.ndmsQueries != nil {
		s.exposureGuard = api.NewExposureGuard(s.settings, s.ndmsQueries.StaticNAT, s.ndmsQueries.HTTPProxy, s.ndmsQueries.Interfaces, h.appLog)
		s.exposureGuard.SetEventBus(s.bus)
		h.settingsHandler.SetExposureGuard(s.exposureGuard)
	}
	h.importHandler = api.NewImportHandler(s.tunnelService, s.tunnels, h.appLog)
	h.importHandler.SetSettingsStore(s.settings)
	h.importHandler.SetPingCheckService(s.pingCheckService)
	h.importHandler.SetTunnelsHandler(h.tunnelsHandler)
	h.importHandler.SetFreeTurnService(s.freeturnService)
	h.importHandler.SetWdttService(s.wdttService)
	h.wanHandler = api.NewWANHandler(s.tunnelService, h.appLog)
	h.pingCheckHandler = api.NewPingCheckHandler(s.pingCheckService, s.tunnels, s.nwgOp, h.appLog)
	h.pingCheckHandler.SetEventBus(s.bus)
	h.pingCheckHandler.SetOrchestrator(s.orch)
	h.tunnelsHandler.SetPingCheckSnapshot(h.pingCheckHandler.PublishSnapshot)
	h.settingsHandler.SetPingCheckSnapshot(h.pingCheckHandler.PublishSnapshot)
	h.loggingHandler = api.NewLoggingHandler(s.loggingService, h.appLog)
	h.loggingHandler.SetEventBus(s.bus)
	h.settingsHandler.SetLogsSnapshot(h.loggingHandler.PublishSnapshot)
	// Wire eager re-apply of MaxAge / per-bucket MaxEntries after a
	// settings PUT — without this the live buffers keep stale caps until
	// the next AppLog tick (lazy apply path was removed).
	h.settingsHandler.SetApplyLoggingSettings(s.loggingService.ApplySettings)
	h.settingsHandler.SetApplyBootstrapDNS(func(server string) error {
		if s.singboxOp == nil {
			return nil
		}
		return s.singboxOp.ApplyBootstrapDNS(server)
	})
	h.settingsHandler.SetApplySingboxLogSettings(func() error {
		if s.singboxOp == nil || s.settings == nil {
			return nil
		}
		return s.singboxOp.ApplyLogLevel(s.settings.GetSingboxLogLevel())
	})
	h.externalHandler = api.NewExternalTunnelsHandler(s.externalService, s.tunnelService, s.tunnels, h.appLog)
	h.externalHandler.SetTunnelListPublisher(h.tunnelsHandler.PublishTunnelList)
	h.updateHandler = api.NewUpdateHandler(s.updaterService, h.appLog)
	h.dnsRouteHandler = api.NewDNSRouteHandler(s.dnsRouteService, h.appLog)
	h.diagRunner = diagnostics.NewRunner(diagnostics.Deps{
		TunnelService:        s.tunnelService,
		NDMSQueries:          s.ndmsQueries,
		NDMSTransport:        s.ndmsTransport,
		KmodLoader:           s.kmodLoader,
		TunnelStore:          s.tunnels,
		LogService:           &diagLogAdapter{svc: s.loggingService},
		AppVersion:           s.config.Version,
		PingCheckFacade:      s.pingCheckService,
		Singbox:              s.singboxOp,
		SingboxSubMembers:    s.singboxSubMembersFn,
		SingboxConfigPreview: s.singboxConfigPreviewFn,
		AppLogger:            s.loggingService,
	})
	h.diagHandler = api.NewDiagnosticsHandler(h.diagRunner)

	// Connections viewer
	h.connectionsService = connections.NewService(s.catalog, s.ndmsTransport, s.dnsRouteService, s.loggingService)
	if s.connectionsMarkProvider != nil {
		h.connectionsService.SetSingboxMarkProvider(s.connectionsMarkProvider)
	}
	h.connectionsHandler = api.NewConnectionsHandler(h.connectionsService)

	h.signatureHandler = api.NewSignatureHandler()
	h.terminalHandler = api.NewTerminalHandler(s.terminalManager, s.loggingService)
	h.systemToolsHandler = api.NewSystemToolsHandler(s.settings, h.appLog)
	h.systemToolsHandler.SetEventBus(s.bus)

	h.eventsHandler = api.NewEventsHandler(s.bus, s.instanceID)

	h.freeturnHandler = api.NewFreeTurnHandler(s.freeturnService)
	if s.ndmsQueries != nil {
		h.freeturnHandler.SetNDMSQueries(s.ndmsQueries)
	}
	h.freeturnHandler.SetLinkedTunnelCleanup(s.tunnels, s.tunnelService)
	h.freeturnHandler.SetTunnelsHandler(h.tunnelsHandler)

	h.wdttHandler = api.NewWdttHandler(s.wdttService)
	if s.ndmsQueries != nil {
		h.wdttHandler.SetNDMSQueries(s.ndmsQueries)
	}
	h.wdttHandler.SetLinkedTunnelCleanup(s.tunnels, s.tunnelService)
	h.wdttHandler.SetTunnelsHandler(h.tunnelsHandler)

	// listen-repair внутри freeturn/wdtt переназначает порт клиента — Endpoint
	// linked AWG-туннеля обязан пойти следом, и не только в хранилище: через
	// handler правка доходит до живого интерфейса (tunnelService.Update) и до
	// фронта (SSE).
	s.wireLinkedEndpointSync(s.freeturnService, h.freeturnHandler.SyncLinkedTunnelEndpoints, "freeturn")
	s.wireLinkedEndpointSync(s.wdttService, h.wdttHandler.SyncLinkedTunnelEndpoints, "wdtt")
	h.tunnelsHandler.SetWdttListSource(s.wdttService)
	h.controlHandler.SetWdttControl(s.tunnels, s.wdttService)

	h.proxyListenerHandler = api.NewProxyListenerHandler(s.freeturnService, s.wdttService)

	// Auth middleware helper
	h.guarded = s.authMiddleware.RequireAuthFunc

	return h
}

// registerCoreRoutes — auth, health, OpenAPI spec, SSE events, NDM hooks, WAN status.
func (s *Server) registerCoreRoutes(mux *http.ServeMux, h *routeHandlers) {
	// Auth endpoints (public)
	mux.HandleFunc("/api/auth/login", h.authHandler.Login)
	mux.HandleFunc("/api/auth/logout", h.authHandler.Logout)
	mux.HandleFunc("/api/auth/status", h.authHandler.Status)

	// Health liveness endpoint (public - used by frontend 5s poller to
	// detect backend offline independently of SSE connection state).
	mux.Handle("/api/health", api.NewHealthHandler(s.config.Version, s.instanceID))

	// OpenAPI spec (protected). Embedded in the binary at build time so
	// the spec served here always matches the running awg-manager —
	// independent of any frontend static-asset sync. Both /api/openapi.yaml
	// and /openapi.yaml are registered for tooling that expects either path.
	openAPIHandler := h.guarded(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(openapi.RawSpec)
	})
	mux.HandleFunc("/api/openapi.yaml", openAPIHandler)
	mux.HandleFunc("/openapi.yaml", openAPIHandler)

	// SSE event stream (protected)
	mux.HandleFunc("/api/events", h.guarded(h.eventsHandler.Stream))

	// NDM hooks (public - called from shell scripts). Also carries the
	// former /api/wan/event traffic via iflayerchanged layer=ipv4.
	h.hookHandler = api.NewHookHandler(s.tunnelService, s.orch, h.appLog)
	if s.ndmsDispatcher != nil {
		h.hookHandler.SetDispatcher(s.ndmsDispatcher)
	}
	if s.tunnelService != nil {
		h.hookHandler.SetWANModel(s.tunnelService.WANModel())
		// Wire the self-create gate so importNativeWG can suppress the
		// ifcreated-driven snapshot republish while its store.Save is
		// still pending.
		s.tunnelService.SetSelfCreateGate(h.hookHandler)
	}
	if s.proxyClientAutostart != nil {
		h.hookHandler.SetProxyClientAutostart(s.proxyClientAutostart)
	}
	mux.HandleFunc("/api/hook/ndms", h.hookHandler.HandleNDMS)

	// WAN status (protected) — event ingress is now /api/hook/ndms.
	mux.HandleFunc("/api/wan/status", h.guarded(h.wanHandler.GetStatus))

}

// registerTunnelRoutes — tunnels CRUD, control operations, testing.
func (s *Server) registerTunnelRoutes(mux *http.ServeMux, h *routeHandlers) {
	// Tunnels CRUD (protected + boot guarded)
	mux.HandleFunc("/api/tunnels/list", h.guarded(h.tunnelsHandler.List))
	mux.HandleFunc("/api/tunnels/all", h.guarded(h.tunnelsHandler.GetAll))
	mux.HandleFunc("/api/tunnels/get", h.guarded(h.tunnelsHandler.Get))
	mux.HandleFunc("/api/tunnels/create", h.guarded(h.tunnelsHandler.Create))
	mux.HandleFunc("/api/tunnels/update", h.guarded(h.tunnelsHandler.Update))
	mux.HandleFunc("/api/tunnels/delete", h.guarded(h.tunnelsHandler.Delete))
	mux.HandleFunc("/api/tunnels/export", h.guarded(h.tunnelsHandler.Export))
	mux.HandleFunc("/api/tunnels/export-all", h.guarded(h.tunnelsHandler.ExportAll))
	mux.HandleFunc("/api/tunnels/replace", h.guarded(h.tunnelsHandler.ReplaceConf))
	mux.HandleFunc("/api/tunnels/traffic", h.guarded(h.tunnelsHandler.Traffic))

	// Control operations (protected + boot guarded)
	mux.HandleFunc("/api/control/start", h.guarded(h.controlHandler.Start))
	mux.HandleFunc("/api/control/stop", h.guarded(h.controlHandler.Stop))
	mux.HandleFunc("/api/control/restart", h.guarded(h.controlHandler.Restart))
	mux.HandleFunc("/api/control/restart-all", h.guarded(h.controlHandler.RestartAll))
	mux.HandleFunc("/api/control/toggle-enabled", h.guarded(h.controlHandler.ToggleEnabled))
	mux.HandleFunc("/api/control/toggle-default-route", h.guarded(h.controlHandler.ToggleDefaultRoute))

	// Testing (protected + boot guarded)
	mux.HandleFunc("/api/test/ip", h.guarded(h.testingHandler.CheckIP))
	mux.HandleFunc("/api/test/ip/services", h.guarded(h.testingHandler.IPCheckServices))
	mux.HandleFunc("/api/test/connectivity", h.guarded(h.testingHandler.CheckConnectivity))
	mux.HandleFunc("/api/test/speed/servers", h.guarded(h.testingHandler.SpeedTestServers))
	mux.HandleFunc("/api/test/speed/stream", h.guarded(h.testingHandler.SpeedTestStream))
	mux.HandleFunc("/api/test/speed", h.guarded(h.testingHandler.SpeedTest))

}

// registerSystemRoutes — system info, HTTP-listen management, HydraRoute, updates.
func (s *Server) registerSystemRoutes(mux *http.ServeMux, h *routeHandlers) {
	// System (protected + boot guarded)
	mux.HandleFunc("/api/system/info", h.guarded(h.systemHandler.Info))
	mux.HandleFunc("/api/system/restart", h.guarded(h.systemHandler.RestartDaemon))
	backupHandler := api.NewBackupHandler(
		s.settings.DataDir(),
		s.config.Version,
		s.QuiesceForBackup,
		s.ResumeAfterBackup,
		s.ScheduleRestart,
		h.appLog,
	)
	mux.HandleFunc("/api/system/backup/export", h.guarded(backupHandler.Export))
	mux.HandleFunc("/api/system/backup/import", h.guarded(backupHandler.Import))
	mux.HandleFunc("/api/system/wan-interfaces", h.guarded(h.systemHandler.WANInterfaces))
	mux.HandleFunc("/api/system/all-interfaces", h.guarded(h.systemHandler.AllInterfaces))
	mux.HandleFunc("/api/system/hydraroute-status", h.guarded(h.systemHandler.HydraRouteStatus))
	mux.HandleFunc("/api/system/hydraroute-control", h.guarded(h.systemHandler.HydraRouteControl))

	// HTTP-listen management (live rebind + confirm-or-revert, listen.go).
	// confirm нарочно БЕЗ session-гарда: его аутентифицирует одноразовый
	// 256-битный токен из /change — cookie сессии привязана к хосту и смену
	// интерфейса не переживает.
	serverListenHandler := api.NewServerListenHandler(s, s.settings, h.appLog)
	mux.HandleFunc("/api/server/listen", h.guarded(serverListenHandler.State))
	mux.HandleFunc("/api/server/listen/change", h.guarded(serverListenHandler.Change))
	mux.HandleFunc("/api/server/listen/confirm", serverListenHandler.Confirm)
	downloadHandler := api.NewDownloadHandler(s.downloadSvc)
	mux.HandleFunc("/api/download/outbounds", h.guarded(downloadHandler.ListOutbounds))

	// HydraRoute settings (protected + boot guarded)
	if s.hydraService != nil {
		hrHandler := api.NewHydraRouteHandler(s.hydraService, s.downloadSvc)
		hrHandler.SetEventBus(s.bus)
		mux.HandleFunc("/api/hydraroute/config", h.guarded(hrHandler.GetConfig))
		mux.HandleFunc("/api/hydraroute/config/update", h.guarded(hrHandler.UpdateConfig))
		mux.HandleFunc("/api/hydraroute/geo-files", h.guarded(hrHandler.ListGeoFiles))
		mux.HandleFunc("/api/hydraroute/geo-files/add", h.guarded(hrHandler.AddGeoFile))
		mux.HandleFunc("/api/hydraroute/geo-files/delete", h.guarded(hrHandler.DeleteGeoFile))
		mux.HandleFunc("/api/hydraroute/geo-files/update", h.guarded(hrHandler.UpdateGeoFile))
		mux.HandleFunc("/api/hydraroute/geo-files/take-control", h.guarded(hrHandler.TakeGeoFileControl))
		mux.HandleFunc("/api/hydraroute/geo-files/rescan", h.guarded(hrHandler.RescanGeoFiles))
		mux.HandleFunc("/api/hydraroute/geo-tags", h.guarded(hrHandler.GetGeoTags))
		mux.HandleFunc("/api/hydraroute/geo-expand", h.guarded(hrHandler.ExpandGeoTag))
		mux.HandleFunc("/api/hydraroute/ipset-usage", h.guarded(hrHandler.GetIpsetUsage))
		mux.HandleFunc("/api/hydraroute/oversized-tags", h.guarded(hrHandler.GetOversizedTags))
		mux.HandleFunc("/api/hydraroute/policy-order", h.guarded(hrHandler.SetPolicyOrder))
	}

	// Update endpoints (protected + boot guarded)
	mux.HandleFunc("/api/system/update/check", h.guarded(h.updateHandler.Check))
	mux.HandleFunc("/api/system/update/apply", h.guarded(h.updateHandler.Apply))
	mux.HandleFunc("/api/system/update/changelog", h.guarded(h.updateHandler.Changelog))

	// System tools (expert): file manager, init.d services, opkg
	mux.HandleFunc("/api/system/files/roots", h.expertGuarded(h.systemToolsHandler.FilesRoots))
	mux.HandleFunc("/api/system/files/list", h.expertGuarded(h.systemToolsHandler.FilesList))
	mux.HandleFunc("/api/system/files/read", h.expertGuarded(h.systemToolsHandler.FilesRead))
	mux.HandleFunc("/api/system/files/write", h.expertGuarded(h.systemToolsHandler.FilesWrite))
	mux.HandleFunc("/api/system/files/mkdir", h.expertGuarded(h.systemToolsHandler.FilesMkdir))
	mux.HandleFunc("/api/system/files/remove", h.expertGuarded(h.systemToolsHandler.FilesRemove))
	mux.HandleFunc("/api/system/files/rename", h.expertGuarded(h.systemToolsHandler.FilesRename))
	mux.HandleFunc("/api/system/files/copy", h.expertGuarded(h.systemToolsHandler.FilesCopy))
	mux.HandleFunc("/api/system/files/chmod", h.expertGuarded(h.systemToolsHandler.FilesChmod))
	mux.HandleFunc("/api/system/files/checksum", h.expertGuarded(h.systemToolsHandler.FilesChecksum))
	mux.HandleFunc("/api/system/files/download", h.expertGuarded(h.systemToolsHandler.FilesDownload))
	mux.HandleFunc("/api/system/files/upload", h.expertGuarded(h.systemToolsHandler.FilesUpload))
	mux.HandleFunc("/api/system/files/script-status", h.expertGuarded(h.systemToolsHandler.FilesScriptStatus))
	mux.HandleFunc("/api/system/files/script-action", h.expertGuarded(h.systemToolsHandler.FilesScriptAction))
	mux.HandleFunc("/api/system/services/list", h.expertGuarded(h.systemToolsHandler.ServicesList))
	mux.HandleFunc("/api/system/services/action", h.expertGuarded(h.systemToolsHandler.ServicesAction))
	mux.HandleFunc("/api/system/services/get", h.expertGuarded(h.systemToolsHandler.ServicesGetScript))
	mux.HandleFunc("/api/system/services/save", h.expertGuarded(h.systemToolsHandler.ServicesSaveScript))
	mux.HandleFunc("/api/system/services/delete", h.expertGuarded(h.systemToolsHandler.ServicesDeleteScript))
	mux.HandleFunc("/api/system/opkg/installed", h.expertGuarded(h.systemToolsHandler.OpkgInstalled))
	mux.HandleFunc("/api/system/opkg/upgradable", h.expertGuarded(h.systemToolsHandler.OpkgUpgradable))
	mux.HandleFunc("/api/system/opkg/search", h.expertGuarded(h.systemToolsHandler.OpkgSearch))
	mux.HandleFunc("/api/system/opkg/update", h.expertGuarded(h.systemToolsHandler.OpkgUpdate))
	mux.HandleFunc("/api/system/opkg/upgrade", h.expertGuarded(h.systemToolsHandler.OpkgUpgrade))
	mux.HandleFunc("/api/system/opkg/install", h.expertGuarded(h.systemToolsHandler.OpkgInstall))
	mux.HandleFunc("/api/system/opkg/remove", h.expertGuarded(h.systemToolsHandler.OpkgRemove))
	mux.HandleFunc("/api/system/opkg/available", h.expertGuarded(h.systemToolsHandler.OpkgAvailable))
	mux.HandleFunc("/api/system/ports/list", h.expertGuarded(h.systemToolsHandler.PortsList))
	mux.HandleFunc("/api/system/ports/inspect", h.expertGuarded(h.systemToolsHandler.PortsInspect))
	mux.HandleFunc("/api/system/ports/kill", h.expertGuarded(h.systemToolsHandler.PortsKill))
	mux.HandleFunc("/api/system/proc/snapshot", h.expertGuarded(h.systemToolsHandler.ProcSnapshot))
	mux.HandleFunc("/api/system/proc/kill", h.expertGuarded(h.systemToolsHandler.ProcKill))

}

// registerRoutingRoutes — DNS routes, static routes, routing catalog/sections, resolve.
func (s *Server) registerRoutingRoutes(mux *http.ServeMux, h *routeHandlers) {
	// DNS routes (NDMS backend on OS5, HydraRoute on any OS)
	mux.HandleFunc("/api/dns-routes/list", h.guarded(h.dnsRouteHandler.List))
	mux.HandleFunc("/api/dns-routes/get", h.guarded(h.dnsRouteHandler.Get))
	mux.HandleFunc("/api/dns-routes/create", h.guarded(h.dnsRouteHandler.Create))
	mux.HandleFunc("/api/dns-routes/update", h.guarded(h.dnsRouteHandler.Update))
	mux.HandleFunc("/api/dns-routes/delete", h.guarded(h.dnsRouteHandler.Delete))
	mux.HandleFunc("/api/dns-routes/delete-batch", h.guarded(h.dnsRouteHandler.DeleteBatch))
	mux.HandleFunc("/api/dns-routes/create-batch", h.guarded(h.dnsRouteHandler.CreateBatch))
	mux.HandleFunc("/api/dns-routes/set-enabled", h.guarded(h.dnsRouteHandler.SetEnabled))
	mux.HandleFunc("/api/dns-routes/refresh", h.guarded(h.dnsRouteHandler.Refresh))
	mux.HandleFunc("/api/dns-routes/bulk-backend", h.guarded(h.dnsRouteHandler.BulkBackend))

	// Static IP routes (protected + boot guarded)
	h.staticRouteHandler = api.NewStaticRouteHandler(s.staticRouteService, h.appLog)
	mux.HandleFunc("/api/static-routes/list", h.guarded(h.staticRouteHandler.List))
	mux.HandleFunc("/api/static-routes/create", h.guarded(h.staticRouteHandler.Create))
	mux.HandleFunc("/api/static-routes/update", h.guarded(h.staticRouteHandler.Update))
	mux.HandleFunc("/api/static-routes/delete", h.guarded(h.staticRouteHandler.Delete))
	mux.HandleFunc("/api/static-routes/set-enabled", h.guarded(h.staticRouteHandler.SetEnabled))
	mux.HandleFunc("/api/static-routes/import", h.guarded(h.staticRouteHandler.Import))

	// Routing: unified tunnel listing for all routing subsystems
	routingHandler := api.NewRoutingHandler(s.catalog, s.ndmsQueries)
	routingHandler.SetEventBus(s.bus)
	mux.HandleFunc("/api/routing/tunnels", h.guarded(routingHandler.Tunnels))
	mux.HandleFunc("/api/routing/refresh", h.guarded(routingHandler.Refresh))

	// Routing: per-section GET endpoints (Task 11 — polling stores).
	// Reuse the dedicated service handlers so there is a single source of
	// truth per section. These URLs mirror the frontend store paths
	// (frontend/src/lib/stores/routing.ts).
	mux.HandleFunc("/api/routing/dns-routes", h.guarded(h.dnsRouteHandler.List))
	mux.HandleFunc("/api/routing/static-routes", h.guarded(h.staticRouteHandler.List))
	// Access policies + client routes + policy interfaces are registered
	// further below once their handlers exist (see "Routing polling GET
	// aliases" below).

	// DNS resolve for routing search
	resolveHandler := api.NewResolveHandler()
	mux.HandleFunc("/api/routing/resolve", h.guarded(resolveHandler.Resolve))

}

// registerSettingsRoutes — settings, ping check, monitoring matrix, NDMS ping-check.
func (s *Server) registerSettingsRoutes(mux *http.ServeMux, h *routeHandlers) {
	// Settings (protected + boot guarded)
	mux.HandleFunc("/api/settings/get", h.guarded(h.settingsHandler.Get))
	mux.HandleFunc("/api/settings/update", h.guarded(h.settingsHandler.Update))
	mux.HandleFunc("/api/settings/regenerate-api-key", h.guarded(h.settingsHandler.RegenerateApiKey))

	// Ping check (protected + boot guarded)
	mux.HandleFunc("/api/pingcheck/status", h.guarded(h.pingCheckHandler.GetStatus))
	mux.HandleFunc("/api/pingcheck/logs", h.guarded(h.pingCheckHandler.GetLogs))
	mux.HandleFunc("/api/pingcheck/check-now", h.guarded(h.pingCheckHandler.CheckNow))
	mux.HandleFunc("/api/pingcheck/logs/clear", h.guarded(h.pingCheckHandler.ClearLogs))

	// Monitoring matrix (protected)
	monitoringHandler := api.NewMonitoringHandler(s.monitoringService)
	mux.HandleFunc("/api/monitoring/matrix", h.guarded(monitoringHandler.GetMatrix))

	// Per-tunnel NDMS ping-check (nativewg)
	mux.HandleFunc("/api/tunnels/pingcheck", h.guarded(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			h.pingCheckHandler.GetTunnelPingCheckStatus(w, r)
		case http.MethodPost:
			h.pingCheckHandler.ConfigureTunnelPingCheck(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	mux.HandleFunc("/api/tunnels/pingcheck/remove", h.guarded(h.pingCheckHandler.RemoveTunnelPingCheck))

	// FreeTurn (protected)
	mux.HandleFunc("/api/proxy/listener", h.guarded(h.proxyListenerHandler.GetListener))
	mux.HandleFunc("/api/proxy/kill-listener", h.guarded(h.proxyListenerHandler.KillListener))

	mux.HandleFunc("/api/freeturn/config", h.guarded(h.freeturnHandler.GetConfig))
	mux.HandleFunc("/api/freeturn/client/config", h.guarded(h.freeturnHandler.UpdateClientConfig))
	mux.HandleFunc("/api/freeturn/server/config", h.guarded(h.freeturnHandler.UpdateServerConfig))
	mux.HandleFunc("/api/freeturn/status", h.guarded(h.freeturnHandler.GetStatus))
	mux.HandleFunc("/api/freeturn/captcha/status", h.guarded(h.freeturnHandler.GetCaptchaStatus))
	mux.HandleFunc("/api/freeturn/client/start", h.guarded(h.freeturnHandler.StartClient))
	mux.HandleFunc("/api/freeturn/client/stop", h.guarded(h.freeturnHandler.StopClient))
	mux.HandleFunc("/api/freeturn/server/start", h.guarded(h.freeturnHandler.StartServer))
	mux.HandleFunc("/api/freeturn/server/stop", h.guarded(h.freeturnHandler.StopServer))
	mux.HandleFunc("/api/freeturn/server/link", h.guarded(h.freeturnHandler.GenerateLink))
	mux.HandleFunc("/api/freeturn/link/decode", h.guarded(h.freeturnHandler.DecodeLink))
	mux.HandleFunc("/api/freeturn/install", h.guarded(h.freeturnHandler.Install))
	mux.HandleFunc("/api/freeturn/clients/", h.guarded(h.freeturnHandler.ServeClients))
	mux.HandleFunc("/api/freeturn/servers/", h.guarded(h.freeturnHandler.ServeServers))
	mux.HandleFunc("/api/freeturn/clients", h.guarded(h.freeturnHandler.CreateClient))
	mux.HandleFunc("/api/freeturn/servers", h.guarded(h.freeturnHandler.CreateServer))

	mux.HandleFunc("/api/wdtt/config", h.guarded(h.wdttHandler.GetConfig))
	mux.HandleFunc("/api/wdtt/client/config", h.guarded(h.wdttHandler.UpdateClientConfig))
	mux.HandleFunc("/api/wdtt/status", h.guarded(h.wdttHandler.GetStatus))
	mux.HandleFunc("/api/wdtt/client/start", h.guarded(h.wdttHandler.StartClient))
	mux.HandleFunc("/api/wdtt/client/stop", h.guarded(h.wdttHandler.StopClient))
	mux.HandleFunc("/api/wdtt/link/decode", h.guarded(h.wdttHandler.DecodeLink))
	mux.HandleFunc("/api/wdtt/link/import", h.guarded(h.wdttHandler.ImportLink))
	mux.HandleFunc("/api/wdtt/install", h.guarded(h.wdttHandler.Install))
	mux.HandleFunc("/api/wdtt/server/config", h.guarded(h.wdttHandler.UpdateServerConfig))
	mux.HandleFunc("/api/wdtt/server/start", h.guarded(h.wdttHandler.StartServer))
	mux.HandleFunc("/api/wdtt/server/stop", h.guarded(h.wdttHandler.StopServer))
	mux.HandleFunc("/api/wdtt/clients/", h.guarded(h.wdttHandler.ServeClients))
	mux.HandleFunc("/api/wdtt/clients", h.guarded(h.wdttHandler.CreateClient))
	mux.HandleFunc("/api/wdtt/servers/", h.guarded(h.wdttHandler.ServeServers))
	mux.HandleFunc("/api/wdtt/servers", h.guarded(h.wdttHandler.CreateServer))

}

// registerDeviceProxyRoutes — device proxy incl. multi-instance endpoints.
func (s *Server) registerDeviceProxyRoutes(mux *http.ServeMux, h *routeHandlers) {
	// Device proxy (protected + boot guarded)
	deviceProxyHandler := api.NewDeviceProxyHandler(s.deviceProxySvc, h.appLog)
	mux.HandleFunc("/api/proxy/config", h.guarded(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			deviceProxyHandler.GetConfig(w, r)
		case http.MethodPut:
			deviceProxyHandler.SaveConfig(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	mux.HandleFunc("/api/proxy/runtime", h.guarded(deviceProxyHandler.GetRuntime))
	mux.HandleFunc("/api/proxy/runtime/select", h.guarded(deviceProxyHandler.SelectRuntime))
	mux.HandleFunc("/api/proxy/apply", h.guarded(deviceProxyHandler.ForceApply))
	mux.HandleFunc("/api/proxy/outbounds", h.guarded(deviceProxyHandler.ListOutbounds))
	mux.HandleFunc("/api/proxy/listen-choices", h.guarded(deviceProxyHandler.ListenChoices))

	// Multi-instance device proxy endpoints
	mux.HandleFunc("/api/proxy/instances", h.guarded(deviceProxyHandler.ListInstances))
	mux.HandleFunc("/api/proxy/instance", h.guarded(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			deviceProxyHandler.GetInstance(w, r)
		case http.MethodPut:
			deviceProxyHandler.SaveInstance(w, r)
		case http.MethodDelete:
			deviceProxyHandler.DeleteInstance(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	mux.HandleFunc("/api/proxy/instances/apply", h.guarded(deviceProxyHandler.ApplyInstances))
	mux.HandleFunc("/api/proxy/instance/runtime", h.guarded(deviceProxyHandler.GetInstanceRuntime))
	mux.HandleFunc("/api/proxy/instance/runtime/select", h.guarded(deviceProxyHandler.SelectInstanceRuntime))
	mux.HandleFunc("/api/proxy/instance/check-ip", h.guarded(deviceProxyHandler.CheckInstanceExternalIP))

}

// registerLogsImportRoutes — logging, import, external tunnels, system WireGuard tunnels.
func (s *Server) registerLogsImportRoutes(mux *http.ServeMux, h *routeHandlers) {
	// Logging (protected + boot guarded)
	mux.HandleFunc("/api/logs", h.guarded(h.loggingHandler.GetLogs))
	mux.HandleFunc("/api/logs/clear", h.guarded(h.loggingHandler.ClearLogs))
	mux.HandleFunc("/api/logs/subgroups", h.guarded(h.loggingHandler.GetSubgroups))

	// Import (protected + boot guarded)
	mux.HandleFunc("/api/import/conf", h.guarded(h.importHandler.ImportConf))

	amneziaCPHandler := api.NewAmneziaCPHandler(h.appLog)
	amneziaCPHandler.SetDownloader(s.downloadSvc)
	mux.HandleFunc("/api/amnezia-premium/login", h.guarded(amneziaCPHandler.Login))
	mux.HandleFunc("/api/amnezia-premium/account-info", h.guarded(amneziaCPHandler.AccountInfo))
	mux.HandleFunc("/api/amnezia-premium/download-config", h.guarded(amneziaCPHandler.DownloadConfig))

	// External tunnels (protected + boot guarded)
	mux.HandleFunc("/api/external-tunnels", h.guarded(h.externalHandler.List))
	mux.HandleFunc("/api/external-tunnels/adopt", h.guarded(h.externalHandler.Adopt))

	// System WireGuard tunnels (protected + boot guarded)
	h.systemTunnelHandler = api.NewSystemTunnelsHandler(s.systemTunnelService, s.settings, s.tunnels, s.loggingService)
	mux.HandleFunc("/api/system-tunnels", h.guarded(h.systemTunnelHandler.List))
	mux.HandleFunc("/api/system-tunnels/get", h.guarded(h.systemTunnelHandler.Get))
	mux.HandleFunc("/api/system-tunnels/asc", h.guarded(h.systemTunnelHandler.ASC))
	mux.HandleFunc("/api/system-tunnels/test-connectivity", h.guarded(h.systemTunnelHandler.CheckConnectivity))
	mux.HandleFunc("/api/system-tunnels/test-ip", h.guarded(h.systemTunnelHandler.CheckIP))
	mux.HandleFunc("/api/system-tunnels/test-speed", h.guarded(h.systemTunnelHandler.SpeedTestStream))

	// System tunnel traffic is now gathered by ndmsMetricsPoller via the
	// runningInterfacesAdapter (wired in main.go).

}

// registerServerRoutes — VPN servers, managed WG servers, signature capture, terminal.
func (s *Server) registerServerRoutes(mux *http.ServeMux, h *routeHandlers) {
	// VPN Servers (protected + boot guarded)
	h.serverHandler = api.NewServersHandler(s.ndmsQueries, s.settings, s.tunnels, h.appLog)
	h.serverHandler.SetCommands(s.ndmsCommands)
	h.serverHandler.SetEventBus(s.bus)
	if s.singboxConnsHandler != nil {
		// System WG-server peer names for the connections monitor (issue
		// #435). Backed by the WGServers list cache (5m TTL + hook
		// invalidation), so per-request cost is an in-memory read.
		s.singboxConnsHandler.SetWGServers(h.serverHandler)
	}
	if h.connectionsService != nil {
		// Те же источники имён для conntrack-соединений (issue #639):
		// клиенты туннелей приходят без MAC и без этого видны как IP.
		var managed connections.ManagedServersLister
		if s.managedServiceImpl != nil {
			managed = s.managedServiceImpl
		}
		h.connectionsService.SetPeerNameSources(h.serverHandler, managed)
	}
	mux.HandleFunc("/api/servers", h.guarded(h.serverHandler.List))
	mux.HandleFunc("/api/servers/all", h.guarded(h.serverHandler.GetAll))
	mux.HandleFunc("/api/servers/get", h.guarded(h.serverHandler.Get))
	mux.HandleFunc("/api/servers/config", h.guarded(h.serverHandler.Config))
	mux.HandleFunc("/api/servers/mark", h.guarded(h.serverHandler.Mark))
	mux.HandleFunc("/api/servers/marked", h.guarded(h.serverHandler.Marked))
	mux.HandleFunc("/api/servers/enabled", h.guarded(h.serverHandler.SetEnabled))
	mux.HandleFunc("/api/servers/restart", h.guarded(h.serverHandler.Restart))
	mux.HandleFunc("/api/servers/wan-ip", h.guarded(h.serverHandler.WANIP))
	mux.HandleFunc("/api/servers/", h.guarded(h.serverHandler.Subtree))

	// Managed WireGuard Servers (protected + boot guarded). The new
	// route table is id-keyed: see ManagedServerHandler.Subtree for the
	// full sub-path dispatch (peers, conf, asc, etc).
	h.managedHandler = api.NewManagedServerHandler(s.managedService)
	h.managedHandler.SetServersHandler(h.serverHandler)
	h.serverHandler.SetManagedHandler(h.managedHandler)
	if s.managedServiceImpl != nil {
		h.serverHandler.SetManagedService(s.managedServiceImpl)
	}
	mux.HandleFunc("/api/managed-servers", h.guarded(h.managedHandler.Collection))
	mux.HandleFunc("/api/managed-servers/", h.guarded(h.managedHandler.Subtree))

	if s.managedServiceImpl != nil {
		managedBackupHandler := api.NewManagedServerBackupHandler(s.managedServiceImpl)
		managedBackupHandler.SetEventBus(s.bus)
		mux.HandleFunc("/api/managed/export", h.guarded(managedBackupHandler.Export))
		mux.HandleFunc("/api/managed/import", h.guarded(managedBackupHandler.Import))
		mux.HandleFunc("/api/managed/drift", h.guarded(managedBackupHandler.Drift))
		mux.HandleFunc("/api/managed/restore-drift", h.guarded(managedBackupHandler.RestoreDrift))
	}

	// Signature capture (protected + boot guarded)
	mux.HandleFunc("/api/signature/capture", h.guarded(h.signatureHandler.Capture))
	mux.HandleFunc("/api/signature/generate", h.guarded(h.signatureHandler.Generate))

	// Terminal
	mux.HandleFunc("/api/terminal/status", h.guarded(h.terminalHandler.Status))
	mux.HandleFunc("/api/terminal/install", h.guarded(h.terminalHandler.Install))
	mux.HandleFunc("/api/terminal/start", h.guarded(h.terminalHandler.Start))
	mux.HandleFunc("/api/terminal/stop", h.guarded(h.terminalHandler.Stop))
	mux.HandleFunc("/api/terminal/ws", h.guarded(h.terminalHandler.WebSocket))

}

// registerPolicyRoutes — access policies, client routes, routing polling aliases.
func (s *Server) registerPolicyRoutes(mux *http.ServeMux, h *routeHandlers) {
	// Access policies — handler created outside block for shared endpoints
	h.accessPolicyHandler = api.NewAccessPolicyHandler(s.accessPolicyService)

	// Devices endpoint uses hotspot RCI — works on both OS4 and OS5
	mux.HandleFunc("/api/access-policies/devices", h.guarded(h.accessPolicyHandler.ListDevices))

	// Политики доступа работают на обеих ветках прошивки: `ip policy` есть с
	// 2.12, `ip policy standalone` с 4.02, а `/show/rc/ip/policy` отдаёт
	// конфиг и на 4.x. Гейта по версии здесь быть не должно ещё и потому, что
	// osdetect отдаёт 4.x, пока не заполнен ndmsinfo: маршруты регистрируются
	// один раз при старте, и медленный NDMS выключал бы вкладку и на 5.x.
	mux.HandleFunc("/api/access-policies", h.guarded(h.accessPolicyHandler.List))
	mux.HandleFunc("/api/access-policies/create", h.guarded(h.accessPolicyHandler.Create))
	mux.HandleFunc("/api/access-policies/delete", h.guarded(h.accessPolicyHandler.Delete))
	mux.HandleFunc("/api/access-policies/description", h.guarded(h.accessPolicyHandler.SetDescription))
	mux.HandleFunc("/api/access-policies/standalone", h.guarded(h.accessPolicyHandler.SetStandalone))
	mux.HandleFunc("/api/access-policies/permit", h.guarded(h.accessPolicyHandler.PermitInterface))
	mux.HandleFunc("/api/access-policies/assign", h.guarded(h.accessPolicyHandler.AssignDevice))
	mux.HandleFunc("/api/access-policies/interfaces", h.guarded(h.accessPolicyHandler.ListGlobalInterfaces))
	mux.HandleFunc("/api/access-policies/interface-up", h.guarded(h.accessPolicyHandler.SetInterfaceUp))

	// Client routes (per-device VPN routing) — works on both OS4 and OS5
	h.crHandler = api.NewClientRouteHandler(s.clientRouteService)
	mux.HandleFunc("/api/client-routes", h.guarded(h.crHandler.HandleList))
	mux.HandleFunc("/api/client-routes/create", h.guarded(h.crHandler.HandleCreate))
	mux.HandleFunc("/api/client-routes/update", h.guarded(h.crHandler.HandleUpdate))
	mux.HandleFunc("/api/client-routes/delete", h.guarded(h.crHandler.HandleDelete))
	mux.HandleFunc("/api/client-routes/toggle", h.guarded(h.crHandler.HandleToggle))

	// Routing polling GET aliases for sections whose handlers live later
	// in this function (accesspolicy + clientroute). See Task 11.
	mux.HandleFunc("/api/routing/client-routes", h.guarded(h.crHandler.HandleList))
	// Policy devices endpoint is unconditional (hotspot RCI works on both OSes).
	mux.HandleFunc("/api/routing/policy-devices", h.guarded(h.accessPolicyHandler.ListDevices))
	mux.HandleFunc("/api/routing/access-policies", h.guarded(h.accessPolicyHandler.List))
	mux.HandleFunc("/api/routing/policy-interfaces", h.guarded(h.accessPolicyHandler.ListGlobalInterfaces))

}

// registerDiagnosticsRoutes — diagnostics, DNS proxy info, connections viewer, boot status.
func (s *Server) registerDiagnosticsRoutes(mux *http.ServeMux, h *routeHandlers) {
	// Diagnostics (protected + boot guarded)
	mux.HandleFunc("/api/diagnostics/run", h.guarded(h.diagHandler.Run))
	mux.HandleFunc("/api/diagnostics/status", h.guarded(h.diagHandler.Status))
	mux.HandleFunc("/api/diagnostics/result", h.guarded(h.diagHandler.Result))
	mux.HandleFunc("/api/diagnostics/stream", h.guarded(h.diagHandler.Stream))

	// DNS proxy info (read-only ndnproxy state). ndmsQueries may be nil on
	// platforms without NDMS wiring — guard the construction.
	if s.ndmsQueries != nil && s.ndmsQueries.DNSProxyStatus != nil {
		dnsProxyInfoHandler := api.NewDnsProxyInfoHandler(s.ndmsQueries.DNSProxyStatus, s.accessPolicyService)
		mux.HandleFunc("/api/diagnostics/dns-proxy", h.guarded(dnsProxyInfoHandler.Get))
	}

	// Connections viewer (protected)
	mux.HandleFunc("/api/connections", h.guarded(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			h.connectionsHandler.Kill(w, r)
			return
		}
		h.connectionsHandler.List(w, r)
	}))

	// NDMS save-coordinator status (protected) — polled by the header
	// save indicator. SaveCoordinator emits a resource:invalidated hint
	// on every state transition so clients refetch this endpoint.
	ndmsHandler := api.NewNDMSHandler(s.ndmsSaveCoord)
	mux.HandleFunc("/api/ndms/save-status", h.guarded(ndmsHandler.GetSaveStatus))

	// Boot status (public - frontend uses instanceId for restart detection).
	// Dedicated api.BootStatusHandler so the endpoint keeps its swagger
	// annotations (@Router /boot-status) — an inline closure here is
	// invisible to `swag init` and silently drops the path from the spec.
	mux.HandleFunc("/api/boot-status", api.NewBootStatusHandler(s.instanceID).Get)

}

// wireCrossHandlers — event-bus and cross-handler wiring (no route registrations of its own except hook aliases).
func (s *Server) wireCrossHandlers(mux *http.ServeMux, h *routeHandlers) {
	// Wire event bus to CRUD handlers for SSE publishing
	h.tunnelsHandler.SetEventBus(s.bus)
	h.tunnelsHandler.SetCatalog(s.catalog)
	h.dnsRouteHandler.SetEventBus(s.bus)
	h.staticRouteHandler.SetEventBus(s.bus)
	h.accessPolicyHandler.SetEventBus(s.bus)
	h.crHandler.SetEventBus(s.bus)
	h.serverHandler.SetEventBus(s.bus)
	if s.awg3Handler != nil {
		s.awg3Handler.SetEventBus(s.bus)
	}

	// Cross-wire servers <-> managed for unified server:updated event
	h.serverHandler.SetManagedHandler(h.managedHandler)
	h.managedHandler.SetServersHandler(h.serverHandler)
	if s.managedServiceImpl != nil {
		h.serverHandler.SetManagedService(s.managedServiceImpl)
	}

	// Plug MetricsPoller into the handler now that ServersHandler is fully
	// wired (bus + managed). The poller re-broadcasts the full server snapshot
	// via serverHandler whenever any server's peer metrics change.
	if s.metricsPoller != nil {
		s.metricsPoller.SetServerSnapshotPublisher(h.serverHandler)
		s.metricsPoller.Start()
	}

	// Composite tunnels snapshot builder — used by GET /api/tunnels/all
	// and by the hook-driven resource:invalidated refresher to assemble
	// the {tunnels, external, system} payload the polling store reads.
	tsb := api.NewTunnelsSnapshotBuilder()
	tsb.SetTunnelsHandler(h.tunnelsHandler)
	tsb.SetExternalHandler(h.externalHandler)
	tsb.SetSystemTunnelsHandler(h.systemTunnelHandler)

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
	h.hookHandler.SetTunnelRefresher(invalidateTunnelsOnHook)
	// Injects the composite {tunnels, external, system} builder used by
	// GetAll so /api/tunnels/all returns the exact shape the polling
	// store expects.
	h.tunnelsHandler.SetTunnelsSnapshotBuilder(func(ctx context.Context) map[string]interface{} {
		return tsb.Build(ctx)
	})
	h.tunnelsHandler.SetSelfCreateGate(h.hookHandler)

	// DNS routing diagnostics
	if s.dnsCheckService != nil {
		dnsCheckHandler := api.NewDnsCheckHandler(s.dnsCheckService)
		mux.HandleFunc("/api/dns-check/start", h.guarded(dnsCheckHandler.Start))
		mux.HandleFunc("/api/dns-check/client", h.guarded(dnsCheckHandler.Client))
		mux.HandleFunc("/api/dns-check/probe", dnsCheckHandler.Probe) // NO auth — cross-origin
	}

}

// registerSingboxRoutes — the sing-box integration surface.
func (s *Server) registerSingboxRoutes(mux *http.ServeMux, h *routeHandlers) {
	// Sing-box integration (protected + boot guarded)
	if s.singboxHandler != nil {
		s.singboxHandler.SetSettingsStore(s.settings)
		mux.HandleFunc("/api/singbox/status", h.guarded(s.singboxHandler.Status))
		mux.HandleFunc("/api/singbox/install", h.guarded(s.singboxHandler.Install))
		mux.HandleFunc("/api/singbox/update", h.guarded(s.singboxHandler.Update))
		mux.HandleFunc("/api/singbox/uninstall", h.guarded(s.singboxHandler.Uninstall))
		mux.HandleFunc("/api/singbox/control", h.guarded(s.singboxHandler.Control))
		mux.HandleFunc("/api/singbox/ndms-proxy", h.guarded(s.singboxHandler.ToggleNDMSProxy))
		mux.HandleFunc("/api/singbox/tunnels/delay-check", h.guarded(s.singboxHandler.DelayCheck))
		mux.HandleFunc("/api/singbox/tunnels/test/connectivity", h.guarded(s.singboxHandler.CheckConnectivity))
		mux.HandleFunc("/api/singbox/tunnels/test/ip", h.guarded(s.singboxHandler.CheckIP))
		mux.HandleFunc("/api/singbox/tunnels/test/speed/stream", h.guarded(s.singboxHandler.SpeedTestStream))
		mux.HandleFunc("/api/singbox/tunnels/get", h.guarded(s.singboxHandler.GetTunnel))
		mux.HandleFunc("/api/singbox/tunnels/rename", h.guarded(s.singboxHandler.RenameTunnel))
		mux.HandleFunc("/api/singbox/tunnels/share-link", h.guarded(s.singboxHandler.ExportShareLink))
		mux.HandleFunc("/api/singbox/tunnels", h.guarded(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				// Одиночный туннель переехал на /api/singbox/tunnels/get
				// (#520). Старый вариант ?tag= отвечаем громким 400, а не
				// тихим списком другой формы — чтобы не обновившийся клиент
				// (закешированная вкладка SPA, чей-то скрипт) получил
				// диагностику, а не молчаливую порчу данных.
				if r.URL.Query().Has("tag") {
					response.BadRequest(w, "single-tunnel GET moved: use /api/singbox/tunnels/get?tag=")
					return
				}
				s.singboxHandler.ListTunnels(w, r)
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
	if s.singboxConfigHandler != nil {
		mux.HandleFunc("/api/singbox/config-preview", h.guarded(s.singboxConfigHandler.Preview))
	}
	if s.singboxConfigEditorHandler != nil {
		ce := s.singboxConfigEditorHandler
		mux.HandleFunc("/api/singbox/config/slots", h.guarded(ce.ListSlots))
		mux.HandleFunc("/api/singbox/config/slot", h.guarded(ce.GetSlot))
		mux.HandleFunc("/api/singbox/config/user", h.guarded(ce.PutUserConfig))
		mux.HandleFunc("/api/singbox/config/user/check", h.guarded(ce.CheckUserConfig))
		mux.HandleFunc("/api/singbox/config/user/apply", h.guarded(ce.ApplyUserConfig))
		mux.HandleFunc("/api/singbox/config/user/discard", h.guarded(ce.DiscardUserConfig))
		mux.HandleFunc("/api/singbox/config/user/enable", h.guarded(ce.EnableUserConfig))
	}
	if s.singboxInboundsHandler != nil {
		mux.HandleFunc("/api/singbox/inbounds", h.guarded(s.singboxInboundsHandler.List))
	}
	if s.clashProxy != nil {
		mux.HandleFunc("/api/singbox/clash/", h.guarded(s.clashProxy.ServeHTTP))
		mux.HandleFunc("/api/singbox/clash", h.guarded(s.clashProxy.ServeHTTP))
	}
	if s.singboxConnsHandler != nil {
		mux.HandleFunc("/api/singbox/connections/clients", h.guarded(s.singboxConnsHandler.Clients))
	}

	if s.singboxRouterHandler != nil {
		rh := s.singboxRouterHandler
		mux.HandleFunc("/api/singbox/router/status", h.guarded(rh.GetStatus))
		mux.HandleFunc("/api/singbox/router/mode", h.guarded(rh.SwitchMode))
		mux.HandleFunc("/api/singbox/router/settings", h.guarded(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				rh.GetSettings(w, r)
			} else {
				rh.PutSettings(w, r)
			}
		}))
		mux.HandleFunc("/api/singbox/router/rules/list", h.guarded(rh.ListRules))
		mux.HandleFunc("/api/singbox/router/rules/add", h.guarded(rh.AddRule))
		mux.HandleFunc("/api/singbox/router/rules/update", h.guarded(rh.UpdateRule))
		mux.HandleFunc("/api/singbox/router/rules/bulk-outbound", h.guarded(rh.BulkSetRuleOutbound))
		mux.HandleFunc("/api/singbox/router/rules/delete", h.guarded(rh.DeleteRule))
		mux.HandleFunc("/api/singbox/router/rules/move", h.guarded(rh.MoveRule))
		mux.HandleFunc("/api/singbox/router/rulesets/list", h.guarded(rh.ListRuleSets))
		mux.HandleFunc("/api/singbox/router/rulesets/add", h.guarded(rh.AddRuleSet))
		mux.HandleFunc("/api/singbox/router/rulesets/update", h.guarded(rh.UpdateRuleSet))
		mux.HandleFunc("/api/singbox/router/rulesets/bulk-detour", h.guarded(rh.BulkSetRuleSetDetour))
		mux.HandleFunc("/api/singbox/router/rulesets/delete", h.guarded(rh.DeleteRuleSet))
		mux.HandleFunc("/api/singbox/router/rulesets/dat-url", h.guarded(rh.DatRuleSetURL))
		mux.HandleFunc("/api/singbox/router/rulesets/dat-srs", rh.DatRuleSetSRS)
		// Каталог SagerNet geosite — дополнение к каталогу пресетов:
		// хендлер создаётся здесь один раз, кэш живёт в нём.
		geositesHandler := api.NewSingboxGeositesHandler(s.downloadSvc)
		mux.HandleFunc("/api/singbox/router/geosites/list", h.guarded(geositesHandler.List))
		mux.HandleFunc("/api/singbox/router/outbounds/list", h.guarded(rh.ListOutbounds))
		mux.HandleFunc("/api/singbox/router/outbounds/add", h.guarded(rh.AddOutbound))
		mux.HandleFunc("/api/singbox/router/outbounds/update", h.guarded(rh.UpdateOutbound))
		mux.HandleFunc("/api/singbox/router/outbounds/delete", h.guarded(rh.DeleteOutbound))
		mux.HandleFunc("/api/singbox/router/presets/list", h.guarded(rh.ListPresets))
		mux.HandleFunc("/api/singbox/router/presets/apply", h.guarded(rh.ApplyPreset))
		mux.HandleFunc("/api/singbox/router/policies", h.guarded(rh.PoliciesCollection))
		mux.HandleFunc("/api/singbox/router/wan-interfaces", h.guarded(rh.ListWANInterfaces))
		mux.HandleFunc("/api/singbox/router/bindable-interfaces", h.guarded(rh.ListBindableInterfaces))
		mux.HandleFunc("/api/singbox/router/ingress-eligible-interfaces", h.guarded(rh.ListIngressEligibleInterfaces))
		mux.HandleFunc("/api/singbox/router/policy-tun/nat-preview", h.guarded(rh.PolicyTunNATPreview))
		mux.HandleFunc("/api/singbox/router/policy-devices", h.guarded(rh.ListPolicyDevices))
		mux.HandleFunc("/api/singbox/router/policy-devices/bind", h.guarded(rh.BindDevice))
		mux.HandleFunc("/api/singbox/router/policy-devices/unbind", h.guarded(rh.UnbindDevice))
		mux.HandleFunc("/api/singbox/router/dns/servers/list", h.guarded(rh.ListDNSServers))
		mux.HandleFunc("/api/singbox/router/dns/servers/add", h.guarded(rh.AddDNSServer))
		mux.HandleFunc("/api/singbox/router/dns/servers/update", h.guarded(rh.UpdateDNSServer))
		mux.HandleFunc("/api/singbox/router/dns/servers/delete", h.guarded(rh.DeleteDNSServer))
		mux.HandleFunc("/api/singbox/router/dns/servers/move", h.guarded(rh.MoveDNSServer))
		mux.HandleFunc("/api/singbox/router/dns/servers/lookup", h.guarded(rh.LookupDNSServer))
		mux.HandleFunc("/api/singbox/router/dns/rules/list", h.guarded(rh.ListDNSRules))
		mux.HandleFunc("/api/singbox/router/dns/rules/add", h.guarded(rh.AddDNSRule))
		mux.HandleFunc("/api/singbox/router/dns/rules/update", h.guarded(rh.UpdateDNSRule))
		mux.HandleFunc("/api/singbox/router/dns/rules/delete", h.guarded(rh.DeleteDNSRule))
		mux.HandleFunc("/api/singbox/router/dns/rules/move", h.guarded(rh.MoveDNSRule))
		mux.HandleFunc("/api/singbox/router/dns/globals", h.guarded(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				rh.GetDNSGlobals(w, r)
			} else {
				rh.PutDNSGlobals(w, r)
			}
		}))
		mux.HandleFunc("/api/singbox/router/dns/chain-preset", h.guarded(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				rh.GetDNSChainPreset(w, r)
			} else {
				rh.PutDNSChainPreset(w, r)
			}
		}))
		mux.HandleFunc("/api/singbox/router/route/final", h.guarded(rh.SetRouteFinal))
		mux.HandleFunc("/api/singbox/router/inspect", h.guarded(rh.Inspect))
		mux.HandleFunc("/api/singbox/router/inspect-dns", h.guarded(rh.InspectDNS))
		mux.HandleFunc("/api/singbox/router/inspect/stream", h.guarded(rh.InspectStream))
		mux.HandleFunc("/api/singbox/router/staging", h.guarded(rh.GetStaging))
		mux.HandleFunc("/api/singbox/router/staging/apply", h.guarded(rh.PostStagingApply))
		mux.HandleFunc("/api/singbox/router/staging/discard", h.guarded(rh.PostStagingDiscard))
	}

	if s.bypassSetHandler != nil {
		bh := s.bypassSetHandler
		mux.HandleFunc("/api/singbox/router/bypass-set/status", h.guarded(bh.GetStatus))
		mux.HandleFunc("/api/singbox/router/bypass-set/install-deps", h.guarded(bh.InstallDeps))
		mux.HandleFunc("/api/singbox/router/bypass-set/install-conntrack", h.guarded(bh.InstallConntrack))
	}

	if s.singboxFakeIPConfigHandler != nil {
		fh := s.singboxFakeIPConfigHandler
		mux.HandleFunc("/api/singbox/fakeip/config/dns/servers/list", h.guarded(fh.ListDNSServers))
		mux.HandleFunc("/api/singbox/fakeip/config/dns/servers/add", h.guarded(fh.AddDNSServer))
		mux.HandleFunc("/api/singbox/fakeip/config/dns/servers/update", h.guarded(fh.UpdateDNSServer))
		mux.HandleFunc("/api/singbox/fakeip/config/dns/servers/delete", h.guarded(fh.DeleteDNSServer))
		mux.HandleFunc("/api/singbox/fakeip/config/dns/servers/move", h.guarded(fh.MoveDNSServer))
		mux.HandleFunc("/api/singbox/fakeip/config/dns/rules/list", h.guarded(fh.ListDNSRules))
		mux.HandleFunc("/api/singbox/fakeip/config/dns/rules/add", h.guarded(fh.AddDNSRule))
		mux.HandleFunc("/api/singbox/fakeip/config/dns/rules/update", h.guarded(fh.UpdateDNSRule))
		mux.HandleFunc("/api/singbox/fakeip/config/dns/rules/delete", h.guarded(fh.DeleteDNSRule))
		mux.HandleFunc("/api/singbox/fakeip/config/dns/rules/move", h.guarded(fh.MoveDNSRule))
		mux.HandleFunc("/api/singbox/fakeip/config/dns/globals", h.guarded(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				fh.GetDNSGlobals(w, r)
			} else {
				fh.PutDNSGlobals(w, r)
			}
		}))
		mux.HandleFunc("/api/singbox/fakeip/config/rules/list", h.guarded(fh.ListRules))
		mux.HandleFunc("/api/singbox/fakeip/config/rules/add", h.guarded(fh.AddRule))
		mux.HandleFunc("/api/singbox/fakeip/config/rules/update", h.guarded(fh.UpdateRule))
		mux.HandleFunc("/api/singbox/fakeip/config/rules/bulk-outbound", h.guarded(fh.BulkSetRuleOutbound))
		mux.HandleFunc("/api/singbox/fakeip/config/rules/delete", h.guarded(fh.DeleteRule))
		mux.HandleFunc("/api/singbox/fakeip/config/rules/move", h.guarded(fh.MoveRule))
		mux.HandleFunc("/api/singbox/fakeip/config/route/final", h.guarded(fh.SetRouteFinal))
		mux.HandleFunc("/api/singbox/fakeip/config/rulesets/list", h.guarded(fh.ListRuleSets))
		mux.HandleFunc("/api/singbox/fakeip/config/rulesets/add", h.guarded(fh.AddRuleSet))
		mux.HandleFunc("/api/singbox/fakeip/config/rulesets/update", h.guarded(fh.UpdateRuleSet))
		mux.HandleFunc("/api/singbox/fakeip/config/rulesets/bulk-detour", h.guarded(fh.BulkSetRuleSetDetour))
		mux.HandleFunc("/api/singbox/fakeip/config/rulesets/delete", h.guarded(fh.DeleteRuleSet))
		mux.HandleFunc("/api/singbox/fakeip/config/outbounds/list", h.guarded(fh.ListOutbounds))
		mux.HandleFunc("/api/singbox/fakeip/config/outbounds/add", h.guarded(fh.AddOutbound))
		mux.HandleFunc("/api/singbox/fakeip/config/outbounds/update", h.guarded(fh.UpdateOutbound))
		mux.HandleFunc("/api/singbox/fakeip/config/outbounds/delete", h.guarded(fh.DeleteOutbound))
	}

	if s.singboxProxiesHandler != nil {
		mux.HandleFunc("/api/singbox/router/proxies/list", h.guarded(s.singboxProxiesHandler.List))
		mux.HandleFunc("/api/singbox/router/proxies/select", h.guarded(s.singboxProxiesHandler.Select))
		mux.HandleFunc("/api/singbox/router/proxies/test", h.guarded(s.singboxProxiesHandler.Test))
	}

	if s.awgOutboundsHandler != nil {
		mux.HandleFunc("/api/singbox/awg-outbounds/tags", h.guarded(s.awgOutboundsHandler.ServeHTTP))
	}

	if s.subscriptionHandler != nil {
		sh := s.subscriptionHandler
		mux.HandleFunc("/api/singbox/subscriptions", h.guarded(sh.List))
		mux.HandleFunc("/api/singbox/subscriptions/create", h.guarded(sh.Create))
		mux.HandleFunc("/api/singbox/subscriptions/get", h.guarded(sh.Get))
		mux.HandleFunc("/api/singbox/subscriptions/update", h.guarded(sh.Update))
		mux.HandleFunc("/api/singbox/subscriptions/delete", h.guarded(sh.Delete))
		mux.HandleFunc("/api/singbox/subscriptions/refresh", h.guarded(sh.Refresh))
		mux.HandleFunc("/api/singbox/subscriptions/active-member", h.guarded(sh.ActiveMember))
		mux.HandleFunc("/api/singbox/subscriptions/active-now", h.guarded(sh.ActiveNow))
		mux.HandleFunc("/api/singbox/subscriptions/get-stream", h.guarded(sh.GetStream))
		mux.HandleFunc("/api/singbox/subscriptions/orphans/delete", h.guarded(sh.OrphansDelete))
		mux.HandleFunc("/api/singbox/subscriptions/rejected/to-info", h.guarded(sh.RejectedToInfo))
		mux.HandleFunc("/api/singbox/subscriptions/info/remove", h.guarded(sh.InfoRemove))
		mux.HandleFunc("/api/singbox/subscriptions/members/add", h.guarded(sh.AddMember))
		mux.HandleFunc("/api/singbox/subscriptions/members/remove", h.guarded(sh.RemoveMember))
		mux.HandleFunc("/api/singbox/subscriptions/members/exclude", h.guarded(sh.ExcludeMembers))
		mux.HandleFunc("/api/singbox/subscriptions/members/restore", h.guarded(sh.RestoreMembers))
		mux.HandleFunc("/api/singbox/subscriptions/preview", h.guarded(sh.PreviewURL))
		mux.HandleFunc("/api/singbox/subscriptions/detect-headers", h.guarded(sh.DetectHeaders))
		mux.HandleFunc("/api/singbox/subscriptions/header-profiles", h.guarded(sh.HeaderProfiles))
		mux.HandleFunc("/api/singbox/subscriptions/happ-keys", h.guarded(sh.HappKeys))
		mux.HandleFunc("/api/singbox/subscriptions/groups", h.guarded(sh.ListGroups))
		mux.HandleFunc("/api/singbox/subscriptions/groups/create", h.guarded(sh.CreateGroup))
		mux.HandleFunc("/api/singbox/subscriptions/groups/update", h.guarded(sh.UpdateGroup))
		mux.HandleFunc("/api/singbox/subscriptions/groups/delete", h.guarded(sh.DeleteGroup))
	}

	if s.dnsRewritesHandler != nil {
		rw := s.dnsRewritesHandler
		mux.HandleFunc("/api/singbox/router/dns/rewrites/list", h.guarded(rw.List))
		mux.HandleFunc("/api/singbox/router/dns/rewrites/add", h.guarded(rw.Add))
		mux.HandleFunc("/api/singbox/router/dns/rewrites/update", h.guarded(rw.Update))
		mux.HandleFunc("/api/singbox/router/dns/rewrites/delete", h.guarded(rw.Delete))
		mux.HandleFunc("/api/singbox/router/dns/rewrites/move", h.guarded(rw.Move))
	}

	// AWG3 endpoint import/CRUD. One handler dispatches by method+path over
	// both the collection route and the item route (trailing slash = subtree).
	if s.awg3Handler != nil {
		mux.HandleFunc("/api/awg3-endpoints", h.guarded(s.awg3Handler.Handle))
		mux.HandleFunc("/api/awg3-endpoints/", h.guarded(s.awg3Handler.Handle))
	}

}

// linkedEndpointSyncSetter реализуют freeturn.Service и wdtt.Service.
// Отдельный узкий интерфейс вместо метода в api.FreeTurnService/api.WdttService:
// те описывают ~35 методов и имеют полные моки в тестах, расширять их ради
// одного сеттера дороже, чем проверить тип здесь.
type linkedEndpointSyncSetter interface {
	SetLinkedEndpointSync(func(clientID, listen string) (int, error))
}

func (s *Server) wireLinkedEndpointSync(
	svc any,
	sync func(ctx context.Context, clientID, listen string) ([]string, []string),
	scope string,
) {
	setter, ok := svc.(linkedEndpointSyncSetter)
	if !ok {
		// Молчать нельзя: смена типа сервиса иначе тихо отключит sync, и
		// endpoint снова начнёт отставать от listen после listen-repair.
		s.appLog.Warn("wiring", scope, "сервис не умеет SetLinkedEndpointSync — endpoint linked-туннелей не синхронизируется")
		return
	}
	setter.SetLinkedEndpointSync(func(clientID, listen string) (int, error) {
		updated, errs := sync(context.Background(), clientID, listen)
		if len(errs) > 0 {
			return len(updated), errors.New(strings.Join(errs, "; "))
		}
		return len(updated), nil
	})
}

// registerStaticRoutes — preset catalog and the SPA static handler (must stay last).
func (s *Server) registerStaticRoutes(mux *http.ServeMux, h *routeHandlers) {
	// Unified preset catalog (protected, read-only in U0)
	if s.presetCatalog != nil {
		presetsHandler := api.NewPresetsHandler(s.presetCatalog)
		mux.HandleFunc("/api/presets", h.guarded(presetsHandler.List))
	}

	// Static files (SPA) - must be last.
	if s.config.FrontendFS != nil {
		mux.Handle("/", spaHandler(s.config.FrontendFS))
	}
}
