package tgwebproxy

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
)

// GenerateTelemtDirectConfig produces the TOML configuration for direct MTProxy (port 8443, Fake-TLS).
func GenerateTelemtDirectConfig(cfg Config) string {
	var b strings.Builder
	b.WriteString("[general]\n")
	b.WriteString("use_middle_proxy = false\n")
	b.WriteString("fast_mode = true\n")
	b.WriteString("log_level = \"normal\"\n")
	b.WriteString("upstream_connect_failfast_hard_errors = false\n")
	b.WriteString("direct_relay_copy_buf_c2s_bytes = 262144\n")
	b.WriteString("direct_relay_copy_buf_s2c_bytes = 524288\n")
	b.WriteString("quota_state_path = \"/opt/var/run/telemt.limit.json\"\n")
	b.WriteString("beobachten_file = \"/tmp/cache/beobachten.txt\"\n\n")

	b.WriteString("[general.modes]\n")
	b.WriteString("classic = false\n")
	b.WriteString("secure = false\n")
	b.WriteString("tls = true\n\n")

	b.WriteString("[network]\n")
	b.WriteString("ipv6 = false\n")
	b.WriteString("prefer = 4\n")
	b.WriteString("stun_use = false\n\n")

	port := cfg.DirectPort
	if port <= 0 {
		port = DefaultDirectPort
	}
	b.WriteString("[server]\n")
	fmt.Fprintf(&b, "port = %d\n\n", port)

	b.WriteString("[server.api]\n")
	b.WriteString("enabled = false\n\n")

	b.WriteString("[[server.listeners]]\n")
	b.WriteString("ip = \"0.0.0.0\"\n")
	fmt.Fprintf(&b, "port = %d\n\n", port)

	domain := strings.TrimSpace(cfg.TlsDomain)
	if domain == "" {
		domain = DefaultTlsDomain
	}
	b.WriteString("[censorship]\n")
	fmt.Fprintf(&b, "tls_domain = %q\n", domain)
	b.WriteString("mask = true\n")
	b.WriteString("tls_emulation = true\n")
	b.WriteString("tls_front_dir = \"/opt/etc/telemt/tlsfront\"\n")
	fmt.Fprintf(&b, "mask_host = %q\n", domain)
	b.WriteString("mask_shape_hardening_aggressive_mode = true\n\n")

	b.WriteString("[access.users]\n")
	if len(cfg.Users) > 0 {
		wroteAny := false
		for idx, u := range cfg.Users {
			if !u.Enabled || strings.TrimSpace(u.Secret) == "" {
				continue
			}
			keyName := fmt.Sprintf("user%d", idx+1)
			fmt.Fprintf(&b, "%s = %q\n", keyName, u.Secret)
			wroteAny = true
		}
		if !wroteAny {
			fmt.Fprintf(&b, "user1 = %q\n", "00000000000000000000000000000000")
		}
	} else {
		sec := strings.TrimSpace(cfg.Secret)
		if sec == "" {
			sec = "00000000000000000000000000000000"
		}
		fmt.Fprintf(&b, "user1 = %q\n", sec)
	}
	if cfg.LegacySecret != "" {
		fmt.Fprintf(&b, "user_legacy = %q\n", cfg.LegacySecret)
	}
	b.WriteString("\n")

	writeTelemtUpstreams(&b, cfg)

	return b.String()
}

// GenerateTelemtRawConfig produces the TOML configuration for loopback raw MTProxy (port 2398).
func GenerateTelemtRawConfig(cfg Config) string {
	var b strings.Builder
	b.WriteString("[general]\n")
	b.WriteString("use_middle_proxy = false\n")
	b.WriteString("log_level = \"normal\"\n")
	b.WriteString("upstream_connect_failfast_hard_errors = false\n")
	b.WriteString("direct_relay_copy_buf_c2s_bytes = 262144\n")
	b.WriteString("quota_state_path = \"/opt/var/run/telemt-raw.limit.json\"\n\n")

	b.WriteString("[general.modes]\n")
	b.WriteString("classic = true\n")
	b.WriteString("secure = true\n")
	b.WriteString("tls = false\n\n")

	b.WriteString("[network]\n")
	b.WriteString("ipv6 = false\n")
	b.WriteString("prefer = 4\n")
	b.WriteString("stun_use = false\n\n")

	b.WriteString("[server]\n")
	fmt.Fprintf(&b, "port = %d\n\n", DefaultRawPort)

	b.WriteString("[server.api]\n")
	b.WriteString("enabled = false\n\n")

	b.WriteString("[[server.listeners]]\n")
	b.WriteString("ip = \"127.0.0.1\"\n")
	fmt.Fprintf(&b, "port = %d\n\n", DefaultRawPort)

	b.WriteString("[censorship]\n")
	b.WriteString("mask = false\n")
	b.WriteString("tls_emulation = false\n\n")

	b.WriteString("[access.users]\n")
	if len(cfg.Users) > 0 {
		wroteAny := false
		for idx, u := range cfg.Users {
			if !u.Enabled || strings.TrimSpace(u.Secret) == "" {
				continue
			}
			keyName := fmt.Sprintf("user%d", idx+1)
			fmt.Fprintf(&b, "%s = %q\n", keyName, u.Secret)
			wroteAny = true
		}
		if !wroteAny {
			fmt.Fprintf(&b, "user1 = %q\n", "00000000000000000000000000000000")
		}
	} else {
		sec := strings.TrimSpace(cfg.Secret)
		if sec == "" {
			sec = "00000000000000000000000000000000"
		}
		fmt.Fprintf(&b, "user1 = %q\n", sec)
	}
	if cfg.LegacySecret != "" {
		fmt.Fprintf(&b, "user_legacy = %q\n", cfg.LegacySecret)
	}
	b.WriteString("\n")

	writeTelemtUpstreams(&b, cfg)

	return b.String()
}

func writeTelemtUpstreams(b *strings.Builder, cfg Config) {
	upstreams := cfg.EnabledUpstreams()
	if len(upstreams) == 0 {
		upstreams = []UpstreamSpec{{
			Type:    UpstreamTypeDirect,
			Weight:  1,
			Enabled: true,
		}}
	}
	for i, u := range upstreams {
		if i > 0 {
			b.WriteString("\n")
		}
		w := u.Weight
		if w <= 0 {
			w = 1
		}
		b.WriteString("[[upstreams]]\n")
		if u.Type == UpstreamTypeSocks5 {
			b.WriteString("type = \"socks5\"\n")
			fmt.Fprintf(b, "address = %q\n", u.Address)
			if u.Username != "" {
				fmt.Fprintf(b, "username = %q\n", u.Username)
			}
			if u.Password != "" {
				fmt.Fprintf(b, "password = %q\n", u.Password)
			}
			fmt.Fprintf(b, "weight = %d\n", w)
			b.WriteString("enabled = true\n")
		} else {
			b.WriteString("type = \"direct\"\n")
			if u.Device != "" && u.Device != "direct" {
				fmt.Fprintf(b, "bindtodevice = %q\n", u.Device)
			}
			fmt.Fprintf(b, "weight = %d\n", w)
			b.WriteString("enabled = true\n")
		}
	}
}

// GenerateEndpointProfilesJSON produces the JSON profiles for a specific tproxy-server endpoint.
func GenerateEndpointProfilesJSON(ep WebProxyEndpoint, cfg Config) ([]byte, error) {
	backend := cfg.Backend
	if backend == "" {
		backend = DefaultBackend
	}
	mode := EffectiveEndpointCarrierMode(ep, cfg)

	var profiles []map[string]interface{}
	if len(cfg.Users) > 0 {
		for idx, u := range cfg.Users {
			if !u.Enabled || strings.TrimSpace(u.Secret) == "" {
				continue
			}
			profName := u.ID
			if len(profiles) == 0 || profName == "" {
				profName = "default"
			} else if profName == "default" {
				profName = fmt.Sprintf("user_%d", idx+1)
			}
			profiles = append(profiles, map[string]interface{}{
				"name":         profName,
				"secret":       u.Secret,
				"backend":      backend,
				"carrier_mode": mode,
			})
		}
		if len(profiles) == 0 {
			profiles = append(profiles, map[string]interface{}{
				"name":         "default",
				"secret":       "00000000000000000000000000000000",
				"backend":      backend,
				"carrier_mode": mode,
			})
		}
	} else {
		sec := strings.TrimSpace(cfg.Secret)
		if sec == "" {
			sec = "00000000000000000000000000000000"
		}
		profiles = []map[string]interface{}{
			{
				"name":         "default",
				"secret":       sec,
				"backend":      backend,
				"carrier_mode": mode,
			},
		}
	}

	if cfg.LegacySecret != "" {
		profiles = append(profiles, map[string]interface{}{
			"name":         "legacy",
			"secret":       cfg.LegacySecret,
			"backend":      backend,
			"carrier_mode": mode,
		})
	}

	data := map[string]interface{}{
		"profiles": profiles,
	}
	return json.MarshalIndent(data, "", "  ")
}

// GenerateProfilesJSON produces the JSON profiles for primary tproxy-server.
func GenerateProfilesJSON(cfg Config) ([]byte, error) {
	eps := cfg.GetEndpoints()
	if len(eps) > 0 {
		return GenerateEndpointProfilesJSON(eps[0], cfg)
	}
	return GenerateEndpointProfilesJSON(WebProxyEndpoint{
		Preset:      cfg.NetworkPreset,
		CarrierMode: cfg.CarrierMode,
		IsCDN: (cfg.Scenario == ScenarioCDNHTTP || cfg.Scenario == ScenarioDual) &&
			!strings.EqualFold(cfg.CarrierMode, "websocket") &&
			cfg.NetworkPreset != PresetKeenDNS && cfg.NetworkPreset != PresetStaticIP,
	}, cfg)
}

func constrainTproxyLimitsForRAMMB(limits map[string]interface{}, totalRAMMB int) map[string]interface{} {
	if totalRAMMB <= 0 || totalRAMMB > 350 {
		return limits
	}
	if limits == nil {
		limits = make(map[string]interface{})
	}
	capInt := func(key string, ceiling int) {
		if v, ok := limits[key].(int); ok && v > 0 && v <= ceiling {
			return
		}
		limits[key] = ceiling
	}
	capInt("carrier_batch_bytes", 512*1024)
	capInt("max_pending_per_session", 4*1024*1024)
	capInt("max_streams_per_session", 64)
	limits["max_pending_global"] = 16 * 1024 * 1024
	limits["max_sessions_global"] = 16
	return limits
}

// GenerateEndpointTproxyConfigJSON produces config JSON for an individual tproxy-server endpoint.
func GenerateEndpointTproxyConfigJSON(ep WebProxyEndpoint, profilesPath, tokenKeyPath string) ([]byte, error) {
	listenPort := ep.ListenPort
	if listenPort <= 0 {
		listenPort = 8084
	}
	adminPort := ep.AdminPort
	if adminPort <= 0 {
		adminPort = 8086
	}
	publicHost := strings.ToLower(strings.TrimSpace(ep.Hostname))
	if publicHost == "" {
		publicHost = "direct-only.invalid"
	}

	timeouts := map[string]interface{}{
		"idle":            "300s",
		"reconnect_grace": "120s",
	}
	var limits map[string]interface{}

	if spec, ok := ResolvePresetSpec(ep.Preset); ok {
		if spec.IdleTimeout != "" {
			timeouts["idle"] = spec.IdleTimeout
		}
		if spec.ReconnectGrace != "" {
			timeouts["reconnect_grace"] = spec.ReconnectGrace
		}
		if spec.LongPollTimeout != "" {
			timeouts["long_poll"] = spec.LongPollTimeout
		}
		limits = map[string]interface{}{
			"carrier_batch_bytes":     spec.CarrierBatchBytes,
			"max_pending_per_session": spec.MaxPendingPerSession,
			"max_streams_per_session": spec.MaxStreamsPerSession,
		}
	}
	limits = constrainTproxyLimitsForRAMMB(limits, osdetect.GetTotalMemoryMB())

	configData := map[string]interface{}{
		"public_hostname": publicHost,
		"listen":          fmt.Sprintf("127.0.0.1:%d", listenPort),
		"admin_listen":    fmt.Sprintf("127.0.0.1:%d", adminPort),
		"token_key_file":  tokenKeyPath,
		"static_routes":   "exact",
		"profiles_file":   profilesPath,
		"timeouts":        timeouts,
	}
	coverUpstream := strings.TrimSpace(ep.PublicUpstream)
	if coverUpstream != "" && !strings.EqualFold(coverUpstream, "none") {
		configData["public_upstream"] = coverUpstream
	} else {
		configData["public_fallback"] = "builtin"
	}
	if len(limits) > 0 {
		configData["limits"] = limits
	}
	return json.MarshalIndent(configData, "", "  ")
}

// GenerateTproxyConfigJSON produces config.json for tproxy-server (primary/legacy).
func GenerateTproxyConfigJSON(cfg Config, profilesPath, tokenKeyPath string) ([]byte, error) {
	eps := cfg.GetEndpoints()
	if len(eps) > 0 {
		ep0 := eps[0]
		if ep0.Preset == "" && cfg.NetworkPreset != "" {
			ep0.Preset = InferEndpointPreset(ep0, cfg)
		}
		if ep0.PublicUpstream == "" && cfg.CoverUpstream != "" {
			ep0.PublicUpstream = cfg.CoverUpstream
		}
		return GenerateEndpointTproxyConfigJSON(ep0, profilesPath, tokenKeyPath)
	}

	listenPort := cfg.ListenPort
	if listenPort <= 0 || listenPort == DefaultListenPort {
		listenPort = 8084
	}
	adminPort := cfg.AdminPort
	if adminPort <= 0 {
		adminPort = DefaultAdminPort
	}
	publicHost := cfg.PublicHostname
	if publicHost == "" {
		if !cfg.IsWebEnabled() {
			publicHost = "direct-only.invalid"
		} else {
			publicHost = DefaultPublicHostname
		}
	}

	return GenerateEndpointTproxyConfigJSON(WebProxyEndpoint{
		Hostname:       publicHost,
		ListenPort:     listenPort,
		AdminPort:      adminPort,
		Preset:         cfg.NetworkPreset,
		PublicUpstream: cfg.CoverUpstream,
	}, profilesPath, tokenKeyPath)
}

func parseLoopbackHTTPPort(rawURL string) (int, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return 0, err
	}
	if u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return 0, fmt.Errorf("public_upstream must be a bare http://loopback:port URL")
	}
	host, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		return 0, fmt.Errorf("public_upstream must include an explicit loopback port")
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || !ip.IsLoopback() {
		return 0, fmt.Errorf("public_upstream host must be a numeric loopback IP")
	}
	p, err := strconv.Atoi(portStr)
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("public_upstream port %q is invalid", portStr)
	}
	return p, nil
}

// ValidatePortsAndUpstreams verifies that Mux, tproxy endpoints, MTProto ports,
// and optional cover upstreams do not collide or form self-referential loops.
func ValidatePortsAndUpstreams(cfg Config) error {
	if !cfg.Enabled {
		return nil
	}
	muxPort := cfg.ListenPort
	if muxPort <= 0 {
		muxPort = DefaultListenPort
	}
	directPort := cfg.DirectPort
	if directPort <= 0 {
		directPort = DefaultDirectPort
	}

	used := make(map[int]string)
	reserve := func(port int, owner string) error {
		if port <= 0 {
			return nil
		}
		if prev, exists := used[port]; exists {
			return fmt.Errorf("port conflict on :%d between %s and %s", port, prev, owner)
		}
		used[port] = owner
		return nil
	}

	if cfg.IsDirectEnabled() {
		if err := reserve(directPort, "MTProto Fake-TLS (direct)"); err != nil {
			return err
		}
	}
	if cfg.IsRawEnabled() {
		if err := reserve(DefaultRawPort, "MTProto raw backend"); err != nil {
			return err
		}
	}
	if cfg.IsWebEnabled() {
		if err := reserve(muxPort, "Telegram Web Proxy Mux"); err != nil {
			return err
		}
		for i, ep := range cfg.GetEndpoints() {
			if !ep.Enabled {
				continue
			}
			epListen := ep.ListenPort
			if epListen <= 0 {
				epListen = 8084
			}
			if ep.ExternalBackend && epListen == muxPort {
				return fmt.Errorf("endpoint %q external backend port :%d self-references Mux listener", ep.Hostname, muxPort)
			}
			if !ep.ExternalBackend {
				if err := reserve(epListen, fmt.Sprintf("tproxy endpoint %d listen", i+1)); err != nil {
					return err
				}
				epAdmin := ep.AdminPort
				if epAdmin <= 0 {
					epAdmin = 8086
				}
				if err := reserve(epAdmin, fmt.Sprintf("tproxy endpoint %d admin", i+1)); err != nil {
					return err
				}
			}
			upstream := strings.TrimSpace(ep.PublicUpstream)
			if upstream == "" {
				upstream = strings.TrimSpace(cfg.CoverUpstream)
			}
			if upstream != "" && !strings.EqualFold(upstream, "none") {
				upPort, err := parseLoopbackHTTPPort(upstream)
				if err != nil {
					return fmt.Errorf("invalid cover upstream %q: %w", upstream, err)
				}
				if upPort == muxPort || (!ep.ExternalBackend && upPort == epListen) {
					return fmt.Errorf("cover upstream %q creates a loopback proxy loop with Telegram Web Proxy (:%d)", upstream, upPort)
				}
				if owner, taken := used[upPort]; taken {
					return fmt.Errorf("cover upstream %q conflicts with %s on port :%d", upstream, owner, upPort)
				}
			}
		}
		if cover := strings.TrimSpace(cfg.CoverUpstream); cover != "" && !strings.EqualFold(cover, "none") {
			upPort, err := parseLoopbackHTTPPort(cover)
			if err != nil {
				return fmt.Errorf("invalid cover upstream %q: %w", cover, err)
			}
			if owner, taken := used[upPort]; taken {
				return fmt.Errorf("cover upstream %q conflicts with %s on port :%d", cover, owner, upPort)
			}
		}
	}
	return nil
}
