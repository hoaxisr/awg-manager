package telemt

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

const (
	ModeDirect = "direct" // Fake-TLS direct MTProxy
	ModeWeb    = "web"    // Native HTTP / WebSocket Web Proxy
)

// DefaultConfig returns the default configuration for telemt.
func DefaultConfig() Config {
	sec, _ := GenerateSecret()
	return Config{
		Enabled:        false,
		Mode:           ModeDirect,
		Port:           DefaultPort,
		ListenIP:       DefaultListenIP,
		Secret:         sec,
		TLSDomain:      DefaultTLSDomain,
		WebCarrier:     "https-lanes",
		WebDecoy:       "http://127.0.0.1:80",
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

// GenerateTOML produces the native TOML configuration for telemt in either Fake-TLS or Web mode.
func GenerateTOML(cfg Config) string {
	var b strings.Builder
	b.WriteString("# Managed by awg-manager. Manual edits will be overwritten.\n\n")

	b.WriteString("[general]\n")
	b.WriteString("use_middle_proxy = false\n")
	b.WriteString("log_level = \"normal\"\n")
	b.WriteString("upstream_connect_failfast_hard_errors = false\n\n")

	port := cfg.Port
	if port <= 0 {
		port = DefaultPort
	}
	listenIP := strings.TrimSpace(cfg.ListenIP)
	if listenIP == "" {
		listenIP = DefaultListenIP
	}

	secret := strings.TrimSpace(cfg.Secret)
	if secret == "" {
		secret, _ = GenerateSecret()
	}

	mode := cfg.Mode
	if mode == "" {
		mode = ModeDirect
	}

	if mode == ModeWeb {
		// Native Web-Proxy mode (HTTP/WebSocket carrier with decoy support)
		b.WriteString("[general.modes]\n")
		b.WriteString("classic = false\n")
		b.WriteString("secure = false\n")
		b.WriteString("tls = false\n\n")

		b.WriteString("[server]\n")
		fmt.Fprintf(&b, "port = %d\n\n", port)

		b.WriteString("[server.api]\n")
		b.WriteString("enabled = false\n\n")

		b.WriteString("[[server.listeners]]\n")
		fmt.Fprintf(&b, "ip = %q\n", listenIP)
		fmt.Fprintf(&b, "port = %d\n", port)
		b.WriteString("transport = \"web\"\n")
		b.WriteString("proxy_protocol = false\n")
		b.WriteString("web_client_ip_source = \"x_forwarded_for\"\n")
		b.WriteString("web_trusted_proxy_cidrs = [\"127.0.0.1/32\", \"192.168.0.0/16\", \"10.0.0.0/8\", \"172.16.0.0/12\"]\n\n")

		carrier := strings.TrimSpace(cfg.WebCarrier)
		if carrier == "" {
			carrier = "https-lanes"
		}

		b.WriteString("[web]\n")
		b.WriteString("enabled = true\n")
		fmt.Fprintf(&b, "carrier = %q\n", carrier)
		b.WriteString("decoy_fasttrack_mode = \"off\"\n")
		b.WriteString("http_connection_capacity_action = \"drop\"\n\n")

		webHost := strings.TrimSpace(cfg.WebHost)
		if webHost == "" {
			webHost = "127.0.0.1"
		}

		b.WriteString("[[web.vhosts]]\n")
		fmt.Fprintf(&b, "host = %q\n", webHost)
		fmt.Fprintf(&b, "public_addr = %q\n\n", fmt.Sprintf("%s:%d", webHost, port))

		decoy := strings.TrimSpace(cfg.WebDecoy)
		if decoy == "" {
			decoy = "http://127.0.0.1:80"
		}
		b.WriteString("[web.vhosts.decoy]\n")
		b.WriteString("mode = \"http_upstream\"\n")
		fmt.Fprintf(&b, "upstream = %q\n\n", decoy)

		b.WriteString("[[web.vhosts.profiles]]\n")
		b.WriteString("user = \"user1\"\n")
		b.WriteString("secret_mode = \"plain\"\n")
		b.WriteString("max_sessions = 16\n")
		b.WriteString("max_streams = 512\n")
		b.WriteString("max_streams_per_session = 64\n\n")

		b.WriteString("[access.users]\n")
		fmt.Fprintf(&b, "user1 = %q\n\n", secret)
	} else {
		// Native Fake-TLS mode (Direct MTProxy)
		b.WriteString("[general.modes]\n")
		b.WriteString("classic = false\n")
		b.WriteString("secure = false\n")
		b.WriteString("tls = true\n\n")

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
		fmt.Fprintf(&b, "user1 = %q\n\n", secret)
	}

	b.WriteString("[[upstreams]]\n")
	b.WriteString("type = \"direct\"\n")
	if cfg.UpstreamDevice != "" && cfg.UpstreamDevice != "direct" {
		fmt.Fprintf(&b, "bindtodevice = %q\n", cfg.UpstreamDevice)
	}

	return b.String()
}

// GenerateTgLink constructs the Telegram client proxy URL.
// In Fake-TLS mode: "tg://proxy?server=...&port=...&secret=ee..."
// In Web mode: "tg://webproxy?server=...&port=...&secret=..."
func GenerateTgLink(mode string, serverHost string, port int, secretHex string, domain string, webHost string) string {
	rawSecret := strings.TrimSpace(secretHex)
	if port <= 0 {
		port = DefaultPort
	}

	if mode == ModeWeb {
		targetHost := strings.TrimSpace(webHost)
		if targetHost == "" {
			targetHost = serverHost
		}
		if targetHost == "" || targetHost == "0.0.0.0" {
			targetHost = "192.168.1.1"
		}
		q := url.Values{}
		q.Set("server", targetHost)
		q.Set("port", strconv.Itoa(port))
		q.Set("secret", rawSecret)
		return "tg://webproxy?" + q.Encode()
	}

	// Fake-TLS mode
	domainClean := strings.TrimSpace(domain)
	if domainClean == "" {
		domainClean = DefaultTLSDomain
	}
	if serverHost == "" || serverHost == "0.0.0.0" {
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
