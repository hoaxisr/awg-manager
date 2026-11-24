package serverwizard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/cdndispatcher"
	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/serverwizard/cdn"
	"github.com/hoaxisr/awg-manager/internal/serverwizard/egress"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy"
	"github.com/hoaxisr/awg-manager/internal/vlessserver"
)

var (
	hostnameRegex = regexp.MustCompile(`^([a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$`)
)

// DesiredWizardConfig is the validated, normalized desired configuration before planning and execution.
// It MUST NOT store secrets (e.g. Telegram secret, Xray client UUID).
type DesiredWizardConfig struct {
	ServerKind          string `json:"server_kind"`
	Scenario            string `json:"scenario,omitempty"`
	DirectHost          string `json:"direct_host,omitempty"`
	DirectPort          int    `json:"direct_port,omitempty"`
	TlsDomain           string `json:"tls_domain,omitempty"`
	ListenPort          int    `json:"listen_port,omitempty"`
	TgListenPort        int    `json:"tg_listen_port,omitempty"`
	PublicHostname      string `json:"public_hostname,omitempty"`
	TgPublicHostname    string `json:"tg_public_hostname,omitempty"`
	PublicPort          int    `json:"public_port,omitempty"`
	Path                string `json:"path,omitempty"`
	DispatcherPort      int    `json:"dispatcher_port,omitempty"`
	CarrierMode         string `json:"carrier_mode,omitempty"`
	UpstreamDevice      string `json:"upstream_device,omitempty"`
	TgUpstreamDevice    string `json:"tg_upstream_device,omitempty"`
	CDNProfileID        string `json:"cdn_profile_id,omitempty"`
	Transport           string `json:"transport,omitempty"`
	Mode                string `json:"mode,omitempty"`
	UplinkMethod        string `json:"uplink_method,omitempty"`
	OutboundMode        string `json:"outbound_mode,omitempty"`
	OutboundInterface   string `json:"outbound_interface,omitempty"`
	OutboundSocksPort   int    `json:"outbound_socks_port,omitempty"`
	ClientRemark        string `json:"client_remark,omitempty"`
	ConnectionWay       string `json:"connection_way,omitempty"`
	OriginType          string `json:"origin_type,omitempty"`
	OriginHost          string `json:"origin_host,omitempty"`
	OriginScheme        string `json:"origin_scheme,omitempty"`
	OriginPort          int    `json:"origin_port,omitempty"`
	KeenDNSLabel        string `json:"keendns_label,omitempty"`
	DisableOtherService bool   `json:"disable_other_service,omitempty"`
}

func resolveTelegramWebPort(req WizardPlanRequest) int {
	if req.Kind == "tgwebproxy" && (req.ListenPort <= 0 || req.ListenPort == 9008) && req.TgListenPort > 0 && req.TgListenPort != 9008 {
		return req.TgListenPort
	}
	if req.ListenPort > 0 {
		return req.ListenPort
	}
	if req.TgListenPort > 0 {
		return req.TgListenPort
	}
	return 8085
}

func (r WizardPlanRequest) forXray() WizardPlanRequest {
	xr := r
	xr.Kind = "xray"
	if xr.ListenPort <= 0 {
		xr.ListenPort = 9008
	}
	return xr
}

func (r WizardPlanRequest) forTelegram() WizardPlanRequest {
	tr := r
	tr.Kind = "tgwebproxy"
	if strings.TrimSpace(r.TgPublicDomain) != "" {
		tr.PublicDomain = strings.TrimSpace(r.TgPublicDomain)
	}
	if strings.TrimSpace(tr.Scenario) == "direct_fake_tls" {
		tr.PublicDomain = ""
		tr.KeenDNSMode = ""
		tr.KeenDNSLabel = ""
		tr.OriginHost = ""
		tr.OriginScheme = ""
		tr.OriginPort = 0
	}
	if r.TgListenPort > 0 {
		tr.ListenPort = r.TgListenPort
	} else {
		tr.ListenPort = 8085
	}
	if strings.TrimSpace(r.TgUpstreamDevice) != "" {
		tr.UpstreamDevice = strings.TrimSpace(r.TgUpstreamDevice)
	}
	return tr
}

// SplitPublicHostnames splits a single or comma-separated public domain string into normalized FQDNs.
func SplitPublicHostnames(raw string) []string {
	parts := strings.Split(raw, ",")
	var out []string
	for _, p := range parts {
		h := strings.TrimSpace(p)
		if h != "" {
			out = append(out, h)
		}
	}
	return out
}

// ValidatePublicHostname validates that a public CDN hostname (or comma-separated list of hostnames)
// consists of valid FQDNs and not IP addresses.
func ValidatePublicHostname(host string) error {
	hosts := SplitPublicHostnames(host)
	if len(hosts) == 0 {
		return errors.New("public hostname cannot be empty")
	}
	for _, h := range hosts {
		if net.ParseIP(h) != nil {
			return errors.New("public hostname must be a valid domain name, not an IP address")
		}
		if !hostnameRegex.MatchString(h) {
			return fmt.Errorf("invalid public hostname format: %s", h)
		}
	}
	return nil
}

// ValidateDirectHost validates a direct host, which may be a valid domain or IP address.
func ValidateDirectHost(host string) error {
	host = strings.TrimSpace(host)
	if host == "" {
		return errors.New("direct host cannot be empty")
	}
	if net.ParseIP(host) != nil {
		return nil
	}
	if !hostnameRegex.MatchString(host) {
		return fmt.Errorf("invalid direct host format: %s", host)
	}
	return nil
}

// ValidateTlsDomain validates a Fake-TLS SNI domain.
func ValidateTlsDomain(domain string) error {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return errors.New("TLS domain cannot be empty")
	}
	if net.ParseIP(domain) != nil {
		return errors.New("TLS domain must be a valid domain name, not an IP address")
	}
	if !hostnameRegex.MatchString(domain) {
		return fmt.Errorf("invalid TLS domain format: %s", domain)
	}
	return nil
}

// BuildDesiredConfig validates and normalizes a WizardPlanRequest using the typed egress resolver.
// Egress direct materializes as "" (no nwg1 hardcoding).
// Strict CDN matrix: cdn_get + xhttp_get, cdn_ws + ws, cdn_full + xhttp_get/ws.
// Mandatory TlsDomain for direct/dual without fallback to req.Mode.
func BuildDesiredConfig(ctx context.Context, egressResolver egress.Resolver, req WizardPlanRequest) (DesiredWizardConfig, error) {
	desired := DesiredWizardConfig{
		ServerKind:          req.Kind,
		ConnectionWay:       strings.TrimSpace(req.ConnectionWay),
		OriginType:          strings.TrimSpace(req.OriginType),
		OriginHost:          strings.TrimSpace(req.OriginHost),
		OriginScheme:        strings.ToLower(strings.TrimSpace(req.OriginScheme)),
		OriginPort:          req.OriginPort,
		DisableOtherService: req.DisableOtherService,
	}
	rawScenario := strings.TrimSpace(req.Scenario)
	isTgDirectOnly := req.Kind == "tgwebproxy" && rawScenario == "direct_fake_tls"
	if isTgDirectOnly {
		kdMode := strings.ToLower(strings.TrimSpace(req.KeenDNSMode))
		if strings.TrimSpace(req.KeenDNSLabel) != "" || (kdMode != "" && kdMode != "manual") {
			return desired, errors.New("домен 4-го уровня KeenDNS поддерживает только HTTP/HTTPS (Web Proxy) и не используется для прямого MTProto")
		}
		desired.ConnectionWay = "direct"
		desired.OriginType = "public_address"
		desired.OriginHost = ""
		desired.OriginScheme = ""
		desired.OriginPort = 0
		desired.KeenDNSLabel = ""
	} else {
		if kdLabel := strings.ToLower(strings.TrimSpace(req.KeenDNSLabel)); kdLabel != "" {
			if err := ndmscommand.ValidateKeenDNSLabel(kdLabel); err != nil {
				return desired, fmt.Errorf("invalid KeenDNS label: %w", err)
			}
			desired.KeenDNSLabel = kdLabel
		}
		if desired.OriginHost != "" {
			if err := ValidateDirectHost(desired.OriginHost); err != nil {
				return desired, fmt.Errorf("invalid origin host: %w", err)
			}
			if desired.OriginScheme != "https" {
				desired.OriginScheme = "http"
			}
		}
	}

	switch req.Kind {
	case "tgwebproxy":
		scenario := rawScenario
		if scenario == "" {
			scenario = "dual"
		}
		switch scenario {
		case "direct_fake_tls", "cdn_http", "dual":
		default:
			return desired, fmt.Errorf("invalid telegram scenario: %s", scenario)
		}
		desired.Scenario = scenario

		if req.DirectPort != 0 && (scenario == "direct_fake_tls" || scenario == "dual") {
			if err := tgwebproxy.ValidateDirectPort(req.DirectPort); err != nil {
				return desired, err
			}
		}
		desired.DirectPort = req.DirectPort
		if desired.DirectPort <= 0 {
			desired.DirectPort = 8443
		}

		desired.ListenPort = resolveTelegramWebPort(req)
		if (scenario == "cdn_http" || scenario == "dual") && desired.ListenPort == 9008 {
			return desired, errors.New("port 9008 is reserved for VLESS (XHTTP) upstream; select another Telegram Web Proxy port (default 8085)")
		}

		desired.DirectHost = strings.TrimSpace(req.DirectHost)
		if scenario == "direct_fake_tls" || scenario == "dual" {
			if desired.DirectHost == "" {
				return desired, errors.New("direct host is required for direct or dual scenario")
			}
			if err := ValidateDirectHost(desired.DirectHost); err != nil {
				return desired, err
			}
		}

		desired.PublicHostname = strings.TrimSpace(req.PublicDomain)
		if scenario == "direct_fake_tls" {
			desired.PublicHostname = ""
		} else if scenario == "cdn_http" || scenario == "dual" {
			if desired.PublicHostname == "" {
				return desired, errors.New("public domain is required for CDN or dual scenario")
			}
			if err := ValidatePublicHostname(desired.PublicHostname); err != nil {
				return desired, err
			}
		}

		if scenario == "direct_fake_tls" || scenario == "dual" {
			tlsDomain := strings.TrimSpace(req.TlsDomain)
			if tlsDomain == "" {
				return desired, errors.New("TLS domain is required for direct or dual Telegram scenario")
			}
			if err := ValidateTlsDomain(tlsDomain); err != nil {
				return desired, err
			}
			desired.TlsDomain = tlsDomain
		} else {
			desired.TlsDomain = ""
		}

		desired.PublicPort = req.PublicPort
		if desired.PublicPort <= 0 {
			desired.PublicPort = 443
		}
		desired.DispatcherPort = req.DispatcherPort
		if desired.DispatcherPort <= 0 {
			desired.DispatcherPort = 9009
		}
		if desired.OriginHost != "" && desired.OriginPort <= 0 {
			desired.OriginPort = desired.DispatcherPort
		}

		// Validate port collisions BEFORE applying
		tgWebEnabled := scenario == "cdn_http" || scenario == "dual"
		tgDirectEnabled := scenario == "direct_fake_tls" || scenario == "dual"
		if tgWebEnabled {
			if desired.ListenPort == 9008 {
				return desired, errors.New("port 9008 is reserved for VLESS (XHTTP) upstream; select another Telegram Web Proxy port (default 8085)")
			}
			if desired.ListenPort == desired.DispatcherPort {
				return desired, fmt.Errorf("конфликт портов: Telegram Web Proxy (%d) совпадает с портом диспетчера (%d)", desired.ListenPort, desired.DispatcherPort)
			}
			if desired.ListenPort == 8084 || desired.ListenPort == 8086 || desired.ListenPort == tgwebproxy.DefaultRawPort {
				return desired, fmt.Errorf("порт %d зарезервирован для внутреннего компонента Telegram Web Proxy", desired.ListenPort)
			}
		}
		if tgDirectEnabled {
			if desired.DirectPort == 9008 {
				return desired, errors.New("port 9008 is reserved for VLESS (XHTTP); choose another MTProto direct port (default 8443)")
			}
			if desired.DirectPort == desired.DispatcherPort {
				return desired, fmt.Errorf("конфликт портов: MTProto Direct (%d) совпадает с портом диспетчера (%d)", desired.DirectPort, desired.DispatcherPort)
			}
			if desired.DirectPort == 8084 || desired.DirectPort == 8086 || desired.DirectPort == tgwebproxy.DefaultRawPort {
				return desired, fmt.Errorf("порт %d зарезервирован для внутреннего компонента Telegram", desired.DirectPort)
			}
		}
		if tgWebEnabled && tgDirectEnabled && desired.ListenPort == desired.DirectPort {
			return desired, fmt.Errorf("конфликт портов между Telegram Web (%d) и MTProto Direct (%d)", desired.ListenPort, desired.DirectPort)
		}

		// Validate CDN profile before apply
		cdnProfile := strings.TrimSpace(req.CdnProfileID)
		if cdnProfile != "" {
			if _, ok := cdn.GetProfile(cdnProfile); !ok {
				return desired, fmt.Errorf("unsupported CDN profile: %s (allowed: direct, cdn_get, cdn_ws, cdn_full)", cdnProfile)
			}
			if scenario == "direct_fake_tls" && cdnProfile != cdn.ProfileDirectID {
				return desired, fmt.Errorf("CDN profile %q is incompatible with direct_fake_tls scenario (use direct)", cdnProfile)
			}
			if tgWebEnabled && desired.ConnectionWay == "cdn" && cdnProfile == cdn.ProfileDirectID {
				return desired, errors.New("CDN connection way requires a proxied CDN profile (cdn_get, cdn_ws, or cdn_full)")
			}
		}
		desired.CDNProfileID = cdnProfile

		// Resolve egress via typed resolver
		if cdnProfile == "cdn_ws" || ((desired.ConnectionWay == "direct" || cdnProfile == "direct") && (req.CarrierMode == "" || req.CarrierMode == "https-lanes")) {
			desired.CarrierMode = "websocket"
		} else if req.CarrierMode != "" {
			desired.CarrierMode = req.CarrierMode
		} else {
			desired.CarrierMode = tgwebproxy.DefaultCarrierMode
		}
		if egressResolver != nil {
			res, err := egressResolver.Resolve(ctx, egress.ResolveRequest{
				ServerKind: "tgwebproxy",
				EgressID:   req.UpstreamDevice,
			})
			if err != nil {
				return desired, fmt.Errorf("resolve telegram egress: %w", err)
			}
			switch res.Mode {
			case "direct":
				desired.OutboundMode = "direct"
				desired.OutboundInterface = ""
				desired.UpstreamDevice = ""
			case "interface":
				desired.OutboundMode = "interface"
				desired.OutboundInterface = res.Interface
				desired.UpstreamDevice = res.Interface
			default:
				return desired, fmt.Errorf("unsupported egress mode for telegram: %s", res.Mode)
			}
		} else {
			dev := strings.TrimSpace(req.UpstreamDevice)
			if dev == "" || dev == "direct" {
				desired.OutboundMode = "direct"
				desired.OutboundInterface = ""
				desired.UpstreamDevice = ""
			} else if dev == "firc:router" || dev == "router" {
				return desired, errors.New("resolve telegram egress: режим маршрутизации FIRC не поддерживается для Telegram без явного выбора маршрута")
			} else if egress.IsLegacyEgress(dev) {
				return desired, fmt.Errorf("resolve telegram egress: %s (%s)", egress.LegacyEgressLabel(), dev)
			} else {
				desired.OutboundMode = "interface"
				desired.OutboundInterface = dev
				desired.UpstreamDevice = dev
			}
		}

	case "xray":
		desired.PublicHostname = strings.TrimSpace(req.PublicDomain)
		if desired.PublicHostname == "" {
			return desired, errors.New("public domain is required for Xray CDN server")
		}
		if err := ValidatePublicHostname(desired.PublicHostname); err != nil {
			return desired, err
		}

		desired.ListenPort = req.ListenPort
		if desired.ListenPort <= 0 {
			desired.ListenPort = 9008
		}

		desired.PublicPort = req.PublicPort
		if desired.PublicPort <= 0 {
			desired.PublicPort = 443
		}
		desired.Path = cdndispatcher.NormalizePathPrefix(req.Path)

		desired.DispatcherPort = req.DispatcherPort
		if desired.DispatcherPort <= 0 {
			desired.DispatcherPort = 9009
		}
		if desired.ListenPort == desired.DispatcherPort {
			return desired, fmt.Errorf("конфликт портов: VLESS (XHTTP) (%d) совпадает с портом диспетчера (%d)", desired.ListenPort, desired.DispatcherPort)
		}
		if desired.OriginHost != "" && desired.OriginPort <= 0 {
			desired.OriginPort = desired.DispatcherPort
		}

		desired.ClientRemark = strings.TrimSpace(req.ClientRemark)
		if desired.ClientRemark == "" {
			desired.ClientRemark = "Основное устройство"
		}

		profileID := req.CdnProfileID
		if profileID == "" {
			if desired.ConnectionWay == "direct" {
				profileID = "direct"
			} else {
				profileID = "cdn_get"
			}
		}
		desired.CDNProfileID = profileID

		if req.PreserveTransport && strings.TrimSpace(req.RawTransport) != "" {
			rawTr := strings.ToLower(strings.TrimSpace(req.RawTransport))
			if rawTr != "xhttp" {
				return desired, fmt.Errorf("%w: %q (only xhttp is supported)", vlessserver.ErrUnsupportedTransport, req.RawTransport)
			}
			desired.Transport = "xhttp"
			desired.Mode = strings.TrimSpace(req.RawMode)
			desired.UplinkMethod = strings.TrimSpace(req.RawUplinkMethod)
		} else {
			// Strict C-only XHTTP matrix:
			// cdn_get            + xhttp_get
			// cdn_full / direct  + xhttp_get
			// cdn_ws / ws        -> REJECTED (WebSocket transport is not supported by awgm-xhttpd)
			switch profileID {
			case "cdn_get":
				if req.Mode != "" && req.Mode != "xhttp_get" {
					return desired, fmt.Errorf("invalid mode %q for CDN profile %s (must be xhttp_get)", req.Mode, profileID)
				}
				desired.Transport = "xhttp"
				desired.Mode = "packet-up"
				desired.UplinkMethod = "GET"
			case "cdn_ws":
				return desired, fmt.Errorf("%w: CDN profile cdn_ws (WebSocket) is not supported by VLESS (XHTTP); choose cdn_get or cdn_full", vlessserver.ErrUnsupportedTransport)
			case "cdn_full", "direct":
				mode := req.Mode
				if mode == "" {
					mode = "xhttp_get"
				}
				switch mode {
				case "xhttp_get":
					desired.Transport = "xhttp"
					if profileID == "direct" {
						// KeenDNS / direct origin supports full-duplex HTTP/2 streaming POSTs.
						// stream-one uses 1 H2 stream per flow (vs 2 in stream-up), halving nginx/dispatcher
						// stream concurrency under Keenetic limits while eliminating split-stream teardown races.
						desired.Mode = "stream-one"
						desired.UplinkMethod = "POST"
					} else {
						desired.Mode = "packet-up"
						desired.UplinkMethod = "GET"
					}
				case "ws":
					return desired, fmt.Errorf("%w: WebSocket mode is not supported by VLESS (XHTTP); use xhttp_get", vlessserver.ErrUnsupportedTransport)
				default:
					return desired, fmt.Errorf("invalid mode %q for CDN profile %s (must be xhttp_get)", mode, profileID)
				}
			default:
				return desired, fmt.Errorf("unsupported CDN profile: %s (allowed: cdn_get, cdn_full, direct)", profileID)
			}
		}

		// Resolve egress via typed resolver
		if egressResolver != nil {
			res, err := egressResolver.Resolve(ctx, egress.ResolveRequest{
				ServerKind: "xray",
				EgressID:   req.UpstreamDevice,
			})
			if err != nil {
				return desired, fmt.Errorf("resolve xray egress: %w", err)
			}
			switch res.Mode {
			case "direct":
				desired.OutboundMode = "direct"
				desired.OutboundInterface = ""
				desired.OutboundSocksPort = 0
				desired.UpstreamDevice = ""
			case "router":
				desired.OutboundMode = "router"
				desired.OutboundInterface = ""
				desired.OutboundSocksPort = 0
				desired.UpstreamDevice = "firc:router"
			case "socks":
				desired.OutboundMode = "socks"
				desired.OutboundSocksPort = res.SocksPort
				desired.OutboundInterface = ""
				desired.UpstreamDevice = ""
			case "interface":
				desired.OutboundMode = "interface"
				desired.OutboundInterface = res.Interface
				desired.OutboundSocksPort = 0
				desired.UpstreamDevice = res.Interface
			default:
				return desired, fmt.Errorf("unsupported egress mode for xray: %s", res.Mode)
			}
		} else {
			dev := strings.TrimSpace(req.UpstreamDevice)
			if dev == "" || dev == "direct" {
				desired.OutboundMode = "direct"
				desired.OutboundInterface = ""
				desired.OutboundSocksPort = 0
				desired.UpstreamDevice = ""
			} else if dev == "firc:router" || dev == "router" {
				desired.OutboundMode = "router"
				desired.OutboundInterface = ""
				desired.OutboundSocksPort = 0
				desired.UpstreamDevice = "firc:router"
			} else if egress.IsLegacyEgress(dev) {
				return desired, fmt.Errorf("resolve xray egress: %s (%s)", egress.LegacyEgressLabel(), dev)
			} else {
				desired.OutboundMode = "interface"
				desired.OutboundInterface = dev
				desired.OutboundSocksPort = 0
				desired.UpstreamDevice = dev
			}
		}

	case "both":
		if (req.UpstreamDevice == "firc:router" || req.UpstreamDevice == "router") && strings.TrimSpace(req.TgUpstreamDevice) == "" {
			return desired, errors.New("telegram: при выборе правил маршрутизации (FIRC) для VLESS необходимо явно указать маршрут выхода трафика для Telegram (tg_upstream_device)")
		}
		xrayDesired, err := BuildDesiredConfig(ctx, egressResolver, req.forXray())
		if err != nil {
			return desired, fmt.Errorf("vless (xhttp): %w", err)
		}
		tgDesired, err := BuildDesiredConfig(ctx, egressResolver, req.forTelegram())
		if err != nil {
			return desired, fmt.Errorf("telegram: %w", err)
		}

		// Check internal port non-collision between Xray, Telegram Web, Telegram Direct, and Dispatcher
		usedPorts := map[int]string{
			xrayDesired.ListenPort:     "VLESS (XHTTP)",
			xrayDesired.DispatcherPort: "Входящие подключения (:9009)",
		}
		if tgDesired.Scenario == "cdn_http" || tgDesired.Scenario == "dual" {
			if prev, exists := usedPorts[tgDesired.ListenPort]; exists {
				return desired, fmt.Errorf("конфликт портов между Telegram Web (%d) и %s", tgDesired.ListenPort, prev)
			}
			usedPorts[tgDesired.ListenPort] = "Telegram Web"
		}
		if tgDesired.Scenario == "direct_fake_tls" || tgDesired.Scenario == "dual" {
			if prev, exists := usedPorts[tgDesired.DirectPort]; exists {
				return desired, fmt.Errorf("конфликт портов между Telegram MTProto (%d) и %s", tgDesired.DirectPort, prev)
			}
		}

		desired = xrayDesired
		desired.ServerKind = "both"
		desired.Scenario = tgDesired.Scenario
		desired.DirectHost = tgDesired.DirectHost
		desired.DirectPort = tgDesired.DirectPort
		desired.TlsDomain = tgDesired.TlsDomain
		desired.TgListenPort = tgDesired.ListenPort
		desired.TgPublicHostname = tgDesired.PublicHostname
		desired.CarrierMode = tgDesired.CarrierMode
		desired.TgUpstreamDevice = tgDesired.UpstreamDevice

	default:
		return desired, fmt.Errorf("unsupported server kind: %s", req.Kind)
	}

	return desired, nil
}

// NormalizeDesiredConfig is a convenience wrapper for backwards compatibility without resolver.
func NormalizeDesiredConfig(req WizardPlanRequest) (DesiredWizardConfig, error) {
	return BuildDesiredConfig(context.Background(), nil, req)
}
