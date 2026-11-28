package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
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
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/migrate"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/probe"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/store"
)

func main() {
	routerURL := flag.String("router", "http://127.0.0.1:2222", "AWGM base URL")
	apiKey := flag.String("apikey", os.Getenv("AWGM_API_KEY"), "AWGM Bearer API key (or AWGM_API_KEY)")
	configPath := flag.String("config", filepath.Join(configDir(), "config.json"), "configuration file")
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

	// The working directory must exist before any migration or store write:
	// a fresh standalone start must not fail its first persist with ENOENT.
	confDir := filepath.Dir(*configPath)
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		fatalf("config directory %s: %v", confDir, err)
	}

	// Legacy flat-layout files (/opt/etc/awgmautolist*.json) move into the
	// config directory BEFORE anything reads the new paths: on the very first
	// start after an upgrade the migrated config must be the one that loads.
	// A failed move is logged and retried on the next start.
	if moved, err := migrate.FromFlatFiles(confDir, migrate.LegacyFlat(), logger.Printf); err != nil {
		logger.Printf("LEGACY migrate: %v", err)
	} else if moved > 0 {
		logger.Printf("LEGACY migrate: moved %d file(s)", moved)
	}
	// The shell-sourced key/enabled/conf get the same one-time move (the init
	// script already did it before sourcing; standalone starts are covered
	// here). A fresh key/enabled always wins, sources survive a failed move.
	if moved, err := migrate.MoveShellFiles(confDir, migrate.LegacyShell(), logger.Printf); err != nil {
		logger.Printf("LEGACY shell migrate: %v", err)
	} else if moved > 0 {
		logger.Printf("LEGACY shell migrate: moved %d file(s)", moved)
	}
	cfgStore := config.NewStore(*configPath)
	report, err := cfgStore.Load()
	if err != nil {
		fatalf("config: %v", err)
	}
	for _, warning := range report.Warnings {
		logger.Printf("CONFIG migration: %s", warning)
	}
	statePath := statePathFor(*configPath)
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

	client, err := awgm.NewClient(resolveRouterURL(*routerURL, logger), *apiKey)
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
	})
	if err := eng.Start(ctx); err != nil {
		fatalf("engine: %v", err)
	}
	if eng.Status().SetupRequired {
		logger.Printf("SETUP required: no target interface configured; the engine is idle until the wizard chooses a tunnel")
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

// DefaultPanelSettingsPath — где панель держит свои настройки. Сателлит
// читает оттуда только порт HTTP-сервера (server.port). Переменная, а не
// константа: тесты подменяют путь.
var DefaultPanelSettingsPath = "/opt/etc/awg-manager/settings.json"

// DefaultPanelPort — последний фолбэк цепочки -router → AWGM_ROUTER_URL →
// settings.json → этот порт. Совпадает с internal/server.DefaultPort панели.
const DefaultPanelPort = 2222

// defaultRouterFlagValue — значение флага -router по умолчанию; оно означает
// «флаг не передан», и только тогда срабатывает цепочка env → settings.json.
const defaultRouterFlagValue = "http://127.0.0.1:2222"

// configDir — каталог с файлами сателлита: AWGM_AUTOLIST_DIR или
// /opt/etc/awgmautolist. Флаг -config перекрывает его целиком.
func configDir() string {
	return migrate.DefaultDir()
}

// statePathFor — путь файла состояния рядом с конфигом. В каталоге раскладки
// config.json живёт рядом с state.json и history.json; старая схема
// "-state.json" оставлена для нестандартных -config вне каталога.
func statePathFor(configPath string) string {
	if filepath.Base(configPath) == "config.json" {
		return filepath.Join(filepath.Dir(configPath), "state.json")
	}
	return strings.TrimSuffix(configPath, ".json") + "-state.json"
}

// resolveRouterURL — цепочка фолбэков адреса панели: явный -router, затем
// AWGM_ROUTER_URL, затем server.port из settings.json панели, затем 2222.
// Выбранный адрес логируется, чтобы смену порта панели было видно в журнале.
func resolveRouterURL(routerFlag string, logger *log.Logger) string {
	if v := strings.TrimSpace(routerFlag); v != "" && v != defaultRouterFlagValue {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("AWGM_ROUTER_URL")); v != "" {
		logger.Printf("ROUTER url=%s (AWGM_ROUTER_URL)", v)
		return v
	}
	port, err := parseServerPort(DefaultPanelSettingsPath)
	if err != nil {
		logger.Printf("ROUTER url=http://127.0.0.1:%d (settings %s: %v)", DefaultPanelPort, DefaultPanelSettingsPath, err)
	} else {
		logger.Printf("ROUTER url=http://127.0.0.1:%d (settings %s)", port, DefaultPanelSettingsPath)
	}
	if port > 0 {
		return fmt.Sprintf("http://127.0.0.1:%d", port)
	}
	return fmt.Sprintf("http://127.0.0.1:%d", DefaultPanelPort)
}

// parseServerPort читает server.port из settings.json панели. Файл пишется
// панелью стандартным encoding/json, поэтому здесь обычный разбор JSON:
// порт 0 или отсутствующий — «панель портом не управляет».
func parseServerPort(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	port, err := extractServerPort(data)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", path, err)
	}
	return port, nil
}

// extractServerPort — чистая часть parseServerPort для тестов.
func extractServerPort(data []byte) (int, error) {
	var doc struct {
		Server struct {
			Port int `json:"port"`
		} `json:"server"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return 0, fmt.Errorf("settings.json: %w", err)
	}
	if doc.Server.Port <= 0 {
		return 0, errors.New("server.port is not set")
	}
	return doc.Server.Port, nil
}

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
