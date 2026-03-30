// Package tunnel provides tunnel management with clean architecture.
// This is the v2 implementation replacing the legacy awg/service.go and sys/tunnel/lifecycle.go.
package tunnel

import (
	"fmt"
	"strings"
	"time"
)

// ISPInterfaceAuto is the sentinel value for "auto-detect ISP interface".
// Used by the routing page to explicitly set auto-detect. Normalized to ""
// before saving to storage — this is a transport-only value.
const ISPInterfaceAuto = "auto"

// ISPInterfaceTunnelPrefix is the prefix for routing through another tunnel.
// Format: "tunnel:<tunnelID>" (e.g., "tunnel:awg10").
const ISPInterfaceTunnelPrefix = "tunnel:"

// IsTunnelRoute checks if ispInterface refers to another tunnel.
func IsTunnelRoute(ispInterface string) bool {
	return strings.HasPrefix(ispInterface, ISPInterfaceTunnelPrefix)
}

// TunnelRouteID extracts the tunnel ID from a "tunnel:<id>" ISPInterface value.
func TunnelRouteID(ispInterface string) string {
	return strings.TrimPrefix(ispInterface, ISPInterfaceTunnelPrefix)
}

// SystemTunnelPrefix is the prefix for system (unmanaged) tunnel IDs.
// Format: "system:<NDMSName>" (e.g., "system:Wireguard0").
const SystemTunnelPrefix = "system:"

// IsSystemTunnel checks if a tunnelID refers to a system (unmanaged) tunnel.
func IsSystemTunnel(tunnelID string) bool {
	return strings.HasPrefix(tunnelID, SystemTunnelPrefix)
}

// SystemTunnelName extracts the NDMS name from a system tunnel ID.
func SystemTunnelName(tunnelID string) string {
	return strings.TrimPrefix(tunnelID, SystemTunnelPrefix)
}

// State represents the current state of a tunnel.
type State int

const (
	// StateUnknown indicates the state could not be determined.
	StateUnknown State = iota
	// StateNotCreated means the tunnel has never been created (no OpkgTun in NDMS).
	StateNotCreated
	// StateStopped means the tunnel exists but is not running (process dead, interface down).
	StateStopped
	// StateStarting means the tunnel is in the process of starting.
	StateStarting
	// StateRunning means the tunnel is fully operational.
	StateRunning
	// StateStopping means the tunnel is in the process of stopping.
	StateStopping
	// StateBroken means the tunnel is in an inconsistent state requiring recovery.
	StateBroken
	// StateNeedsStart means conf: running but no process (after reboot / kill).
	StateNeedsStart
	// StateNeedsStop means conf: disabled but process still alive (toggle off in router UI).
	StateNeedsStop
	// StateDisabled means conf: disabled and all clean (admin turned off).
	StateDisabled
)

// String returns a human-readable representation of the state.
func (s State) String() string {
	switch s {
	case StateUnknown:
		return "unknown"
	case StateNotCreated:
		return "not_created"
	case StateStopped:
		return "stopped"
	case StateStarting:
		return "starting"
	case StateRunning:
		return "running"
	case StateStopping:
		return "stopping"
	case StateBroken:
		return "broken"
	case StateNeedsStart:
		return "needs_start"
	case StateNeedsStop:
		return "needs_stop"
	case StateDisabled:
		return "disabled"
	default:
		return fmt.Sprintf("state(%d)", s)
	}
}

// IsTerminal returns true if this is a stable state (not transitioning).
func (s State) IsTerminal() bool {
	switch s {
	case StateNotCreated, StateStopped, StateRunning, StateBroken, StateDisabled:
		return true
	default:
		return false
	}
}

// StateInfo contains comprehensive information about a tunnel's state.
type StateInfo struct {
	// State is the determined tunnel state.
	State State `json:"state"`

	// Component states
	OpkgTunExists  bool      `json:"opkgTunExists"`  // OpkgTun registered in NDMS
	InterfaceUp    bool      `json:"interfaceUp"`     // Interface is in UP state
	ProcessRunning bool      `json:"processRunning"`  // tunnel process is alive
	ProcessPID     int       `json:"processPID"`      // PID of the process (0 if not running)
	HasPeer        bool      `json:"hasPeer"`         // WireGuard peer is configured
	HasHandshake   bool      `json:"hasHandshake"`    // Recent handshake occurred
	LastHandshake  time.Time `json:"lastHandshake"`

	// Traffic statistics (from sysfs)
	RxBytes int64 `json:"rxBytes"`
	TxBytes int64 `json:"txBytes"`

	// Backend information
	BackendType string `json:"backendType,omitempty"` // "kernel" or "nativewg"

	// Connection timestamp (from NDMS "connected" field for nativewg)
	ConnectedAt string `json:"connectedAt,omitempty"`

	// PeerVia is the NDMS WAN name the peer routes through (e.g. "PPPoE0").
	// Populated for NativeWG tunnels from RCI show interface peer "via" field.
	PeerVia string `json:"peerVia,omitempty"`

	// Diagnostics
	Error   error  `json:"error"`            // Error encountered during state detection
	Details string `json:"details,omitempty"` // Human-readable details about the state
}

// Config holds configuration for tunnel operations.
type Config struct {
	// Identity
	ID   string // Tunnel ID (e.g., "awg0")
	Name string // Human-readable name

	// Network configuration
	Address     string   // IPv4 address (e.g., "10.0.0.1")
	AddressIPv6 string   // IPv6 address (optional)
	MTU         int      // MTU size (default 1420)
	DNS         []string // DNS servers to apply on the router (from .conf DNS field)

	// WireGuard configuration
	ConfPath string // Path to .conf file

	// Routing
	DefaultRoute bool   // Create NDMS default route (ip route default OpkgTunX)
	ISPInterface string // NDMS WAN interface name for ActiveWAN tracking (empty = auto-detect)
	KernelDevice string // Kernel interface name for endpoint routing (e.g., "eth3"); empty = no oif constraint
	EndpointIP   string // Resolved endpoint IP (for routing)
	Endpoint     string // Original endpoint (host:port)
}

// Validate checks if the configuration is valid.
func (c *Config) Validate() error {
	if c.ID == "" {
		return fmt.Errorf("tunnel ID is required")
	}
	if c.Address == "" {
		return fmt.Errorf("address is required")
	}
	if c.MTU <= 0 {
		c.MTU = 1420 // default
	}
	if c.ConfPath == "" {
		return fmt.Errorf("config path is required")
	}
	return nil
}

// Names contains all naming conventions for a tunnel.
type Names struct {
	TunnelID   string // Original tunnel ID (e.g., "awg0")
	TunnelNum  string // Numeric suffix (e.g., "0")
	NDMSName   string // NDMS interface name (e.g., "OpkgTun0")
	IfaceName  string // Kernel interface name (e.g., "opkgtun0")
	ConfPath   string // Config file path
	PIDPath    string // PID file path
	SocketPath string // Control socket path
}

// NewNames creates a Names struct from a tunnel ID.
// Handles different naming conventions:
// - OS 5.x: awg10 -> OpkgTun10/opkgtun10 (valid indices: 10-16)
// - OS 4.x: awgm0 -> awgm0 (direct, no NDMS)
// - Legacy: awg0 -> OpkgTun0/opkgtun0
func NewNames(tunnelID string) Names {
	num := extractTunnelNum(tunnelID)

	var ndmsName, ifaceName string

	// Check if this is an OS4-style "awgmX" tunnel
	if strings.HasPrefix(tunnelID, "awgm") {
		// OS 4.x style: interface name is the tunnel ID itself
		ifaceName = tunnelID
		ndmsName = "" // No NDMS on OS4
	} else {
		// OS 5.x style or legacy: use OpkgTunX/opkgtunX
		ndmsName = fmt.Sprintf("OpkgTun%s", num)
		ifaceName = strings.ToLower(ndmsName)
	}

	return Names{
		TunnelID:   tunnelID,
		TunnelNum:  num,
		NDMSName:   ndmsName,
		IfaceName:  ifaceName,
		ConfPath:   fmt.Sprintf("/opt/etc/awg-manager/%s.conf", tunnelID),
		PIDPath:    fmt.Sprintf("/opt/var/run/awg-manager/%s.pid", tunnelID),
		SocketPath: fmt.Sprintf("/tmp/run/amneziawg/%s.sock", ifaceName),
	}
}

// extractTunnelNum extracts the numeric suffix from a tunnel ID.
// Examples:
// - "awg0" -> "0"
// - "awg123" -> "123"
// - "awgm0" -> "0"
// - "awgm5" -> "5"
func extractTunnelNum(id string) string {
	for i := 0; i < len(id); i++ {
		if id[i] >= '0' && id[i] <= '9' {
			return id[i:]
		}
	}
	return "0"
}

