// Package config парсит CLI-флаги клиента и сервера.
//
// Функции Parse* без побочных эффектов: валидируют ввод и декодируют wrap-ключ,
// но не трогают сеть, DNS и состояние процесса. Подключение этих эффектов -
// ответственность main() после возврата Parse*.
//
// Опции сгруппированы по доменам (TURN, Obf, Proxy, VK, DNS, Log) - структура
// зеркалит концептуальные слои прокси.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/transport/kcptun"
	"github.com/samosvalishe/free-turn-proxy/internal/uri"
	"github.com/samosvalishe/free-turn-proxy/internal/wire/rtpopus"
)

const (
	dnsModePlain           = "plain"
	dnsModeDoH             = "doh"
	dnsModeAuto            = "auto"
	defaultStreamsPerCache = 10
)

// ProxyMode выбирает payload прикладного уровня, который идёт через TURN-туннель.
// На клиенте доступны все три; на сервере только UDP / TCPFwd
// (bond определяется автоматически per-stream по magic-префиксу).
type ProxyMode string

const (
	ProxyModeUDP        ProxyMode = "udp"         // -mode udp (default): UDP-релей пакетов (WireGuard)
	ProxyModeTCPFwd     ProxyMode = "tcpfwd"      // -mode tcp: TCP-форвардер через smux
	ProxyModeTCPFwdBond ProxyMode = "tcpfwd-bond" // -mode tcp -bond: bond TCP по N smux-сессиям
)

// TURNOpts - опции TURN-сервера (куда и как подключаться).
type TURNOpts struct {
	Host         string // -turn: переопределить IP/host TURN-сервера
	Port         string // -port: переопределить порт TURN
	TransportUDP bool   // -transport udp: подключение к TURN по UDP (по умолчанию TCP/TLS)
	N            int    // -n: число TURN-потоков (только клиент)
}

// ObfProfile выбирает wire-профиль обфускации TURN-payload.
// Профили живут в internal/wire/<profile>/ - сейчас только rtpopus,
// под добавление новых (rtph264, vp8 и т.д.).
type ObfProfile string

const (
	ObfProfileNone     ObfProfile = "none"     // обфускация отключена
	ObfProfileRTPOpus  ObfProfile = "rtpopus"  // RTP/opus + ChaCha20-Poly1305 AEAD
	ObfProfileRTPOpus2 ObfProfile = "rtpopus2" // rtpopus + RTP header extension (мимикрия под современный WebRTC)
	ObfProfileRTPOpus3 ObfProfile = "rtpopus3" // rtpopus2 + abs-send-time + VAD + loss simulation + variable ts
)

// ObfOpts - опции обфускации TURN-payload.
type ObfOpts struct {
	Profile ObfProfile    // -obf-profile: none (default) | rtpopus | rtpopus2
	Key     []byte        // -obf-key (декодированный): 32-байтовый общий ключ; nil если Profile=none
	GenKey  bool          // -gen-obf-key: напечатать новый ключ и выйти
	Timing  time.Duration // -obf-timing: межпакетная задержка (RTP-мимикрия); 0=выкл
}

// Enabled возвращает true когда выбран реальный профиль обфускации.
func (o ObfOpts) Enabled() bool { return o.Profile != ObfProfileNone }

// ProxyOpts - опции прокси прикладного уровня.
type ProxyOpts struct {
	Mode    ProxyMode // udp | tcpfwd | tcpfwd-bond (сервер: udp | tcpfwd)
	Listen  string    // -listen: локальный bind (клиент: WG/TCP entry; сервер: TURN entry)
	Connect string    // -connect: backend (только сервер)
	Peer    string    // -peer: адрес серверного прокси, куда дозванивается клиент (только клиент)
}

// Browser выбирает браузерный профиль для control-plane запросов VK-провайдера.
type Browser string

const (
	BrowserChrome  Browser = "chrome"
	BrowserFirefox Browser = "firefox"
	BrowserSafari  Browser = "safari"
)

// Platform выбирает класс устройства персоны (мобильность UA/device/client hints).
type Platform string

const (
	PlatformDesktop Platform = "desktop"
	PlatformMobile  Platform = "mobile"
)

// VKOpts - опции VK-учёток и captcha (только клиент, провайдер "vk").
type VKOpts struct {
	Links          []string // -links (нормализованные join-коды); несколько = больше стримов
	StreamsPerCred int      // -streams-per-cred
	ManualCaptcha         bool     // -manual-captcha: только ручная капча
	ManualCaptchaFallback bool     // -captcha-manual-fallback: auto, затем :8765
	Browser               Browser  // -browser: chrome | firefox | safari
	Platform       Platform // -platform: desktop | mobile
}

// ProviderOpts выбирает реализацию provider.Provider.
type ProviderOpts struct {
	Name string // -provider: vk (default)
}

// Известные имена провайдеров.
const (
	ProviderVK = "vk"
)

// DNSOpts - опции DNS-резолвинга (только клиент).
type DNSOpts struct {
	Mode    string   // -dns-mode: plain | doh | auto
	Servers []string // -dns-servers (через запятую); nil если флаг пуст
}

// LogOpts - опции логирования.
type LogOpts struct {
	Debug bool // -debug
}

// KCPOpts - параметры KCP-туннеля, хардкодятся из DefaultProfile/FEC{}.
type KCPOpts struct {
	Profile kcptun.Profile
	FEC     kcptun.FEC
}

// Client - разобранные и провалидированные CLI-опции клиента.
type Client struct {
	TURN     TURNOpts
	Obf      ObfOpts
	Proxy    ProxyOpts
	Provider ProviderOpts
	VK       VKOpts
	DNS      DNSOpts
	Log      LogOpts
	KCP      KCPOpts
	ClientID string
	SubURL   string
}

// Server - разобранные и провалидированные CLI-опции сервера.
type Server struct {
	Obf         ObfOpts
	Proxy       ProxyOpts
	Log         LogOpts
	KCP         KCPOpts
	ClientsFile string // -clients-file
}

// PeekSubURL вытаскивает значение -sub из сырых args без полного парсинга.
// Нужно до ParseClient: подписка отдаёт peer, без которого ParseClient падает
// на валидации. Вызывающий тянет подписку и подсовывает URI ноды позиционным
// аргументом - дальше применение идёт общим путём в ParseClient.
func PeekSubURL(args []string) string {
	for i := range args {
		a := args[i]
		if v, ok := strings.CutPrefix(a, "-sub="); ok {
			return v
		}
		if v, ok := strings.CutPrefix(a, "--sub="); ok {
			return v
		}
		if (a == "-sub" || a == "--sub") && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// ParseClient разбирает args (без имени программы) в Client.
// При flag.ErrHelp возвращает (nil, flag.ErrHelp) - вызывающий выходит штатно.
func ParseClient(args []string, errOut io.Writer) (*Client, error) {
	fs := flag.NewFlagSet("client", flag.ContinueOnError)
	if errOut != nil {
		fs.SetOutput(errOut)
	}

	turn := fs.String("turn", "", "IP TURN-сервера; override creds провайдера")
	port := fs.String("port", "", "порт TURN-сервера; override creds провайдера")
	listen := fs.String("listen", "127.0.0.1:9000", "локальный ip:port для WireGuard/Xray клиента")
	provider := fs.String("provider", ProviderVK, "источник TURN-creds: vk")
	link := fs.String("link", "", "(устарел) одна ссылка VK Calls, используйте -links")
	links := fs.String("links", "", "ссылки VK Calls через запятую: https://vk.ru/call/join/...,https://vk.ru/call/join/...")
	peer := fs.String("peer", "", "адрес сервера на VPS, host:port; обязательно")
	n := fs.Int("n", 10, "число параллельных TURN-потоков")
	transport := fs.String("transport", "tcp", "транспорт до TURN-реле: tcp | udp")
	mode := fs.String("mode", "udp", "режим туннеля: udp (WireGuard) | tcp (Xray/sing-box)")
	bond := fs.Bool("bond", false, "страйпинг TCP по smux-сессиям; только с -mode tcp")
	obfProfile := fs.String("obf-profile", string(ObfProfileNone), "wire-профиль обфускации: none | rtpopus | rtpopus2 | rtpopus3; должен совпадать с сервером")
	obfKey := fs.String("obf-key", "", "ключ для -obf-profile != none: 32 байта hex (64 символа)")
	genObfKey := fs.Bool("gen-obf-key", false, "напечатать новый -obf-key и выйти")
	obfTiming := fs.Duration("obf-timing", 0, "межпакетная задержка для RTP-мимикрии (напр. 20ms); 0=выкл")
	streamsPerCred := fs.Int("streams-per-cred", defaultStreamsPerCache, "TURN-потоков на один кеш VK-creds; только -provider vk")
	debug := fs.Bool("debug", false, "подробные debug-логи")
	manualCaptcha := fs.Bool("manual-captcha", false, "только ручная VK captcha в браузере; только -provider vk")
	manualCaptchaFallback := fs.Bool("captcha-manual-fallback", false, "после auto-раундов открыть :8765 для ручной captcha; только -provider vk")
	browser := fs.String("browser", string(BrowserChrome), "браузерный профиль VK-auth: chrome | firefox | safari; только -provider vk")
	platform := fs.String("platform", string(PlatformDesktop), "класс устройства персоны VK-auth: desktop | mobile; только -provider vk")
	dnsMode := fs.String("dns-mode", dnsModeAuto, "резолвер клиента: plain | doh | auto")
	dnsServers := fs.String("dns-servers", "", "свои UDP/53 DNS через запятую: ip[:port][,ip[:port]...]")
	clientID := fs.String("client-id", "", "уникальный ID клиента (автогенерация если не задан)")
	subURL := fs.String("sub", "", "URL подписки (sub.md) для получения списка серверов")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	c := &Client{
		TURN: TURNOpts{
			Host:         *turn,
			Port:         *port,
			TransportUDP: *transport == "udp",
			N:            *n,
		},
		Obf: ObfOpts{
			Profile: ObfProfile(*obfProfile),
			GenKey:  *genObfKey,
			Timing:  *obfTiming,
		},
		Proxy: ProxyOpts{
			Mode:   ClientProxyMode(*mode, *bond),
			Listen: *listen,
			Peer:   *peer,
		},
		Provider: ProviderOpts{
			Name: *provider,
		},
		VK: VKOpts{
			StreamsPerCred:        *streamsPerCred,
			ManualCaptcha:         *manualCaptcha,
			ManualCaptchaFallback: *manualCaptchaFallback,
			Browser:               Browser(*browser),
			Platform:              Platform(*platform),
		},
		DNS: DNSOpts{
			Mode: *dnsMode,
		},
		Log: LogOpts{
			Debug: *debug,
		},
		KCP: KCPOpts{
			Profile: kcptun.DefaultProfile(),
			FEC:     kcptun.FEC{},
		},
		ClientID: *clientID,
		SubURL:   *subURL,
	}

	// Обработка позиционного аргумента URI
	if fs.NArg() > 0 {
		arg := fs.Arg(0)
		if strings.HasPrefix(arg, "freeturn://") {
			ucfg, err := uri.Parse(arg)
			if err != nil {
				return nil, fmt.Errorf("failed to parse freeturn:// URI: %w", err)
			}
			if ucfg.Provider != "" {
				c.Provider.Name = ucfg.Provider
			}
			if ucfg.Transport != "" {
				*transport = ucfg.Transport
			}
			if ucfg.Mode != "" {
				*mode = ucfg.Mode
			}
			if ucfg.Bond {
				*bond = true
			}
			if ucfg.N > 0 {
				c.TURN.N = ucfg.N
			}
			if ucfg.StreamsPerCred > 0 {
				c.VK.StreamsPerCred = ucfg.StreamsPerCred
			}
			if ucfg.ObfProfile != "" {
				c.Obf.Profile = ObfProfile(ucfg.ObfProfile)
			}
			if ucfg.ObfKey != "" {
				*obfKey = ucfg.ObfKey
			}
			if ucfg.Peer != "" {
				c.Proxy.Peer = ucfg.Peer
			}
			if ucfg.ClientID != "" {
				c.ClientID = ucfg.ClientID
			}
			if ucfg.Listen != "" {
				c.Proxy.Listen = ucfg.Listen
			}
			if ucfg.DNSMode != "" {
				c.DNS.Mode = ucfg.DNSMode
			}
			if ucfg.DNSServers != "" {
				*dnsServers = ucfg.DNSServers
			}
			if ucfg.ManualCaptcha {
				c.VK.ManualCaptcha = true
			}
		}
	}

	// Пересчитываем после возможного изменения из URI
	c.Proxy.Mode = ClientProxyMode(*mode, *bond)
	c.TURN.TransportUDP = *transport == "udp"

	switch *transport {
	case "tcp", "udp":
	default:
		return nil, fmt.Errorf("invalid -transport value %q: must be tcp | udp", *transport)
	}
	switch *mode {
	case "udp", "tcp":
	default:
		return nil, fmt.Errorf("invalid -mode value %q: must be udp | tcp", *mode)
	}
	if *bond && *mode != "tcp" {
		return nil, fmt.Errorf("-bond requires -mode tcp")
	}
	switch c.DNS.Mode {
	case dnsModePlain, dnsModeDoH, dnsModeAuto:
	default:
		return nil, fmt.Errorf("invalid -dns-mode value %q: must be plain | doh | auto", c.DNS.Mode)
	}
	if *dnsServers != "" {
		c.DNS.Servers = strings.Split(*dnsServers, ",")
	}

	if c.Obf.GenKey {
		return c, nil
	}

	if c.Proxy.Peer == "" {
		return nil, errors.New("need peer address")
	}
	switch c.Provider.Name {
	case ProviderVK:
		if *links == "" && *link == "" {
			return nil, errors.New("need -links (или -link) (required for -provider vk)")
		}
		if c.VK.StreamsPerCred <= 0 {
			return nil, fmt.Errorf("-streams-per-cred must be positive")
		}
		switch c.VK.Browser {
		case BrowserChrome, BrowserFirefox, BrowserSafari:
		default:
			return nil, fmt.Errorf("invalid -browser value %q: must be %s | %s | %s", c.VK.Browser, BrowserChrome, BrowserFirefox, BrowserSafari)
		}
		if c.VK.Platform == "" {
			c.VK.Platform = PlatformDesktop
		}
		switch c.VK.Platform {
		case PlatformDesktop, PlatformMobile:
		default:
			return nil, fmt.Errorf("invalid -platform value %q: must be %s | %s", c.VK.Platform, PlatformDesktop, PlatformMobile)
		}
		rawLinks := strings.Split(*links, ",")
		if len(rawLinks) == 1 && rawLinks[0] == "" {
			// -links не задан, используем -link (backward compat)
			rawLinks = []string{*link}
		}
		for _, raw := range rawLinks {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			parts := strings.Split(raw, "join/")
			normalized := parts[len(parts)-1]
			if idx := strings.IndexAny(normalized, "/?#"); idx != -1 {
				normalized = normalized[:idx]
			}
			if normalized != "" {
				c.VK.Links = append(c.VK.Links, normalized)
			}
		}
		if len(c.VK.Links) == 0 {
			return nil, errors.New("need at least one valid VK link")
		}
	default:
		return nil, fmt.Errorf("invalid -provider value %q: must be %s", c.Provider.Name, ProviderVK)
	}
	if err := validateObfProfile(c.Obf.Profile); err != nil {
		return nil, err
	}
	key, err := rtpopus.DecodeKey(c.Obf.Enabled(), *obfKey)
	if err != nil {
		return nil, err
	}
	c.Obf.Key = key
	if err := validateObfTiming(c.Obf, c.Proxy.Mode); err != nil {
		return nil, err
	}
	if c.TURN.N <= 0 {
		c.TURN.N = 10
	}

	return c, nil
}

// ParseServer разбирает args (без имени программы) в Server.
func ParseServer(args []string, errOut io.Writer) (*Server, error) {
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	if errOut != nil {
		fs.SetOutput(errOut)
	}

	listen := fs.String("listen", "0.0.0.0:56000", "локальный адрес прослушивания ip:port")
	connect := fs.String("connect", "", "локальный бэкенд host:port; обязательно: WG 127.0.0.1:51820 | Xray 127.0.0.1:443")
	mode := fs.String("mode", "udp", "режим туннеля: udp (WireGuard) | tcp (Xray/sing-box; bond авто)")
	obfProfile := fs.String("obf-profile", string(ObfProfileNone), "wire-профиль обфускации: none | rtpopus | rtpopus2 | rtpopus3; должен совпадать с клиентом")
	obfKey := fs.String("obf-key", "", "ключ для -obf-profile != none: 32 байта hex (64 символа)")
	genObfKey := fs.Bool("gen-obf-key", false, "напечатать новый -obf-key и выйти")
	obfTiming := fs.Duration("obf-timing", 0, "межпакетная задержка для RTP-мимикрии (напр. 10ms); 0=выкл")
	debug := fs.Bool("debug", false, "подробные debug-логи")
	clientsFile := fs.String("clients-file", "", "путь к файлу clients.json для авторизации по Client ID")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	s := &Server{
		Obf: ObfOpts{
			Profile: ObfProfile(*obfProfile),
			GenKey:  *genObfKey,
			Timing:  *obfTiming,
		},
		Proxy: ProxyOpts{
			Mode:    serverProxyMode(*mode),
			Listen:  *listen,
			Connect: *connect,
		},
		Log: LogOpts{
			Debug: *debug,
		},
		KCP: KCPOpts{
			Profile: kcptun.DefaultProfile(),
			FEC:     kcptun.FEC{},
		},
		ClientsFile: *clientsFile,
	}

	switch *mode {
	case "udp", "tcp":
	default:
		return nil, fmt.Errorf("invalid -mode value %q: must be udp | tcp", *mode)
	}

	if s.Obf.GenKey {
		return s, nil
	}

	if s.Proxy.Connect == "" {
		return nil, fmt.Errorf("server address is required")
	}
	if err := validateObfProfile(s.Obf.Profile); err != nil {
		return nil, err
	}
	key, err := rtpopus.DecodeKey(s.Obf.Enabled(), *obfKey)
	if err != nil {
		return nil, err
	}
	s.Obf.Key = key
	if err := validateObfTiming(s.Obf, s.Proxy.Mode); err != nil {
		return nil, err
	}

	return s, nil
}

// validateObfTiming ограничивает -obf-timing UDP-релеем с включённой обфускацией:
// без RTP-профиля паковать нечего, а в tcp-режиме pacing ломает RTT/конгешн KCP.
func validateObfTiming(o ObfOpts, mode ProxyMode) error {
	if o.Timing <= 0 {
		return nil
	}
	if !o.Enabled() {
		return errors.New("-obf-timing requires -obf-profile != none")
	}
	if mode != ProxyModeUDP {
		return errors.New("-obf-timing supported only with -mode udp")
	}
	return nil
}

// validateObfProfile проверяет что -obf-profile содержит известное значение.
func validateObfProfile(p ObfProfile) error {
	switch p {
	case ObfProfileNone, ObfProfileRTPOpus, ObfProfileRTPOpus2, ObfProfileRTPOpus3:
		return nil
	default:
		return fmt.Errorf("invalid -obf-profile value %q: must be %s | %s | %s | %s", p, ObfProfileNone, ObfProfileRTPOpus, ObfProfileRTPOpus2, ObfProfileRTPOpus3)
	}
}

func ClientProxyMode(mode string, bond bool) ProxyMode {
	switch {
	case mode == "tcp" && bond:
		return ProxyModeTCPFwdBond
	case mode == "tcp":
		return ProxyModeTCPFwd
	default:
		return ProxyModeUDP
	}
}

func serverProxyMode(mode string) ProxyMode {
	if mode == "tcp" {
		return ProxyModeTCPFwd
	}
	return ProxyModeUDP
}
