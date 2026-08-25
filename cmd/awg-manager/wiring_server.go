package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hoaxisr/awg-manager/frontend"
	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/backup"
	"github.com/hoaxisr/awg-manager/internal/deviceproxy"
	"github.com/hoaxisr/awg-manager/internal/diagnostics"
	"github.com/hoaxisr/awg-manager/internal/dnscheck"
	"github.com/hoaxisr/awg-manager/internal/downloader"
	"github.com/hoaxisr/awg-manager/internal/freeturn"
	"github.com/hoaxisr/awg-manager/internal/hydraroute"
	"github.com/hoaxisr/awg-manager/internal/listenfirewall"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/monitoring"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/server"
	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/singbox/awgoutbounds"
	singboxcfg "github.com/hoaxisr/awg-manager/internal/singbox/configmerge"
	"github.com/hoaxisr/awg-manager/internal/singbox/dnsrewrite"
	"github.com/hoaxisr/awg-manager/internal/singbox/installer"
	singboxorch "github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/singbox/router/bypassset"
	"github.com/hoaxisr/awg-manager/internal/singbox/subscription"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
	"github.com/hoaxisr/awg-manager/internal/wdtt"
)

// setupServer registers routing snapshot providers and constructs the HTTP
// server with its dependency set.
func (a *app) setupServer() {
	// Register routing snapshot providers with catalog. Each returns (data, err);
	// errors cause the section to appear in RoutingSnapshot.Missing so the UI can
	// show a "not loaded" state and offer a refresh action.
	a.catalog.SetSnapshotProvider("dnsRoutes", func(ctx context.Context) (interface{}, error) {
		return a.dnsRouteService.List(ctx)
	})
	a.catalog.SetSnapshotProvider("staticRoutes", func(ctx context.Context) (interface{}, error) {
		return a.staticRouteService.List()
	})
	a.catalog.SetSnapshotProvider("accessPolicies", func(ctx context.Context) (interface{}, error) {
		return a.accessPolicySvc.List(ctx)
	})
	a.catalog.SetSnapshotProvider("policyDevices", func(ctx context.Context) (interface{}, error) {
		return a.accessPolicySvc.ListDevices(ctx)
	})
	a.catalog.SetSnapshotProvider("policyInterfaces", func(ctx context.Context) (interface{}, error) {
		return a.accessPolicySvc.ListGlobalInterfaces(ctx)
	})
	a.catalog.SetSnapshotProvider("clientRoutes", func(ctx context.Context) (interface{}, error) {
		return a.clientRouteService.List()
	})
	a.catalog.SetSnapshotProvider("hydrarouteStatus", func(ctx context.Context) (interface{}, error) {
		return a.hydraService.GetStatus(), nil
	})

	var slowHTTPThreshold time.Duration
	if a.slowReqMS > 0 {
		slowHTTPThreshold = time.Duration(a.slowReqMS) * time.Millisecond
	}
	frontendFS, err := frontend.FS()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load embedded frontend: %v\n", err)
		os.Exit(1)
	}

	a.srv = server.New(
		server.Config{
			Version:              version,
			FrontendFS:           frontendFS,
			PprofStandaloneAddr:  strings.TrimSpace(a.pprofListen),
			PprofOnMain:          a.pprofOnMain,
			SlowRequestThreshold: slowHTTPThreshold,
		},
		server.Deps{
			TunnelService:       a.tunnelService,
			OpkgTunOccupancy:    a.opkgTunOccupancy,
			ExternalService:     a.externalService,
			TestingService:      a.testService,
			Keenetic:            a.keeneticClient,
			Sessions:            a.sessionStore,
			Settings:            a.settingsStore,
			Tunnels:             a.awgStore,
			PingCheckService:    a.pingCheckFacade,
			FreeTurnService:     a.freeturnService,
			WdttService:         a.wdttService,
			LoggingService:      a.loggingService,
			KmodLoader:          a.kmodLoader,
			UpdaterService:      a.updaterService,
			NdmsQueries:         a.ndmsQueries,
			NdmsCommands:        a.ndmsCommands,
			TrafficHistory:      a.trafficHistory,
			DnsRouteService:     a.dnsRouteService,
			StaticRouteService:  a.staticRouteService,
			SystemTunnelService: a.systemTunnelSvc,
			ManagedService:      a.managedService,
			ManagedServiceImpl:  a.managedService,
			NwgOp:               a.nwgOp,
			TerminalManager:     a.terminalManager,
			AccessPolicySvc:     a.accessPolicySvc,
			ClientRouteSvc:      a.clientRouteService,
			Catalog:             a.catalog,
			Orch:                a.orch,
			Bus:                 a.eventBus,
			HydraService:        a.hydraService,
			SingboxHandler:      a.singboxHandler,
			SingboxOrch:         a.sbOrch,
			ClashProxy:          a.clashProxy,
			SingboxConnsHandler: a.singboxConnsHandler,
			MonitoringService:   a.monitoringService,
			SingboxSubMembers: func() []diagnostics.SingboxSubMember {
				subs := a.subSvc.List()
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
				return singboxcfg.MergeDir(a.sbOrch.ConfigDir())
			},
		},
	)

	a.srv.SetSingboxOperator(a.singboxOp)
	a.singboxOp.SetEventBus(a.eventBus)

}

// setupDeviceProxy wires awg-outbounds, the device-proxy service and the
// shared download service (+ geo/dns refresh schedulers, installer
// auto-migration).
func (a *app) setupDeviceProxy() {
	// systemTunnelDPAdapter bridges Keenetic NativeWG tunnels (from NDMS)
	// into both awgoutbounds (canonical tag writer) and deviceproxy
	// (SystemTunnelQuery — kept for its List interface in adapters).
	systemTunnelDPAdapter := deviceproxy.NewSystemTunnelAdapter(a.systemTunnelSvc)

	// awgoutbounds — canonical writer of AWG-direct outbounds in
	// config.d/15-awg.json. Sources managed AWG tunnels from storage
	// and system (NativeWG) tunnels via the deviceproxy adapter.
	// Must be constructed before deviceProxySvc so we can pass it as
	// AWGOutbounds dep (deviceproxy now queries tags instead of enumerating).
	a.awgoutboundsSvc = awgoutbounds.NewService(awgoutbounds.Deps{
		AWGTunnels:     newAWGStoreAdapter(a.awgStore),
		SystemTunnels:  newSystemTunnelStoreAdapter(systemTunnelDPAdapter),
		ManagedServers: newSettingsManagedServersAdapter(a.settingsStore),
		Singbox:        newAwgoutboundsSingboxAdapter(a.singboxOp),
		AppLog:         logging.NewScopedLogger(a.loggingService, logging.GroupRouting, logging.SubAWGOutbounds),
		Bus:            a.eventBus,
		Orch:           a.sbOrch,
	})
	awgoutboundsUnsub := a.awgoutboundsSvc.SubscribeBus(context.Background())
	a.deferOnExit(awgoutboundsUnsub)
	// Boot reconcile — populates 15-awg.json before sing-box starts so
	// the merged config.d is consistent on first read. Reload-free.
	if err := a.awgoutboundsSvc.Reconcile(context.Background()); err != nil {
		a.bootLog.Warn("awgoutbounds-reconcile", "", err.Error())
	}

	// Device-proxy service — LAN-facing SOCKS/HTTP proxy managed through
	// sing-box. See docs/superpowers/specs/2026-04-24-device-proxy-design.md.
	deviceProxyStore := deviceproxy.NewStore(filepath.Join(a.dataDir, "deviceproxy.json"))
	deviceProxySingboxAdapter := deviceproxy.NewSingboxAdapter(a.singboxOp)
	deviceProxySingboxAdapter.SetOrch(a.sbOrch)

	subOutboundsAdapter := &deviceproxySubscriptionOutboundsAdapter{src: a.subSvc}

	a.deviceProxySvc = deviceproxy.NewService(deviceproxy.Deps{
		Store:                 deviceProxyStore,
		Singbox:               deviceProxySingboxAdapter,
		SubscriptionOutbounds: subOutboundsAdapter,
		NDMSQuery:             deviceproxy.NewNDMSAdapter(a.ndmsQueries),
		Bus:                   a.eventBus,
		AWGOutbounds:          &deviceproxyAWGOutboundsAdapter{src: a.awgoutboundsSvc},
		AppLogger:             a.loggingService,
	})
	// Reflect deviceproxy storage state into the orchestrator slot so
	// the saved Enabled flag matches the on-disk active/disabled
	// location of 30-deviceproxy.json from boot.
	_ = a.sbOrch.SetEnabled(singboxorch.SlotDeviceProxy, deviceProxyStore.Get().Enabled)
	a.deviceProxySvc.SetTunnelInboundPorts(func() []int {
		cfg, err := a.singboxOp.LoadCurrentConfig()
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
	// NB: и подписка на шину (SubscribeBus), и начальный boot-Reconcile
	// device-proxy выполняются НИЖЕ, после SetRouterOutbounds — Reconcile
	// выключает инстансы с отсутствующими в каталоге outbound'ами, и без
	// каталога роутера он на каждой загрузке стирал бы выбор router-композита
	// (vpn/vpn2) у инстансов (issue #465). Ранняя подписка оставляла бы окно:
	// событие tunnels/subscriptions в нём триггерило бы Reconcile с nil-каталогом
	// роутера → та же потеря композитов.
	sharedDownloadSvc := downloader.NewSettingsBackedService(
		a.deviceProxySvc,
		a.singboxOp,
		a.subSvc,
		a.settingsStore,
		a.awgStore,
	)
	a.geoRefreshScheduler = hydraroute.NewGeoRefreshScheduler(
		a.hydraService, a.settingsStore, a.loggingService,
		func(ctx context.Context) (*http.Client, string, func(), error) {
			lease, err := sharedDownloadSvc.ResolveClient(ctx, nil)
			if err != nil {
				return nil, "", nil, err
			}
			return lease.Client, lease.Route.DisplayName(), lease.Close, nil
		},
	)
	a.dnsRouteService.SetDownloader(&dnsRouteDownloaderAdapter{svc: sharedDownloadSvc})
	a.dnsRefreshScheduler.Start()
	a.geoRefreshScheduler.Start()
	a.updaterService.SetDownloader(sharedDownloadSvc)
	// FreeTurn one-click install: закреплённые в билде спеки по арху +
	// общий загрузчик. Нет спеков для арха → кнопка недоступна, панель
	// оставляет подсказку о ручной установке.
	a.freeturnService.SetLogger(a.loggingService)
	if specs, ok := freeturn.EmbeddedBinaries[detectArch()]; ok {
		a.freeturnService.SetInstallSpecs(specs)
		a.freeturnService.SetDownloader(&freeturnDownloaderAdapter{svc: sharedDownloadSvc})
	}
	a.wdttService.SetLogger(a.loggingService)
	if a.managedService != nil {
		a.wdttService.SetAccessManager(&wdttAccessAdapter{
			svc:    a.managedService,
			ifaces: a.ndmsCommands.Interfaces,
		})
	}
	a.wdttService.SetInterfaceChecker(wdtt.NewSysNetChecker())
	if specs, ok := wdtt.EmbeddedBinaries[detectArch()]; ok {
		a.wdttService.SetInstallSpecs(specs)
		a.wdttService.SetDownloader(&wdttDownloaderAdapter{svc: sharedDownloadSvc})
	}
	// Автостарт FreeTurn/WDTT — в boot.go (cold-boot/post-restore/daemon-restart)
	// и по WAN UP hook; не здесь: ранний старт ловит DNS до sing-box.
	a.srv.SetProxyClientAutostart(a.resumeEnabledProxyClients)
	if a.singboxInstaller != nil {
		a.singboxInstaller.SetDownloader(&installerDownloaderAdapter{svc: sharedDownloadSvc})
		// Auto-migration goroutine: when legacy sing-box-naive opkg
		// package is present but managed binary is missing, run the
		// jump from opkg → managed in the background. Failures keep
		// awg-manager on the legacy install — retry happens on next boot.
		go func() {
			ctx := context.Background()
			if a.singboxInstaller.CurrentVersion(ctx) != "" {
				return // managed binary already in place
			}
			if !a.singboxInstaller.IsLegacyOpkgInstalled(ctx) {
				return // nothing to migrate
			}
			// Skip migration if there is not enough disk space — GetStatus
			// will surface InstallStateMissingNoSpace automatically; no
			// point burning bandwidth on a download that will fail.
			if a.singboxInstaller.EvaluateInstallState() == installer.InstallStateMissingNoSpace {
				a.bootLog.Warn("singbox-auto-migration", "", "skipped: not enough disk space")
				return
			}
			lc := &operatorLifecycle{op: a.singboxOp}
			if err := a.singboxInstaller.Migrate(ctx, lc); err != nil {
				a.bootLog.Warn("singbox-auto-migration", "", "deferred: "+err.Error())
			}
		}()
	}

	a.srv.SetPresetCatalog(a.presetCatalog)
	a.srv.SetDeviceProxyService(a.deviceProxySvc)
	a.srv.SetDownloadService(sharedDownloadSvc)
	// Note: legacy awg-* outbound cleanup happens lazily on first
	// deviceproxy CRUD via pruneAWGOutbounds(nil) inside EnsureDeviceProxy.
	// We deliberately do NOT call ForceApply on boot because it triggers
	// ApplyConfig → startAndWait, which spuriously starts sing-box even
	// when both the deviceproxy and the router engine are disabled —
	// once started, only an explicit Stop call (no UI today) brings it
	// back down. Reconcile already handles cleanup on the next legitimate
	// trigger; the migration tax is at most one stale file fragment that
	// gets stripped on the next Save/Enable.
	a.srv.SetNDMSDispatcher(a.ndmsDispatcher)
	a.srv.SetNDMSTransport(a.ndmsTransportClient)
	a.srv.SetNDMSSaveCoordinator(a.ndmsSaveCoord)
	a.srv.SetMetricsPoller(a.ndmsMetricsPoller)

}

// setupRouter builds the sing-box router service with its adapters,
// the geoip bypass set, subscription scheduler/handler and the remaining
// sing-box HTTP handlers.
func (a *app) setupRouter() {
	// Порты инбаундов sb-router лежат внутри эфемерного диапазона ядра
	// (49000-61001 на Keenetic), поэтому исходящее соединение может занять
	// их в окно рестарта sing-box и уронить старт с EADDRINUSE (#762).
	if err := router.ReserveListenPorts(); err != nil {
		logging.NewScopedLogger(a.loggingService, logging.GroupRouting, logging.SubSingboxRouter).
			Warn("reserve-ports", "", "зарезервировать порты инбаундов: "+err.Error())
	}
	bindableAdapter := &routerWANInterfaceAdapter{store: a.ndmsQueries.Interfaces, nativeProxies: a.singboxOp.ListNativeProxies}
	routerSvc := router.NewService(router.Deps{
		AppLog:                 a.loggingService,
		Settings:               a.settingsStore,
		Singbox:                a.singboxOp,
		Policies:               &routerAccessPolicyAdapter{svc: a.accessPolicySvc, wan: a.wanModel},
		Events:                 a.eventBus,
		Bus:                    a.eventBus,
		AWGTags:                &routerAWGTagAdapter{src: a.awgoutboundsSvc, awg3: a.awg3Svc},
		AWGOutboundsRefresh:    a.awgoutboundsSvc.Reconcile,
		SingboxTunnels:         &routerSingboxTunnelAdapter{src: a.singboxOp},
		SubscriptionComposites: router.NewSubscriptionCompositesAdapter(a.subAdapter),
		Orch:                   a.sbOrch,
		WANInterfaces:          &routerWANInterfaceAdapter{store: a.ndmsQueries.Interfaces},
		BindableInterfaces:     bindableAdapter,
		IngressResolver:        &routerIngressResolverAdapter{store: a.ndmsQueries.Interfaces},
		PresetCatalog:          a.presetCatalog,
		GeoData:                a.geoDataStore,
		GeoTagCounts:           a.geoDataStore,
		OpkgTun:                a.ndmsCommands.Interfaces, // *InterfaceCommands satisfies OpkgTunProvisioner directly
		StaticRoutes:           &routerStaticRouteAdapter{routes: a.ndmsCommands.Routes},
		OpkgTunIndices:         &routerOpkgTunIndexAdapter{store: a.ndmsQueries.Interfaces},
		// Пины ЧУЖИХ владельцев: записи туннелей и записи NDMS без живого
		// устройства. Своя удерживающая запись сюда не входит — она приходит
		// из настроек, и подмешивание её в занятость перепинило бы режим
		// роутера сам на себя.
		OpkgTunPins:   storage.MergeOpkgTunPins(a.awgStore.OpkgTunPinsOf, a.opkgNDMSPins),
		OpkgTunScan:   opkgTunScanner(a.ndmsQueries.Interfaces),
		DefaultRoute:  a.ndmsCommands.Routes, // *RouteCommands satisfies DefaultRouteProvider directly
		SegmentNAT:    a.ndmsCommands.NAT,    // *NATCommands satisfies SegmentNATProvider directly
		Segments:      &routerSegmentDetailsAdapter{store: a.ndmsQueries.Interfaces},
		RunningConfig: a.ndmsQueries.RunningConfig,
		NATState:      &routerNATStateAdapter{nat: a.ndmsQueries.NAT, static: a.ndmsQueries.StaticNAT},
		// *RouteStore satisfies DefaultGatewayResolver directly.
		DefaultGateway: a.ndmsQueries.Routes,
		FakeIPTun: func() router.FakeIPTunParams {
			p := router.DefaultFakeIPTunParams()
			p.CachePath = singbox.DefaultCacheDBPath()
			return p
		}(),
		// Синхронный мост «роутер → device-proxy»: после перепарковки слотов
		// маршрутизации (Enable/Disable/смена режима) слот 30 перегенерируется
		// ДО ближайшего reload — селекторы device-proxy деградируют ссылки на
		// недоступные композиты до их default-членов (и восстанавливают их при
		// включении), вместо того чтобы prune оркестратора молча вырезал
		// vpn/vpn2 и sing-box увёл трафик в произвольный член (issue #465).
		OnRoutingSlotsChanged: func() {
			if err := a.deviceProxySvc.ApplyInstances(context.Background()); err != nil {
				logging.NewScopedLogger(a.loggingService, logging.GroupRouting, logging.SubDeviceProxy).
					Warn("router-slots-changed", "", "re-apply device-proxy instances: "+err.Error())
			}
		},
	})
	a.routerSvc = routerSvc
	a.subSvc.SetBindInterfaceValidator(subscriptionBindValidator{adapter: bindableAdapter})
	// Health-check бинаря ipset пишет вердикты в журнал (битый Entware-бинарь
	// вида «libc.so: cannot open shared object file» иначе виден только как
	// молчаливые exit 127 на каждой команде). До подключения логгер nil-safe.
	bypassset.SetHealthLogger(logging.NewScopedLogger(a.loggingService, logging.GroupRouting, logging.SubBypassSet))
	// Содержимое bypass-набора выведено из .dat-файлов: их обновление/удаление
	// обязано пересобирать набор (сам триггер — no-op вне tproxy и без тегов).
	a.geoDataStore.SetOnChange(routerSvc.TriggerBypassSetPopulate)
	a.srv.SetBypassSetHandler(api.NewBypassSetHandler(routerSvc, a.loggingService))

	// Exclude interfaces already bound by an existing direct outbound from the
	// bindable picker (#323). Wired post-construction — needs routerSvc.
	bindableAdapter.occupiedBinds = func(ctx context.Context) (map[string]bool, error) {
		obs, err := routerSvc.ListCompositeOutbounds(ctx)
		if err != nil {
			return nil, err
		}
		set := make(map[string]bool)
		for _, o := range obs {
			if o.Type == "direct" && o.BindInterface != "" {
				set[o.BindInterface] = true
			}
		}
		return set, nil
	}
	a.singboxOp.SetOutboundReferenceRenamer(routerSvc)
	a.tunnelService.SetAWGSyncer(a.awgoutboundsSvc)
	a.tunnelService.SetDeviceProxyRefChecker(a.deviceProxySvc)
	a.tunnelService.SetRouterRefChecker(routerSvc)
	a.singboxHandler.SetOutboundRefCheckers(a.deviceProxySvc, routerSvc)
	a.singboxHandler.SetBindValidator(subscriptionBindValidator{adapter: bindableAdapter}.ValidateBindInterface)
	a.deviceProxySvc.SetRouterOutbounds(&deviceproxyRouterOutboundsAdapter{src: routerSvc})
	// Initial reconcile on boot — idempotent, brings config.json in sync
	// with storage + current tunnel set. Runs strictly AFTER
	// SetRouterOutbounds (см. комментарий у SubscribeBus выше): каталог
	// роутера должен быть виден, иначе Reconcile считает router-композиты
	// удалёнными и выключает ссылающиеся на них инстансы.
	if err := a.deviceProxySvc.Reconcile(context.Background()); err != nil {
		a.bootLog.Warn("deviceproxy-reconcile", "", err.Error())
	}
	// Подписка на шину — строго ПОСЛЕ SetRouterOutbounds + boot-Reconcile:
	// ранняя подписка запускала бы Reconcile по событию ещё без каталога
	// роутера (см. NB выше). События, опубликованные до этой строки, терять
	// безопасно: это инвалидации состояния (не рёбра), потребители перечитывают
	// его целиком, а boot-Reconcile строкой выше уже учёл текущее состояние.
	deviceProxyUnsub := a.deviceProxySvc.SubscribeBus(context.Background())
	a.deferOnExit(deviceProxyUnsub)
	routerStartupLog := logging.NewScopedLogger(a.loggingService, logging.GroupRouting, logging.SubSingboxRouter)
	go func() {
		// Startup-only: reap rule-set artifacts (rule-sets/inline, rule-sets/dat)
		// orphaned by deletes/renames on older AWGM versions that never cleaned
		// them up (issue #448). Steady-state GC runs after each ApplyStaging.
		routerSvc.GCRuleSetArtifacts()
		// Reconcile сам реапает fakeip-сирот (ReapOrphanedFakeIPTun) на каждом
		// тике, включая этот стартовый — отдельный вызов не нужен.
		if err := routerSvc.Reconcile(context.Background()); err != nil {
			routerStartupLog.Error("reconcile", "startup", err.Error())
		}
	}()
	a.routerScheduler = router.NewScheduler(routerSvc, a.settingsStore)
	a.routerScheduler.Start()

	// Late-bind sing-box / router / Clash deps into the monitoring scheduler.
	// monitoringService is constructed early (line ~421) so the matrix can
	// include Keenetic-native tunnels; singboxOp + routerSvc + clashProxy
	// are constructed later in the bootstrap, hence the deferred wiring.
	a.monitoringService.SetSingboxTunnels(&monitoringSingboxTunnelAdapter{op: a.singboxOp, sub: a.subSvc})
	a.monitoringService.SetComposites(&monitoringCompositesAdapter{svc: routerSvc})
	a.monitoringService.SetClashState(monitoring.NewClashState(a.clashProxy.ClashBaseURL, nil))
	a.monitoringService.SetSingboxDelay(a.singboxOp.Clash())

	singboxRouterHandler := api.NewSingboxRouterHandler(routerSvc, a.loggingService)
	singboxRouterHandler.SetOutboundRefCheckers(a.deviceProxySvc, routerSvc)
	a.srv.SetSingboxRouterHandler(singboxRouterHandler)
	// Атрибуция tproxy-потоков на странице соединений: connmark == PolicyMark
	// активной sb-router-политики. Кэшируется внутри connections.Service (60s).
	a.srv.SetConnectionsMarkProvider(func(ctx context.Context) (string, bool) {
		st, err := routerSvc.GetStatus(ctx)
		if err != nil || !st.Active || st.PolicyMark == "" {
			return "", false
		}
		return st.PolicyMark, true
	})
	a.srv.SetSingboxFakeIPConfigHandler(api.NewSingboxFakeIPConfigHandler(routerSvc, a.loggingService))
	mergedAWGOutbounds := &awg3MergedAWGOutbounds{inner: a.awgoutboundsSvc, awg3: a.awg3Svc}
	a.srv.SetAWGOutboundsHandler(api.NewAWGOutboundsHandler(mergedAWGOutbounds))
	// AWG3 endpoint import/CRUD. Wired here (not setupSingbox) because the
	// rename-conflict check needs routerSvc's ListRules, only built in this phase.
	awg3Handler := api.NewAwg3Handler(a.awg3Store, a.awg3Svc, routerSvc, a.loggingService)
	// Same merged catalog the rule-editor dropdown uses — lets import/rename
	// reject a tag already taken by any outbound early with a clear message.
	awg3Handler.SetOutboundTagLister(mergedAWGOutbounds)
	a.srv.SetAwg3Handler(awg3Handler)
	a.srv.SetSingboxConfigHandler(api.NewSingboxConfigHandler(a.sbOrch.ConfigDir))
	// Эксперт-редактор конфигурации: обзор слотов config.d + draft-пайплайн
	// пользовательского слота 90-user.json (единственный слот без продюсера).
	a.srv.SetSingboxConfigEditorHandler(api.NewSingboxConfigEditorHandler(a.sbOrch, a.loggingService))
	// Зеркало inbound'ов merged-конфига: per-slot чтение config.d с атрибуцией
	// источника (подписка/группа/туннель/device-proxy/QoS/движок). Резолверы
	// nil-safe — при частичном bootstrap источник деградирует до слота.
	a.srv.SetSingboxInboundsHandler(api.NewSingboxInboundsHandler(api.SingboxInboundsDeps{
		ConfigDir: a.sbOrch.ConfigDir,
		Subscriptions: func() []subscription.Subscription {
			if a.subStore == nil {
				return nil
			}
			return a.subStore.List()
		},
		Groups: func() []subscription.AggregateGroup {
			if a.subGroupStore == nil {
				return nil
			}
			return a.subGroupStore.List()
		},
		DeviceProxyInstances: func() []deviceproxy.Instance {
			return a.deviceProxySvc.GetSnapshot().Instances
		},
		NDMSProxyEnabled: a.settingsStore.IsSingboxNDMSProxyEnabled,
	}))

	proxiesHandler := api.NewSingboxProxiesHandler(
		a.clashProxy.ClashBaseURL,
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
	a.srv.SetSingboxProxiesHandler(proxiesHandler)

	// Wire subscription handler + start refresh scheduler.
	// subSvc and subAdapter are constructed earlier (after sbOrch.Bootstrap).
	subSched := subscription.NewScheduler(a.subStore, func(ctx context.Context, id string) error {
		_, err := a.subSvc.Refresh(ctx, id)
		return err
	})
	subSched.SetAppLogger(a.loggingService)
	subSched.Start(context.Background())
	subHandler := api.NewSubscriptionHandler(a.subSvc, a.singboxOp, a.loggingService)
	subHandler.SetNDMSProxyToggler(a.settingsStore)
	subHandler.SetOutboundRefCheckers(a.deviceProxySvc, routerSvc)
	a.srv.SetSubscriptionHandler(subHandler)
	a.srv.AddShutdownHook(subSched.Stop)

	if a.wdttService != nil && a.ndmsCommands != nil {
		policyMarks := &policyTableAdapter{marks: ndmsquery.NewPolicyMarkStore(a.ndmsTransportClient, nil)}
		a.wdttService.SetNDMSInterfaceCommands(a.ndmsCommands.Interfaces)
		a.wdttService.SetOpkgTunIndexLister(&routerOpkgTunIndexAdapter{store: a.ndmsQueries.Interfaces})
		a.wdttService.SetOpkgTunExistChecker(&opkgTunExistAdapter{store: a.ndmsQueries.Interfaces})
		a.wdttService.SetOpkgTunScanner(opkgTunScanner(a.ndmsQueries.Interfaces))
		a.wdttService.SetRouterReconciler(routerSvc)
		a.wdttService.SetClientRouteHooks(a.clientRouteService)
		a.wdttService.SetNDMSPolicyRouting(
			a.ndmsCommands.Policies,
			a.ndmsQueries.Policies,
			policyMarks,
		)
		a.wdttService.SetPolicyMarkGetter(policyMarks)
		a.wdttService.SetIngressRefEnsurer(&wdttIngressEnsurer{
			settings: a.settingsStore,
			router:   routerSvc,
		})
		a.accessPolicySvc.SetOpkgPolicyRouteSyncer(a.wdttService)
	}

}

// setupListen wires DNS rewrites, selects the HTTP port, applies the
// listen spec and logs startup.
func (a *app) setupListen() {
	// DNS Rewrites — sing-box slot 17-dns-rewrites.json.
	dnsRewriteStorePath := filepath.Join(a.dataDir, "dns_rewrites.json")
	dnsRewriteStore := storage.NewDNSRewriteStore(dnsRewriteStorePath)
	dnsRewriteSvc := dnsrewrite.NewService(dnsRewriteStore, &dnsRewriteOrchAdapter{orch: a.sbOrch}, a.eventBus)
	if err := dnsRewriteSvc.Resync(); err != nil {
		a.bootLog.Warn("dnsrewrite-resync", "", err.Error())
	}
	a.srv.SetDNSRewritesHandler(api.NewDNSRewritesHandler(dnsRewriteSvc, a.loggingService))
	// keendns preset → имена KeenDNS резолвит сам роутер, а его адреса идут
	// мимо sing-box. Прежняя подмена FQDN на LAN IP ломала доступ к морде
	// роутера по имени KeenDNS (issue #729).
	if a.routerSvc != nil {
		a.routerSvc.SetKeenDNSPreset(
			keenDNSInfoAdapter{
				keendns:  a.ndmsQueries.KeenDNS,
				dnsProxy: a.ndmsQueries.DNSProxyStatus,
			},
			dnsRewriteSvc,
		)
		// Догоняющий sync: startup-Reconcile (setupRouter) стартовал раньше
		// SetKeenDNSPreset и мог увидеть nil-syncer. В горутине и с ctx —
		// синхронный вызов ходит в NDMS и до 30с держал бы a.serve().
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			a.routerSvc.SyncKeenDNSPreset(ctx)
		}()
	}

	// Boot status: 0 = booting, 1 = done. Used by /api/system/info.
	a.srv.SetBootStatusFunc(func() bool { return atomic.LoadInt32(&a.bootDone) == 0 })

	// Get port from settings, with fallback logic
	selectedPort := a.settings.Server.Port
	if selectedPort == 0 || !server.IsPortFree(selectedPort) {
		var err error
		selectedPort, err = a.srv.FindFreePort(a.settings.Server.Port)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to find free port: %v\n", err)
			os.Exit(1)
		}
	}

	// Persist actual port in settings so postinst / status / hooks show the right URL.
	if selectedPort != a.settings.Server.Port {
		fmt.Fprintf(os.Stderr, "Warning: port %d occupied, using port %d\n", a.settings.Server.Port, selectedPort)
		a.settings.Server.Port = selectedPort
		_ = a.settingsStore.Save(a.settings)
	}

	// Bind-адреса применяет мультилистенер-менеджер (server/listen.go):
	// по IPv4 на каждый интерфейс из настроек (пусто = 0.0.0.0) +
	// безусловный loopback (реверс-прокси NDMS, health-пробы, спасательный
	// люк). Интерфейс без IP на boot пропускается — heal-тикер добиндит.
	a.srv.SetListenSpec(server.ListenSpec{Port: selectedPort, Interfaces: a.settings.Server.Interfaces})

	// DNS routing diagnostics
	dnsCheckService := dnscheck.NewService(
		a.ndmsTransportClient,
		a.ndmsQueries.Hotspot,
		a.ndmsQueries.IPHost,
		a.ndmsQueries.DNSProxyConfig,
		&dnsRouteCountAdapter{store: a.dnsRouteStore},
		&runningTunnelAdapter{svc: a.tunnelService},
		a.loggingService,
	)
	dnsCheckService.EnsureIPHost(context.Background())
	a.srv.SetDnsCheckService(dnsCheckService)

	logStartup(a.bootLog, version, string(osdetect.Get()),
		fmt.Sprintf("port %d, interfaces: %s", selectedPort, describeListenInterfaces(a.settings.Server.Interfaces)), a.settings)

}

// setupShutdown creates the shutdown context and registers graceful
// shutdown hooks.
func (a *app) setupShutdown() {
	// Shutdown context — cancelled on shutdown
	a.shutdownCtx, a.shutdownCancel = context.WithCancel(context.Background())
	a.deferOnExit(a.shutdownCancel)

	// Start the monitoring scheduler now that shutdownCtx exists.
	a.monitoringService.Start(a.shutdownCtx)
	// Re-apply WDTT entware iptables NAT — sing-box router reconcile can flush rules.
	a.wdttService.StartNATReconciler(a.shutdownCtx)
	// Супервизор гейтится теми же условиями, что и автостарт прокси-клиентов:
	// boot-фазы (NDMS/WAN/DNS) и маркер post-restore. Без гейта его первый тик
	// поднимал бы клиентов раньше резолвера и вопреки восстановлению из архива.
	proxyReady := func() bool {
		return atomic.LoadInt32(&a.bootDone) == 1 && !backup.HasPostRestoreMarker(a.dataDir)
	}
	a.freeturnService.StartSupervisor(a.shutdownCtx, proxyReady)
	a.wdttService.StartSupervisor(a.shutdownCtx, proxyReady)
	// Re-apply FreeTurn/WDTT listen-port INPUT rules after iptables flushes.
	listenfirewall.StartReconciler(a.shutdownCtx, func() []listenfirewall.PortSpec {
		var out []listenfirewall.PortSpec
		out = append(out, a.freeturnService.RunningServerListenPorts()...)
		out = append(out, a.wdttService.RunningServerListenPorts()...)
		return listenfirewall.MergePortSpecs(out)
	})

	// Register shutdown hooks for graceful cleanup before syscall.Exec restart.
	a.srv.AddShutdownHook(a.shutdownCancel)
	a.srv.AddShutdownHook(func() {
		if !backup.HasPostRestoreMarker(a.dataDir) {
			return
		}
		a.freeturnService.Stop()
		a.wdttService.Stop()
		backup.KillOrphanProxyProcesses(filepath.Join(a.dataDir, "run"))
	})
	// Intentionally NOT removing kmod proxy slots on restart: the
	// reconnect path (EventReconnect → ActionRestoreKmod →
	// KmodManager.RestoreTunnel) adopts each existing slot without
	// touching /proc/awg_proxy/del, so kernel WG keeps forwarding
	// through the live slot across syscall.Exec. This is what makes
	// "Перезапуск AWGM, туннели продолжат работать" actually true on
	// proxy-firmware. Touching /proc/del here also opened a kernel-side
	// race on awg_proxy < 1.1.10 (issue #234) — slots are now left
	// to the reconnect path.
	a.srv.AddShutdownHook(a.pingCheckService.Stop)
	a.srv.AddShutdownHook(a.monitoringService.Stop)
	a.srv.AddShutdownHook(a.dnsRefreshScheduler.Stop)
	a.srv.AddShutdownHook(a.geoRefreshScheduler.Stop)
	a.srv.AddShutdownHook(a.routerScheduler.Stop)
	a.srv.AddShutdownHook(a.sessionStore.Stop)
	a.srv.AddShutdownHook(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := a.ndmsSaveCoord.Flush(ctx); err != nil {
			a.bootLog.Warn("ndms-savecoord-flush", "shutdown", err.Error())
		}
	})
	a.srv.AddShutdownHook(a.ndmsDispatcher.Stop)
	a.srv.AddShutdownHook(a.sysfsTrafficPoller.Stop)
	a.srv.AddShutdownHook(a.ndmsMetricsPoller.Stop)
	a.srv.AddShutdownHook(a.loggingService.Stop)
	a.srv.AddShutdownHook(a.trafficHistory.Stop)
	a.srv.AddShutdownHook(func() { a.terminalManager.Shutdown(context.Background()) })

}
