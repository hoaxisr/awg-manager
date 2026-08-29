package main

import (
	"context"
	"errors"

	"log/slog"

	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/updater"
)

// singboxUpdaterAdapter adapts *singbox.Operator to updater.SingboxUpdater
// so the updater package can drive sing-box auto-install without importing
// internal/singbox. Errors are translated to the updater package's own
// sentinel so the scheduler doesn't need to know about singbox.ErrInstallInProgress.
type singboxUpdaterAdapter struct {
	op *singbox.Operator
}

func (a *singboxUpdaterAdapter) UpdateStatus(ctx context.Context) (installed, updateAvailable bool, current, required string) {
	st := a.op.GetStatus(ctx)
	return st.Installed, st.UpdateAvailable, st.CurrentVersion, st.RequiredVersion
}

func (a *singboxUpdaterAdapter) Update(ctx context.Context) error {
	err := a.op.Update(ctx)
	if errors.Is(err, singbox.ErrInstallInProgress) {
		return updater.ErrSingboxInstallInProgress
	}
	return err
}

// setupSingbox wires the sing-box runtime (setupSingboxRuntime) and then
// its periphery: HTTP handlers and the background workers (watchdog,
// traffic, delay, log forwarder) plus the updater service.
func (a *app) setupSingbox() {
	a.setupSingboxRuntime()

	delayChecker := singbox.NewDelayChecker(
		a.singboxOp.Clash(),
		&singboxAndSubLister{op: a.singboxOp, sub: a.subSvc, awg3: a.awg3Svc},
		a.eventBus,
	)
	a.singboxHandler = api.NewSingboxHandler(a.singboxOp, a.eventBus, delayChecker, a.testService, a.loggingService)
	singboxMigrator := singbox.NewMigrator(a.singboxOp, a.settingsStore, a.loggingService)
	a.singboxHandler.SetNDMSProxyMigrator(singboxMigrator, a.settingsStore)
	a.clashProxy = api.NewClashProxy(a.singboxOp)
	a.singboxConnsHandler = api.NewSingboxConnectionsHandler(a.ndmsQueries.Hotspot)
	// Managed WG-server peer names for the connections monitor (issue
	// #435): in-memory store read, no NDMS round-trip. The system-server
	// source is wired in server.go once ServersHandler exists.
	if a.managedService != nil {
		a.singboxConnsHandler.SetManagedServers(a.managedService)
	}

	// Watchdog: runs an immediate reconcile (replacing the old one-shot
	// startup reconcile) and keeps checking every 30s. If sing-box crashes
	// while awgm is running, the next tick detects the dead PID and
	// restarts it; the UI is notified via resource:invalidated SSE hints
	// only when the running state actually flips.
	watchdogCtx, watchdogCancel := context.WithCancel(context.Background())
	a.deferOnExit(watchdogCancel)
	go singbox.NewWatchdog(a.singboxOp, a.eventBus, slog.Default().With("component", "singbox-watchdog")).Run(watchdogCtx)

	trafficCtx, trafficCancel := context.WithCancel(context.Background())
	a.deferOnExit(trafficCancel)
	go singbox.NewTrafficAggregator(a.singboxOp.Clash().Address, a.eventBus, a.trafficHistory).Run(trafficCtx)

	delayCtx, delayCancel := context.WithCancel(context.Background())
	a.deferOnExit(delayCancel)
	go delayChecker.Run(delayCtx)

	// Forward sing-box runtime logs from clash_api /logs into the app's
	// UI log view (replaces the old file-based log; see process.go).
	logFwdCtx, logFwdCancel := context.WithCancel(context.Background())
	a.deferOnExit(logFwdCancel)
	go singbox.NewLogForwarder(a.singboxOp.Clash().Address, a.loggingService).Run(logFwdCtx)

	// Updater service (awg-manager self-update check/apply + scheduled
	// auto-install for both awg-manager and the managed sing-box binary).
	// Constructed here rather than in setupServices because the sing-box
	// auto-install path needs a.singboxOp, which does not exist yet at
	// that earlier point in main.go's setup sequence.
	a.updaterService = updater.New(version, a.settingsStore, a.loggingService, a.dataDir, &singboxUpdaterAdapter{op: a.singboxOp})
	a.updaterService.Start()
	a.deferOnExit(a.updaterService.Stop)
}
