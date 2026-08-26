package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hoaxisr/awg-manager/internal/backup"
	"github.com/hoaxisr/awg-manager/internal/logging"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// logStartup logs system startup information.
// describeListenInterfaces renders the configured bind scope for logs.
func describeListenInterfaces(ifaces []string) string {
	if len(ifaces) == 0 {
		return "all (0.0.0.0)"
	}
	return strings.Join(ifaces, ", ")
}

func logStartup(appLog *logging.ScopedLogger, version, osVersion, listenAddr string, settings *storage.Settings) {
	appLog.Info("startup", "", fmt.Sprintf("AWG Manager v%s started", version))
	appLog.Info("startup", "", fmt.Sprintf("Keenetic OS: %s", osVersion))
	appLog.Info("startup", "", fmt.Sprintf("Listening on %s", listenAddr))

	// Log feature status
	if settings.PingCheck.Enabled {
		appLog.Info("startup", "", "Ping Check: enabled")
	}
	if settings.Logging.Enabled {
		appLog.Info("startup", "", "Logging: enabled")
	}
}

// populateWANModel queries NDMS for current WAN interfaces and fills the
// unified WAN model so that AnyUp() works before any WAN hooks fire.
func populateWANModel(ctx context.Context, queries *ndmsquery.Queries, model *wan.Model, appLog *logging.ScopedLogger) {
	interfaces, err := queries.Interfaces.ListWAN(ctx)
	if err != nil {
		appLog.Warn("populate-wan", "", "failed to get WAN interfaces: "+err.Error())
		return
	}
	model.Populate(interfaces)
	ifaceList := make([]string, 0, len(interfaces))
	for _, iface := range interfaces {
		ifaceList = append(ifaceList, fmt.Sprintf("%s(up=%t)", iface.Name, iface.Up))
	}
	appLog.Info("populate-wan", "", fmt.Sprintf("WAN model populated, count=%d ifaces=[%s]", len(interfaces), strings.Join(ifaceList, " ")))
}

// startBootSequence detects boot vs daemon-restart and runs the boot
// pipeline (NDMS wait, WAN stability, migrations, orchestrator events).
func (a *app) startBootSequence() {
	// Boot vs restart detection
	a.uptime = getUptime()
	tunnelCount := 0
	if list, err := a.awgStore.List(); err == nil {
		tunnelCount = len(list)
	}
	a.bootLog.Info("startup", "",
		fmt.Sprintf("awg-manager %s started (uptime %ds, tunnels in config: %d)", version, int(a.uptime), tunnelCount))
	const bootDetectionMax = 300 // 5 minutes
	isBoot := (a.uptime > 0 && a.uptime < bootDetectionMax) || a.forceBoot
	if isBoot {
		a.bootLog.Info("startup", "",
			fmt.Sprintf("Boot detected (uptime %ds), starting tunnels", int(a.uptime)))

		go func() {
			bootStart := time.Now()

			// === Phase 1: Wait for NDMS readiness ===
			//
			// Previously used a fixed sleep to reach 120s system uptime. The
			// fixed window was brittle: slow NDMS (interface subsystem not yet
			// initialized) delayed the tunnel start by several extra minutes
			// because the sequential migration steps that followed each needed
			// a working RCI endpoint.
			//
			// Now we probe NDMS every second and return as soon as it answers
			// — even "no default route yet" is sufficient signal that the RCI
			// endpoint is alive. If the deadline expires, we proceed anyway
			// (the WAN-down path later handles the missing-default-route case).
			const minBootUptime = 120 // seconds — minimum uptime before tunnel start
			if a.uptime < float64(minBootUptime) {
				ndmsWait := time.Duration(float64(minBootUptime)-a.uptime) * time.Second
				a.bootLog.Info("startup", "",
					fmt.Sprintf("Phase 1: wait for NDMS (uptime %ds, max wait %v)", int(a.uptime), ndmsWait))

				ndmsWaitCtx, ndmsWaitCancel := context.WithTimeout(a.shutdownCtx, ndmsWait)
				if err := ndmsinfo.WaitForNDMS(ndmsWaitCtx, a.ndmsQueries.Routes, a.bootLog); err != nil {
					a.bootLog.Info("startup", "",
						fmt.Sprintf("Phase 1: NDMS wait deadline expired (%v) after %s — proceeding anyway",
							ndmsWait, time.Since(bootStart).Round(time.Second)))
				} else {
					a.bootLog.Info("startup", "",
						fmt.Sprintf("Phase 1: NDMS ready after %s", time.Since(bootStart).Round(time.Second)))
				}
				ndmsWaitCancel()

				// === Phase 1b: Wait for WAN gateway stability ===
				//
				// RCI alive does not guarantee the WAN interface has settled —
				// USB modems and slow carrier links can flap for tens of seconds
				// after NDMS reports ready. Without a stable WAN, opkgtun tunnels
				// loop start/stop because their active-WAN anchor disappears.
				//
				// Poll GetDefaultGatewayInterface every second; proceed when
				// the same gateway name has been returned for wanStableDuration
				// without change. Deadline equals Phase 1 ceiling — if WAN
				// never stabilises, proceed anyway (WAN-down path handles it).
				const wanStableDuration = 5 * time.Second
				wanDeadline := bootStart.Add(ndmsWait)
				var lastGW string
				var stableSince time.Time

				for {
					select {
					case <-a.shutdownCtx.Done():
						return
					case <-time.After(time.Second):
					}

					// Force a fresh /show/ip/route read each poll: the route
					// cache (routeTTL 30m) is warmed by Phase 1's WaitForNDMS and
					// is invalidated only out-of-band by NDMS hooks, so without
					// this drop Phase 1b would re-read a frozen gateway and never
					// observe a flap — degenerating into a fixed delay.
					a.ndmsQueries.Routes.InvalidateAll()
					gw, err := a.ndmsQueries.Routes.GetDefaultGatewayInterface(a.shutdownCtx)
					now := time.Now()

					if err == nil && gw != "" {
						if gw != lastGW {
							lastGW = gw
							stableSince = now
							a.bootLog.Debug("startup", "",
								fmt.Sprintf("Phase 1b: WAN gateway changed to %s at %s", gw,
									time.Since(bootStart).Round(time.Second)))
						}
						if now.Sub(stableSince) >= wanStableDuration {
							a.bootLog.Info("startup", "",
								fmt.Sprintf("Phase 1b: WAN stable (%s) after %s", gw,
									time.Since(bootStart).Round(time.Second)))
							break
						}
					} else {
						// Gateway unavailable — reset so a reappearing
						// gateway with the same name does not inherit
						// stability from before the outage.
						lastGW = ""
						stableSince = time.Time{}
					}
					if now.After(wanDeadline) {
						a.bootLog.Info("startup", "",
							fmt.Sprintf("Phase 1b: WAN stability deadline expired after %s — proceeding anyway",
								time.Since(bootStart).Round(time.Second)))
						break
					}
				}
			} else {
				a.bootLog.Info("startup", "",
					fmt.Sprintf("Phase 1: skipped (uptime %ds ≥ %ds)", int(a.uptime), minBootUptime))
			}

			// Seed WAN model with current interface state from NDMS.
			// Must happen before tunnel start so ISP resolution works.
			populateWANModel(a.shutdownCtx, a.ndmsQueries, a.wanModel, a.bootLog)

			// Best-effort, idempotent migrations — must NOT block tunnel start.
			// tunnelService cleanup below is fast and stays inline.
			var bgDone sync.WaitGroup
			bgDone.Add(2)
			go func() {
				defer bgDone.Done()
				// Back-fill ManagedServer.PrivateKey for entries created before
				// the field existed in storage. Best-effort, idempotent — already
				// populated entries are skipped. Must run AFTER the NDMS interface
				// cache is ready so kernel-name resolution works.
				a.managedService.MigratePrivateKeys(a.shutdownCtx)
			}()
			go func() {
				defer bgDone.Done()
				// One-time sweep: strip the legacy default 0.0.0.0/0 from
				// managed-server peers' allow-ips (per-peer /32 only). New
				// firmware rejects multiple peers sharing 0.0.0.0/0. Gated by a
				// persisted flag; best-effort, retries next boot if NDMS is down.
				a.managedService.MigratePeerAllowIPs(a.shutdownCtx)
			}()

			// Migrate legacy NDMS ID values to kernel names (one-time after model is populated).
			a.tunnelService.MigrateISPInterfaceToKernel()
			// Clear stored.ActiveWAN entries that don't name a real kernel iface
			// (legacy garbage from the pre-hardened resolver, e.g. "ISP").
			a.tunnelService.HealStaleActiveWAN()

			// Detect actual WAN state.
			gwIface, err := a.ndmsQueries.Routes.GetDefaultGatewayInterface(a.shutdownCtx)
			if err != nil {
				a.bootLog.Info("startup", "",
					fmt.Sprintf("WAN down at boot after %s — waiting for WAN UP event",
						time.Since(bootStart).Round(time.Second)))
			} else {
				a.bootLog.Info("startup", "", fmt.Sprintf("wan-decision gw=%s after=%s", gwIface, time.Since(bootStart).Round(time.Second)))
				a.bootLog.Info("startup", "",
					fmt.Sprintf("Tunnel start at %s (uptime ~%ds)",
						time.Since(bootStart).Round(time.Second),
						int(time.Since(bootStart).Seconds())+int(a.uptime)))
				a.orch.LoadState(a.shutdownCtx)
				a.orch.HandleEvent(a.shutdownCtx, orchestrator.Event{Type: orchestrator.EventBoot})
			}

			// Маркер надо снять и здесь: холодный старт и так поднимается из
			// восстановленного конфига, а невынутый маркер на всех последующих
			// загрузках заставлял бы shutdown-хук гасить прокси-инстансы и
			// добивать их процессы (wiring_server.go).
			if backup.ConsumePostRestoreMarker(a.dataDir) {
				a.bootLog.Info("startup", "", "Post-restore marker consumed on cold boot")
			}

			// Прокси-рантайм — после Phase 1/1b (NDMS+WAN): его посев ходит в
			// RCI, и на холодном старте первая попытка (горутина проводки)
			// вполне могла упасть fail-closed. Эндпоинты связанных туннелей
			// чинит ресурс linked_endpoint роли, отдельный проход не нужен.
			a.proxyRuntimeNudge("cold-boot", proxyrt.EventBoot)

			// Wait for background migrations to finish (non-critical but
			// we track them so they don't leak on shutdown).
			bgDone.Wait()

			atomic.StoreInt32(&a.bootDone, 1)
			a.bootLog.Info("startup", "",
				fmt.Sprintf("Boot initialization complete after %s", time.Since(bootStart).Round(time.Second)))
		}()
	} else {
		atomic.StoreInt32(&a.bootDone, 1) // Not booting — mark done immediately.
		// Normal start (daemon restart / upgrade): reconnect to surviving processes.
		// syscall.Exec preserves child processes — amneziawg-go, TUN devices,
		// iptables rules, routes, NDMS config all survive. Only in-memory
		// operator maps (endpointRoutes, resolvedISP) need restoration.
		populateWANModel(context.Background(), a.ndmsQueries, a.wanModel, a.bootLog)

		// Migrate legacy NDMS ID values to kernel names (one-time after model is populated).
		a.tunnelService.MigrateISPInterfaceToKernel()
		// Clear stored.ActiveWAN entries that don't name a real kernel iface
		// (legacy garbage from the pre-hardened resolver, e.g. "ISP").
		a.tunnelService.HealStaleActiveWAN()

		// One-time sweep on daemon restart/upgrade too (NDMS already up):
		// strip the legacy default 0.0.0.0/0 from managed-server peers'
		// allow-ips. Flag-gated, best-effort. Without this the migration
		// would only fire on a cold router boot (isBoot branch above).
		a.managedService.MigratePeerAllowIPs(context.Background())

		if backup.ConsumePostRestoreMarker(a.dataDir) {
			a.bootLog.Info("startup", "",
				"Post-restore boot: syncing linked endpoints and cold-starting from archive")
			a.orch.LoadState(context.Background())
			a.orch.HandleEvent(context.Background(), orchestrator.Event{Type: orchestrator.EventBoot})
			a.proxyRuntimeNudge("post-restore", proxyrt.EventBoot)
			return
		}

		a.bootLog.Info("startup", "",
			"Daemon restart detected — reconnecting to running tunnels")

		a.orch.LoadState(context.Background())
		a.orch.HandleEvent(context.Background(), orchestrator.Event{Type: orchestrator.EventReconnect})
		// Как на cold-boot: посев мог не состояться, если RCI ещё не отвечал
		// сразу после opkg upgrade.
		a.proxyRuntimeNudge("daemon-restart", proxyrt.EventBoot)
	}

}

// serve installs signal handlers and runs the HTTP server until shutdown.
func (a *app) serve() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	// SIGHUP's default action would kill the daemon with no cleanup, and
	// admins habitually send HUP expecting a reload. We have no reload-from-
	// disk semantics (config is API-driven), so ignore it.
	signal.Ignore(syscall.SIGHUP)

	go func() {
		<-sigCh
		os.Remove(pidFile)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		a.srv.Shutdown(ctx)
	}()

	if err := a.srv.Start(); err != nil && err.Error() != "http: Server closed" {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}
