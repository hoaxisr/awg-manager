package singbox

import "encoding/json"

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
}

// ProcessState is the internal lifecycle state.
type ProcessState int

const (
	StateNotInstalled ProcessState = iota
	StateStopped
	StateRunning
	StateDead // PID file exists but process is gone
)
