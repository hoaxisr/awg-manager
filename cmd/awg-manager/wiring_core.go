package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hoaxisr/awg-manager/internal/diagnostics"
	"github.com/hoaxisr/awg-manager/internal/logging"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	ndmstransport "github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/env"
	"github.com/hoaxisr/awg-manager/internal/sys/kmod"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
)

// setupCore prepares the data dir, settings, GC limits and app logging.
func (a *app) setupCore() {
	var err error
	if err := os.MkdirAll(a.dataDir, 0755); err != nil {
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

	a.uptime = getUptime()

	// Settings (load first to get server config)
	a.settingsStore = storage.NewSettingsStore(a.dataDir)
	a.settings, err = a.settingsStore.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load settings: %v\n", err)
		os.Exit(1)
	}
	applyGoMemoryLimits(a.settings.DisableMemorySaving)

	a.awgStore = storage.NewAWGTunnelStore(
		filepath.Join(a.dataDir, "tunnels"),
	)

	// Хранилище прокси-инстансов строится здесь, а не в wireProxyrt: его
	// читают фазы, идущие раньше рантайма (подбор listen-порта, импорт
	// связанного туннеля), а владелец файла обязан быть один.
	a.proxyStore = instancestore.New(a.dataDir)

	// Logging service (created early — injected into tunnel service, pingcheck, dnsroute, operator, state, firewall, nwg)
	a.loggingService = logging.NewService(a.settingsStore)
	// События восстановления хранилищ (карантин битых файлов, откат к .bak):
	// живой sink — большинство хранилищ грузятся лениво позже этой точки,
	// разовый дрейн терял бы их события. Заодно выгружается накопленное
	// до связывания (настройки грузятся раньше журнала).
	storage.SetNoticeSink(func(n storage.Notice) {
		a.loggingService.AppLog(logging.LevelWarn, logging.GroupSystem, logging.SubStorage, n.Action, n.Target, n.Message)
	})
	a.deferOnExit(a.loggingService.Stop)

	// bootLog: UI-visible scoped logger for all bootstrap diagnostics. Replaces
	// the legacy no-op *logger.Logger that used to silently drop everything.
	a.bootLog = logging.NewScopedLogger(a.loggingService, logging.GroupSystem, logging.SubBoot)

}

// setupNDMS builds the NDMS CQRS transport/queries, initializes ndmsinfo,
// loads the kernel module and prewarms NDMS list caches.
func (a *app) setupNDMS() {
	// === NEW NDMS LAYER (CQRS: query.Queries + command.Commands) ===
	// Transport + Queries are constructed early so downstream consumers
	// (state.Manager, routing.Catalog, etc.) can depend on them. Commands
	// + SaveCoordinator are constructed later (they depend on eventBus +
	// orchestrator).
	ndmsSem := ndmstransport.NewSemaphore(env.IntDefault("AWG_NDMS_CAP", 30))
	a.ndmsTransportClient = ndmstransport.New(ndmsSem)
	a.ndmsTransportClient.SetAppLogger(a.loggingService)
	a.deferOnExit(a.ndmsTransportClient.Close) // graceful batcher shutdown — финальный flush pending'а

	a.ndmsQueries = ndmsquery.NewQueries(ndmsquery.Deps{
		Getter: a.ndmsTransportClient,
		Logger: queryLogger(a.loggingService),
		IsOS5:  osdetect.Is5,
	})

	// Initialize SystemInfoStore at boot — one-shot fetch of /show/version.
	// Compute timeout based on system uptime (wait longer at early boot).
	ndmsTimeout := time.Second // normal restart: single attempt
	if a.uptime > 0 && a.uptime < 120 {
		ndmsTimeout = 30 * time.Second // boot: wait for NDMS
	}
	// Wire ndmsinfo to the SystemInfoStore, then initialize with retry.
	// MUST run before kmod.New(): the kmod loader reads model/SoC from
	// ndmsinfo.Get() at construction time.
	if err := ndmsinfo.Init(context.Background(), a.ndmsQueries.SystemInfo, ndmsTimeout); err != nil {
		a.bootLog.Warn("ndms-version", "", err.Error())
	}

	// Load kernel module if available (before backend detection).
	// kmod.New() reads model/SoC from ndmsinfo, so it must run after Init above.
	a.kmodLoader = kmod.New()
	a.kmodLoader.Warn = func(msg string) { a.bootLog.Warn("kmod", "", msg) }

	// Clean up old SoC-based module directories from previous IPK versions
	a.kmodLoader.CleanupLegacyModules()
	// EnsureModule: select bundled .ko if available → insmod
	if err := a.kmodLoader.EnsureModule(context.Background()); err != nil {
		a.bootLog.Warn("kmod", "", "kernel module not available: "+err.Error())
	}

	// Warm NDMS list caches before accepting clients so the first SSE snapshot
	// is not empty while the caches populate lazily. Failures are non-fatal:
	// the corresponding sections will appear in RoutingSnapshot.Missing and
	// the UI will prompt the user to retry.
	{
		warmCtx, warmCancel := context.WithTimeout(context.Background(), 15*time.Second)
		if _, err := a.ndmsQueries.Policies.List(warmCtx); err != nil {
			a.bootLog.Warn("ndms-prewarm", "policies", err.Error())
		}
		if _, err := a.ndmsQueries.Hotspot.List(warmCtx); err != nil {
			a.bootLog.Warn("ndms-prewarm", "hotspot", err.Error())
		}
		if _, err := a.ndmsQueries.Interfaces.List(warmCtx); err != nil {
			a.bootLog.Warn("ndms-prewarm", "interfaces", err.Error())
		}
		if _, err := a.ndmsQueries.RunningConfig.Lines(warmCtx); err != nil {
			a.bootLog.Warn("ndms-prewarm", "running-config", err.Error())
		}
		warmCancel()
	}

}
