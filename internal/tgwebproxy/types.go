package tgwebproxy

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

var (
	ErrInvalidSecret   = errors.New("invalid MTProto secret")
	ErrDuplicateSecret = errors.New("duplicate MTProto secret")
	ErrInvalidUpstream = errors.New("invalid upstream configuration")
)

const (
	CurrentSchemaVersion = 2

	DefaultListenPort     = 8085
	DefaultAdminPort      = 8086
	DefaultDirectPort     = 8443
	DefaultRawPort        = 2398
	DefaultPublicHostname = ""
	DefaultDirectHost     = ""
	DefaultBackend        = "127.0.0.1:2398"
	DefaultCarrierMode    = "websocket"
	DefaultUpstreamDevice = ""
	DefaultTlsDomain      = ""
	DefaultLegacyGraceTTL = 7 * 24 * time.Hour

	CarrierModeFileHTTPS = "file-https-v1"

	ScenarioDirectFakeTLS = "direct_fake_tls"
	ScenarioCDNHTTP       = "cdn_http"
	ScenarioDual          = "dual"

	PresetCDNKeenDNS   = "cdn_keendns"
	PresetCDNStatic    = "cdn_static"
	PresetCDNFileHTTPS = "cdn_file_https"
	PresetKeenDNS      = "keendns"
	PresetStaticIP     = "static_ip"

	UpstreamTypeDirect = "direct"
	UpstreamTypeSocks5 = "socks5"

	PhaseStaged      = "staged"
	PhaseApplying    = "applying"
	PhaseCommitted   = "committed"
	PhaseRollingBack = "rolling_back"
)

type PresetSpec struct {
	ID                   string `json:"id"`
	Title                string `json:"title"`
	Badge                string `json:"badge"`
	Description          string `json:"description"`
	IsCDN                bool   `json:"is_cdn"`
	CarrierMode          string `json:"carrier_mode"`
	CarrierBatchBytes    int    `json:"carrier_batch_bytes"`
	MaxPendingPerSession int    `json:"max_pending_per_session"`
	MaxStreamsPerSession int    `json:"max_streams_per_session"`
	IdleTimeout          string `json:"idle_timeout"`
	ReconnectGrace       string `json:"reconnect_grace"`
	LongPollTimeout      string `json:"long_poll_timeout"`
}

func ResolvePresetSpec(preset string) (PresetSpec, bool) {
	switch strings.TrimSpace(strings.ToLower(preset)) {
	case PresetCDNKeenDNS:
		return PresetSpec{
			ID:                   PresetCDNKeenDNS,
			Title:                "CDN + KeenDNS Cloud",
			Badge:                "CDN + KeenDNS",
			Description:          "CDN поверх облачного домена KeenDNS (роутер за NAT / серый IP)",
			IsCDN:                true,
			CarrierMode:          "https-lanes",
			CarrierBatchBytes:    2 * 1024 * 1024,  // 2 MiB
			MaxPendingPerSession: 32 * 1024 * 1024, // 32 MiB
			MaxStreamsPerSession: 128,
			IdleTimeout:          "300s",
			ReconnectGrace:       "120s",
			LongPollTimeout:      "25s",
		}, true
	case PresetCDNStatic:
		return PresetSpec{
			ID:                   PresetCDNStatic,
			Title:                "CDN + Static IP",
			Badge:                "CDN + Static IP",
			Description:          "CDN напрямую к белому IP роутера (быстрые батчи 512 КБ без пауз)",
			IsCDN:                true,
			CarrierMode:          "https-lanes",
			CarrierBatchBytes:    512 * 1024,       // 512 KiB (eliminates 2 MiB arrayBuffer window-drain pauses)
			MaxPendingPerSession: 64 * 1024 * 1024, // 64 MiB
			MaxStreamsPerSession: 256,
			IdleTimeout:          "300s",
			ReconnectGrace:       "120s",
			LongPollTimeout:      "20s",
		}, true
	case PresetCDNFileHTTPS:
		return PresetSpec{
			ID:                   PresetCDNFileHTTPS,
			Title:                "CDN + File HTTPS",
			Badge:                "CDN File HTTPS",
			Description:          "CDN через безопасные GET-запросы с расширениями (.bin/.html) для жестких фильтров",
			IsCDN:                true,
			CarrierMode:          CarrierModeFileHTTPS,
			CarrierBatchBytes:    2048,
			MaxPendingPerSession: 16 * 1024 * 1024,
			MaxStreamsPerSession: 128,
			IdleTimeout:          "300s",
			ReconnectGrace:       "120s",
			LongPollTimeout:      "20s",
		}, true
	case PresetKeenDNS:
		return PresetSpec{
			ID:                   PresetKeenDNS,
			Title:                "KeenDNS Cloud",
			Badge:                "KeenDNS",
			Description:          "Прямой облачный домен KeenDNS без промежуточного CDN",
			IsCDN:                false,
			CarrierMode:          "websocket",
			CarrierBatchBytes:    1024 * 1024,      // 1 MiB
			MaxPendingPerSession: 32 * 1024 * 1024, // 32 MiB
			MaxStreamsPerSession: 128,
			IdleTimeout:          "180s",
			ReconnectGrace:       "120s",
			LongPollTimeout:      "25s",
		}, true
	case PresetStaticIP:
		return PresetSpec{
			ID:                   PresetStaticIP,
			Title:                "Static IP",
			Badge:                "Static IP",
			Description:          "Прямое подключение к белому IP / прямому домену роутера без CDN",
			IsCDN:                false,
			CarrierMode:          "websocket-lanes",
			CarrierBatchBytes:    2 * 1024 * 1024,  // 2 MiB
			MaxPendingPerSession: 64 * 1024 * 1024, // 64 MiB
			MaxStreamsPerSession: 256,
			IdleTimeout:          "300s",
			ReconnectGrace:       "90s",
			LongPollTimeout:      "25s",
		}, true
	default:
		return PresetSpec{}, false
	}
}

func isKeenDNSCloudHostname(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	return strings.Contains(h, ".netcraze.club") ||
		strings.Contains(h, ".keenetic.pro") ||
		strings.Contains(h, ".keenetic.link") ||
		strings.Contains(h, ".keenetic.name")
}

func InferEndpointPreset(ep WebProxyEndpoint, cfg Config) string {
	if _, ok := ResolvePresetSpec(ep.Preset); ok {
		return strings.TrimSpace(strings.ToLower(ep.Preset))
	}
	if _, ok := ResolvePresetSpec(cfg.NetworkPreset); ok {
		np := strings.TrimSpace(strings.ToLower(cfg.NetworkPreset))
		switch np {
		case PresetCDNKeenDNS, PresetKeenDNS:
			if ep.IsCDN {
				return PresetCDNKeenDNS
			}
			return PresetKeenDNS
		case PresetCDNStatic, PresetStaticIP:
			if ep.IsCDN {
				return PresetCDNStatic
			}
			return PresetStaticIP
		}
	}
	hasKeenCloud := isKeenDNSCloudHostname(ep.Hostname) || isKeenDNSCloudHostname(cfg.DirectHost) || isKeenDNSCloudHostname(cfg.PublicHostname)
	if !hasKeenCloud {
		for _, other := range cfg.Endpoints {
			if isKeenDNSCloudHostname(other.Hostname) {
				hasKeenCloud = true
				break
			}
		}
	}
	if ep.IsCDN {
		if hasKeenCloud {
			return PresetCDNKeenDNS
		}
		return PresetCDNStatic
	}
	if hasKeenCloud {
		return PresetKeenDNS
	}
	return PresetStaticIP
}

func EffectiveEndpointCarrierMode(ep WebProxyEndpoint, cfg Config) string {
	if ep.CarrierMode != "" {
		return ep.CarrierMode
	}
	if spec, ok := ResolvePresetSpec(ep.Preset); ok {
		return spec.CarrierMode
	}
	if _, ok := ResolvePresetSpec(cfg.NetworkPreset); ok {
		if spec, ok2 := ResolvePresetSpec(InferEndpointPreset(ep, cfg)); ok2 {
			return spec.CarrierMode
		}
	}
	if cfg.CarrierMode != "" {
		return cfg.CarrierMode
	}
	return DefaultCarrierMode
}

type UpstreamSpec struct {
	ID       string `json:"id"`
	Type     string `json:"type"`               // "direct" | "socks5"
	Device   string `json:"device,omitempty"`   // optional Linux iface for SO_BINDTODEVICE
	Address  string `json:"address,omitempty"`  // host:port for socks5
	Username string `json:"username,omitempty"` // optional socks5 username
	Password string `json:"password,omitempty"` // optional socks5 password
	Weight   int    `json:"weight"`             // >= 1 (default 1)
	Enabled  bool   `json:"enabled"`
}

// PublicUpstreamSpec is the API view of an UpstreamSpec: SOCKS5 credentials
// are reduced to PasswordSet so regular config/status responses never carry
// the password. UpdateConfig keeps a stored password when the client sends it
// back empty for the same upstream (see preserveUpstreamPasswords).
type PublicUpstreamSpec struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Device      string `json:"device,omitempty"`
	Address     string `json:"address,omitempty"`
	Username    string `json:"username,omitempty"`
	PasswordSet bool   `json:"password_set"`
	Weight      int    `json:"weight"`
	Enabled     bool   `json:"enabled"`
}

func publicUpstreams(list []UpstreamSpec) []PublicUpstreamSpec {
	if list == nil {
		return nil
	}
	out := make([]PublicUpstreamSpec, len(list))
	for i, u := range list {
		out[i] = PublicUpstreamSpec{
			ID:          u.ID,
			Type:        u.Type,
			Device:      u.Device,
			Address:     u.Address,
			Username:    u.Username,
			PasswordSet: u.Password != "",
			Weight:      u.Weight,
			Enabled:     u.Enabled,
		}
	}
	return out
}

// UpstreamSpecsFromPublic converts public views back to specs without
// credentials, for read-only consumers such as reachability checks.
func UpstreamSpecsFromPublic(list []PublicUpstreamSpec) []UpstreamSpec {
	if list == nil {
		return nil
	}
	out := make([]UpstreamSpec, len(list))
	for i, u := range list {
		out[i] = UpstreamSpec{ID: u.ID, Type: u.Type, Device: u.Device, Address: u.Address, Username: u.Username, Weight: u.Weight, Enabled: u.Enabled}
	}
	return out
}

// preserveUpstreamPasswords restores stored SOCKS5 passwords that a client
// sent back empty after reading the redacted public view. A password is only
// reused for the same upstream ID, type, address and username: re-targeting an
// upstream requires re-entering the credential, so it is never handed to a
// different proxy server.
func preserveUpstreamPasswords(incoming, stored []UpstreamSpec) []UpstreamSpec {
	if len(incoming) == 0 || len(stored) == 0 {
		return incoming
	}
	byID := make(map[string]UpstreamSpec, len(stored))
	for _, u := range NormalizeUpstreams(stored) {
		byID[u.ID] = u
	}
	for i, u := range incoming {
		if u.Password != "" || u.Type != UpstreamTypeSocks5 {
			continue
		}
		old, ok := byID[u.ID]
		if ok && old.Type == UpstreamTypeSocks5 && old.Address == u.Address && old.Username == u.Username {
			incoming[i].Password = old.Password
		}
	}
	return incoming
}

func cloneUpstreams(list []UpstreamSpec) []UpstreamSpec {
	if list == nil {
		return nil
	}
	out := make([]UpstreamSpec, len(list))
	copy(out, list)
	return out
}

// NormalizeMTProtoSecret validates and normalizes a user-supplied MTProto secret.
// Accepts raw 32-char hex keys as well as full "dd<32hex>" or Fake-TLS "ee<32hex><domain_hex>" secrets.
func NormalizeMTProtoSecret(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return "", fmt.Errorf("%w: secret cannot be empty", ErrInvalidSecret)
	}
	if _, err := hex.DecodeString(s); err != nil {
		return "", fmt.Errorf("%w: must be valid hex", ErrInvalidSecret)
	}
	switch {
	case len(s) == 32:
		return s, nil
	case len(s) == 34 && strings.HasPrefix(s, "dd"):
		return s[2:], nil
	case len(s) > 34 && strings.HasPrefix(s, "ee") && len(s)%2 == 0:
		return s[2:34], nil
	default:
		return "", fmt.Errorf("%w: expected 32 hex chars (or ee/dd MTProto secret)", ErrInvalidSecret)
	}
}

func NormalizeUpstreams(list []UpstreamSpec) []UpstreamSpec {
	if len(list) == 0 {
		return nil
	}
	out := make([]UpstreamSpec, len(list))
	for i, u := range list {
		typ := strings.ToLower(strings.TrimSpace(u.Type))
		switch typ {
		case "", UpstreamTypeDirect:
			typ = UpstreamTypeDirect
		case "socks", UpstreamTypeSocks5:
			typ = UpstreamTypeSocks5
		}
		dev := strings.TrimSpace(u.Device)
		if dev == "direct" {
			dev = ""
		}
		addr := strings.TrimSpace(u.Address)
		w := u.Weight
		if w <= 0 {
			w = 1
		}
		id := strings.TrimSpace(u.ID)
		if id == "" {
			id = fmt.Sprintf("up-%d", i+1)
		}
		out[i] = UpstreamSpec{
			ID:       id,
			Type:     typ,
			Device:   dev,
			Address:  addr,
			Username: strings.TrimSpace(u.Username),
			Password: u.Password,
			Weight:   w,
			Enabled:  u.Enabled,
		}
	}
	return out
}

func ValidateUpstreams(list []UpstreamSpec) error {
	if len(list) == 0 {
		return nil
	}
	enabledCount := 0
	for i, u := range list {
		typ := strings.ToLower(strings.TrimSpace(u.Type))
		if typ == "" {
			typ = UpstreamTypeDirect
		}
		w := u.Weight
		if w == 0 {
			w = 1
		}
		if w < 1 || w > 65535 {
			return fmt.Errorf("%w: upstream #%d weight must be between 1 and 65535", ErrInvalidUpstream, i+1)
		}
		switch typ {
		case UpstreamTypeDirect:
			dev := strings.TrimSpace(u.Device)
			if dev != "" && dev != "direct" {
				if len(dev) > 32 || strings.ContainsAny(dev, " \t\r\n/") {
					return fmt.Errorf("%w: invalid interface name %q", ErrInvalidUpstream, dev)
				}
			}
		case UpstreamTypeSocks5, "socks":
			addr := strings.TrimSpace(u.Address)
			host, portStr, err := net.SplitHostPort(addr)
			if err != nil || strings.TrimSpace(host) == "" {
				return fmt.Errorf("%w: socks5 upstream #%d must use host:port", ErrInvalidUpstream, i+1)
			}
			port, err := strconv.Atoi(portStr)
			if err != nil || port < 1 || port > 65535 {
				return fmt.Errorf("%w: socks5 upstream #%d has invalid port %q", ErrInvalidUpstream, i+1, portStr)
			}
		default:
			return fmt.Errorf("%w: unsupported upstream type %q", ErrInvalidUpstream, u.Type)
		}
		if u.Enabled {
			enabledCount++
		}
	}
	if enabledCount == 0 {
		return fmt.Errorf("%w: at least one upstream must be enabled", ErrInvalidUpstream)
	}
	return nil
}

type Config struct {
	SchemaVersion   int                `json:"schema_version"`
	Enabled         bool               `json:"enabled"`
	Scenario        string             `json:"scenario,omitempty"`       // direct_fake_tls, cdn_http, dual
	NetworkPreset   string             `json:"network_preset,omitempty"` // cdn_keendns, cdn_static, keendns, static_ip
	ListenPort      int                `json:"listen_port"`              // Default: 8085 (tproxy-server)
	AdminPort       int                `json:"admin_port"`               // Default: 8086 (tproxy-server metrics)
	PublicHostname  string             `json:"public_hostname"`          // Public CDN ingress hostname
	DirectHost      string             `json:"direct_host"`              // Direct MTProxy hostname or IP
	DirectPort      int                `json:"direct_port"`              // Default: 8443 (telemt Fake-TLS)
	Secret          string             `json:"secret"`                   // 16 bytes hex (32 chars)
	LegacySecret    string             `json:"legacy_secret"`            // 16 bytes hex (32 chars)
	LegacyExpiresAt string             `json:"legacy_expires_at"`        // RFC3339 timestamp
	Backend         string             `json:"backend"`                  // Default: 127.0.0.1:2398 (raw telemt)
	CarrierMode     string             `json:"carrier_mode"`             // Default: websocket
	UpstreamDevice  string             `json:"upstream_device"`          // Egress interface (optional)
	Upstreams       []UpstreamSpec     `json:"upstreams,omitempty"`      // Optional weighted failover upstreams
	CoverUpstream   string             `json:"cover_upstream,omitempty"` // Optional validated loopback HTTP upstream (e.g. Xray); empty uses built-in static fallback
	TlsDomain       string             `json:"tls_domain"`               // Fake-TLS SNI domain
	Endpoints       []WebProxyEndpoint `json:"endpoints,omitempty"`      // Multiple web proxy backends
	Users           []TelegramUser     `json:"users,omitempty"`          // Multi-user secrets (MTProto + Web Proxy)
}

func (c Config) EffectiveUpstreams() []UpstreamSpec {
	if len(c.Upstreams) > 0 {
		return NormalizeUpstreams(c.Upstreams)
	}
	dev := strings.TrimSpace(c.UpstreamDevice)
	if dev == "direct" {
		dev = ""
	}
	return []UpstreamSpec{
		{
			ID:      "up-1",
			Type:    UpstreamTypeDirect,
			Device:  dev,
			Weight:  1,
			Enabled: true,
		},
	}
}

func (c Config) EnabledUpstreams() []UpstreamSpec {
	var out []UpstreamSpec
	for _, u := range c.EffectiveUpstreams() {
		if u.Enabled {
			out = append(out, u)
		}
	}
	return out
}

func (c Config) PrimaryUpstreamDevice() string {
	enabled := c.EnabledUpstreams()
	if len(enabled) == 0 {
		return ""
	}
	if enabled[0].Type != UpstreamTypeDirect {
		return ""
	}
	if enabled[0].Device == "" {
		return "direct"
	}
	return enabled[0].Device
}

type TelegramUser struct {
	ID           string `json:"id"`
	Remark       string `json:"remark"`
	Secret       string `json:"secret,omitempty"`
	SecretMasked string `json:"secret_masked,omitempty"`
	Enabled      bool   `json:"enabled"`
	CreatedAt    string `json:"created_at,omitempty"`
}

func (c Config) GetUsers() []TelegramUser {
	if len(c.Users) > 0 {
		out := make([]TelegramUser, len(c.Users))
		copy(out, c.Users)
		return out
	}
	if c.Secret != "" {
		return []TelegramUser{
			{
				ID:      "default",
				Remark:  "Основной",
				Secret:  c.Secret,
				Enabled: true,
			},
		}
	}
	return nil
}

func (c Config) GetPublicUsers() []TelegramUser {
	users := c.GetUsers()
	if len(users) == 0 {
		return nil
	}
	out := make([]TelegramUser, len(users))
	for i, u := range users {
		out[i] = TelegramUser{
			ID:           u.ID,
			Remark:       u.Remark,
			SecretMasked: maskSecret(u.Secret),
			Enabled:      u.Enabled,
			CreatedAt:    u.CreatedAt,
		}
	}
	return out
}

func (c Config) ActiveSecrets() []string {
	seen := make(map[string]bool)
	var out []string
	add := func(sec string) {
		s := strings.ToLower(strings.TrimSpace(sec))
		if len(s) == 32 && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if len(c.Users) > 0 {
		for _, u := range c.Users {
			if u.Enabled {
				add(u.Secret)
			}
		}
	} else {
		add(c.Secret)
	}
	add(c.LegacySecret)
	return out
}

type WebProxyEndpoint struct {
	ID              string `json:"id"`
	Hostname        string `json:"hostname"`
	ListenPort      int    `json:"listen_port"`
	AdminPort       int    `json:"admin_port"`
	Description     string `json:"description,omitempty"`
	Preset          string `json:"preset,omitempty"`       // cdn_keendns, cdn_static, keendns, static_ip
	CarrierMode     string `json:"carrier_mode,omitempty"` // https-lanes, websocket, websocket-lanes, https
	IsCDN           bool   `json:"is_cdn"`
	Enabled         bool   `json:"enabled"`
	ExternalBackend bool   `json:"external_backend,omitempty"` // If true, Mux proxies to ListenPort without spawning a managed tproxy-server worker
	PublicUpstream  string `json:"public_upstream,omitempty"`  // Optional validated loopback HTTP cover upstream
}

type WebProxyLink struct {
	ID          string `json:"id"`
	Hostname    string `json:"hostname"`
	Description string `json:"description,omitempty"`
	Preset      string `json:"preset,omitempty"`
	CarrierMode string `json:"carrier_mode,omitempty"`
	TgURL       string `json:"tg_url"`
	TmeURL      string `json:"tme_url"`
	BridgeURL   string `json:"bridge_url"`
	IsCDN       bool   `json:"is_cdn"`
}

// WebProxyStatusLink is the secret-free view of a web proxy endpoint exposed by
// the regular status API. Connection URLs (tg://, t.me, bridge) embed the user
// secret or an HMAC capability derived from it and are only returned by
// RevealUserSecret via WebProxyLink.
type WebProxyStatusLink struct {
	ID          string `json:"id"`
	Hostname    string `json:"hostname"`
	Description string `json:"description,omitempty"`
	Preset      string `json:"preset,omitempty"`
	CarrierMode string `json:"carrier_mode,omitempty"`
	IsCDN       bool   `json:"is_cdn"`
}

func (c Config) GetEndpoints() []WebProxyEndpoint {
	if len(c.Endpoints) > 0 {
		return c.Endpoints
	}
	if c.PublicHostname != "" {
		listenPort := 8084
		adminPort := c.AdminPort
		if adminPort <= 0 {
			adminPort = 8086
		}
		hostLower := strings.ToLower(c.PublicHostname)
		isCDNHint := strings.Contains(hostLower, "edge") || strings.Contains(hostLower, "cdn") ||
			strings.Contains(hostLower, "selcdn")
		isExplicitDirect := strings.EqualFold(c.NetworkPreset, PresetKeenDNS) ||
			strings.EqualFold(c.NetworkPreset, PresetStaticIP) ||
			(c.NetworkPreset == "" && strings.EqualFold(c.CarrierMode, "websocket") && !isCDNHint)
		isCDN := isCDNHint || (!isExplicitDirect && (c.Scenario == ScenarioCDNHTTP || c.Scenario == ScenarioDual))
		return []WebProxyEndpoint{
			{
				ID:          "ep-primary",
				Hostname:    c.PublicHostname,
				ListenPort:  listenPort,
				AdminPort:   adminPort,
				Description: "Основной веб-прокси",
				Preset:      c.NetworkPreset,
				CarrierMode: c.CarrierMode,
				IsCDN:       isCDN,
				Enabled:     true,
			},
		}
	}
	return nil
}

func (c Config) IsDirectEnabled() bool {
	if !c.Enabled {
		return false
	}
	return c.Scenario == ScenarioDirectFakeTLS || c.Scenario == ScenarioDual || c.Scenario == ""
}

// OpensDirectWAN — открывать ли прямой порт на WAN. Без прямого адреса MTProto
// слушает только для LAN: снаружи подключаться некуда.
func (c Config) OpensDirectWAN() bool {
	return c.IsDirectEnabled() && strings.TrimSpace(c.DirectHost) != ""
}

func (c PublicConfig) OpensDirectWAN() bool {
	return Config{Enabled: c.Enabled, Scenario: c.Scenario, DirectHost: c.DirectHost}.OpensDirectWAN()
}

func (c Config) effectiveDirectPort() int {
	if c.DirectPort <= 0 {
		return DefaultDirectPort
	}
	return c.DirectPort
}

func (c Config) IsRawEnabled() bool {
	if !c.Enabled {
		return false
	}
	return c.Scenario == ScenarioCDNHTTP || c.Scenario == ScenarioDual || c.Scenario == ""
}

func (c Config) IsWebEnabled() bool {
	if !c.Enabled {
		return false
	}
	return c.Scenario == ScenarioCDNHTTP || c.Scenario == ScenarioDual || c.Scenario == ""
}

// ManagedIngressConfig represents full-state configuration managed by server ingress coordinator.
type ManagedIngressConfig struct {
	Enabled        bool   `json:"enabled"`
	ListenPort     int    `json:"listen_port"`
	PublicHostname string `json:"public_hostname"`
}

type PublicConfig struct {
	SchemaVersion      int                  `json:"schema_version"`
	Enabled            bool                 `json:"enabled"`
	Scenario           string               `json:"scenario,omitempty"`
	NetworkPreset      string               `json:"network_preset,omitempty"`
	ListenPort         int                  `json:"listen_port"`
	AdminPort          int                  `json:"admin_port"`
	PublicHostname     string               `json:"public_hostname"`
	DirectHost         string               `json:"direct_host"`
	DirectPort         int                  `json:"direct_port"`
	SecretMasked       string               `json:"secret_masked"`
	Backend            string               `json:"backend"`
	CarrierMode        string               `json:"carrier_mode"`
	UpstreamDevice     string               `json:"upstream_device"`
	Upstreams          []PublicUpstreamSpec `json:"upstreams,omitempty"`
	TlsDomain          string               `json:"tls_domain"`
	LegacyActive       bool                 `json:"legacy_active"`
	LegacyExpiresAt    string               `json:"legacy_expires_at,omitempty"`
	LegacySecretMasked string               `json:"legacy_secret_masked,omitempty"`
	Endpoints          []WebProxyEndpoint   `json:"endpoints,omitempty"`
	Users              []TelegramUser       `json:"users,omitempty"`
}

type SnapshotManifest struct {
	TxID             string            `json:"tx_id"`
	CreatedAt        string            `json:"created_at"`
	OldConfig        Config            `json:"old_config"`
	CandidateConfig  Config            `json:"candidate_config"`
	WasRunning       bool              `json:"was_running"`
	CandidateRunning bool              `json:"candidate_running"`
	State            string            `json:"state"` // "prepared", "active", "finalized", "rolled_back"
	Checksums        map[string]string `json:"checksums,omitempty"`
	SchemaVersion    int               `json:"schema_version"`
}

type Status struct {
	Installed                     bool                 `json:"installed"`
	InstalledTelemt               bool                 `json:"installed_telemt"`
	InstalledTproxy               bool                 `json:"installed_tproxy"`
	Running                       bool                 `json:"running"`
	PID                           int                  `json:"pid"`
	DirectPID                     int                  `json:"direct_pid,omitempty"`
	Port                          int                  `json:"port"`
	DirectOnline                  bool                 `json:"direct_online"`
	RawOnline                     bool                 `json:"raw_online"`
	WebProxyOnline                bool                 `json:"webproxy_online"`
	BackendOnline                 bool                 `json:"backend_online"`
	BackendAddr                   string               `json:"backend_addr,omitempty"`
	UpstreamStatus                string               `json:"upstream_status"` // "ok", "degraded", "interface_down"
	Upstreams                     []PublicUpstreamSpec `json:"upstreams,omitempty"`
	NetworkPreset                 string               `json:"network_preset,omitempty"`
	CarrierMode                   string               `json:"carrier_mode"`
	PublicHost                    string               `json:"public_host"`
	DirectHost                    string               `json:"direct_host"`
	DirectPort                    int                  `json:"direct_port"`
	TlsDomain                     string               `json:"tls_domain"`
	SecretMasked                  string               `json:"secret_masked"`
	LegacySecretMasked            string               `json:"legacy_secret_masked"`
	LegacyActive                  bool                 `json:"legacy_active"`
	LegacyExpiresAt               string               `json:"legacy_expires_at"`
	LegacyExpiredPendingReconcile bool                 `json:"legacy_expired_pending_reconcile"`
	RecoveryRequired              bool                 `json:"recovery_required"`
	LastStartupError              string               `json:"last_startup_error,omitempty"`
	DirectWANError                string               `json:"direct_wan_error,omitempty"` // сбой правила WAN-файрвола прямого порта
	WebProxies                    []WebProxyStatusLink `json:"web_proxies,omitempty"`
	Users                         []TelegramUser       `json:"users,omitempty"`
}

type UserRevealData struct {
	ID            string         `json:"id"`
	Remark        string         `json:"remark"`
	Enabled       bool           `json:"enabled"`
	Secret        string         `json:"secret"`
	TgURL         string         `json:"tg_url"`
	TmeURL        string         `json:"tme_url"`
	BridgeURL     string         `json:"bridge_url"`
	MtproxySecret string         `json:"mtproxy_secret"`
	MtproxyURL    string         `json:"mtproxy_url"`
	MtproxyTmeURL string         `json:"mtproxy_tme_url"`
	WebProxies    []WebProxyLink `json:"web_proxies,omitempty"`
}

type RevealData struct {
	UserID        string           `json:"user_id,omitempty"`
	UserRemark    string           `json:"user_remark,omitempty"`
	Secret        string           `json:"secret"`
	LegacySecret  string           `json:"legacy_secret,omitempty"`
	DirectHost    string           `json:"direct_host"`
	DirectPort    int              `json:"direct_port"`
	TlsDomain     string           `json:"tls_domain"`
	TgURL         string           `json:"tg_url"`
	TmeURL        string           `json:"tme_url"`
	BridgeURL     string           `json:"bridge_url"`
	MtproxySecret string           `json:"mtproxy_secret"`
	MtproxyURL    string           `json:"mtproxy_url"`
	MtproxyTmeURL string           `json:"mtproxy_tme_url"`
	WebProxies    []WebProxyLink   `json:"web_proxies,omitempty"`
	Users         []UserRevealData `json:"users,omitempty"`
}

type WorkerProcessState struct {
	Worker    string `json:"worker"`
	Enabled   bool   `json:"enabled"`
	Running   bool   `json:"running"`
	PID       int    `json:"pid"`
	StartTime int64  `json:"start_time"`
}

type ManifestFile struct {
	TargetPath    string      `json:"target_path"`
	StagingPath   string      `json:"staging_path"`
	BackupPath    string      `json:"backup_path"`
	Sha256Old     string      `json:"sha256_old"`
	Sha256New     string      `json:"sha256_new"`
	ExistedBefore bool        `json:"existed_before"`
	Mode          os.FileMode `json:"mode"`
	UID           int         `json:"uid"`
	GID           int         `json:"gid"`
}

type TransactionManifest struct {
	TxID               string                        `json:"txid"`
	Phase              string                        `json:"phase"` // staged, applying, committed, rolling_back
	CreatedAt          string                        `json:"created_at"`
	Files              []ManifestFile                `json:"files"`
	ProcessStateBefore map[string]WorkerProcessState `json:"process_state_before"`
}
