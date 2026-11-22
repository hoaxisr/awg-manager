package openflux

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

const (
	// Transport priorities (higher = tried first in session mode).
	PriorityWebdav     = 110
	PriorityMailru     = 100
	PriorityVyandex    = 95
	PriorityYandex     = 90
	PriorityBoards     = 88
	PriorityAvito      = 85
	PriorityCupsonline = 80
	PriorityTelemost   = 75
	PriorityOneme      = 70
	PriorityDirect     = 50

	DefaultDirectPort       = 8445
	DefaultClientSocks5Port = 1085
	DefaultClientHttpPort   = 1086
	MinSecretChars          = 16
)

// DefaultTransportPriority returns the default session priority for a transport name.
func DefaultTransportPriority(t string) int {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "webdav":
		return PriorityWebdav
	case "mailru":
		return PriorityMailru
	case "vyandex":
		return PriorityVyandex
	case "yandex":
		return PriorityYandex
	case "boards":
		return PriorityBoards
	case "avito":
		return PriorityAvito
	case "cupsonline":
		return PriorityCupsonline
	case "telemost":
		return PriorityTelemost
	case "oneme":
		return PriorityOneme
	case "direct":
		return PriorityDirect
	default:
		return PriorityDirect
	}
}

// Config stores persistent settings for the OpenFlux exit node service.
type Config struct {
	Enabled             bool           `json:"enabled"`
	Mode                string         `json:"mode"`          // "l4" | "l3"
	TransportMode       string         `json:"transportMode"` // "session" | "single"
	Transports          []string       `json:"transports"`
	TransportPriorities map[string]int `json:"transportPriorities,omitempty"`
	WebdavURL           string         `json:"webdavUrl,omitempty"`
	WebdavUser          string         `json:"webdavUser,omitempty"`
	WebdavPassword      string         `json:"webdavPassword,omitempty"`
	MailruURL           string         `json:"mailruUrl,omitempty"`
	MailruCookies       string         `json:"mailruCookies,omitempty"`
	YandexURL           string         `json:"yandexUrl,omitempty"`
	VyandexURL          string         `json:"vyandexUrl,omitempty"`
	BoardsURL           string         `json:"boardsUrl,omitempty"`
	AvitoURL            string         `json:"avitoUrl,omitempty"`
	AvitoCookies        string         `json:"avitoCookies,omitempty"`
	CupsonlineURL       string         `json:"cupsonlineUrl,omitempty"`
	TelemostURL         string         `json:"telemostUrl,omitempty"`
	OnemeToken          string         `json:"onemeToken,omitempty"`
	OnemeUID            string         `json:"onemeUid,omitempty"`
	DirectListen        string         `json:"directListen,omitempty"`
	DirectHost          string         `json:"directHost,omitempty"`
	Secret              string         `json:"secret,omitempty"`
	Codec               string         `json:"codec,omitempty"` // "batched" | "legacy"
	UpstreamProxy       string         `json:"upstreamProxy,omitempty"`
	LowMemory           bool           `json:"lowMemory"`
	Lanes               int            `json:"lanes,omitempty"`
	NativeScriptsDir    string         `json:"nativeScriptsDir,omitempty"`
	OpenFirewall        bool           `json:"openFirewall"`
	Debug               int            `json:"debug"`
	Name                string         `json:"name,omitempty"`

	// Routing, NAT and Egress matching WDTT server
	NatMode          string   `json:"natMode,omitempty"`      // "full" (default), "internet-only", "none"
	NatStaticWAN     string   `json:"natStaticWan,omitempty"` // legacy single WAN
	NatStaticWANs    []string `json:"natStaticWans,omitempty"`
	Policy           string   `json:"policy,omitempty"`
	LANSegments      []string `json:"lanSegments,omitempty"`
	ExposeToPolicies bool     `json:"exposeToPolicies"`
	EgressInterface  string   `json:"egressInterface,omitempty"`
	PolicyIP         string   `json:"-"`
	NdmsIface        string   `json:"ndmsIface,omitempty"`
	TunIface         string   `json:"tunIface,omitempty"`
	TunMTU           int      `json:"tunMtu,omitempty"`
}

// DefaultConfig returns sensible defaults for running the OpenFlux exit node on a router.
func DefaultConfig() Config {
	secret, _ := GenerateSecret()
	return Config{
		Enabled:       false,
		Mode:          "l4",
		TransportMode: "session",
		Transports:    []string{"mailru", "yandex", "vyandex"},
		DirectListen:  fmt.Sprintf("0.0.0.0:%d", DefaultDirectPort),
		Secret:        secret,
		Codec:         "batched",
		LowMemory:     true,
		OpenFirewall:  true,
		Debug:         0,
		Name:          "OpenFlux",
		NatMode:       "full",
	}
}

// StaticNATList returns the active WAN interfaces configured for internet-only NAT.
func (c *Config) StaticNATList() []string {
	if len(c.NatStaticWANs) > 0 {
		return c.NatStaticWANs
	}
	if w := strings.TrimSpace(c.NatStaticWAN); w != "" {
		return []string{w}
	}
	return nil
}

// GenerateSecret generates a secure random 32-character hexadecimal secret.
func GenerateSecret() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// SecretChars counts UTF-16 code units (matching OpenFlux core requirement).
func SecretChars(s string) int {
	return len(utf16.Encode([]rune(s)))
}

// Validate checks that the configuration is valid for running the exit node.
func (c *Config) Validate() error {
	mode := strings.ToLower(strings.TrimSpace(c.Mode))
	if mode != "" && mode != "l4" && mode != "l3" {
		return fmt.Errorf("недопустимый режим %q: ожидается l4 или l3", c.Mode)
	}
	if mode == "l3" && strings.TrimSpace(c.UpstreamProxy) != "" {
		return fmt.Errorf("upstreamProxy поддерживается только в режиме l4")
	}
	if c.Lanes < 0 || c.Lanes > 16 {
		return fmt.Errorf("недопустимое число потоков lanes (%d): ожидается от 0 до 16", c.Lanes)
	}

	if c.TransportMode != "single" && c.TransportMode != "session" && c.TransportMode != "" {
		return fmt.Errorf("недопустимый режим транспорта %q: ожидается session или single", c.TransportMode)
	}

	isSession := c.TransportMode == "session" || c.TransportMode == "" || len(c.Transports) > 1
	if isSession {
		if strings.TrimSpace(c.Secret) == "" {
			return fmt.Errorf("для сессионного режима требуется общий ключ шифрования (минимум %d символов)", MinSecretChars)
		}
		if SecretChars(strings.TrimSpace(c.Secret)) < MinSecretChars {
			return fmt.Errorf("ключ шифрования слишком короткий (%d символов, требуется минимум %d)", SecretChars(c.Secret), MinSecretChars)
		}
	} else if strings.TrimSpace(c.Secret) != "" && SecretChars(strings.TrimSpace(c.Secret)) < MinSecretChars {
		return fmt.Errorf("ключ шифрования должен содержать не менее %d символов", MinSecretChars)
	}

	hasDirect := false
	if isSession {
		for _, t := range c.Transports {
			if strings.EqualFold(strings.TrimSpace(t), "direct") {
				hasDirect = true
				break
			}
		}
	} else if len(c.Transports) > 0 && strings.EqualFold(strings.TrimSpace(c.Transports[0]), "direct") {
		hasDirect = true
	}

	if hasDirect {
		dl := strings.TrimSpace(c.DirectListen)
		if dl == "" {
			return fmt.Errorf("для транспорта direct не указан адрес прослушивания (например, 0.0.0.0:%d)", DefaultDirectPort)
		}
		host, portStr, err := net.SplitHostPort(dl)
		if err != nil {
			return fmt.Errorf("некорректный адрес directListen %q: %w", dl, err)
		}
		port, err := strconv.Atoi(portStr)
		if err != nil || port <= 0 || port > 65535 {
			return fmt.Errorf("некорректный порт в directListen %q", dl)
		}
		switch port {
		case 22, 2222, 1099, 51820:
			return fmt.Errorf("порт %d зарезервирован системой", port)
		}
		_ = host
	}

	switch strings.ToLower(strings.TrimSpace(c.NatMode)) {
	case "full", "none", "":
	case "internet-only":
		if len(c.StaticNATList()) == 0 {
			return fmt.Errorf("natMode internet-only: не выбран WAN (natStaticWans)")
		}
	default:
		return fmt.Errorf("natMode %q: ожидали full|internet-only|none", c.NatMode)
	}

	return nil
}

// DirectPort returns the TCP port number from DirectListen, or DefaultDirectPort on error.
func (c *Config) DirectPort() int {
	_, portStr, err := net.SplitHostPort(strings.TrimSpace(c.DirectListen))
	if err != nil {
		return DefaultDirectPort
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return DefaultDirectPort
	}
	return port
}

// ResolveInterfaceIPv4 returns the first non-loopback IPv4 address for an interface name or IP string.
func ResolveInterfaceIPv4(target string) string {
	s := strings.TrimSpace(target)
	if s == "" {
		return ""
	}
	if ip := net.ParseIP(s); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			return ip4.String()
		}
		return ""
	}
	ifi, err := net.InterfaceByName(s)
	if err != nil {
		return ""
	}
	addrs, err := ifi.Addrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok {
			if ip4 := ipnet.IP.To4(); ip4 != nil && !ip4.IsLoopback() {
				return ip4.String()
			}
		}
	}
	return ""
}

// sanitizeContextURL parses raw URL and strips user credentials (user:pass@)
// so that sensitive passwords never leak into --session-context or share context.
func sanitizeContextURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	parsed.User = nil
	return parsed.String()
}

func parseWebdavURL(raw, user, pass string) (cleanURL, outUser, outPass string) {
	cleanURL = strings.TrimSpace(raw)
	outUser = strings.TrimSpace(user)
	outPass = strings.TrimSpace(pass)
	if parsed, err := url.Parse(cleanURL); err == nil && parsed.User != nil {
		if outUser == "" {
			outUser = parsed.User.Username()
		}
		if outPass == "" {
			outPass, _ = parsed.User.Password()
		}
		parsed.User = nil
		cleanURL = parsed.String()
	}
	return cleanURL, outUser, outPass
}

// NormalizeMultiURL normalizes a single URL or multi-link striped URL list
// separated by commas, pipes, or whitespace/newlines into a compact
// comma-separated string compatible with OpenFlux's transport registry.
func NormalizeMultiURL(raw string) string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '|' || r == ' ' || r == '\n' || r == '\t' || r == '\r'
	})
	var clean []string
	for _, f := range fields {
		if u := strings.TrimSpace(f); u != "" {
			clean = append(clean, u)
		}
	}
	return strings.Join(clean, ",")
}

// NormalizeURLs normalizes multi-link transport URL fields in place.
func (c *Config) NormalizeURLs() {
	c.MailruURL = NormalizeMultiURL(c.MailruURL)
	c.YandexURL = NormalizeMultiURL(c.YandexURL)
	c.VyandexURL = NormalizeMultiURL(c.VyandexURL)
	c.BoardsURL = NormalizeMultiURL(c.BoardsURL)
	c.AvitoURL = NormalizeMultiURL(c.AvitoURL)
	c.CupsonlineURL = NormalizeMultiURL(c.CupsonlineURL)
	c.TelemostURL = NormalizeMultiURL(c.TelemostURL)
}

// NormalizeURLs normalizes multi-link transport URL fields in place.
func (c *ClientConfig) NormalizeURLs() {
	c.MailruURL = NormalizeMultiURL(c.MailruURL)
	c.YandexURL = NormalizeMultiURL(c.YandexURL)
	c.VyandexURL = NormalizeMultiURL(c.VyandexURL)
	c.BoardsURL = NormalizeMultiURL(c.BoardsURL)
	c.AvitoURL = NormalizeMultiURL(c.AvitoURL)
	c.CupsonlineURL = NormalizeMultiURL(c.CupsonlineURL)
	c.TelemostURL = NormalizeMultiURL(c.TelemostURL)
}

// BuildArgsOptions supplies runtime file paths for sensitive or persistent CLI flags.
type BuildArgsOptions struct {
	MailruCookiesFile string
	CookieStoreFile   string
}

func appendCookieArgs(args []string, mailruCookies string, opts BuildArgsOptions) []string {
	if csFile := strings.TrimSpace(opts.CookieStoreFile); csFile != "" {
		args = append(args, "--cookie-store="+csFile)
	}
	if mcFile := strings.TrimSpace(opts.MailruCookiesFile); mcFile != "" {
		args = append(args, "--mailru-cookies="+mcFile)
	} else if mc := strings.TrimSpace(mailruCookies); mc != "" {
		args = append(args, "--mailru-cookies="+mc)
	}
	return args
}

// BuildArgs converts Config to OpenFlux CLI invocation arguments.
func (c *Config) BuildArgs(keyFilePath string) []string {
	return c.BuildArgsWithOptions(keyFilePath, BuildArgsOptions{})
}

// BuildArgsWithOptions converts Config to OpenFlux CLI arguments using optional runtime file paths.
func (c *Config) BuildArgsWithOptions(keyFilePath string, opts BuildArgsOptions) []string {
	args := []string{
		"--role=exit",
	}

	mode := strings.ToLower(strings.TrimSpace(c.Mode))
	if mode == "" {
		mode = "l4"
	}
	args = append(args, "--mode="+mode)

	if mode == "l4" {
		if up := strings.TrimSpace(c.UpstreamProxy); up != "" {
			args = append(args, "--upstream-proxy="+up)
		}
	} else {
		tunName := strings.TrimSpace(c.TunIface)
		if tunName == "" && strings.TrimSpace(c.NdmsIface) != "" {
			tunName = strings.ToLower(strings.TrimSpace(c.NdmsIface))
		}
		if tunName != "" {
			args = append(args, "--tun-name="+tunName)
		}
		if c.TunMTU > 0 {
			args = append(args, fmt.Sprintf("--tun-mtu=%d", c.TunMTU))
		}
	}

	egressTarget := strings.TrimSpace(c.EgressInterface)
	if egressTarget == "" && c.NatMode == "internet-only" && len(c.StaticNATList()) > 0 {
		egressTarget = strings.TrimSpace(c.StaticNATList()[0])
	}
	if egressTarget != "" {
		if ip := ResolveInterfaceIPv4(egressTarget); ip != "" {
			args = append(args, "--local-ip="+ip)
		}
	} else if strings.TrimSpace(c.PolicyIP) != "" {
		args = append(args, "--local-ip="+strings.TrimSpace(c.PolicyIP))
	}

	codec := strings.ToLower(strings.TrimSpace(c.Codec))
	if codec == "" {
		codec = "batched"
	}
	args = append(args, "--codec="+codec)

	if c.LowMemory {
		args = append(args, "--low-memory")
	}
	if c.Lanes >= 2 {
		args = append(args, fmt.Sprintf("--lanes=%d", c.Lanes))
	}
	if dir := strings.TrimSpace(c.NativeScriptsDir); dir != "" {
		args = append(args, "--native-scripts="+dir)
	}
	if cookies := strings.TrimSpace(c.AvitoCookies); cookies != "" {
		args = append(args, "--avito-cookies="+cookies)
	}
	args = appendCookieArgs(args, c.MailruCookies, opts)

	isSession := c.TransportMode == "session" || c.TransportMode == "" || len(c.Transports) > 1

	if isSession {
		args = append(args, "--negotiate")
		if keyFilePath != "" {
			args = append(args, "--encryption-key-file="+keyFilePath)
		}
		contextURL := "http://#"
		if strings.TrimSpace(c.WebdavURL) != "" {
			contextURL = sanitizeContextURL(c.WebdavURL)
		} else if u := NormalizeMultiURL(c.MailruURL); u != "" {
			contextURL = u
		} else if u := NormalizeMultiURL(c.YandexURL); u != "" {
			contextURL = u
		} else if u := NormalizeMultiURL(c.VyandexURL); u != "" {
			contextURL = u
		} else if u := NormalizeMultiURL(c.BoardsURL); u != "" {
			contextURL = u
		} else if u := NormalizeMultiURL(c.AvitoURL); u != "" {
			contextURL = u
		} else if u := NormalizeMultiURL(c.CupsonlineURL); u != "" {
			contextURL = u
		} else if u := NormalizeMultiURL(c.TelemostURL); u != "" {
			contextURL = u
		}
		args = append(args, "--session-context="+contextURL)

		type transportPrioItem struct {
			part string
			prio int
		}
		var transportItems []transportPrioItem
		var hasDirect, hasWebdav, hasMailru, hasYandex, hasVyandex, hasBoards, hasAvito, hasCups, hasTelemost, hasOneme bool

		for _, t := range c.Transports {
			norm := strings.ToLower(strings.TrimSpace(t))
			prio := DefaultTransportPriority(norm)
			if c.TransportPriorities != nil {
				if custom, ok := c.TransportPriorities[norm]; ok && custom > 0 {
					prio = custom
				}
			}
			switch norm {
			case "webdav":
				if strings.TrimSpace(c.WebdavURL) != "" {
					transportItems = append(transportItems, transportPrioItem{part: fmt.Sprintf("webdav:%d", prio), prio: prio})
					hasWebdav = true
				}
			case "direct":
				transportItems = append(transportItems, transportPrioItem{part: fmt.Sprintf("direct:%d", prio), prio: prio})
				hasDirect = true
			case "mailru":
				transportItems = append(transportItems, transportPrioItem{part: fmt.Sprintf("mailru:%d", prio), prio: prio})
				hasMailru = true
			case "yandex":
				transportItems = append(transportItems, transportPrioItem{part: fmt.Sprintf("yandex:%d", prio), prio: prio})
				hasYandex = true
			case "vyandex":
				transportItems = append(transportItems, transportPrioItem{part: fmt.Sprintf("vyandex:%d", prio), prio: prio})
				hasVyandex = true
			case "boards":
				transportItems = append(transportItems, transportPrioItem{part: fmt.Sprintf("boards:%d", prio), prio: prio})
				hasBoards = true
			case "avito":
				transportItems = append(transportItems, transportPrioItem{part: fmt.Sprintf("avito:%d", prio), prio: prio})
				hasAvito = true
			case "cupsonline":
				transportItems = append(transportItems, transportPrioItem{part: fmt.Sprintf("cupsonline:%d", prio), prio: prio})
				hasCups = true
			case "telemost":
				transportItems = append(transportItems, transportPrioItem{part: fmt.Sprintf("telemost:%d", prio), prio: prio})
				hasTelemost = true
			case "oneme":
				if strings.TrimSpace(c.OnemeToken) != "" && strings.TrimSpace(c.OnemeUID) != "" {
					transportItems = append(transportItems, transportPrioItem{part: fmt.Sprintf("oneme:%d", prio), prio: prio})
					hasOneme = true
				}
			}
		}

		sort.SliceStable(transportItems, func(i, j int) bool {
			return transportItems[i].prio > transportItems[j].prio
		})

		var parts []string
		for _, item := range transportItems {
			parts = append(parts, item.part)
		}
		if len(parts) > 0 {
			args = append(args, "--transports="+strings.Join(parts, ","))
		}

		if hasDirect {
			dl := strings.TrimSpace(c.DirectListen)
			if dl == "" {
				dl = fmt.Sprintf("0.0.0.0:%d", DefaultDirectPort)
			}
			args = append(args, "--direct-listen="+dl)
		}
		if hasWebdav && strings.TrimSpace(c.WebdavURL) != "" {
			webdavURL, webdavUser, webdavPass := parseWebdavURL(c.WebdavURL, c.WebdavUser, c.WebdavPassword)
			args = append(args, "--webdav-url="+webdavURL)
			if webdavUser != "" {
				args = append(args, "--webdav-user="+webdavUser)
			}
			if webdavPass != "" {
				args = append(args, "--webdav-pass="+webdavPass)
			}
		}
		if hasMailru && NormalizeMultiURL(c.MailruURL) != "" {
			args = append(args, "--mailru-url="+NormalizeMultiURL(c.MailruURL))
		}
		if hasYandex && NormalizeMultiURL(c.YandexURL) != "" {
			args = append(args, "--yandex-url="+NormalizeMultiURL(c.YandexURL))
		}
		if hasVyandex {
			vyURL := NormalizeMultiURL(c.VyandexURL)
			if vyURL == "" {
				vyURL = NormalizeMultiURL(c.YandexURL)
			}
			if vyURL != "" {
				args = append(args, "--vyandex-url="+vyURL)
			}
		}
		if hasBoards && NormalizeMultiURL(c.BoardsURL) != "" {
			args = append(args, "--boards-url="+NormalizeMultiURL(c.BoardsURL))
		}
		if hasAvito && NormalizeMultiURL(c.AvitoURL) != "" {
			args = append(args, "--avito-url="+NormalizeMultiURL(c.AvitoURL))
		}
		if hasCups && NormalizeMultiURL(c.CupsonlineURL) != "" {
			args = append(args, "--cupsonline-url="+NormalizeMultiURL(c.CupsonlineURL))
		}
		if hasTelemost && NormalizeMultiURL(c.TelemostURL) != "" {
			args = append(args, "--telemost-url="+NormalizeMultiURL(c.TelemostURL))
		}
		if hasOneme {
			args = append(args, "--oneme-token="+strings.TrimSpace(c.OnemeToken))
			args = append(args, "--oneme-uid="+strings.TrimSpace(c.OnemeUID))
		}
	} else {
		singleType := "mailru"
		if len(c.Transports) > 0 {
			singleType = strings.ToLower(strings.TrimSpace(c.Transports[0]))
		}
		args = append(args, "--transport="+singleType)

		if keyFilePath != "" && strings.TrimSpace(c.Secret) != "" {
			args = append(args, "--encryption-key-file="+keyFilePath)
		}

		switch singleType {
		case "webdav":
			if strings.TrimSpace(c.WebdavURL) != "" {
				webdavURL, webdavUser, webdavPass := parseWebdavURL(c.WebdavURL, c.WebdavUser, c.WebdavPassword)
				args = append(args, "--url="+webdavURL)
				if webdavUser != "" {
					args = append(args, "--webdav-user="+webdavUser)
				}
				if webdavPass != "" {
					args = append(args, "--webdav-pass="+webdavPass)
				}
			}
		case "direct":
			dl := strings.TrimSpace(c.DirectListen)
			if dl == "" {
				dl = fmt.Sprintf("0.0.0.0:%d", DefaultDirectPort)
			}
			args = append(args, "--direct-listen="+dl)
		case "mailru":
			if u := NormalizeMultiURL(c.MailruURL); u != "" {
				args = append(args, "--url="+u)
			}
		case "yandex":
			if u := NormalizeMultiURL(c.YandexURL); u != "" {
				args = append(args, "--url="+u)
			}
		case "vyandex":
			vyURL := NormalizeMultiURL(c.VyandexURL)
			if vyURL == "" {
				vyURL = NormalizeMultiURL(c.YandexURL)
			}
			if vyURL != "" {
				args = append(args, "--url="+vyURL)
			}
		case "boards":
			if u := NormalizeMultiURL(c.BoardsURL); u != "" {
				args = append(args, "--url="+u)
			}
		case "avito":
			if u := NormalizeMultiURL(c.AvitoURL); u != "" {
				args = append(args, "--url="+u)
			}
		case "cupsonline":
			if u := NormalizeMultiURL(c.CupsonlineURL); u != "" {
				args = append(args, "--url="+u)
			}
		case "telemost":
			if u := NormalizeMultiURL(c.TelemostURL); u != "" {
				args = append(args, "--url="+u)
			}
		case "oneme":
			if strings.TrimSpace(c.OnemeToken) != "" {
				args = append(args, "--maxToken="+strings.TrimSpace(c.OnemeToken))
			}
			if strings.TrimSpace(c.OnemeUID) != "" {
				args = append(args, "--maxUid="+strings.TrimSpace(c.OnemeUID))
			}
		}
	}

	switch c.Debug {
	case 1:
		args = append(args, "-d")
	case 2:
		args = append(args, "-dd")
	case 3:
		args = append(args, "-ddd")
	}

	return args
}

// ClientConfig represents OpenFlux client configuration (OpkgTun L3 interface or SOCKS5 / HTTP proxy outbound on router).
type ClientConfig struct {
	Enabled          bool     `json:"enabled"`
	Name             string   `json:"name,omitempty"`
	InboundMode      string   `json:"inboundMode,omitempty"` // "tun" (default, L3 OpkgTun interface) | "socks5" (L4 local proxy)
	NdmsIface        string   `json:"ndmsIface,omitempty"`   // NDMS interface name, e.g. "OpkgTun2"
	TunIface         string   `json:"tunIface,omitempty"`    // Linux TUN interface name, e.g. "opkgtun2"
	TunAddr          string   `json:"tunAddr,omitempty"`     // IPv4 CIDR, default "10.10.10.2/24"
	TunMTU           int      `json:"tunMtu,omitempty"`      // MTU, default 1280
	Policy           string   `json:"policy,omitempty"`      // Keenetic Access Policy name, e.g. "Policy0" or ""
	ExposeToPolicies bool     `json:"exposeToPolicies"`      // "ip global auto" in NDMS (default true)
	DefaultRoute     bool     `json:"defaultRoute"`          // Candidate for default route in NDMS
	TransportMode    string   `json:"transportMode"`         // "session" (default, --negotiate) | "single" | "stream"
	Transports       []string `json:"transports"`            // enabled transports: "direct", "webdav", "mailru", "yandex", "vyandex", "boards", "avito", "cupsonline", "telemost", "oneme"
	DirectDial       string   `json:"directDial,omitempty"`
	WebdavURL        string   `json:"webdavUrl,omitempty"`
	WebdavUser       string   `json:"webdavUser,omitempty"`
	WebdavPassword   string   `json:"webdavPassword,omitempty"`
	MailruURL        string   `json:"mailruUrl,omitempty"`
	MailruCookies    string   `json:"mailruCookies,omitempty"`
	YandexURL        string   `json:"yandexUrl,omitempty"`
	VyandexURL       string   `json:"vyandexUrl,omitempty"`
	BoardsURL        string   `json:"boardsUrl,omitempty"`
	AvitoURL         string   `json:"avitoUrl,omitempty"`
	AvitoCookies     string   `json:"avitoCookies,omitempty"`
	CupsonlineURL    string   `json:"cupsonlineUrl,omitempty"`
	TelemostURL      string   `json:"telemostUrl,omitempty"`
	OnemeToken       string   `json:"onemeToken,omitempty"`
	OnemeUID         string   `json:"onemeUid,omitempty"`
	Secret           string   `json:"secret,omitempty"`
	Context          string   `json:"context,omitempty"`
	Codec            string   `json:"codec,omitempty"` // "batched" (default) | "legacy"
	LowMemory        bool     `json:"lowMemory"`
	Lanes            int      `json:"lanes,omitempty"`
	NativeScriptsDir string   `json:"nativeScriptsDir,omitempty"`
	Socks5Listen     string   `json:"socks5Listen,omitempty"` // default "127.0.0.1:1085"
	HttpProxyListen  string   `json:"httpProxyListen,omitempty"`
	AutoReconnect    bool     `json:"autoReconnect"`
	Debug            int      `json:"debug"`
}

// DefaultClientConfig returns recommended default settings for an OpenFlux client on a router.
func DefaultClientConfig() ClientConfig {
	return ClientConfig{
		Enabled:          false,
		Name:             "OpenFlux Выход",
		InboundMode:      "tun",
		NdmsIface:        "",
		TunIface:         "",
		TunAddr:          "10.10.10.2/24",
		TunMTU:           1280,
		Policy:           "",
		ExposeToPolicies: true,
		DefaultRoute:     false,
		TransportMode:    "session",
		Transports:       []string{"mailru", "yandex", "vyandex"},
		Codec:            "batched",
		LowMemory:        true,
		Socks5Listen:     fmt.Sprintf("127.0.0.1:%d", DefaultClientSocks5Port),
		AutoReconnect:    true,
		Debug:            0,
	}
}

func validateListenAddr(addr string, defaultPort int, fieldName string) (int, error) {
	a := strings.TrimSpace(addr)
	if a == "" {
		return defaultPort, nil
	}
	if strings.HasPrefix(a, ":") {
		a = "127.0.0.1" + a
	}
	_, portStr, err := net.SplitHostPort(a)
	if err != nil {
		return 0, fmt.Errorf("некорректный адрес %s %q: %w", fieldName, addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return 0, fmt.Errorf("некорректный порт в %s %q", fieldName, addr)
	}
	switch port {
	case 22, 2222, 53, 80, 443, 1099, 8443, 9090, 51820:
		return 0, fmt.Errorf("порт %d зарезервирован системой", port)
	}
	return port, nil
}

// Validate checks structural validity of ClientConfig. If Enabled is true, it also checks readiness for start.
func (c *ClientConfig) Validate() error {
	inbound := strings.ToLower(strings.TrimSpace(c.InboundMode))
	if inbound != "" && inbound != "tun" && inbound != "socks5" {
		return fmt.Errorf("недопустимый режим входящего трафика %q: ожидается tun или socks5", c.InboundMode)
	}

	tm := strings.ToLower(strings.TrimSpace(c.TransportMode))
	if tm != "" && tm != "session" && tm != "single" && tm != "stream" {
		return fmt.Errorf("недопустимый режим транспорта %q: ожидается session, single или stream", c.TransportMode)
	}

	codec := strings.ToLower(strings.TrimSpace(c.Codec))
	if codec != "" && codec != "batched" && codec != "legacy" {
		return fmt.Errorf("недопустимый кодек %q: ожидается batched или legacy", c.Codec)
	}

	if c.Lanes < 0 || c.Lanes > 16 {
		return fmt.Errorf("недопустимое число потоков lanes (%d): ожидается от 0 до 16", c.Lanes)
	}

	if inbound == "tun" || inbound == "" {
		if c.TunMTU < 0 || (c.TunMTU > 0 && (c.TunMTU < 576 || c.TunMTU > 1500)) {
			return fmt.Errorf("некорректный MTU %d (ожидается от 576 до 1500)", c.TunMTU)
		}
		if addr := strings.TrimSpace(c.TunAddr); addr != "" {
			if _, _, err := net.ParseCIDR(addr); err != nil {
				if ip := net.ParseIP(addr); ip == nil {
					return fmt.Errorf("некорректный IP адрес туннеля %q (ожидается CIDR, например 10.10.10.2/24)", addr)
				}
			}
		}
	}

	socksPort, err := validateListenAddr(c.Socks5Listen, DefaultClientSocks5Port, "socks5Listen")
	if err != nil {
		return err
	}

	if strings.TrimSpace(c.HttpProxyListen) != "" {
		httpPort, err := validateListenAddr(c.HttpProxyListen, DefaultClientHttpPort, "httpProxyListen")
		if err != nil {
			return err
		}
		if httpPort == socksPort {
			return fmt.Errorf("порты SOCKS5 и HTTP-прокси не должны совпадать (%d)", socksPort)
		}
	}

	if dd := strings.TrimSpace(c.DirectDial); dd != "" {
		host, portStr, err := net.SplitHostPort(dd)
		if err != nil || strings.TrimSpace(host) == "" {
			return fmt.Errorf("некорректный адрес directDial %q (ожидается host:port)", dd)
		}
		port, err := strconv.Atoi(portStr)
		if err != nil || port <= 0 || port > 65535 {
			return fmt.Errorf("некорректный порт в directDial %q", dd)
		}
	}

	if sec := strings.TrimSpace(c.Secret); sec != "" && SecretChars(sec) < MinSecretChars {
		return fmt.Errorf("ключ шифрования слишком короткий (%d символов, требуется минимум %d)", SecretChars(sec), MinSecretChars)
	}

	if c.Enabled {
		return c.ValidateForStart()
	}
	return nil
}

// ValidateForStart checks that the client configuration has all required fields to establish a connection.
func (c *ClientConfig) ValidateForStart() error {
	tm := strings.ToLower(strings.TrimSpace(c.TransportMode))
	if tm == "" {
		tm = "session"
	}

	if tm == "stream" {
		if strings.TrimSpace(c.MailruURL) == "" && strings.TrimSpace(c.CupsonlineURL) == "" {
			return fmt.Errorf("для stream-режима укажите URL документа Mail.ru или комнаты Cups.online")
		}
		return nil
	}

	isSession := tm == "session" || len(c.Transports) > 1
	if isSession {
		sec := strings.TrimSpace(c.Secret)
		if sec == "" {
			return fmt.Errorf("для сессионного режима требуется общий ключ шифрования (минимум %d символов)", MinSecretChars)
		}
		if SecretChars(sec) < MinSecretChars {
			return fmt.Errorf("ключ шифрования слишком короткий (%d символов, требуется минимум %d)", SecretChars(sec), MinSecretChars)
		}
	}

	hasUsableTransport := false
	for _, t := range c.Transports {
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "direct":
			if strings.TrimSpace(c.DirectDial) != "" {
				hasUsableTransport = true
			}
		case "webdav":
			if strings.TrimSpace(c.WebdavURL) != "" {
				hasUsableTransport = true
			}
		case "mailru":
			if strings.TrimSpace(c.MailruURL) != "" {
				hasUsableTransport = true
			}
		case "vyandex":
			if strings.TrimSpace(c.VyandexURL) != "" || strings.TrimSpace(c.YandexURL) != "" {
				hasUsableTransport = true
			}
		case "yandex":
			if strings.TrimSpace(c.YandexURL) != "" {
				hasUsableTransport = true
			}
		case "boards":
			if strings.TrimSpace(c.BoardsURL) != "" {
				hasUsableTransport = true
			}
		case "avito":
			if strings.TrimSpace(c.AvitoURL) != "" {
				hasUsableTransport = true
			}
		case "cupsonline":
			if strings.TrimSpace(c.CupsonlineURL) != "" {
				hasUsableTransport = true
			}
		case "telemost":
			if strings.TrimSpace(c.TelemostURL) != "" {
				hasUsableTransport = true
			}
		case "oneme":
			if strings.TrimSpace(c.OnemeToken) != "" && strings.TrimSpace(c.OnemeUID) != "" {
				hasUsableTransport = true
			}
		}
	}

	if !hasUsableTransport {
		return fmt.Errorf("нет настроенных транспортов: импортируйте ссылку openflux:// или укажите адрес Direct-сервера (host:port) либо URL облачного транспорта")
	}

	return nil
}

// Socks5Port returns the TCP port number from Socks5Listen, or DefaultClientSocks5Port.
func (c *ClientConfig) Socks5Port() int {
	a := strings.TrimSpace(c.Socks5Listen)
	if a == "" {
		return DefaultClientSocks5Port
	}
	if strings.HasPrefix(a, ":") {
		a = "127.0.0.1" + a
	}
	_, portStr, err := net.SplitHostPort(a)
	if err != nil {
		return DefaultClientSocks5Port
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return DefaultClientSocks5Port
	}
	return port
}

// Socks5URI returns a local socks5:// URI usable by sing-box, FIRC, or local clients.
func (c *ClientConfig) Socks5URI() string {
	port := c.Socks5Port()
	name := strings.TrimSpace(c.Name)
	if name == "" {
		name = "OpenFlux"
	}
	return fmt.Sprintf("socks5://127.0.0.1:%d#%s", port, url.PathEscape(name))
}

// HttpURI returns the local HTTP proxy URL if HttpProxyListen is configured.
func (c *ClientConfig) HttpURI() string {
	a := strings.TrimSpace(c.HttpProxyListen)
	if a == "" {
		return ""
	}
	if strings.HasPrefix(a, ":") {
		a = "127.0.0.1" + a
	}
	_, portStr, err := net.SplitHostPort(a)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("http://127.0.0.1:%s", portStr)
}

// BuildArgs converts ClientConfig to OpenFlux CLI arguments for --role=client.
func (c *ClientConfig) BuildArgs(keyFilePath string) []string {
	return c.BuildArgsWithOptions(keyFilePath, BuildArgsOptions{})
}

// BuildArgsWithOptions converts ClientConfig to OpenFlux CLI arguments using optional runtime file paths.
func (c *ClientConfig) BuildArgsWithOptions(keyFilePath string, opts BuildArgsOptions) []string {
	inbound := strings.ToLower(strings.TrimSpace(c.InboundMode))
	if inbound == "" {
		inbound = "tun"
	}

	args := []string{
		"--role=client",
	}

	if inbound == "tun" {
		args = append(args, "--inbound=tun")
		if tunIface := strings.TrimSpace(c.TunIface); tunIface != "" {
			args = append(args, "--tun-name="+tunIface)
		}
		if tunAddr := strings.TrimSpace(c.TunAddr); tunAddr != "" {
			args = append(args, "--tun-addr="+tunAddr)
		}
		if c.TunMTU > 0 {
			args = append(args, fmt.Sprintf("--tun-mtu=%d", c.TunMTU))
		}
	} else {
		args = append(args, "--inbound=socks5")
		socks := strings.TrimSpace(c.Socks5Listen)
		if socks == "" {
			socks = fmt.Sprintf("127.0.0.1:%d", DefaultClientSocks5Port)
		}
		args = append(args, "--socks5="+socks)

		if hp := strings.TrimSpace(c.HttpProxyListen); hp != "" {
			args = append(args, "--http-proxy="+hp)
		}
	}

	codec := strings.ToLower(strings.TrimSpace(c.Codec))
	if codec == "" {
		codec = "batched"
	}
	args = append(args, "--codec="+codec)

	if c.LowMemory {
		args = append(args, "--low-memory")
	}
	if c.Lanes >= 2 {
		args = append(args, fmt.Sprintf("--lanes=%d", c.Lanes))
	}
	if dir := strings.TrimSpace(c.NativeScriptsDir); dir != "" {
		args = append(args, "--native-scripts="+dir)
	}
	if cookies := strings.TrimSpace(c.AvitoCookies); cookies != "" {
		args = append(args, "--avito-cookies="+cookies)
	}
	args = appendCookieArgs(args, c.MailruCookies, opts)

	tm := strings.ToLower(strings.TrimSpace(c.TransportMode))
	if tm == "" {
		tm = "session"
	}

	type transportPrioItem struct {
		part string
		prio int
	}
	var transportItems []transportPrioItem
	var hasDirect, hasWebdav, hasMailru, hasYandex, hasVyandex, hasBoards, hasAvito, hasCups, hasTelemost, hasOneme bool

	for _, t := range c.Transports {
		norm := strings.ToLower(strings.TrimSpace(t))
		prio := DefaultTransportPriority(norm)
		switch norm {
		case "webdav":
			if strings.TrimSpace(c.WebdavURL) != "" {
				transportItems = append(transportItems, transportPrioItem{part: fmt.Sprintf("webdav:%d", prio), prio: prio})
				hasWebdav = true
			}
		case "direct":
			if strings.TrimSpace(c.DirectDial) != "" {
				transportItems = append(transportItems, transportPrioItem{part: fmt.Sprintf("direct:%d", prio), prio: prio})
				hasDirect = true
			}
		case "mailru":
			if strings.TrimSpace(c.MailruURL) != "" {
				transportItems = append(transportItems, transportPrioItem{part: fmt.Sprintf("mailru:%d", prio), prio: prio})
				hasMailru = true
			}
		case "yandex":
			if strings.TrimSpace(c.YandexURL) != "" {
				transportItems = append(transportItems, transportPrioItem{part: fmt.Sprintf("yandex:%d", prio), prio: prio})
				hasYandex = true
			}
		case "vyandex":
			vyURL := strings.TrimSpace(c.VyandexURL)
			if vyURL == "" {
				vyURL = strings.TrimSpace(c.YandexURL)
			}
			if vyURL != "" {
				transportItems = append(transportItems, transportPrioItem{part: fmt.Sprintf("vyandex:%d", prio), prio: prio})
				hasVyandex = true
			}
		case "boards":
			if strings.TrimSpace(c.BoardsURL) != "" {
				transportItems = append(transportItems, transportPrioItem{part: fmt.Sprintf("boards:%d", prio), prio: prio})
				hasBoards = true
			}
		case "avito":
			if strings.TrimSpace(c.AvitoURL) != "" {
				transportItems = append(transportItems, transportPrioItem{part: fmt.Sprintf("avito:%d", prio), prio: prio})
				hasAvito = true
			}
		case "cupsonline":
			if strings.TrimSpace(c.CupsonlineURL) != "" {
				transportItems = append(transportItems, transportPrioItem{part: fmt.Sprintf("cupsonline:%d", prio), prio: prio})
				hasCups = true
			}
		case "telemost":
			if strings.TrimSpace(c.TelemostURL) != "" {
				transportItems = append(transportItems, transportPrioItem{part: fmt.Sprintf("telemost:%d", prio), prio: prio})
				hasTelemost = true
			}
		case "oneme":
			if strings.TrimSpace(c.OnemeToken) != "" && strings.TrimSpace(c.OnemeUID) != "" {
				transportItems = append(transportItems, transportPrioItem{part: fmt.Sprintf("oneme:%d", prio), prio: prio})
				hasOneme = true
			}
		}
	}

	sort.SliceStable(transportItems, func(i, j int) bool {
		return transportItems[i].prio > transportItems[j].prio
	})

	var sessionParts []string
	for _, item := range transportItems {
		sessionParts = append(sessionParts, item.part)
	}

	if tm == "stream" {
		args = append(args, "--mode=stream")
		singleType := "mailru"
		if len(c.Transports) > 0 && strings.EqualFold(strings.TrimSpace(c.Transports[0]), "cupsonline") {
			singleType = "cupsonline"
		} else if NormalizeMultiURL(c.MailruURL) == "" && NormalizeMultiURL(c.CupsonlineURL) != "" {
			singleType = "cupsonline"
		}
		args = append(args, "--transport="+singleType)
		if singleType == "cupsonline" && NormalizeMultiURL(c.CupsonlineURL) != "" {
			args = append(args, "--url="+NormalizeMultiURL(c.CupsonlineURL))
		} else if u := NormalizeMultiURL(c.MailruURL); u != "" {
			args = append(args, "--url="+u)
		}
	} else if len(c.Transports) == 1 && strings.EqualFold(strings.TrimSpace(c.Transports[0]), "telemost") {
		singleType := "telemost"
		args = append(args, "--transport="+singleType)
		if keyFilePath != "" && strings.TrimSpace(c.Secret) != "" {
			args = append(args, "--encryption-key-file="+keyFilePath)
			if strings.TrimSpace(c.Context) != "" {
				args = append(args, "--session-context="+sanitizeContextURL(NormalizeMultiURL(c.Context)))
			}
		}
		if u := NormalizeMultiURL(c.TelemostURL); u != "" {
			args = append(args, "--url="+u)
		}
	} else if (tm == "session" || len(c.Transports) > 1) && len(sessionParts) > 0 {
		args = append(args, "--negotiate")
		if keyFilePath != "" {
			args = append(args, "--encryption-key-file="+keyFilePath)
		}

		contextURL := strings.TrimSpace(c.Context)
		if contextURL == "" {
			if strings.TrimSpace(c.WebdavURL) != "" {
				contextURL = sanitizeContextURL(c.WebdavURL)
			} else if u := NormalizeMultiURL(c.MailruURL); u != "" {
				contextURL = u
			} else if u := NormalizeMultiURL(c.YandexURL); u != "" {
				contextURL = u
			} else if u := NormalizeMultiURL(c.VyandexURL); u != "" {
				contextURL = u
			} else if u := NormalizeMultiURL(c.BoardsURL); u != "" {
				contextURL = u
			} else if u := NormalizeMultiURL(c.AvitoURL); u != "" {
				contextURL = u
			} else if u := NormalizeMultiURL(c.CupsonlineURL); u != "" {
				contextURL = u
			} else if u := NormalizeMultiURL(c.TelemostURL); u != "" {
				contextURL = u
			} else {
				contextURL = "http://#"
			}
		} else {
			contextURL = sanitizeContextURL(NormalizeMultiURL(contextURL))
		}
		args = append(args, "--session-context="+contextURL)
		args = append(args, "--transports="+strings.Join(sessionParts, ","))

		if hasDirect {
			args = append(args, "--direct-dial="+strings.TrimSpace(c.DirectDial))
		}
		if hasWebdav {
			webdavURL, webdavUser, webdavPass := parseWebdavURL(c.WebdavURL, c.WebdavUser, c.WebdavPassword)
			args = append(args, "--webdav-url="+webdavURL)
			if webdavUser != "" {
				args = append(args, "--webdav-user="+webdavUser)
			}
			if webdavPass != "" {
				args = append(args, "--webdav-pass="+webdavPass)
			}
		}
		if hasMailru {
			args = append(args, "--mailru-url="+NormalizeMultiURL(c.MailruURL))
		}
		if hasYandex {
			args = append(args, "--yandex-url="+NormalizeMultiURL(c.YandexURL))
		}
		if hasVyandex {
			vyURL := NormalizeMultiURL(c.VyandexURL)
			if vyURL == "" {
				vyURL = NormalizeMultiURL(c.YandexURL)
			}
			if vyURL != "" {
				args = append(args, "--vyandex-url="+vyURL)
			}
		}
		if hasBoards {
			args = append(args, "--boards-url="+NormalizeMultiURL(c.BoardsURL))
		}
		if hasAvito {
			args = append(args, "--avito-url="+NormalizeMultiURL(c.AvitoURL))
		}
		if hasCups {
			args = append(args, "--cupsonline-url="+NormalizeMultiURL(c.CupsonlineURL))
		}
		if hasTelemost && NormalizeMultiURL(c.TelemostURL) != "" {
			args = append(args, "--telemost-url="+NormalizeMultiURL(c.TelemostURL))
		}
		if hasOneme {
			args = append(args, "--oneme-token="+strings.TrimSpace(c.OnemeToken))
			args = append(args, "--oneme-uid="+strings.TrimSpace(c.OnemeUID))
		}
	} else {
		singleType := "mailru"
		if len(c.Transports) > 0 {
			singleType = strings.ToLower(strings.TrimSpace(c.Transports[0]))
		}
		args = append(args, "--transport="+singleType)

		if keyFilePath != "" && strings.TrimSpace(c.Secret) != "" {
			args = append(args, "--encryption-key-file="+keyFilePath)
			if strings.TrimSpace(c.Context) != "" {
				args = append(args, "--session-context="+sanitizeContextURL(NormalizeMultiURL(c.Context)))
			}
		}

		switch singleType {
		case "webdav":
			webdavURL, webdavUser, webdavPass := parseWebdavURL(c.WebdavURL, c.WebdavUser, c.WebdavPassword)
			if webdavURL != "" {
				args = append(args, "--url="+webdavURL)
			}
			if webdavUser != "" {
				args = append(args, "--webdav-user="+webdavUser)
			}
			if webdavPass != "" {
				args = append(args, "--webdav-pass="+webdavPass)
			}
		case "direct":
			if strings.TrimSpace(c.DirectDial) != "" {
				args = append(args, "--direct-dial="+strings.TrimSpace(c.DirectDial))
			}
		case "mailru":
			if u := NormalizeMultiURL(c.MailruURL); u != "" {
				args = append(args, "--url="+u)
			}
		case "yandex":
			if u := NormalizeMultiURL(c.YandexURL); u != "" {
				args = append(args, "--url="+u)
			}
		case "vyandex":
			vyURL := NormalizeMultiURL(c.VyandexURL)
			if vyURL == "" {
				vyURL = NormalizeMultiURL(c.YandexURL)
			}
			if vyURL != "" {
				args = append(args, "--url="+vyURL)
			}
		case "boards":
			if u := NormalizeMultiURL(c.BoardsURL); u != "" {
				args = append(args, "--url="+u)
			}
		case "avito":
			if u := NormalizeMultiURL(c.AvitoURL); u != "" {
				args = append(args, "--url="+u)
			}
		case "cupsonline":
			if u := NormalizeMultiURL(c.CupsonlineURL); u != "" {
				args = append(args, "--url="+u)
			}
		case "telemost":
			if u := NormalizeMultiURL(c.TelemostURL); u != "" {
				args = append(args, "--url="+u)
			}
		case "oneme":
			if strings.TrimSpace(c.OnemeToken) != "" {
				args = append(args, "--maxToken="+strings.TrimSpace(c.OnemeToken))
			}
			if strings.TrimSpace(c.OnemeUID) != "" {
				args = append(args, "--maxUid="+strings.TrimSpace(c.OnemeUID))
			}
		}
	}

	switch c.Debug {
	case 1:
		args = append(args, "-d")
	case 2:
		args = append(args, "-dd")
	case 3:
		args = append(args, "-ddd")
	}

	return args
}
