package tgwebproxy

import (
	"encoding/json"
	"fmt"
	"strings"
)

// GenerateTelemtDirectConfig produces the TOML configuration for direct MTProxy (port 8443, Fake-TLS).
func GenerateTelemtDirectConfig(cfg Config) string {
	var b strings.Builder
	b.WriteString("[general]\n")
	b.WriteString("use_middle_proxy = false\n")
	b.WriteString("log_level = \"normal\"\n")
	b.WriteString("upstream_connect_failfast_hard_errors = false\n")
	b.WriteString("quota_state_path = \"/opt/var/run/telemt.limit.json\"\n")
	b.WriteString("beobachten_file = \"/tmp/cache/beobachten.txt\"\n\n")

	b.WriteString("[general.modes]\n")
	b.WriteString("classic = false\n")
	b.WriteString("secure = false\n")
	b.WriteString("tls = true\n\n")

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

	domain := cfg.TlsDomain
	b.WriteString("[censorship]\n")
	fmt.Fprintf(&b, "tls_domain = %q\n", domain)
	b.WriteString("mask = true\n")
	b.WriteString("tls_emulation = true\n")
	b.WriteString("tls_front_dir = \"/opt/etc/telemt/tlsfront\"\n")
	fmt.Fprintf(&b, "mask_host = %q\n", domain)
	b.WriteString("mask_shape_hardening_aggressive_mode = true\n\n")

	b.WriteString("[access.users]\n")
	fmt.Fprintf(&b, "user1 = %q\n", cfg.Secret)
	if cfg.LegacySecret != "" {
		fmt.Fprintf(&b, "user_legacy = %q\n", cfg.LegacySecret)
	}
	b.WriteString("\n")

	b.WriteString("[[upstreams]]\n")
	b.WriteString("type = \"direct\"\n")
	if cfg.UpstreamDevice != "" && cfg.UpstreamDevice != "direct" {
		fmt.Fprintf(&b, "bindtodevice = %q\n", cfg.UpstreamDevice)
	}

	return b.String()
}

// GenerateTelemtRawConfig produces the TOML configuration for loopback raw MTProxy (port 2398).
func GenerateTelemtRawConfig(cfg Config) string {
	var b strings.Builder
	b.WriteString("[general]\n")
	b.WriteString("use_middle_proxy = false\n")
	b.WriteString("log_level = \"normal\"\n")
	b.WriteString("upstream_connect_failfast_hard_errors = false\n")
	b.WriteString("quota_state_path = \"/opt/var/run/telemt-raw.limit.json\"\n\n")

	b.WriteString("[general.modes]\n")
	b.WriteString("classic = true\n")
	b.WriteString("secure = true\n")
	b.WriteString("tls = false\n\n")

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
	fmt.Fprintf(&b, "user1 = %q\n", cfg.Secret)
	if cfg.LegacySecret != "" {
		fmt.Fprintf(&b, "user_legacy = %q\n", cfg.LegacySecret)
	}
	b.WriteString("\n")

	b.WriteString("[[upstreams]]\n")
	b.WriteString("type = \"direct\"\n")
	if cfg.UpstreamDevice != "" && cfg.UpstreamDevice != "direct" {
		fmt.Fprintf(&b, "bindtodevice = %q\n", cfg.UpstreamDevice)
	}

	return b.String()
}

// GenerateProfilesJSON produces the JSON profiles for tproxy-server.
func GenerateProfilesJSON(cfg Config) ([]byte, error) {
	backend := cfg.Backend
	if backend == "" {
		backend = DefaultBackend
	}
	mode := cfg.CarrierMode
	if mode == "" {
		mode = DefaultCarrierMode
	}

	profiles := []map[string]interface{}{
		{
			"name":         "default",
			"secret":       cfg.Secret,
			"backend":      backend,
			"carrier_mode": mode,
		},
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

// GenerateTproxyConfigJSON produces config.json for tproxy-server.
func GenerateTproxyConfigJSON(cfg Config, profilesPath, tokenKeyPath string) ([]byte, error) {
	listenPort := cfg.ListenPort
	if listenPort <= 0 {
		listenPort = DefaultListenPort
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

	configData := map[string]interface{}{
		"public_hostname": publicHost,
		"listen":          fmt.Sprintf("127.0.0.1:%d", listenPort),
		"admin_listen":    fmt.Sprintf("127.0.0.1:%d", adminPort),
		"public_upstream": "http://127.0.0.1:9008",
		"token_key_file":  tokenKeyPath,
		"static_routes":   "exact",
		"profiles_file":   profilesPath,
	}
	return json.MarshalIndent(configData, "", "  ")
}
