package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/sys/routerclock"
)

const defaultDataDir = "/opt/etc/awg-manager"

// version is set via ldflags at build time
var version = "dev"

func main() {
	dataDir := flag.String("data-dir", defaultDataDir, "Data directory path")
	showVersion := flag.Bool("version", false, "Show version and exit")
	cleanup := flag.Bool("cleanup", false, "Stop and delete all tunnels, then exit (for uninstall)")
	serviceAction := flag.String("service", "", "Service management (start|stop|restart|status)")
	forceBoot := flag.Bool("force-boot", false, "Simulate boot mode (for testing boot path on running router)")
	pprofListen := flag.String("pprof-listen", "", "Dedicated TCP address for Go /debug/pprof only (recommended: 127.0.0.1:6060); empty disables standalone pprof")
	slowReqMS := flag.Int("slow-request-ms", 0, "Log HTTP handlers slower than this (ms) to stderr via slog (0 disables); long-lived SSE/WS routes are excluded")
	flag.Parse()

	// Adopt the router's local timezone before anything reads time.Local.
	// Keenetic stores the zone as a POSIX string ("MSK-3") in /var/TZ, which
	// the Go runtime does not honor via /etc/localtime — so without this the
	// daemon runs in UTC and daily HH:MM schedulers fire 3h off.
	routerclock.InstallAsLocal()

	// Ensure Go can find CA certificates on entware-based systems (Keenetic).
	// Must run before any HTTPS calls (kmod download, etc.).
	ensureCACerts()

	if *showVersion {
		fmt.Printf("awg-manager version %s\n", version)
		os.Exit(0)
	}

	// Cleanup mode: delete all tunnels and exit
	if *cleanup {
		runCleanup(*dataDir)
		os.Exit(0)
	}

	// Service management (start/stop/restart/status)
	if *serviceAction != "" {
		runService(*serviceAction, *dataDir)
		os.Exit(0)
	}

	a := &app{
		dataDir:     *dataDir,
		forceBoot:   *forceBoot,
		pprofListen: strings.TrimSpace(*pprofListen),
		slowReqMS:   *slowReqMS,
	}
	// Deferred cleanups collected by the setup phases run when the HTTP
	// server returns — same LIFO order the original in-main defers had.
	defer a.runOnExit()

	a.setupCore()
	a.setupNDMS()
	a.setupTunnels()
	a.setupServices()
	a.setupOrchestrator()
	a.setupEventWiring()
	a.setupSingbox()
	a.setupServer()
	a.setupDeviceProxy()
	a.setupRouter()
	a.setupListen()
	a.setupShutdown()
	// Прокси-инстансы гасятся ДО остановки HTTP: Shutdown снимает воркеры и
	// закрывает управляющие сокеты, а на пути самоперезапуска (syscall.Exec)
	// незакрытые сокеты уехали бы в новый образ процесса. Дочерние процессы
	// при этом ЖИВУТ — усыновление после перезапуска демона держится на них.
	a.srv.AddShutdownHook(func() {
		if a.proxyMgr != nil {
			a.proxyMgr.Shutdown()
		}
	})
	a.startBootSequence()
	a.serve()
}
