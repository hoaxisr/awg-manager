package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/awgm"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/buildinfo"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/capture"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/config"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/coverage"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/engine"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/httpapi"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/logging"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/probe"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/store"
)

func main() {
	routerURL := flag.String("router", "http://127.0.0.1:2222", "AWGM base URL")
	apiKey := flag.String("apikey", os.Getenv("AWGM_API_KEY"), "AWGM Bearer API key (or AWGM_API_KEY)")
	configPath := flag.String("config", "/opt/etc/awgmautolist.json", "configuration file")
	listen := flag.String("listen", "127.0.0.1:2226", "loopback operational API address")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("awgmautolist", buildinfo.Version)
		return
	}
	if flag.NArg() != 0 {
		log.Fatal("positional manual probes are not supported")
	}
	if err := httpapi.ValidateLoopbackListen(*listen); err != nil {
		log.Fatal(err)
	}
	if _, err := exec.LookPath("curl"); err != nil {
		log.Fatal("required Entware curl was not found")
	}

	output, err := logging.OpenRAMLog("/tmp/awgmautolist.log")
	if err != nil {
		log.Fatalf("open RAM log: %v", err)
	}
	defer output.Close()
	logger := log.New(output, "awgmautolist ", log.Ldate|log.Ltime|log.Lmicroseconds)
	logger.Printf("START version=%s pid=%d", buildinfo.Version, os.Getpid())
	fatalf := func(format string, args ...any) {
		logger.Printf("FATAL "+format, args...)
		_ = output.Close()
		os.Exit(1)
	}

	cfgStore := config.NewStore(*configPath)
	report, err := cfgStore.Load()
	if err != nil {
		fatalf("config: %v", err)
	}
	for _, warning := range report.Warnings {
		logger.Printf("CONFIG migration: %s", warning)
	}
	statePath := strings.TrimSuffix(*configPath, ".json") + "-state.json"
	stateStore := store.New(statePath)
	if err := stateStore.Load(); err != nil {
		fatalf("state: %v", err)
	}
	if _, err := os.Stat(statePath); os.IsNotExist(err) {
		legacyPath := strings.TrimSuffix(*configPath, ".json") + "-history.json"
		if err := stateStore.ImportLegacyHistory(legacyPath); err != nil {
			logger.Printf("HISTORY legacy import: %v", err)
		}
	}

	client, err := awgm.NewClient(*routerURL, *apiKey)
	if err != nil {
		fatalf("AWGM client: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	interfaces, err := discoverLANBridges(ctx, client, logger)
	if err != nil {
		fatalf("LAN discovery stopped: %v", err)
	}
	routes := awgm.NewRouteResolver(client)
	coverageService := coverage.New(client, routes)
	writer := awgm.NewWriter(client)
	var eng *engine.Engine
	eng = engine.New(engine.Dependencies{
		Config:   cfgStore,
		State:    stateStore,
		Coverage: coverageService,
		Probes:   probe.PackageFuncs{},
		Routes:   routes,
		Writer:   writer,
		CaptureFactory: func(udpAll bool, emit func(capture.Suspect)) engine.Capture {
			return capture.NewController(interfaces, udpAll, emit, func(err error) {
				logger.Printf("CAPTURE error=%q", err)
				if eng != nil {
					eng.CaptureError(err)
				}
			})
		},
		Logger: logger,
		DefaultTunnel: func(ctx context.Context) (awgm.Tunnel, error) {
			return awgm.FirstTunnel(ctx, client)
		},
	})
	if err := eng.Start(ctx); err != nil {
		fatalf("engine: %v", err)
	}

	server := &http.Server{
		Addr:              *listen,
		Handler:           httpapi.New(eng, cfgStore, stateStore, buildinfo.Version),
		ReadHeaderTimeout: 5 * time.Second,
	}
	serverErrors := make(chan error, 1)
	go func() {
		logger.Printf("HTTP operational API on http://%s/status", *listen)
		serverErrors <- server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
	case err := <-serverErrors:
		if err != nil && err != http.ErrServerClosed {
			logger.Printf("HTTP error: %v", err)
			stop()
		}
	}
	eng.Stop()
	_ = server.Close()
	logger.Printf("STOP")
}

var lanDiscoveryRetry = time.Second

type lanDiscoveryClient interface {
	LANBridges(context.Context) ([]string, error)
}

// discoverLANBridges tolerates AWGM's slower boot. Backoff is capped, and only
// SIGTERM/context cancellation stops the retry loop without a usable bridge.
func discoverLANBridges(ctx context.Context, client lanDiscoveryClient, logger *log.Logger) ([]string, error) {
	delay := lanDiscoveryRetry
	for attempt := 1; ; attempt++ {
		interfaces, err := client.LANBridges(ctx)
		if err == nil && len(interfaces) > 0 {
			return interfaces, nil
		}
		if err == nil {
			err = fmt.Errorf("no private up LAN bridge interfaces")
		}
		logger.Printf("LAN discovery attempt=%d error=%q retry_in=%s", attempt, err, delay)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		delay *= 2
		if delay > 5*time.Second {
			delay = 5 * time.Second
		}
	}
}
