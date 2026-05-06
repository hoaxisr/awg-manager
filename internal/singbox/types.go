package singbox

import (
	"encoding/json"
	"fmt"
)

// TunnelInfo is the UI-facing summary of one sing-box tunnel.
// Derived from config.json: outbound + matching inbound + route rule.
type TunnelInfo struct {
	Tag            string `json:"tag"`
	Protocol       string `json:"protocol"` // "vless" | "hysteria2" | "naive"
	Server         string `json:"server"`
	Port           int    `json:"port"`
	Security       string `json:"security"`       // "reality" | "tls" | "none"
	Transport      string `json:"transport"`      // "tcp" | "grpc" | "quic" | "https"
	ListenPort     int    `json:"listenPort"`     // local SOCKS5 port
	ProxyInterface string `json:"proxyInterface"` // "Proxy0", "Proxy1"...

	// Protocol-specific hints (optional, for UI)
	SNI             string `json:"sni,omitempty"`
	Fingerprint     string `json:"fingerprint,omitempty"`
	Username        string `json:"username,omitempty"`
	KernelInterface string `json:"kernelInterface,omitempty"`

	// Running reports whether this specific tunnel is actually operational:
	// the sing-box process is alive AND the expected TUN interface exists
	// in the kernel. Computed at ListTunnels time (not persisted in config).
	// False when: sing-box is dead, sing-box is alive but hasn't yet created
	// the TUN (transient), or the tunnel has no kernelInterface hint.
	Running bool `json:"running"`
}

// ParsedOutbound is the result of parsing a share link.
type ParsedOutbound struct {
	Tag      string // from URI fragment (#name) or auto-generated
	Protocol string // "vless" | "hysteria2" | "naive"
	Server   string
	Port     int
	Outbound json.RawMessage // sing-box outbound JSON, ready to splice into config
}

// BatchError records which input failed to parse or apply.
type BatchError struct {
	Line  int
	Input string
	Err   error
}

func (e BatchError) Error() string {
	return fmt.Sprintf("line %d: %v", e.Line, e.Err)
}

// Status is the top-level process + install state.
type Status struct {
	Installed   bool   `json:"installed"`
	Version     string `json:"version,omitempty"`
	Running     bool   `json:"running"`
	PID         int    `json:"pid,omitempty"`
	TunnelCount int    `json:"tunnelCount"`
	// ProxyComponent reports whether the NDMS "proxy" component is
	// installed. Without it, ProxyN interfaces cannot be created and
	// sing-box integration cannot route any traffic — the binary may be
	// installed, but nothing works end-to-end.
	ProxyComponent bool `json:"proxyComponent"`
	// Features enumerates the build tags of the installed sing-box
	// binary (parsed from the `Tags:` line of `sing-box version`).
	// Example: ["with_gvisor","with_quic","with_naive_outbound",…].
	// The UI uses this to warn when a protocol the user configured
	// isn't supported by the installed build (e.g. NaiveProxy needs
	// with_naive_outbound).
	Features []string `json:"features,omitempty"`
	// LastError is the last fatal sing-box stderr message captured by
	// Process.OnExit. Cleared on successful start. UI surfaces this when
	// Running=false to explain why sing-box is down.
	LastError string `json:"lastError,omitempty"`
	// CurrentVersion is the version of the binary on disk ("" when not installed).
	CurrentVersion string `json:"currentVersion,omitempty"`
	// RequiredVersion is the version this awg-manager build is pinned to.
	RequiredVersion string `json:"requiredVersion"`
	// UpdateAvailable is true when CurrentVersion != "" and differs from RequiredVersion.
	UpdateAvailable bool `json:"updateAvailable"`
}

// InboundServerInfo represents a sing-box inbound server configuration.
type InboundServerInfo struct {
	Tag        string           `json:"tag"`
	Protocol   string           `json:"protocol"` // e.g., "vless", "hysteria2", "naive"
	Listen     string           `json:"listen"`   // e.g., "0.0.0.0"
	ListenPort int              `json:"listenPort"`
	TLS        *TLSConfig       `json:"tls,omitempty"`
	Users      []InboundUser    `json:"users,omitempty"`
	Reality    *RealityConfig   `json:"reality,omitempty"`
	Hysteria2  *Hysteria2Config `json:"hysteria2,omitempty"`
	Naive      *NaiveConfig     `json:"naive,omitempty"`
	Running    bool             `json:"running"` // computed at list time
}

// TLSConfig for inbound TLS settings.
type TLSConfig struct {
	Enabled         bool        `json:"enabled"`
	ServerName      string      `json:"serverName,omitempty"`
	CertificatePath string      `json:"certificatePath,omitempty"`
	KeyPath         string      `json:"keyPath,omitempty"`
	ACME            *ACMEConfig `json:"acme,omitempty"`
}

// ACMEConfig for automatic certificates.
type ACMEConfig struct {
	Domain   string `json:"domain"`
	Email    string `json:"email"`
	Provider string `json:"provider"` // e.g., "letsencrypt"
}

// InboundUser for protocols requiring authentication.
type InboundUser struct {
	Name     string `json:"name,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	UUID     string `json:"uuid,omitempty"` // for vless
	Flow     string `json:"flow,omitempty"` // for vless
}

// RealityConfig for VLESS Reality server mode.
type RealityConfig struct {
	Enabled           bool   `json:"enabled"`
	HandshakeServer   string `json:"handshakeServer,omitempty"`
	HandshakePort     int    `json:"handshakePort,omitempty"`
	PrivateKey        string `json:"privateKey,omitempty"`
	ShortID           string `json:"shortId,omitempty"`
	MaxTimeDifference string `json:"maxTimeDifference,omitempty"`
}

// Hysteria2Config for Hysteria2 inbound-specific settings.
type Hysteria2Config struct {
	UpMbps                int    `json:"upMbps,omitempty"`
	DownMbps              int    `json:"downMbps,omitempty"`
	ObfsPassword          string `json:"obfsPassword,omitempty"`
	IgnoreClientBandwidth bool   `json:"ignoreClientBandwidth,omitempty"`
}

// NaiveConfig for Naive inbound-specific settings.
type NaiveConfig struct {
	Network               string `json:"network,omitempty"` // "tcp" | "udp" | ""
	QuicCongestionControl string `json:"quicCongestionControl,omitempty"`
}

// ProcessState is the internal lifecycle state.
type ProcessState int

const (
	StateNotInstalled ProcessState = iota
	StateStopped
	StateRunning
	StateDead // PID file exists but process is gone
)
