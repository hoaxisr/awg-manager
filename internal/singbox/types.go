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
	// NDMSProxyEnabled mirrors Settings.CreateNDMSProxyForSingbox. When
	// false, the UI hides ProxyComponent warnings and renders sing-box
	// tunnel cards with a neutral "via sing-box" badge instead of the
	// per-tunnel ProxyN label.
	NDMSProxyEnabled bool `json:"ndmsProxyEnabled"`
	// Features — теги сборки установленного sing-box. У бинаря не
	// пробуются: для pinned-версии берутся из installer.RequiredTags,
	// для любой другой пусты (неизвестны). UI по ним предупреждает про
	// неподдерживаемый протокол (NaiveProxy → with_naive_outbound).
	Features []string `json:"features,omitempty"`
	// LastError is the last fatal sing-box stderr message captured by
	// Process.OnExit. Cleared on successful start. UI surfaces this when
	// Running=false to explain why sing-box is down.
	LastError string `json:"lastError,omitempty"`
	// CurrentVersion is the version of the binary on disk ("" when not installed).
	CurrentVersion string `json:"currentVersion,omitempty"`
	// RequiredVersion is the version this awg-manager build is pinned to.
	RequiredVersion string `json:"requiredVersion"`
	// CurrentSHA256 is the checksum of the binary on disk.
	CurrentSHA256 string `json:"currentSha256,omitempty"`
	// RequiredSHA256 is the checksum this awg-manager build is pinned to.
	RequiredSHA256 string `json:"requiredSha256,omitempty"`
	// UpdateAvailable is true when version or SHA256 differs from the pinned binary
	// (a UPX-packed copy of the pinned version does not count, see installer.MatchesPinnedBytes).
	UpdateAvailable bool `json:"updateAvailable"`
	// InstallState классифицирует состояние managed binary относительно
	// pin-версии и доступного места. UI рендерит на его основе.
	InstallState string `json:"installState"`
	// RequiredBytes — размер требуемого бинарника + safetyMargin (байты).
	// 0 когда RequiredSize неизвестен (старая схема).
	RequiredBytes int64 `json:"requiredBytes"`
	// FreeBytes — свободное место на FS managed binary dir (после diskReserveBytes-минус).
	// 0 когда statfs недоступен.
	FreeBytes int64 `json:"freeBytes"`
}

// ProcessState is the internal lifecycle state.
type ProcessState int

const (
	StateNotInstalled ProcessState = iota
	StateStopped
	StateRunning
	StateDead // PID file exists but process is gone
)
