package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/hoaxisr/awg-manager/internal/accesspolicy"
	"github.com/hoaxisr/awg-manager/internal/cleanup"
	"github.com/hoaxisr/awg-manager/internal/clientroute"
	"github.com/hoaxisr/awg-manager/internal/dnsroute"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/managed"
	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	ndmstransport "github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/env"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/backend"
	"github.com/hoaxisr/awg-manager/internal/tunnel/firewall"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ops"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
	"github.com/hoaxisr/awg-manager/internal/tunnel/state"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wg"
)

// configSaver adapts *ndmscommand.SaveCoordinator to the cleanup.ConfigSaver
// interface (Save(ctx) error). Flush forces the debounced NDMS save to run
// synchronously, matching the contract expected by the cleanup service.
type configSaver struct {
	sc *ndmscommand.SaveCoordinator
}

func (c configSaver) Save(ctx context.Context) error {
	return c.sc.Flush(ctx)
}

// runCleanup removes all awg-manager resources and config files.
// Called during package uninstall (opkg remove).
func runCleanup(dataDir string) {
	fmt.Println("awg-manager cleanup: removing all managed resources...")

	settingsStore := storage.NewSettingsStore(dataDir)
	// Настройки читаются ради SingboxManuallyStopped (см. ниже). Отказ не
	// останавливает снос — но и не глотается: на битом settings.json cleanup
	// молча уходил на дефолты, и понять это было нельзя (F37). Журнала здесь
	// ещё нет — он строится строкой ниже от этого же стора.
	cleanupSettings, err := settingsStore.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cleanup: settings load failed, using defaults: %v\n", err)
	}

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
	backendImpl := backend.NewKernel()
	stateMgr := state.New(cleanupNDMSQueries.Interfaces, wgClient, backendImpl, nil)
	firewallMgr := firewall.New(true /* mssClamp */, osdetect.Is5(), nil)

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

	// Cleanup собирает то же sing-box-ядро тем же конструктором, что демон
	// (buildSingboxCore); сам cleanup зовёт только singboxOp.Cleanup.
	core := buildSingboxCore(singboxCoreDeps{
		queries:                cleanupNDMSQueries,
		commands:               cleanupNDMSCommands,
		settings:               settingsStore,
		appLog:                 loggingService,
		bus:                    cleanupEventBus,
		bootLog:                bootLog,
		dataDir:                dataDir,
		initialManuallyStopped: cleanupSettings != nil && cleanupSettings.SingboxManuallyStopped,
	})
	singboxOp := core.op

	accessPolicySvc := accesspolicy.New(cleanupNDMSCommands.Policies, cleanupNDMSCommands.Interfaces, cleanupNDMSQueries, settingsStore, nil, ndmsquery.NewPolicyMarkStore(cleanupNDMSTransport, nil))

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Single cleanup call — all business logic in CleanupService
	cleanupSvc := cleanup.New(tunnelService, awgStore, dnsSvc, managedSvc, accessPolicySvc, clientRouteSvc, singboxOp, configSaver{sc: cleanupNDMSSave})
	if err := cleanupSvc.CleanupAll(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Cleanup error: %v\n", err)
	}

	// Интерфейс policy-tun живёт в NDMS и переживает удаление файлов: снимаем
	// его отдельно, вместе с записанным NAT сегментов и NDMS-дефолтом.
	//
	// СВОЙ бюджет, а не остаток общего: при большом числе туннелей CleanupAll
	// съедает 60 секунд целиком, и снятие не успело бы даже начаться — а
	// повторить его некому, демона после удаления пакета уже нет.
	ptCtx, ptCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer ptCancel()
	if err := router.ReleasePolicyTunForRemoval(ptCtx, router.Deps{
		AppLog:       loggingService,
		Settings:     settingsStore,
		OpkgTun:      cleanupNDMSCommands.Interfaces,
		DefaultRoute: cleanupNDMSCommands.Routes,
		SegmentNAT:   cleanupNDMSCommands.NAT,
		NATState:     &routerNATStateAdapter{nat: cleanupNDMSQueries.NAT, static: cleanupNDMSQueries.StaticNAT},
		// Скан по описанию: без него снятие шло бы по индексу вслепую и на
		// удалении пакета разобрало бы ЧУЖОЙ OpkgTun, занявший наш номер.
		OpkgTunScan: opkgTunScanner(cleanupNDMSQueries.Interfaces),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "policy-tun cleanup error: %v\n", err)
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
