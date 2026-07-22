// Package mobile экспортирует минимальный API для gomobile bind (iOS/Android).
// Все экспортированные функции используют только примитивные типы — ограничение gomobile.
package mobile

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/client/dnsdial"
	"github.com/samosvalishe/free-turn-proxy/internal/config"
	"github.com/samosvalishe/free-turn-proxy/internal/logx"
	"github.com/samosvalishe/free-turn-proxy/internal/provider"
	"github.com/samosvalishe/free-turn-proxy/internal/provider/multi"
	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk"
	"github.com/samosvalishe/free-turn-proxy/internal/proxy/bondclient"
	"github.com/samosvalishe/free-turn-proxy/internal/proxy/tcpfwd"
	"github.com/samosvalishe/free-turn-proxy/internal/proxy/udprelay"
	"github.com/samosvalishe/free-turn-proxy/internal/stats"
	"github.com/samosvalishe/free-turn-proxy/internal/sub"
	"github.com/samosvalishe/free-turn-proxy/internal/transport/dtlsdial"
)

// State — состояние подключения.
// gomobile генерирует константы MobileStateXxx.
const (
	StateIdle       = "idle"
	StateConnecting = "connecting"
	StateConnected  = "connected"
	StateError      = "error"
	// StateCaptcha — пользователь решает captcha вручную. Отдельный статус, чтобы
	// UI не показывал ошибку, а watchdog не считал это зависшим подключением.
	StateCaptcha = "captcha"
)

// Snapshot — единый консистентный снимок состояния сессии для UI: и стадия
// подключения, и статистика трафика. gomobile bind транслирует в ObjC-класс
// MobileSnapshot. Один GetState() на тик заменяет прежние GetStatus/GetStats/
// IsRunning — UI получает согласованный срез без гонок порядка чтения.
type Snapshot struct {
	State   string // idle | connecting | connected | error
	Streams int    // подключённых TURN-потоков прямо сейчас
	Total   int    // целевое число потоков
	ErrMsg  string // непустой при State == error
	TxTotal int64  // всего отправлено байт
	RxTotal int64  // всего получено байт
	TxRate  int64  // текущая скорость отправки, байт/с
	RxRate  int64  // текущая скорость получения, байт/с
}

// statusInfo — внутренний снимок стадии подключения (без статистики).
type statusInfo struct {
	state   string
	streams int
	total   int
	errMsg  string
}

// connectTimeout — сколько ждём первый успешный стрим. Если за это время ни
// один поток так и не поднялся (connectedStreams не стал > 0), считаем, что
// все стримы упали, и переходим в error вместо вечного connecting. Ошибка
// одного стрима не страшна — пока хоть один живой, таймаут не срабатывает.
const connectTimeout = 15 * time.Second

var (
	mu         sync.Mutex
	cancelFn   context.CancelFunc
	statusVal  atomic.Value // *statusInfo
	running    atomic.Bool
	sessionGen atomic.Int64 // номер текущей сессии; растёт на каждый Start/Stop
	trafficVal atomic.Pointer[sessionTraffic]
)

func setStatus(s *statusInfo) { statusVal.Store(s) }

// clampToInt64 насыщает uint64 до int64: gomobile экспортирует только int64,
// а счётчики байт — uint64. Реальный трафик до math.MaxInt64 не доходит, но
// насыщение делает конверсию безопасной от переполнения.
func clampToInt64(u uint64) int64 {
	if u > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(u)
}

// GetState возвращает текущий снимок состояния сессии: стадию подключения
// плюс статистику трафика, собранные на чтении из внутренних атомиков.
func GetState() *Snapshot {
	st, _ := statusVal.Load().(*statusInfo)
	if st == nil {
		st = &statusInfo{state: StateIdle}
	}
	var tx, rx uint64
	var txRate, rxRate int64
	if traffic := trafficVal.Load(); traffic != nil {
		tx, rx = traffic.stats.Counters()
		txRate = traffic.txRate.Load()
		rxRate = traffic.rxRate.Load()
	}
	return &Snapshot{
		State:   st.state,
		Streams: st.streams,
		Total:   st.total,
		ErrMsg:  st.errMsg,
		TxTotal: clampToInt64(tx),
		RxTotal: clampToInt64(rx),
		TxRate:  txRate,
		RxRate:  rxRate,
	}
}

func clientArgs(link, peer, dns, listen, transport, obfKey string) []string {
	args := []string{"-listen", listen, "-transport", transport}
	if peer != "" {
		args = append(args, "-peer", peer)
	}
	isURI := strings.HasPrefix(link, "freeturn://")
	if !isURI {
		args = append(args, "-link", link)
	}
	if dns != "" {
		args = append(args, "-dns-servers", dns)
	}
	if obfKey != "" {
		args = append(args, "-obf-profile", "rtpopus", "-obf-key", obfKey)
	}
	if isURI {
		args = append(args, link)
	}
	return args
}

func getClientID(clientType string) string {
	if clientType == "" {
		return "mobile"
	}
	if !strings.HasSuffix(clientType, "-mobile") {
		return clientType + "-mobile"
	}
	return clientType
}

// Start запускает прокси-клиент (сохранен для обратной совместимости).
//
//   - link:       ссылка VK https://vk.com/call/join/... или freeturn:// URI
//   - peer:       адрес freeturn-сервера на VPS, например "1.2.3.4:56000"
//   - dns:        DNS-серверы через запятую, например "8.8.8.8"; "" - авто
//   - listen:     локальный bind, например "127.0.0.1:9000"; "" - дефолт
//   - transport:  "tcp" или "udp"; "" - дефолт tcp
//   - obfKey:     ключ обфускации rtpopus (64 hex символа); "" - без обфускации.
//   - clientType: тип клиента ("ios", "android" и т.д.)
func Start(link, peer, dns, listen, transport, obfKey, clientType string) error {
	if listen == "" {
		listen = "127.0.0.1:9000"
	}
	if transport == "" {
		transport = "tcp"
	}
	return startWithArgs(clientArgs(link, peer, dns, listen, transport, obfKey), clientType)
}

// StartFlags запускает клиент с произвольным набором флагов командной строки,
// передаваемым как одна строка (где флаги разделены переводом строки "\n").
// Поддерживает абсолютно все возможности консольного клиента (-sub, -mode tcp, -kcp и т.д.).
// Идеально подходит для Android/iOS приложений, формирующих список аргументов.
func StartFlags(flagsJoined string) error {
	args := strings.Split(flagsJoined, "\n")
	var cleaned []string
	for _, a := range args {
		if s := strings.TrimSpace(a); s != "" {
			cleaned = append(cleaned, s)
		}
	}
	return startWithArgs(cleaned, "")
}

func startWithArgs(args []string, clientType string) error {
	mu.Lock()
	defer mu.Unlock()

	if running.Load() {
		return fmt.Errorf("already running")
	}

	if subURL := config.PeekSubURL(args); subURL != "" {
		s, ferr := sub.Fetch(context.Background(), subURL)
		if ferr != nil {
			return fmt.Errorf("failed to fetch subscription: %v", ferr)
		}
		if len(s.Nodes) == 0 || s.Nodes[0].URI == nil {
			return fmt.Errorf("no nodes found in subscription")
		}
		args = append(args, s.Nodes[0].URI.String())
	}

	presenter := currentCaptchaPresenter()
	if manualCaptchaOnly.Load() && presenter == nil {
		return fmt.Errorf("manual captcha requires presenter")
	}

	cfg, err := config.ParseClient(args, &bytes.Buffer{})
	if err != nil {
		return err
	}

	if clientType != "" {
		cfg.ClientID = getClientID(clientType)
	} else {
		cfg.ClientID = resolveClientID(cfg.ClientID)
	}

	ClearLogs()
	traffic := &sessionTraffic{stats: stats.New(true)}
	trafficVal.Store(traffic)

	ctx, cancel := context.WithCancel(context.Background())
	cancelFn = cancel
	running.Store(true)
	gen := sessionGen.Add(1)
	setStatus(&statusInfo{state: StateConnecting, total: cfg.TURN.N})
	go traffic.rateMeter(ctx)

	go func() {
		var finalErr error
		var watchdogErr error
		var counters sync.WaitGroup

		defer func() {
			cancel()
			counters.Wait()
			mu.Lock()
			defer mu.Unlock()
			if sessionGen.Load() != gen {
				return
			}
			trafficVal.CompareAndSwap(traffic, nil)
			err := finalErr
			if err == nil {
				err = watchdogErr
			}
			if err != nil {
				setStatus(&statusInfo{state: StateError, total: cfg.TURN.N, errMsg: err.Error()})
			} else {
				setStatus(&statusInfo{state: StateIdle})
			}
			running.Store(false)
			cancelFn = nil
		}()

		logger := &bufLogger{debug: cfg.Log.Debug}
		dnsdial.SetLogger(logger)

		if cfg.DNS.Servers != nil {
			dnsdial.SetUDPDNSServers(cfg.DNS.Servers)
		}
		appDialer := dnsdial.AppDialer(cfg.DNS.Mode)
		dnsdial.InstallGlobalResolver(cfg.DNS.Mode)

		var connectedStreams atomic.Int32

		var captchaActive atomic.Bool
		var solver vk.ManualSolverFunc
		if presenter != nil {
			solver = vk.ProxyManualSolver(
				func(url string) { captchaActive.Store(true); presenter.Show(url) },
				func() { captchaActive.Store(false); presenter.Hide() },
			)
		}

		prov, err := buildProvider(cfg, appDialer, &connectedStreams, solver, logger)
		if err != nil {
			finalErr = fmt.Errorf("provider init: %v", err)
			return
		}

		peerAddr, err := net.ResolveUDPAddr("udp", cfg.Proxy.Peer)
		if err != nil {
			finalErr = fmt.Errorf("bad peer addr: %v", err)
			return
		}

		getCreds := func(ctx context.Context, streamID int) (string, string, []string, error) {
			c, err := prov.GetCredentials(ctx, streamID)
			if err != nil {
				return "", "", nil, err
			}
			return c.User, c.Pass, c.ServerAddrs, nil
		}

		totalStreams := cfg.TURN.N * max(len(cfg.VK.Links), 1)

		counters.Add(1)
		go func() {
			defer counters.Done()
			deadline := time.Now().Add(connectTimeout)
			everConnected := false
			for {
				select {
				case <-ctx.Done():
					return
				case <-time.After(500 * time.Millisecond):
					n := connectedStreams.Load()
					if n > 0 {
						everConnected = true
					}

					if captchaActive.Load() {
						deadline = time.Now().Add(connectTimeout)
						setStatus(&statusInfo{state: StateCaptcha, streams: int(n), total: totalStreams})
						continue
					}

					state := StateConnecting
					if n > 0 {
						state = StateConnected
					}
					setStatus(&statusInfo{state: state, streams: int(n), total: totalStreams})

					if !everConnected && time.Now().After(deadline) {
						watchdogErr = fmt.Errorf("не удалось подключиться: ни один поток не поднялся за %s - проверьте ссылку на звонок и адрес сервера (подробности в логах)", connectTimeout)
						cancel()
						return
					}
				}
			}
		}()

		if cfg.Proxy.Mode != config.ProxyModeUDP {
			tcpDtlsDialer := &dtlsdial.Dialer{
				HandshakeTimeout: 30 * time.Second,
				HandshakeSem:     make(chan struct{}, 3),
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
				TrafficStats: traffic.stats,
			}
			if err := tcpfwd.Run(ctx, tcpDeps, tcpParams, peerAddr, cfg.Proxy.Listen, totalStreams, cfg.Proxy.Mode == config.ProxyModeTCPFwdBond); err != nil {
				if !errors.Is(ctx.Err(), context.Canceled) {
					finalErr = err
				}
			}
			return
		}

		udpDtlsDialer := &dtlsdial.Dialer{
			HandshakeTimeout: 20 * time.Second,
			HandshakeSem:     make(chan struct{}, 3),
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
			TrafficStats: traffic.stats,
		}

		if err := udprelay.Run(ctx, udpDtlsDialer, prov, logger, &connectedStreams, udpParams, peerAddr, cfg.Proxy.Listen, totalStreams); err != nil {
			if !errors.Is(ctx.Err(), context.Canceled) {
				finalErr = err
			}
		}
	}()

	return nil
}

// Stop останавливает прокси-клиент.
func Stop() {
	mu.Lock()
	defer mu.Unlock()
	if cancelFn != nil {
		cancelFn()
		cancelFn = nil
		running.Store(false)
		sessionGen.Add(1)
		trafficVal.Store(nil)
	}
	setStatus(&statusInfo{state: StateIdle})
}

func buildProvider(cfg *config.Client, dialer net.Dialer, connected *atomic.Int32, solver vk.ManualSolverFunc, logger logx.Logger) (provider.Provider, error) {
	switch cfg.Provider.Name {
	case config.ProviderVK:
		if len(cfg.VK.Links) == 0 {
			return nil, fmt.Errorf("vk: no links configured")
		}
		newVK := func(link string) (provider.Provider, error) {
			return vk.New(vk.Config{
				Link:            link,
				Dialer:          dialer,
				ManualOnly:      cfg.VK.ManualCaptcha,
				Browser:         string(cfg.VK.Browser),
				StreamsPerCache: cfg.VK.StreamsPerCred,
				StreamsAlive:    connected.Load,
				Log:             logger,
				Debug:           cfg.Log.Debug,
			}, solver)
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

	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var lc localCfg
		if err := json.Unmarshal(b, &lc); err == nil && lc.ClientID != "" {
			return lc.ClientID
		}
	}

	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		log.Fatalf("failed to generate random client ID: %v", err)
	}
	newID := hex.EncodeToString(idBytes)

	lc := localCfg{ClientID: newID}
	b, _ := json.MarshalIndent(lc, "", "  ")

	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			continue
		}
		if err := os.WriteFile(path, b, 0o600); err == nil {
			return newID
		}
	}
	log.Printf("warning: failed to persist client ID to any writable path (%v); ID will rotate next launch", paths)

	return newID
}

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
