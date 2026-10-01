package telemt

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
)

// DefaultConfig returns the default configuration for telemt.
func DefaultConfig() Config {
	sec, _ := GenerateSecret()
	return Config{
		Enabled:        false,
		Port:           DefaultPort,
		ListenIP:       DefaultListenIP,
		Secret:         sec,
		TLSDomain:      DefaultTLSDomain,
		UpstreamDevice: "direct",
	}
}

// GenerateSecret produces a 32-character hex secret (16 cryptographic random bytes).
func GenerateSecret() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// GenerateTOML produces the native TOML configuration for telemt.
func GenerateTOML(cfg Config) string {
	var b strings.Builder
	b.WriteString("# Managed by awg-manager. Manual edits will be overwritten.\n\n")

	b.WriteString("[general]\n")
	b.WriteString("use_middle_proxy = false\n")
	b.WriteString("log_level = \"normal\"\n")
	b.WriteString("upstream_connect_failfast_hard_errors = false\n\n")

	b.WriteString("[general.modes]\n")
	b.WriteString("classic = false\n")
	b.WriteString("secure = false\n")
	b.WriteString("tls = true\n\n")

	port := cfg.Port
	if port <= 0 {
		port = DefaultPort
	}
	listenIP := strings.TrimSpace(cfg.ListenIP)
	if listenIP == "" {
		listenIP = DefaultListenIP
	}

	b.WriteString("[server]\n")
	fmt.Fprintf(&b, "port = %d\n\n", port)

	b.WriteString("[server.api]\n")
	b.WriteString("enabled = false\n\n")

	b.WriteString("[[server.listeners]]\n")
	fmt.Fprintf(&b, "ip = %q\n", listenIP)
	fmt.Fprintf(&b, "port = %d\n\n", port)

	domain := strings.TrimSpace(cfg.TLSDomain)
	if domain == "" {
		domain = DefaultTLSDomain
	}
	b.WriteString("[censorship]\n")
	fmt.Fprintf(&b, "tls_domain = %q\n", domain)
	b.WriteString("mask = true\n")
	b.WriteString("tls_emulation = true\n")
	fmt.Fprintf(&b, "mask_host = %q\n", domain)
	b.WriteString("mask_shape_hardening_aggressive_mode = true\n\n")

	b.WriteString("[access.users]\n")
	fmt.Fprintf(&b, "user1 = %q\n\n", strings.TrimSpace(cfg.Secret))

	b.WriteString("[[upstreams]]\n")
	b.WriteString("type = \"direct\"\n")
	if cfg.UpstreamDevice != "" && cfg.UpstreamDevice != "direct" {
		fmt.Fprintf(&b, "bindtodevice = %q\n", cfg.UpstreamDevice)
	}

	return b.String()
}

// GenerateTgLink constructs the Telegram client proxy URL with Fake-TLS secret encoding.
// Secret in Fake-TLS mode: "ee" + 32-char hex secret + hex-encoded TLS domain.
func GenerateTgLink(serverHost string, port int, secretHex string, domain string) string {
	rawSecret := strings.TrimSpace(secretHex)
	domainClean := strings.TrimSpace(domain)
	if domainClean == "" {
		domainClean = DefaultTLSDomain
	}
	if port <= 0 {
		port = DefaultPort
	}
	if serverHost == "" {
		serverHost = "192.168.1.1"
	}

	var fullSecret string
	if strings.HasPrefix(rawSecret, "ee") && len(rawSecret) > 34 {
		fullSecret = rawSecret
	} else {
		domainHex := hex.EncodeToString([]byte(domainClean))
		fullSecret = "ee" + rawSecret + domainHex
	}

	q := url.Values{}
	q.Set("server", serverHost)
	q.Set("port", fmt.Sprintf("%d", port))
	q.Set("secret", fullSecret)

	return "tg://proxy?" + q.Encode()
}
