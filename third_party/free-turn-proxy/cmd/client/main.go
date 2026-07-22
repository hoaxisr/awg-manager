package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"path/filepath"

	"github.com/samosvalishe/free-turn-proxy/internal/client/dnsdial"
	"github.com/samosvalishe/free-turn-proxy/internal/config"
	"github.com/samosvalishe/free-turn-proxy/internal/logx"
	"github.com/samosvalishe/free-turn-proxy/internal/provider"
	"github.com/samosvalishe/free-turn-proxy/internal/provider/multi"
	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk"
	"github.com/samosvalishe/free-turn-proxy/internal/proxy/bondclient"
	"github.com/samosvalishe/free-turn-proxy/internal/proxy/tcpfwd"
	"github.com/samosvalishe/free-turn-proxy/internal/proxy/udprelay"
	"github.com/samosvalishe/free-turn-proxy/internal/sub"
	"github.com/samosvalishe/free-turn-proxy/internal/transport/dtlsdial"
	"github.com/samosvalishe/free-turn-proxy/internal/wire/rtpopus"
)

// version is populated at build time via -ldflags "-X main.version=...".
var version = "dev"

const dtlsHandshakeConcurrency = 3

func main() {
	args := os.Args[1:]

	// -sub: тянем список серверов до парсинга и подсовываем URI первой ноды
	// (Nodes[0], без failover) позиционным freeturn:// - ParseClient применит его
	// тем же путём, что и URI из CLI. Подписка должна стоять до парсинга: она даёт
	// peer, без которого ParseClient падает на валидации.
	if subURL := config.PeekSubURL(args); subURL != "" {
		s, ferr := sub.Fetch(context.Background(), subURL)
		if ferr != nil {
			log.Fatalf("failed to fetch subscription: %v", ferr)
		}
		if len(s.Nodes) == 0 || s.Nodes[0].URI == nil {
			log.Fatalf("no nodes found in subscription")
		}
		args = append(args, s.Nodes[0].URI.String())
	}

	cfg, err := config.ParseClient(args, os.Stderr)
	if err != nil {
		// -help/-h: usage уже напечатан в ParseClient, выходим штатно.
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		// логгер ещё не создан - единственный fatal до его инициализации.
		log.Fatalf("%v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg.ClientID = resolveClientID(cfg.ClientID)

	logger := logx.New(cfg.Log.Debug)
	logger.Infof("Free Turn Proxy client version=%s", version)
	logger.Infof("Client ID: %s", cfg.ClientID)
	dnsdial.SetLogger(logger)
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-signalChan
		logger.Infof("Terminating...")
		cancel()
		select {
		case <-signalChan:
		case <-time.After(5 * time.Second):
		}
		logger.Errorf("Exit...")
		cancel()
		os.Exit(1)
	}()

	if cfg.DNS.Servers != nil {
		dnsdial.SetUDPDNSServers(cfg.DNS.Servers)
		logger.Infof("[DNS] using custom UDP servers: %v", cfg.DNS.Servers)
	}
	appDialer := dnsdial.AppDialer(cfg.DNS.Mode)
	dnsdial.InstallGlobalResolver(cfg.DNS.Mode)
	if cfg.Obf.GenKey {
		key, gerr := rtpopus.GenKeyHex()
		if gerr != nil {
			logger.Errorf("gen-obf-key: %v", gerr)
			os.Exit(1)
		}
		fmt.Println(key)
		return
	}
	peer, err := net.ResolveUDPAddr("udp", cfg.Proxy.Peer)
	if err != nil {
		logger.Errorf("resolve peer addr: %v", err)
		os.Exit(1)
	}
	if cfg.Obf.Enabled() {
		logger.Infof("OBF profile=%s: peer server must use matching -obf-profile and -obf-key", cfg.Obf.Profile)
	}

	var connectedStreams atomic.Int32

	prov, err := buildProvider(cfg, appDialer, &connectedStreams, logger)
	if err != nil {
		logger.Errorf("provider init: %v", err)
		os.Exit(1)
	}
	logger.Infof("provider=%s", prov.Name())

	getCreds := func(ctx context.Context, streamID int) (string, string, []string, error) {
		c, err := prov.GetCredentials(ctx, streamID)
		if err != nil {
			return "", "", nil, err
		}
		return c.User, c.Pass, c.ServerAddrs, nil
	}

	// Несколько -links расширяют пул: каждый звонок даёт cfg.TURN.N стримов,
	// все объединяются в общий пул (больше параллельных TURN-аллокаций).
	totalStreams := cfg.TURN.N * max(len(cfg.VK.Links), 1)

	if cfg.Proxy.Mode != config.ProxyModeUDP {
		tcpDtlsDialer := &dtlsdial.Dialer{
			HandshakeTimeout: 30 * time.Second,
			HandshakeSem:     make(chan struct{}, dtlsHandshakeConcurrency),
		}
		bondH := &bondclient.Handler{Deps: bondclient.Deps{Log: logger}}
		tcpDeps := &tcpfwd.Deps{
			DTLSDialer:  tcpDtlsDialer,
			Log:         logger,
			BondHandler: bondH.Handle,
		}
		tcpParams := &tcpfwd.Params{
			Host:         cfg.TURN.Host,
			Port:         cfg.TURN.Port,
			TransportUDP: cfg.TURN.TransportUDP,
			Profile:      string(cfg.Obf.Profile),
			ObfKey:       cfg.Obf.Key,
			GetCreds:     tcpfwd.GetCredsFunc(getCreds),
			KCPProfile:   cfg.KCP.Profile,
			KCPFEC:       cfg.KCP.FEC,
			ClientID:     cfg.ClientID,
		}
		if err := tcpfwd.Run(ctx, tcpDeps, tcpParams, peer, cfg.Proxy.Listen, totalStreams, cfg.Proxy.Mode == config.ProxyModeTCPFwdBond); err != nil {
			logger.Errorf("tcpfwd: %v", err)
			os.Exit(1)
		}
		return
	}

	udpDtlsDialer := &dtlsdial.Dialer{
		HandshakeTimeout: 20 * time.Second,
		HandshakeSem:     make(chan struct{}, dtlsHandshakeConcurrency),
	}
	udpParams := &udprelay.Params{
		Host:         cfg.TURN.Host,
		Port:         cfg.TURN.Port,
		TransportUDP: cfg.TURN.TransportUDP,
		Profile:      string(cfg.Obf.Profile),
		ObfKey:       cfg.Obf.Key,
		ObfTiming:    cfg.Obf.Timing,
		GetCreds:     udprelay.GetCredsFunc(getCreds),
		ClientID:     cfg.ClientID,
	}
	if err := udprelay.Run(ctx, udpDtlsDialer, prov, logger, &connectedStreams, udpParams, peer, cfg.Proxy.Listen, totalStreams); err != nil {
		if errors.Is(err, udprelay.ErrFatal) {
			logger.Errorf("udprelay: fatal: %v", err)
		} else {
			logger.Errorf("udprelay: %v", err)
		}
		os.Exit(1)
	}
}

// buildProvider строит provider.Provider по cfg.Provider.Name. Одна -links даёт
// обычный vk.Provider; несколько - multi.Provider (один vk.Provider на ссылку).
func buildProvider(cfg *config.Client, dialer net.Dialer, connected *atomic.Int32, logger logx.Logger) (provider.Provider, error) {
	switch cfg.Provider.Name {
	case config.ProviderVK:
		if len(cfg.VK.Links) == 0 {
			return nil, fmt.Errorf("vk: no links configured")
		}
		newVK := func(link string) (provider.Provider, error) {
			manualSolver := vk.DefaultManualSolver
			if !cfg.VK.ManualCaptcha && !cfg.VK.ManualCaptchaFallback {
				manualSolver = nil
			}
			return vk.New(vk.Config{
				Link:            link,
				Dialer:          dialer,
				ManualOnly:      cfg.VK.ManualCaptcha,
				Browser:         string(cfg.VK.Browser),
				Platform:        string(cfg.VK.Platform),
				StreamsPerCache: cfg.VK.StreamsPerCred,
				StreamsAlive:    connected.Load,
				Log:             logger,
				Debug:           cfg.Log.Debug,
			}, manualSolver)
		}
		if len(cfg.VK.Links) == 1 {
			return newVK(cfg.VK.Links[0])
		}
		providers := make([]provider.Provider, 0, len(cfg.VK.Links))
		for i, link := range cfg.VK.Links {
			p, err := newVK(link)
			if err != nil {
				return nil, fmt.Errorf("vk provider [%d]: %w", i, err)
			}
			providers = append(providers, p)
		}
		logger.Infof("multi-provider: %d VK links, %d total streams", len(providers), cfg.TURN.N*len(providers))
		return multi.New(providers), nil
	default:
		return nil, fmt.Errorf("unknown provider %q", cfg.Provider.Name)
	}
}

func resolveClientID(cliID string) string {
	if cliID != "" {
		return cliID
	}

	type localCfg struct {
		ClientID string `json:"client_id"`
	}

	paths := clientConfigPaths()

	// Чтение: первый файл с непустым client_id.
	for _, path := range paths {
		b, err := os.ReadFile(path) //nolint:gosec // фиксированное имя из clientConfigPaths, не пользовательский ввод
		if err != nil {
			continue
		}
		var lc localCfg
		if err := json.Unmarshal(b, &lc); err == nil && lc.ClientID != "" {
			return lc.ClientID
		}
	}

	// Generate 16 bytes hex ID
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		log.Fatalf("failed to generate random client ID: %v", err)
	}
	newID := hex.EncodeToString(idBytes)

	lc := localCfg{ClientID: newID}
	b, _ := json.MarshalIndent(lc, "", "  ")

	// Запись: первый доступный для записи путь. На Android каталог рядом с
	// бинарём (/data/app/.../lib/arm64) read-only - падаем на UserConfigDir,
	// затем TempDir. Иначе ID не сохраняется и ротируется на каждый запуск,
	// ломая allowlist (-clients-file) и статистику.
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			continue
		}
		if err := os.WriteFile(path, b, 0o600); err == nil { //nolint:gosec // 0o600 для auth-токена
			return newID
		}
	}
	log.Printf("warning: failed to persist client ID to any writable path (%v); ID will rotate next launch", paths)

	return newID
}

// clientConfigPaths возвращает кандидатов client_config.json в порядке
// предпочтения: рядом с бинарём (desktop, переносимость), затем per-user
// UserConfigDir и TempDir (Android, где каталог бинаря read-only).
func clientConfigPaths() []string {
	const name = "client_config.json"
	seen := map[string]bool{}
	var dirs []string
	add := func(d string) {
		if d == "" || seen[d] {
			return
		}
		seen[d] = true
		dirs = append(dirs, d)
	}
	if exe, err := os.Executable(); err == nil {
		add(filepath.Dir(exe))
	}
	add(filepath.Dir(os.Args[0]))
	if cfgDir, err := os.UserConfigDir(); err == nil {
		add(filepath.Join(cfgDir, "free-turn-proxy"))
	}
	add(os.TempDir())

	paths := make([]string, 0, len(dirs))
	for _, d := range dirs {
		paths = append(paths, filepath.Join(d, name))
	}
	return paths
}
