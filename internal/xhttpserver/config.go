package xhttpserver

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	FPKeySize              = 32
	FPKeyHexLen            = 64
	defaultStage2ProbeID   = "stage2probe"
	defaultMaxSessionsIP   = 16
	defaultMaxPreAuthTotal = 64
	defaultMaxPreAuthIP    = 8
	defaultMaxPreAuthBIP   = 256 * 1024
	defaultMaxPreAuthBGlob = 4 * 1024 * 1024
)

var (
	ErrNoValidClients       = errors.New("xhttpserver: at least one valid client UUID is required")
	ErrInvalidClientUUID    = errors.New("xhttpserver: invalid client UUID format")
	ErrInvalidTrustedProxy  = errors.New("xhttpserver: invalid trusted proxy IP or CIDR")
	ErrForbiddenDestination = errors.New("xhttpserver: destination blocked by private address ACL")
	ErrFPUUIDDerivedKey     = errors.New("xhttpserver: UUID-derived FiniteProto secret is forbidden")
	ErrFPInvalidCredentials = errors.New("xhttpserver: invalid FiniteProto credentials")
	ErrUnsupportedEgress    = errors.New("xhttpserver: unsupported egress mode")
	ErrCDaemonDisabled      = errors.New("xhttpserver: C backend awgm-xhttpd is explicitly disabled via AWGM_DISABLE_C_XHTTPD")
	ErrBinaryUnavailable    = errors.New("xhttpserver: C backend awgm-xhttpd binary is unavailable")
	ErrPortConflict         = errors.New("xhttpserver: listen address is already in use by another service")
)

// EgressConfig configures how awgm-xhttpd dials upstream targets.
type EgressConfig struct {
	Mode                     string // "router" | "direct" | "socks" | "interface" | "outbound"
	InterfaceName            string // Linux network interface for SO_BINDTODEVICE
	SocksAddr                string // SOCKS5 upstream (e.g. "127.0.0.1:1099")
	DNSProxyAddr             string // Local DNS resolver (e.g. "127.0.0.1:3553")
	DomainStrategy           string // Optional hint ("prefer_ipv4", "ipv4_only")
	AllowPrivateDestinations bool   // Allow RFC1918/loopback targets (LAN access)
}

// Config configures the C-backed VLESS XHTTP + FiniteProto server (awgm-xhttpd).
type Config struct {
	ListenAddr              string
	BinaryPath              string
	ConfigPath              string
	PathPrefix              string
	PathPrefixes            []string
	Clients                 []string
	FiniteCredentials       map[string][]byte
	TrustedProxies          []string
	EstablishedIdleTimeout  time.Duration
	NoDownlinkTimeout       time.Duration
	PreAuthSessionTTL       time.Duration
	MaxTotalSessions        int
	MaxSessionsPerIP        int
	MaxPreAuthSessions      int
	MaxPreAuthSessionsPerIP int
	MaxPreAuthBytesPerIP    int
	MaxPreAuthBytesGlobal   int
	Egress                  EgressConfig
}

// ParseUUID parses a canonical 36-char or 32-hex VLESS UUID into 16 bytes.
func ParseUUID(s string) ([16]byte, error) {
	var u [16]byte
	clean := strings.ReplaceAll(strings.TrimSpace(s), "-", "")
	if len(clean) != 32 {
		return u, fmt.Errorf("%w: %q", ErrInvalidClientUUID, s)
	}
	b, err := hex.DecodeString(clean)
	if err != nil || len(b) != 16 {
		return u, fmt.Errorf("%w: %q", ErrInvalidClientUUID, s)
	}
	copy(u[:], b)
	return u, nil
}

func formatUUIDCanonical(u [16]byte) string {
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

// GenerateFPCredentials creates a v2 independent random FiniteProto client ID and 32-byte secret.
func GenerateFPCredentials() (clientID string, secretHex string, secret []byte, err error) {
	var idBytes [8]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return "", "", nil, fmt.Errorf("generate fp client id: %w", err)
	}
	sec := make([]byte, FPKeySize)
	if _, err := rand.Read(sec); err != nil {
		return "", "", nil, fmt.Errorf("generate fp secret: %w", err)
	}
	return "fp_" + hex.EncodeToString(idBytes[:]), hex.EncodeToString(sec), sec, nil
}

// IsUUIDDerivedFPSecret checks if a 32-byte key is derived from the given UUID via any legacy formula.
func IsUUIDDerivedFPSecret(secret []byte, uuid [16]byte) bool {
	if len(secret) != FPKeySize {
		return false
	}
	canonical := formatUUIDCanonical(uuid)
	v1Sum := sha256.Sum256([]byte("awgm-fp-v1:" + canonical))
	if subtle.ConstantTimeCompare(secret, v1Sum[:]) == 1 {
		return true
	}
	rawSum := sha256.Sum256(uuid[:])
	if subtle.ConstantTimeCompare(secret, rawSum[:]) == 1 {
		return true
	}
	hexNoDash := hex.EncodeToString(uuid[:])
	if subtle.ConstantTimeCompare(secret, []byte(hexNoDash)) == 1 {
		return true
	}
	return false
}

func isUUIDDerivedSecretForAllowed(clientID string, secret []byte, allowed map[[16]byte]struct{}) bool {
	if len(secret) != FPKeySize {
		return true
	}
	allZero := true
	for _, b := range secret {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return true
	}
	if cidUUID, err := ParseUUID(clientID); err == nil {
		if IsUUIDDerivedFPSecret(secret, cidUUID) {
			return true
		}
	}
	for u := range allowed {
		if IsUUIDDerivedFPSecret(secret, u) {
			return true
		}
	}
	return false
}

// CheckDestinationACL verifies whether dialing host is permitted under allowPrivate.
func CheckDestinationACL(host string, allowPrivate bool) error {
	h := strings.TrimSpace(host)
	if h == "" {
		return ErrForbiddenDestination
	}
	if strings.EqualFold(h, "localhost") || strings.HasSuffix(strings.ToLower(h), ".localhost") {
		if !allowPrivate {
			return ErrForbiddenDestination
		}
		return nil
	}
	addr, err := netip.ParseAddr(h)
	if err != nil {
		return nil // Domain names are checked post-resolution by awgm-xhttpd
	}
	addr = addr.Unmap()
	if addr.IsUnspecified() || addr.IsMulticast() {
		return ErrForbiddenDestination
	}
	if !allowPrivate {
		if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() {
			return ErrForbiddenDestination
		}
		if addr.Is4() {
			b := addr.As4()
			// CGNAT 100.64.0.0/10 and benchmark 198.18.0.0/15
			if (b[0] == 100 && b[1] >= 64 && b[1] <= 127) || (b[0] == 198 && (b[1] == 18 || b[1] == 19)) {
				return ErrForbiddenDestination
			}
		}
	}
	return nil
}

func normalizePrefixes(cfg Config) []string {
	var raw []string
	if len(cfg.PathPrefixes) > 0 {
		raw = append(raw, cfg.PathPrefixes...)
	}
	if strings.TrimSpace(cfg.PathPrefix) != "" {
		raw = append(raw, cfg.PathPrefix)
	}
	if len(raw) == 0 {
		return []string{"/cdn-bridge"}
	}
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		clean := "/" + strings.Trim(strings.TrimSpace(p), "/")
		if clean == "/" {
			clean = "/cdn-bridge"
		}
		if _, ok := seen[clean]; !ok {
			seen[clean] = struct{}{}
			out = append(out, clean)
		}
	}
	return out
}

func validateTrustedProxies(proxies []string) ([]string, error) {
	if len(proxies) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(proxies))
	for _, raw := range proxies {
		s := strings.TrimSpace(raw)
		if s == "" {
			return nil, fmt.Errorf("%w: empty entry", ErrInvalidTrustedProxy)
		}
		if strings.Contains(s, "/") {
			pfx, err := netip.ParsePrefix(s)
			if err != nil {
				return nil, fmt.Errorf("%w: %q", ErrInvalidTrustedProxy, raw)
			}
			out = append(out, pfx.Masked().String())
		} else {
			ip, err := netip.ParseAddr(s)
			if err != nil {
				return nil, fmt.Errorf("%w: %q", ErrInvalidTrustedProxy, raw)
			}
			out = append(out, ip.String())
		}
	}
	return out, nil
}

func validateAndNormalizeConfig(cfg Config) ([]string, map[[16]byte]struct{}, []string, error) {
	if len(cfg.Clients) == 0 {
		return nil, nil, nil, ErrNoValidClients
	}
	allowed := make(map[[16]byte]struct{}, len(cfg.Clients))
	for _, c := range cfg.Clients {
		if strings.TrimSpace(c) == "" {
			continue
		}
		u, err := ParseUUID(c)
		if err != nil {
			return nil, nil, nil, err
		}
		allowed[u] = struct{}{}
	}
	if len(allowed) == 0 {
		return nil, nil, nil, ErrNoValidClients
	}

	trusted, err := validateTrustedProxies(cfg.TrustedProxies)
	if err != nil {
		return nil, nil, nil, err
	}

	mode := strings.ToLower(strings.TrimSpace(cfg.Egress.Mode))
	switch mode {
	case "", "router", "direct":
	case "socks":
		if strings.TrimSpace(cfg.Egress.SocksAddr) != "" {
			if _, _, err := net.SplitHostPort(strings.TrimSpace(cfg.Egress.SocksAddr)); err != nil {
				return nil, nil, nil, fmt.Errorf("%w: invalid socks address %q: %v", ErrUnsupportedEgress, cfg.Egress.SocksAddr, err)
			}
		}
	case "interface", "outbound":
		if strings.TrimSpace(cfg.Egress.InterfaceName) == "" {
			return nil, nil, nil, fmt.Errorf("%w: mode %q requires non-empty InterfaceName", ErrUnsupportedEgress, mode)
		}
	default:
		return nil, nil, nil, fmt.Errorf("%w: %q", ErrUnsupportedEgress, cfg.Egress.Mode)
	}

	return normalizePrefixes(cfg), allowed, trusted, nil
}

// Validate checks whether Config is structurally valid for awgm-xhttpd.
func (cfg Config) Validate() error {
	_, _, _, err := validateAndNormalizeConfig(cfg)
	return err
}

func renderCXHTTPDConfig(cfg Config, listenAddr string, prefixes []string, allowed map[[16]byte]struct{}, trusted []string) string {
	host, portStr, err := net.SplitHostPort(listenAddr)
	if err != nil || host == "" {
		host = "127.0.0.1"
		portStr = "9008"
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 {
		port = 9008
	}

	var b strings.Builder
	fmt.Fprintf(&b, "listen_host=%s\n", host)
	fmt.Fprintf(&b, "listen_port=%d\n", port)

	for _, p := range prefixes {
		if strings.TrimSpace(p) != "" {
			fmt.Fprintf(&b, "prefix=%s\n", strings.TrimSpace(p))
		}
	}

	clients := make([]string, 0, len(allowed))
	for u := range allowed {
		clients = append(clients, formatUUIDCanonical(u))
	}
	sort.Strings(clients)
	for _, canonical := range clients {
		fmt.Fprintf(&b, "client=%s\n", canonical)
	}

	if len(allowed) > 0 {
		creds := make(map[string][]byte, len(cfg.FiniteCredentials)+1)
		for rawID, sec := range cfg.FiniteCredentials {
			id := strings.TrimSpace(rawID)
			if id == "" || strings.ContainsAny(id, ":\r\n") || len(sec) != FPKeySize {
				continue
			}
			if isUUIDDerivedSecretForAllowed(id, sec, allowed) {
				continue
			}
			cp := make([]byte, FPKeySize)
			copy(cp, sec)
			creds[id] = cp
		}
		if os.Getenv("FP_ENABLE_DIAG_CRED") == "1" {
			diagHex := strings.TrimSpace(os.Getenv("FP_SECRET_HEX"))
			if len(diagHex) == FPKeyHexLen {
				if sec, err := hex.DecodeString(diagHex); err == nil && len(sec) == FPKeySize {
					if !isUUIDDerivedSecretForAllowed(defaultStage2ProbeID, sec, allowed) {
						creds[defaultStage2ProbeID] = sec
					}
				}
			}
		}
		fpIDs := make([]string, 0, len(creds))
		for id := range creds {
			fpIDs = append(fpIDs, id)
		}
		sort.Strings(fpIDs)
		for _, id := range fpIDs {
			fmt.Fprintf(&b, "fp_cred=%s:%s\n", id, hex.EncodeToString(creds[id]))
		}
	}

	mode := strings.ToLower(strings.TrimSpace(cfg.Egress.Mode))
	switch mode {
	case "":
		mode = "router"
	case "outbound":
		mode = "interface"
	}
	fmt.Fprintf(&b, "mode=%s\n", mode)
	if strings.TrimSpace(cfg.Egress.InterfaceName) != "" {
		fmt.Fprintf(&b, "interface=%s\n", strings.TrimSpace(cfg.Egress.InterfaceName))
	}

	sHost, sPortStr, err := net.SplitHostPort(strings.TrimSpace(cfg.Egress.SocksAddr))
	if err != nil || sHost == "" {
		sHost = "127.0.0.1"
		sPortStr = "1099"
	}
	fmt.Fprintf(&b, "socks_host=%s\n", sHost)
	fmt.Fprintf(&b, "socks_port=%s\n", sPortStr)

	dHost, dPortStr, err := net.SplitHostPort(strings.TrimSpace(cfg.Egress.DNSProxyAddr))
	if err != nil || dHost == "" {
		dHost = "127.0.0.1"
		dPortStr = "3553"
	}
	fmt.Fprintf(&b, "dns_proxy_host=%s\n", dHost)
	fmt.Fprintf(&b, "dns_proxy_port=%s\n", dPortStr)

	if cfg.Egress.AllowPrivateDestinations {
		fmt.Fprintf(&b, "allow_private=1\n")
	} else {
		fmt.Fprintf(&b, "allow_private=0\n")
	}

	idleSec := int(cfg.EstablishedIdleTimeout.Seconds())
	if idleSec <= 5 {
		idleSec = 120
	}
	fmt.Fprintf(&b, "idle_timeout_sec=%d\n", idleSec)

	maxSessIP := cfg.MaxSessionsPerIP
	if maxSessIP <= 0 {
		maxSessIP = defaultMaxSessionsIP
	}
	fmt.Fprintf(&b, "max_sessions_per_ip=%d\n", maxSessIP)

	maxPATotal := cfg.MaxPreAuthSessions
	if maxPATotal <= 0 {
		maxPATotal = defaultMaxPreAuthTotal
	}
	fmt.Fprintf(&b, "max_pre_auth_sessions=%d\n", maxPATotal)

	maxPAIP := cfg.MaxPreAuthSessionsPerIP
	if maxPAIP <= 0 {
		maxPAIP = defaultMaxPreAuthIP
	}
	fmt.Fprintf(&b, "max_pre_auth_per_ip=%d\n", maxPAIP)

	maxPABytesIP := cfg.MaxPreAuthBytesPerIP
	if maxPABytesIP <= 0 {
		maxPABytesIP = defaultMaxPreAuthBIP
	}
	fmt.Fprintf(&b, "max_pre_auth_bytes_per_ip=%d\n", maxPABytesIP)

	maxPABytesGlob := cfg.MaxPreAuthBytesGlobal
	if maxPABytesGlob <= 0 {
		maxPABytesGlob = defaultMaxPreAuthBGlob
	}
	fmt.Fprintf(&b, "max_pre_auth_bytes_global=%d\n", maxPABytesGlob)

	paTTLms := int64(cfg.PreAuthSessionTTL / time.Millisecond)
	if paTTLms <= 0 {
		paTTLms = 10000
	}
	fmt.Fprintf(&b, "pre_auth_ttl_ms=%d\n", paTTLms)

	for _, tp := range trusted {
		fmt.Fprintf(&b, "trusted_proxy=%s\n", tp)
	}
	return b.String()
}
