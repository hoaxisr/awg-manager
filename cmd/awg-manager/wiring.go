package main

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/accesspolicy"
	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/auth"
	"github.com/hoaxisr/awg-manager/internal/awg3endpoint"
	"github.com/hoaxisr/awg-manager/internal/clientroute"
	"github.com/hoaxisr/awg-manager/internal/deviceproxy"
	"github.com/hoaxisr/awg-manager/internal/dnsroute"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/freeturn"
	"github.com/hoaxisr/awg-manager/internal/hydraroute"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/managed"
	"github.com/hoaxisr/awg-manager/internal/monitoring"
	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	ndmsevents "github.com/hoaxisr/awg-manager/internal/ndms/events"
	ndmsmetrics "github.com/hoaxisr/awg-manager/internal/ndms/metrics"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	ndmstransport "github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/pingcheck"
	"github.com/hoaxisr/awg-manager/internal/presets"
	"github.com/hoaxisr/awg-manager/internal/routing"
	"github.com/hoaxisr/awg-manager/internal/server"
	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/singbox/awgoutbounds"
	"github.com/hoaxisr/awg-manager/internal/singbox/installer"
	singboxorch "github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/singbox/subscription"
	"github.com/hoaxisr/awg-manager/internal/staticroute"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/kmod"
	"github.com/hoaxisr/awg-manager/internal/terminal"
	"github.com/hoaxisr/awg-manager/internal/testing"
	"github.com/hoaxisr/awg-manager/internal/traffic"
	"github.com/hoaxisr/awg-manager/internal/tunnel/backend"
	"github.com/hoaxisr/awg-manager/internal/tunnel/external"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ops"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
	"github.com/hoaxisr/awg-manager/internal/tunnel/state"
	"github.com/hoaxisr/awg-manager/internal/tunnel/systemtunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wg"
	"github.com/hoaxisr/awg-manager/internal/updater"
	"github.com/hoaxisr/awg-manager/internal/wdtt"
)

// app is the composition root of the daemon: every subsystem constructed by
// the setup* phases lives here so later phases (and the boot sequence) can
// reach what earlier phases built. main() creates one app, runs the phases
// in order and serves; the phase methods are NOT independent — they rely on
// the construction order encoded in main().
//
// Adapters between subsystems intentionally live in package main (see
// *_adapters.go): they exist to keep internal/ packages decoupled from each
// other, so they belong to the composition root, not to either package they
// bridge.
type app struct {
	// flags
	dataDir     string
	forceBoot   bool
	pprofListen string
	pprofOnMain bool
	slowReqMS   int

	// process state
	uptime   float64
	bootDone int32 // 0 = booting, 1 = done (atomic; /api/system/info)

	shutdownCtx    context.Context
	shutdownCancel context.CancelFunc

	// storage / settings / logging
	settingsStore *storage.SettingsStore
	settings      *storage.Settings
	awgStore      *storage.AWGTunnelStore

	loggingService *logging.Service
	bootLog        *logging.ScopedLogger

	// NDMS CQRS layer
	ndmsTransportClient *ndmstransport.Client
	ndmsQueries         *ndmsquery.Queries
	ndmsSaveCoord       *ndmscommand.SaveCoordinator
	ndmsCommands        *ndmscommand.Commands
	ndmsDispatcher      *ndmsevents.Dispatcher
	ndmsMetricsPoller   *ndmsmetrics.Poller

	// tunnel core
	kmodLoader    *kmod.Loader
	wgClient      *wg.ClientImpl
	backendImpl   *backend.KernelBackend
	stateMgr      *state.ManagerImpl
	eventBus      *events.Bus
	operator      ops.Operator
	nwgOp         *nwg.OperatorNativeWG
	wanModel      *wan.Model
	tunnelService *service.ServiceImpl
	// opkgTunOccupancy — занятость номеров OpkgTun: живые интерфейсы плюс пины
	// владельцев. Собирается один раз и раздаётся всем, кто выдаёт номера.
	opkgTunOccupancy storage.OpkgTunPins
	catalog          *routing.CatalogImpl
	orch             *orchestrator.Orchestrator

	// routing / aux services
	hydraService        *hydraroute.Service
	geoDataStore        *hydraroute.GeoDataStore
	geoRefreshScheduler *hydraroute.GeoRefreshScheduler
	dnsRouteStore       *dnsroute.Store
	dnsRouteService     *dnsroute.ServiceImpl
	dnsFailover         *dnsroute.FailoverManager
	dnsRefreshScheduler *dnsroute.Scheduler
	staticRouteStore    *storage.StaticRouteStore
	staticRouteService  *staticroute.ServiceImpl
	presetCatalog       *presets.Catalog
	externalService     *external.Service
	systemTunnelSvc     *systemtunnel.ServiceImpl
	testService         *testing.Service
	pingCheckService    *pingcheck.Service
	pingCheckFacade     *pingcheck.Facade
	freeturnService     *freeturn.Service
	wdttService         *wdtt.Service
	monitoringService   *monitoring.Service
	keeneticClient      *auth.KeeneticClient
	sessionStore        *auth.SessionStore
	trafficHistory      *traffic.History
	sysfsTrafficPoller  *traffic.SysfsPoller
	updaterService      *updater.Service
	managedService      *managed.Service
	terminalManager     *terminal.ManagerImpl
	clientRouteService  *clientroute.ServiceImpl
	accessPolicySvc     *accesspolicy.ServiceImpl

	// sing-box
	singboxOp           *singbox.Operator
	sbOrch              *singboxorch.Orchestrator
	singboxInstaller    *installer.Installer
	singboxHandler      *api.SingboxHandler
	clashProxy          *api.ClashProxy
	singboxConnsHandler *api.SingboxConnectionsHandler
	subStore            *subscription.Store
	subAdapter          *subscription.OperatorAdapter
	subSvc              *subscription.Service
	subGroupStore       *subscription.GroupStore
	awgoutboundsSvc     *awgoutbounds.ServiceImpl
	deviceProxySvc      *deviceproxy.Service
	routerSvc           *router.ServiceImpl
	routerScheduler     *router.Scheduler
	awg3Store           *awg3endpoint.Store
	awg3Svc             *awg3endpoint.Service

	// HTTP
	srv *server.Server

	// onExit collects the cleanups the setup phases used to register via
	// in-main defer statements. main() runs them (reverse order — same LIFO
	// as defer) after srv.Start returns.
	onExit []func()
}

// deferOnExit registers fn to run when main() returns. Phase methods use it
// instead of defer — a defer inside a setup method would fire at the end of
// that method, not at daemon shutdown.
func (a *app) deferOnExit(fn func()) {
	a.onExit = append(a.onExit, fn)
}

// runOnExit runs the registered cleanups in reverse registration order,
// mirroring defer's LIFO semantics.
func (a *app) runOnExit() {
	for i := len(a.onExit) - 1; i >= 0; i-- {
		a.onExit[i]()
	}
}
