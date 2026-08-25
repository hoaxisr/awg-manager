package main

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/hoaxisr/awg-manager/internal/auth"
	"github.com/hoaxisr/awg-manager/internal/clientroute"
	"github.com/hoaxisr/awg-manager/internal/dnsroute"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/freeturn"
	"github.com/hoaxisr/awg-manager/internal/hydraroute"
	"github.com/hoaxisr/awg-manager/internal/logging"
	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/pingcheck"
	"github.com/hoaxisr/awg-manager/internal/presets"
	"github.com/hoaxisr/awg-manager/internal/proxyhealth"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/exitreg"
	"github.com/hoaxisr/awg-manager/internal/routing"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/env"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
	"github.com/hoaxisr/awg-manager/internal/terminal"
	"github.com/hoaxisr/awg-manager/internal/testing"
	"github.com/hoaxisr/awg-manager/internal/traffic"
	"github.com/hoaxisr/awg-manager/internal/tunnel/backend"
	"github.com/hoaxisr/awg-manager/internal/tunnel/external"
	"github.com/hoaxisr/awg-manager/internal/tunnel/firewall"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ops"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
	"github.com/hoaxisr/awg-manager/internal/tunnel/state"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wg"
	"github.com/hoaxisr/awg-manager/internal/wdtt"
)

// setupTunnels wires the tunnel core (wg/backend/state/firewall, NDMS
// commands, operators, WAN model, tunnel service), the routing catalog and
// the HydraRoute/geo-data integration.
func (a *app) setupTunnels() {
	// Create tunnel service components
	a.wgClient = wg.New()
	a.backendImpl = backend.New(a.bootLog)
	a.stateMgr = state.New(a.ndmsQueries.Interfaces, a.wgClient, a.backendImpl, a.loggingService)
	firewallMgr := firewall.New(a.backendImpl.Type() == backend.TypeKernel, osdetect.Is5(), a.loggingService)

	// Build NDMS CQRS Commands eagerly so the Operator can consume them.
	// HookNotifier is wired later (ndmsCommands.SetHookNotifier(orch)) once
	// the orchestrator exists — this breaks the construction cycle.
	a.eventBus = events.NewBus()
	a.ndmsSaveCoord = ndmscommand.NewSaveCoordinator(
		a.ndmsTransportClient,
		a.eventBus,
		3*time.Second,
		10*time.Second,
		env.DurationDefault("AWG_NDMS_SAVE_SETTLE_DELAY", 2*time.Second),
		a.ndmsQueries.RunningConfig,
	)
	a.ndmsCommands = ndmscommand.NewCommands(ndmscommand.Deps{
		Poster:       a.ndmsTransportClient,
		Save:         a.ndmsSaveCoord,
		Queries:      a.ndmsQueries,
		HookNotifier: nil, // wired after orchestrator construction below
		IsOS5:        osdetect.Is5,
	})

	a.operator = ops.NewOperator(a.ndmsQueries, a.ndmsCommands, a.wgClient, a.backendImpl, firewallMgr)

	// Create NativeWG operator
	a.nwgOp = nwg.NewOperator(a.ndmsQueries, a.ndmsCommands, a.ndmsTransportClient, a.loggingService)
	// Endpoint-стражу нужна свежая запись туннеля: пересборка kmod-слота на
	// новый адрес идёт по актуальным ключам, а не по снимку из реестра.
	a.nwgOp.SetTunnelLookup(a.awgStore.Get)

	// Load awg_proxy.ko if firmware < 5.1 Alpha 4
	if !ndmsinfo.SupportsWireguardASC() {
		if err := a.nwgOp.EnsureKmodLoaded(); err != nil {
			a.bootLog.Warn("kmod", "awg_proxy.ko", "not available: "+err.Error())
		}
	}

	// Create WAN state model (populated at boot, updated by hooks).
	// Re-populate callback fires when a hook reports an unknown interface
	// (USB hotplug, new PPPoE configured after boot, etc.).
	a.wanModel = wan.NewModel()
	a.wanModel.SetRepopulateFn(func() {
		populateWANModel(context.Background(), a.ndmsQueries, a.wanModel, a.bootLog)
	})

	// Create the main tunnel service
	a.tunnelService = service.New(a.awgStore, a.nwgOp, a.operator, a.stateMgr, a.wanModel, a.loggingService)

	// Migrate legacy ISPInterface="none" to "" (auto) for tunnels from older versions.
	a.tunnelService.MigrateISPInterfaceNone()
	a.tunnelService.MigrateEmptyBackend()

	// Реестр выходов прокси-рантайма (§5). Писателей у него до плана 5 нет:
	// объявление ведомости и подтверждение посева зовёт писатель конфига
	// инстансов, и пока он не проведён, реестр пуст, гейт посева закрыт,
	// уборка зеркальных записей не запускается ни при каких условиях, а
	// каталог живёт на резидуальном фолбэке. Имена этих двух методов здесь не
	// пишутся намеренно: ворота плана грепают их по прод-коду подстрокой.
	a.exitMirror = exitreg.NewStoreMirror(a.awgStore, a.eventBus)
	a.exitRegistry = exitreg.New(
		a.exitMirror,
		logging.NewScopedLogger(a.loggingService, logging.GroupTunnel, logging.SubLifecycle),
	)

	// Routing catalog — unified tunnel listing for all routing subsystems
	a.catalog = routing.NewCatalog(
		&tunnelProviderAdapter{svc: a.tunnelService, store: a.awgStore},
		a.ndmsQueries.Interfaces,
		&storeAdapter{store: a.awgStore},
		exitRegistryAdapter{reg: a.exitRegistry},
		a.loggingService,
	)

	// HydraRoute Neo integration (optional — detected at startup)
	a.hydraService = hydraroute.NewService(a.catalog, a.loggingService)
	a.geoDataStore = hydraroute.NewGeoDataStore(a.dataDir)
	a.geoDataStore.SetAppLogger(a.loggingService)
	a.hydraService.SetGeoDataStore(a.geoDataStore)
	// Adopt any geo files already listed in hrneo.conf (e.g. added manually
	// before awg-manager was installed) so they show up in the UI. Adoption
	// is stat-only — TagCount is populated lazily in the background.
	if cfg, err := hydraroute.ReadConfig(); err == nil {
		if n, err := a.geoDataStore.AdoptExternalFiles(cfg); err != nil {
			a.bootLog.Warn("hrneo-adopt-geo", "", err.Error())
		} else if n > 0 {
			a.bootLog.Info("hrneo-adopt-geo", "", fmt.Sprintf("adopted %d files from hrneo.conf", n))
		}
		if err := a.hydraService.SyncGeoFilesToConfig(); err != nil {
			a.bootLog.Warn("hrneo-sync-geo", "", "failed after adopt: "+err.Error())
		}
	}
	// Warm up tag cache for entries with TagCount=0 in a background goroutine.
	// Runs sequentially (one file at a time) to keep I/O pressure low — the
	// streaming parser holds ~64 KB RAM regardless of file size.
	go func() {
		for _, e := range a.geoDataStore.List() {
			if e.TagCount != 0 {
				continue
			}
			if _, err := a.geoDataStore.GetTags(e.Path); err != nil {
				a.bootLog.Warn("subscription-tag-parse", e.Path, err.Error())
			}
		}
	}()
	// NDMS wiring (SetQueries + SetPolicies) happens after ndmsCommands is
	// constructed — see below.

}

// setupServices constructs the auxiliary services: dns-route/static-route
// stores, presets, external tunnels, testing, pingcheck, auth, traffic
// history, updater, terminal and client routes.
func (a *app) setupServices() {
	// DNS route service (OS5 only — routes domains through tunnels via NDMS)
	// (constructed later, after ndmsCommands is available.)
	a.dnsRouteStore = dnsroute.NewStore(a.dataDir)
	if _, err := a.dnsRouteStore.Load(); err != nil {
		a.bootLog.Warn("dns-routes-load", "", err.Error())
	}

	a.hydraService.SetDnsListProvider(func() []hydraroute.DnsListInfo {
		data := a.dnsRouteStore.GetCached()
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
	a.staticRouteStore = storage.NewStaticRouteStore(a.dataDir)

	// Unified preset catalog (U0: read-only, no CRUD yet)
	presetStore := presets.NewStore(a.dataDir)
	a.presetCatalog = presets.NewCatalog(presetStore)

	// Create external tunnel service
	a.externalService = external.NewService(a.awgStore, a.settingsStore, a.tunnelService, a.loggingService)

	// System WireGuard tunnels (read-only + ASC editing) — constructed later,
	// after ndmsQueries/ndmsCommands are available.

	a.testService = testing.NewService(a.awgStore, a.loggingService)
	a.testService.SetSettingsStore(a.settingsStore)

	// Ping check service
	a.pingCheckService = pingcheck.NewService(a.settingsStore, a.awgStore, a.wgClient, a.loggingService)
	a.pingCheckService.Start()
	a.deferOnExit(a.pingCheckService.Stop)

	// FreeTurn service (TURN-tunnel client/server)
	a.freeturnService = freeturn.NewService(
		a.dataDir,
		filepath.Join(a.dataDir, "run"),
		"/opt/bin/freeturn-client",
		"/opt/bin/freeturn-server",
	)
	a.wdttService = wdtt.NewService(
		a.dataDir,
		filepath.Join(a.dataDir, "run"),
		"/opt/bin/wdtt-client",
		"/opt/bin/wdtt-server",
	)
	a.freeturnService.SetListenPortChecker(&crossListenPortChecker{
		AWGStore:           a.awgStore,
		WDTT:               a.wdttService,
		IncludeWdttClients: true,
	})
	a.wdttService.SetListenPortChecker(&crossListenPortChecker{
		AWGStore:               a.awgStore,
		FreeTurn:               a.freeturnService,
		IncludeFreeTurnClients: true,
	})
	relayProbe := &proxyhealth.HTTPRelayProbe{
		CheckURL: func() string {
			if a.settingsStore == nil {
				return ""
			}
			st, err := a.settingsStore.Load()
			if err != nil || st == nil {
				return ""
			}
			return st.ConnectivityCheckURL
		},
	}
	linkedTunnels := &proxyhealth.AWGLinkedTunnelResolver{Store: a.awgStore}
	a.freeturnService.SetRelayProbe(relayProbe)
	a.freeturnService.SetLinkedTunnelResolver(linkedTunnels)
	a.wdttService.SetRelayProbe(relayProbe)
	a.deferOnExit(a.freeturnService.Stop)
	a.deferOnExit(a.wdttService.Stop)

	// Unified facade: kernel → custom loop, NativeWG → NDMS native
	a.pingCheckFacade = pingcheck.NewFacade(a.pingCheckService, a.awgStore, a.nwgOp)
	a.pingCheckFacade.SetNativeWGLatencyProbe(func(ctx context.Context, tunnelID string) (int, string) {
		res, err := a.testService.CheckConnectivity(ctx, tunnelID)
		return pingcheck.LatencyFromConnectivity(res, err)
	})
	// Escalation path for NativeWG monitoring: when NDMS restarts of the
	// interface do not help, restart the tunnel the same way the user would.
	a.pingCheckFacade.SetTunnelRestarter(func(ctx context.Context, tunnelID string) error {
		return a.tunnelService.Restart(ctx, tunnelID)
	})

	// monitoringService is constructed below after systemTunnelSvc is wired,
	// so the matrix can include Keenetic-native tunnels.

	// Auth components. Session TTL is read live from settings on every
	// expiry check, so sessionTtlHours changes apply immediately to
	// existing sessions.
	a.keeneticClient = auth.NewKeeneticClient()
	a.sessionStore = auth.NewSessionStore(a.settingsStore.GetSessionTTL)
	a.sessionStore.SetLogger(logging.NewScopedLogger(a.loggingService, logging.GroupSystem, logging.SubAuth))
	a.deferOnExit(a.sessionStore.Stop)

	a.operator.SetAppLogger(a.loggingService)

	// Traffic history (in-memory, 48h)
	a.trafficHistory = traffic.New()
	a.deferOnExit(a.trafficHistory.Stop)

	// Updater service is constructed in setupSingbox (needs a.singboxOp for
	// the sing-box auto-install path, which is only wired at that point).

	// Managed WireGuard server service — constructed after ndmsCommands is built.

	// Terminal manager (ttyd lifecycle)
	a.terminalManager = terminal.New(a.loggingService)

	// Autostart ttyd if already installed — silent, non-blocking.
	if a.terminalManager.IsInstalled(context.Background()) {
		if port, err := a.terminalManager.Start(context.Background()); err != nil {
			a.bootLog.Warn("ttyd-autostart", "", err.Error())
		} else {
			a.bootLog.Info("ttyd-autostart", "", fmt.Sprintf("started on port %d", port))
		}
	}

	// Client route service (per-device VPN routing)
	clientRouteStore := storage.NewClientRouteStore(a.dataDir)
	a.clientRouteService = clientroute.New(
		clientRouteStore,
		a.operator,
		a.catalog,
		a.loggingService,
	)
}
