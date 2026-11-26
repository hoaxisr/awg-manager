package tgwebproxy

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
)

var (
	disabledUsersDummyOnce sync.Once
	disabledUsersDummyVal  string
	disabledUsersDummyErr  error
)

func disabledUsersDummySecret() (string, error) {
	disabledUsersDummyOnce.Do(func() {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			disabledUsersDummyErr = fmt.Errorf("generate dummy profile secret: %w", err)
			return
		}
		disabledUsersDummyVal = hex.EncodeToString(b)
	})
	return disabledUsersDummyVal, disabledUsersDummyErr
}

func seedReservedEndpointPorts(cfg Config) map[int]bool {
	listenPort := cfg.ListenPort
	if listenPort <= 0 {
		listenPort = DefaultListenPort
	}
	adminPort := cfg.AdminPort
	if adminPort <= 0 {
		adminPort = DefaultAdminPort
	}
	directPort := cfg.DirectPort
	if directPort <= 0 {
		directPort = DefaultDirectPort
	}
	used := map[int]bool{
		8084:           true,
		8086:           true,
		listenPort:     true,
		adminPort:      true,
		DefaultRawPort: true,
	}
	if cfg.Scenario != ScenarioCDNHTTP {
		used[directPort] = true
	}
	return used
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
			dummySec, err := disabledUsersDummySecret()
			if err != nil {
				return nil, err
			}
			profiles = append(profiles, map[string]interface{}{
				"name":         "default",
				"secret":       dummySec,
				"backend":      backend,
				"carrier_mode": mode,
			})
		}
	} else {
		sec := strings.TrimSpace(cfg.Secret)
		if sec == "" {
			var err error
			sec, err = disabledUsersDummySecret()
			if err != nil {
				return nil, err
			}
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
	publicHost := strings.ToLower(strings.TrimSpace(strings.Split(ep.Hostname, ",")[0]))
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
			return fmt.Errorf("%w: port conflict on :%d between %s and %s", ErrInvalidConfig, port, prev, owner)
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
				return fmt.Errorf("%w: endpoint %q external backend port :%d self-references Mux listener", ErrInvalidConfig, ep.Hostname, muxPort)
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
					return fmt.Errorf("%w: invalid cover upstream %q: %v", ErrInvalidConfig, upstream, err)
				}
				if upPort == muxPort || (!ep.ExternalBackend && upPort == epListen) {
					return fmt.Errorf("%w: cover upstream %q creates a loopback proxy loop with Telegram Web Proxy (:%d)", ErrInvalidConfig, upstream, upPort)
				}
				if owner, taken := used[upPort]; taken {
					return fmt.Errorf("%w: cover upstream %q conflicts with %s on port :%d", ErrInvalidConfig, upstream, owner, upPort)
				}
			}
		}
		if cover := strings.TrimSpace(cfg.CoverUpstream); cover != "" && !strings.EqualFold(cover, "none") {
			upPort, err := parseLoopbackHTTPPort(cover)
			if err != nil {
				return fmt.Errorf("%w: invalid cover upstream %q: %v", ErrInvalidConfig, cover, err)
			}
			if owner, taken := used[upPort]; taken {
				return fmt.Errorf("%w: cover upstream %q conflicts with %s on port :%d", ErrInvalidConfig, cover, owner, upPort)
			}
		}
	}
	return nil
}
