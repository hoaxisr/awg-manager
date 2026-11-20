package vlessserver

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/xhttpserver"
)

var (
	ErrRecoveryRequired         = errors.New("vless server configuration is corrupt or requires manual recovery")
	ErrStaleTransactionConflict = errors.New("vless server transaction conflict: candidate was prepared against a stale configuration revision")
	ErrUnsupportedTransport     = errors.New("unsupported VLESS server transport: legacy WebSocket (ws) server transport has been removed; use xhttp or finiteproto")
	ErrUnsupportedLegacyConfig  = errors.New("unsupported legacy configuration: only VLESS over XHTTP is supported for automatic migration")
	ErrLegacyRuntimeRemoved     = errors.New("legacy Xray runtime (keep_legacy / S99xray-cdn) has been removed; use awgm-xhttpd")
)

type Client struct {
	ID          string `json:"id"`
	Remark      string `json:"remark"`
	Enabled     bool   `json:"enabled"`
	CreatedAt   string `json:"created_at"`
	FPClientID  string `json:"fp_client_id,omitempty"`
	FPSecretHex string `json:"fp_secret_hex,omitempty"`
}

func hasValidFPCredentials(c Client) bool {
	id := strings.TrimSpace(c.FPClientID)
	secHex := strings.TrimSpace(c.FPSecretHex)
	if id == "" || strings.ContainsAny(id, ":\r\n") || len(secHex) != xhttpserver.FPKeyHexLen {
		return false
	}
	sec, err := hex.DecodeString(secHex)
	if err != nil || len(sec) != xhttpserver.FPKeySize {
		return false
	}
	if u, err := xhttpserver.ParseUUID(c.ID); err == nil && xhttpserver.IsUUIDDerivedFPSecret(sec, u) {
		return false
	}
	if u2, err := xhttpserver.ParseUUID(id); err == nil && xhttpserver.IsUUIDDerivedFPSecret(sec, u2) {
		return false
	}
	return true
}

func ensureClientFPCredentials(c *Client) (bool, error) {
	if c == nil || hasValidFPCredentials(*c) {
		return false, nil
	}
	fpID, fpSecHex, _, err := xhttpserver.GenerateFPCredentials()
	if err != nil {
		return false, fmt.Errorf("generate FiniteProto v2 credentials: %w", err)
	}
	c.FPClientID = fpID
	c.FPSecretHex = fpSecHex
	return true, nil
}

func ensureClientsFPCredentials(clients []Client) (bool, error) {
	changed := false
	for i := range clients {
		ok, err := ensureClientFPCredentials(&clients[i])
		if err != nil {
			return changed, err
		}
		if ok {
			changed = true
		}
	}
	return changed, nil
}

type EndpointConfig struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	Enabled       bool         `json:"enabled"`
	ConnectionWay string       `json:"connection_way,omitempty"` // "direct" or "external_https"
	OriginType    string       `json:"origin_type,omitempty"`    // "public_address" or "cloud_address"
	PublicDomain  string       `json:"public_domain"`
	PublicPort    int          `json:"public_port"`
	OriginHost    string       `json:"origin_host,omitempty"`
	OriginScheme  string       `json:"origin_scheme,omitempty"`
	OriginPort    int          `json:"origin_port,omitempty"`
	Path          string       `json:"path,omitempty"`
	Transport     string       `json:"transport,omitempty"`
	Mode          string       `json:"mode,omitempty"`
	UplinkMethod  string       `json:"uplink_method,omitempty"`
	XHTTPTuning   *XHTTPTuning `json:"xhttp_tuning,omitempty"`
}

type Config struct {
	Enabled            bool             `json:"enabled"`
	ListenAddress      string           `json:"listen_address"`               // Default: "127.0.0.1"
	ListenPort         int              `json:"listen_port"`                  // Default: 9008
	DispatcherPort     int              `json:"dispatcher_port"`              // Default: 9009
	PublicDomain       string           `json:"public_domain"`                // Domain configured for public ingress
	PublicPort         int              `json:"public_port"`                  // Ingress public port (usually 443)
	ConnectionWay      string           `json:"connection_way,omitempty"`     // "direct" or "external_https"
	OriginType         string           `json:"origin_type,omitempty"`        // "public_address" or "cloud_address"
	OriginHost         string           `json:"origin_host,omitempty"`        // Router/origin address reached by external HTTPS service
	OriginScheme       string           `json:"origin_scheme,omitempty"`      // "http" or "https"
	OriginPort         int              `json:"origin_port,omitempty"`        // Router/origin port reached by external HTTPS service
	Path               string           `json:"path"`                         // Path (e.g. /cdn-bridge/)
	Transport          string           `json:"transport,omitempty"`          // "xhttp" (default) or "finiteproto"
	Mode               string           `json:"mode"`                         // Transport mode (packet-up or finiteproto)
	UplinkMethod       string           `json:"uplink_method"`                // HTTP uplink method (GET / POST)
	XmuxMaxConnections int              `json:"xmux_max_connections"`         // Default: 2
	OutboundMode       string           `json:"outbound_mode,omitempty"`      // "direct", "socks", "interface", "router"
	OutboundInterface  string           `json:"outbound_interface,omitempty"` // network device name (e.g. "nwg1")
	OutboundSocksPort  int              `json:"outbound_socks_port"`          // Forwarding port (e.g. 1099 for local SOCKS5 policy routing, 0 for direct)
	AllowLAN           bool             `json:"allow_lan"`                    // Allow clients to reach RFC1918 LAN devices & router web UI
	DomainStrategy     string           `json:"domain_strategy,omitempty"`    // DNS resolution strategy (default: "ipv4_only")
	Clients            []Client         `json:"clients"`
	XHTTPTuning        *XHTTPTuning     `json:"xhttp_tuning,omitempty"`
	Endpoints          []EndpointConfig `json:"endpoints,omitempty"`
}

func validateSupportedTransport(transport string) error {
	tr := strings.ToLower(strings.TrimSpace(transport))
	switch tr {
	case "", "xhttp", "finiteproto":
		return nil
	default:
		return fmt.Errorf("%w (got %q)", ErrUnsupportedTransport, transport)
	}
}

func validateConfigTransports(cfg Config) error {
	if err := validateSupportedTransport(cfg.Transport); err != nil {
		return err
	}
	for _, ep := range cfg.Endpoints {
		if err := validateSupportedTransport(ep.Transport); err != nil {
			return fmt.Errorf("endpoint %q: %w", ep.ID, err)
		}
	}
	return nil
}

func IsKeenDNSHost(domain string) bool {
	d := strings.ToLower(strings.TrimSpace(domain))
	return strings.Contains(d, ".crazedns.") ||
		strings.Contains(d, ".keenetic.") ||
		strings.Contains(d, ".netcraze.")
}

func DefaultEndpointName(domain, connectionWay, originType string) string {
	d := strings.ToLower(strings.TrimSpace(domain))
	if IsKeenDNSHost(d) || connectionWay == "direct" {
		if originType == "cloud_address" {
			return "KeenDNS (Серый IP / Облако)"
		}
		return "KeenDNS (Белый / Серый IP)"
	}
	if strings.HasPrefix(d, "twcdn.") || strings.Contains(d, "timeweb") ||
		strings.HasPrefix(d, "cdn.") || strings.Contains(d, "selcdn") || strings.Contains(d, "selectel") {
		return "Через CDN"
	}
	return "CDN (" + strings.TrimSpace(domain) + ")"
}

// SynthesizeEndpoints returns the normalized list of endpoints from cfg.Endpoints if present,
// or derives initial parallel endpoint configurations from cfg.PublicDomain.
func SynthesizeEndpoints(cfg Config) []EndpointConfig {
	baseTuning := cfg.GetTuning()
	basePath := strings.TrimSpace(cfg.Path)
	if basePath == "" {
		basePath = "/cdn-bridge"
	}
	basePort := cfg.PublicPort
	if basePort <= 0 {
		basePort = 443
	}
	baseTransport := cfg.Transport
	if baseTransport == "" {
		baseTransport = "xhttp"
	}
	baseMode := cfg.Mode
	if baseMode == "" {
		baseMode = "packet-up"
	}

	if len(cfg.Endpoints) > 0 {
		out := make([]EndpointConfig, 0, len(cfg.Endpoints))
		for i, ep := range cfg.Endpoints {
			dom := strings.TrimSpace(ep.PublicDomain)
			if dom == "" {
				continue
			}
			ep.PublicDomain = dom
			if strings.TrimSpace(ep.ID) == "" {
				ep.ID = fmt.Sprintf("ep-%d-%s", i+1, strings.ReplaceAll(dom, ",", "-"))
			}
			if ep.ConnectionWay == "" {
				if IsKeenDNSHost(dom) {
					ep.ConnectionWay = "direct"
				} else {
					ep.ConnectionWay = "external_https"
				}
			}
			if ep.OriginType == "" {
				if cfg.OriginType != "" {
					ep.OriginType = cfg.OriginType
				} else {
					ep.OriginType = "public_address"
				}
			}
			if strings.TrimSpace(ep.Name) == "" {
				ep.Name = DefaultEndpointName(dom, ep.ConnectionWay, ep.OriginType)
			}
			if ep.PublicPort <= 0 {
				ep.PublicPort = basePort
			}
			if strings.TrimSpace(ep.Path) == "" {
				ep.Path = basePath
			}
			if strings.TrimSpace(ep.Transport) == "" {
				ep.Transport = baseTransport
			}
			if strings.TrimSpace(ep.Mode) == "" {
				ep.Mode = baseMode
			}
			if ep.XHTTPTuning != nil {
				t := *ep.XHTTPTuning
				_ = ValidateAndNormalizeXHTTPTuning(&t)
				ep.XHTTPTuning = &t
				ep.UplinkMethod = t.UplinkMethod
			} else {
				t := baseTuning
				ep.XHTTPTuning = &t
				if ep.UplinkMethod == "" {
					ep.UplinkMethod = t.UplinkMethod
				}
			}
			out = append(out, ep)
		}
		return out
	}

	rawDomains := strings.Split(cfg.PublicDomain, ",")
	var domains []string
	seen := make(map[string]bool)
	hasNonTgKeenDNS := false
	firstKeenDNS := ""
	for _, r := range rawDomains {
		d := strings.TrimSpace(r)
		if d == "" {
			continue
		}
		dl := strings.ToLower(d)
		if seen[dl] {
			continue
		}
		seen[dl] = true
		domains = append(domains, d)
		if IsKeenDNSHost(dl) {
			if !strings.HasPrefix(dl, "tg.") {
				hasNonTgKeenDNS = true
				if firstKeenDNS == "" {
					firstKeenDNS = d
				}
			} else if firstKeenDNS == "" {
				firstKeenDNS = d
			}
		}
	}
	if len(domains) == 0 {
		return nil
	}

	var out []EndpointConfig
	for _, d := range domains {
		dl := strings.ToLower(d)
		if IsKeenDNSHost(dl) && strings.HasPrefix(dl, "tg.") && hasNonTgKeenDNS {
			continue
		}
		connWay := cfg.ConnectionWay
		if len(domains) > 1 || connWay == "" {
			if IsKeenDNSHost(dl) {
				connWay = "direct"
			} else {
				connWay = "external_https"
			}
		}
		origType := cfg.OriginType
		if origType == "" {
			origType = "public_address"
		}
		origHost := cfg.OriginHost
		if connWay == "external_https" && origHost == "" && firstKeenDNS != "" {
			origHost = firstKeenDNS
		}
		origScheme := cfg.OriginScheme
		if origScheme == "" {
			origScheme = "https"
		}
		origPort := cfg.OriginPort
		if origPort <= 0 {
			origPort = 443
		}
		t := baseTuning
		out = append(out, EndpointConfig{
			ID:            "ep-" + dl,
			Name:          DefaultEndpointName(d, connWay, origType),
			Enabled:       true,
			ConnectionWay: connWay,
			OriginType:    origType,
			PublicDomain:  d,
			PublicPort:    basePort,
			OriginHost:    origHost,
			OriginScheme:  origScheme,
			OriginPort:    origPort,
			Path:          basePath,
			Transport:     baseTransport,
			Mode:          baseMode,
			UplinkMethod:  t.UplinkMethod,
			XHTTPTuning:   &t,
		})
	}
	return out
}

// NormalizeEndpoints normalizes cfg.Endpoints (if non-empty) and synchronizes top-level cfg.PublicDomain
// with the comma-separated union of enabled endpoint domains.
func NormalizeEndpoints(cfg *Config) {
	if cfg == nil || len(cfg.Endpoints) == 0 {
		return
	}
	cfg.Endpoints = SynthesizeEndpoints(*cfg)
	var enabledDomains []string
	var allDomains []string
	seenEnabled := make(map[string]bool)
	seenAll := make(map[string]bool)
	var primary *EndpointConfig

	for i := range cfg.Endpoints {
		ep := &cfg.Endpoints[i]
		for _, part := range strings.Split(ep.PublicDomain, ",") {
			d := strings.TrimSpace(part)
			if d == "" {
				continue
			}
			dl := strings.ToLower(d)
			if !seenAll[dl] {
				seenAll[dl] = true
				allDomains = append(allDomains, d)
			}
			if ep.Enabled && !seenEnabled[dl] {
				seenEnabled[dl] = true
				enabledDomains = append(enabledDomains, d)
			}
		}
		if ep.Enabled && primary == nil {
			primary = ep
		}
	}
	if primary == nil && len(cfg.Endpoints) > 0 {
		primary = &cfg.Endpoints[0]
	}
	if len(enabledDomains) > 0 {
		cfg.PublicDomain = strings.Join(enabledDomains, ",")
	} else if len(allDomains) > 0 {
		cfg.PublicDomain = strings.Join(allDomains, ",")
	}
	if primary != nil {
		if primary.PublicPort > 0 {
			cfg.PublicPort = primary.PublicPort
		}
		if primary.Path != "" {
			cfg.Path = primary.Path
		}
		if primary.ConnectionWay != "" {
			cfg.ConnectionWay = primary.ConnectionWay
		}
		if primary.OriginType != "" {
			cfg.OriginType = primary.OriginType
		}
		if primary.OriginHost != "" {
			cfg.OriginHost = primary.OriginHost
		}
		if primary.Mode != "" {
			cfg.Mode = primary.Mode
		}
		if primary.XHTTPTuning != nil {
			t := *primary.XHTTPTuning
			cfg.XHTTPTuning = &t
			cfg.UplinkMethod = t.UplinkMethod
		}
	}
}

func (c Config) GetTuning() XHTTPTuning {
	if c.XHTTPTuning != nil {
		t := *c.XHTTPTuning
		_ = ValidateAndNormalizeXHTTPTuning(&t)
		return t
	}
	t := DefaultLiveLowLatencyTuning()
	if c.UplinkMethod == "GET" || c.UplinkMethod == "POST" {
		t.UplinkMethod = c.UplinkMethod
	}
	return t
}

type Status struct {
	Backend             string   `json:"backend"`
	BinaryPath          string   `json:"binary_path,omitempty"`
	Installed           bool     `json:"installed"`
	Running             bool     `json:"running"`
	PID                 int      `json:"pid"`
	ListenAddress       string   `json:"listen_address,omitempty"`
	Port                int      `json:"port"`
	Version             string   `json:"version"`
	UnavailableReason   string   `json:"unavailable_reason,omitempty"`
	Domain              string   `json:"domain"`
	Configured          bool     `json:"configured"`
	ClientsCount        int      `json:"clients_count"`
	Clients             []Client `json:"clients"`
	RecoveryRequired    bool     `json:"recovery_required,omitempty"`
	RecoveryReason      string   `json:"recovery_reason,omitempty"`
	RecoveryFingerprint string   `json:"recovery_fingerprint,omitempty"`
	MigrationStatus     string   `json:"migration_status,omitempty"`
	ActiveGeneration    string   `json:"active_generation,omitempty"`
}

// ShareLinks holds external client export formats for a VLESS/XHTTP/FiniteProto client.
// MihomoYAML is strictly an external client YAML snippet export (Clash.Meta/Mihomo client apps),
// not a local router engine.
type ShareLinks struct {
	VlessURL       string `json:"vless_url"`
	FiniteProtoURL string `json:"finiteproto_url,omitempty"`
	HappJSON       string `json:"happ_json"`
	SingboxJSON    string `json:"singbox_json"`
	MihomoYAML     string `json:"mihomo_yaml"`
	Remark         string `json:"remark"`
	UUID           string `json:"uuid"`
}
