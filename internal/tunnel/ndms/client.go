// Package ndms provides an interface for Keenetic NDMS operations.
// NDMS (Network Device Management System) is Keenetic's configuration management system.
// All operations are performed via the ndmc CLI tool.
package ndms

import (
	"context"
	"encoding/json"

	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// Client is the interface for NDMS operations.
// Used only on Keenetic OS 5.0+.
type Client interface {
	// OpkgTun management

	// CreateOpkgTun creates an OpkgTun interface in NDMS.
	// Commands: interface <name>, description, security-level, ip global
	CreateOpkgTun(ctx context.Context, name, description string) error

	// DeleteOpkgTun removes an OpkgTun interface from NDMS.
	// Commands: no interface <name>
	DeleteOpkgTun(ctx context.Context, name string) error

	// OpkgTunExists checks if an OpkgTun interface exists in NDMS.
	OpkgTunExists(ctx context.Context, name string) bool

	// ShowInterface returns raw "show interface <name>" output.
	// Used by state detection to parse conf layer and determine NDMS intent.
	ShowInterface(ctx context.Context, name string) (string, error)

	// Interface configuration

	// SetAddress sets the IPv4 address of an interface.
	// Address can be in CIDR notation (10.0.0.2/32) or plain IP (10.0.0.2).
	// If plain IP, /32 is assumed for point-to-point tunnels.
	// Command: interface <name> ip address <addr>
	SetAddress(ctx context.Context, name, address string) error

	// SetIPv6Address sets the IPv6 address of an interface.
	// Command: interface <name> ipv6 address <addr>/128
	SetIPv6Address(ctx context.Context, name, address string) error

	// ClearIPv6Address removes all IPv6 addresses from an interface.
	ClearIPv6Address(ctx context.Context, name string)

	// SetMTU sets the MTU of an interface.
	// Commands: interface <name> ip mtu <mtu>, ip tcp adjust-mss pmtu
	SetMTU(ctx context.Context, name string, mtu int) error

	// SetDNS registers DNS servers for a tunnel interface.
	// Command: ip name-server <dns_ip> <interface_name>
	// Called during Start/Reconcile to tell the router's DNS proxy
	// to use these servers for queries routed through this interface.
	SetDNS(ctx context.Context, name string, servers []string) error

	// ClearDNS removes DNS servers registered for a tunnel interface.
	// Command: no ip name-server <dns_ip> <interface_name>
	// Called during Stop/Delete to clean up.
	ClearDNS(ctx context.Context, name string, servers []string) error

	// SetDescription sets the description of an interface.
	SetDescription(ctx context.Context, name, description string) error

	// Interface state

	// InterfaceUp brings an interface up.
	// Command: interface <name> up
	InterfaceUp(ctx context.Context, name string) error

	// InterfaceDown brings an interface down.
	// Command: interface <name> down
	InterfaceDown(ctx context.Context, name string) error

	// IPv4 Routing

	// SetDefaultRoute sets the default IPv4 route via an interface.
	// Command: ip route default <name>
	SetDefaultRoute(ctx context.Context, name string) error

	// RemoveDefaultRoute removes the default IPv4 route for an interface.
	// Command: no ip route default <name>
	RemoveDefaultRoute(ctx context.Context, name string) error

	// RemoveHostRoute removes a host route.
	// Command: no ip route <host>
	RemoveHostRoute(ctx context.Context, host string) error

	// IPv6 Routing

	// SetIPv6DefaultRoute sets the default IPv6 route via an interface.
	SetIPv6DefaultRoute(ctx context.Context, name string) error

	// RemoveIPv6DefaultRoute removes the default IPv6 route for an interface.
	RemoveIPv6DefaultRoute(ctx context.Context, name string)

	// WAN interface detection

	// GetDefaultGatewayInterface returns the current default gateway interface.
	// Filters out tunnel interfaces to find the real ISP.
	GetDefaultGatewayInterface(ctx context.Context) (string, error)

	// GetInterfaceAddress returns the IPv4 address and mask of an interface.
	GetInterfaceAddress(ctx context.Context, iface string) (address, mask string, err error)

	// QueryAllWANInterfaces returns all WAN interfaces.
	// Used at boot to populate wan.Model. Exclusion filtering is the model's job.
	QueryAllWANInterfaces(ctx context.Context) ([]wan.Interface, error)

	// QueryAllInterfaces returns all router interfaces (no security-level filter).
	// Used for "show all interfaces" mode in routing UI.
	// Excludes only our own managed tunnels (opkgtun/awgm).
	QueryAllInterfaces(ctx context.Context) ([]AllInterface, error)

	// Diagnostics

	// DumpIPv4Routes returns NDMS IPv4 route table as a formatted string for diagnostics.
	DumpIPv4Routes(ctx context.Context) string

	// LAN client discovery

	// HasWANIPv6 checks if a WAN interface has a global IPv6 address.
	// Returns true when the interface's ipv6 layer is "running".
	HasWANIPv6(ctx context.Context, ifaceName string) bool

	// GetHotspotClients returns LAN devices from the router's hotspot table.
	// Used for access policy UI to let users select clients by hostname+IP.
	GetHotspotClients(ctx context.Context) ([]HotspotClient, error)

	// Name resolution

	// GetSystemName resolves an NDMS name to its kernel interface name.
	// Returns ndmsName unchanged if resolution fails.
	GetSystemName(ctx context.Context, ndmsName string) string

	// Configuration persistence

	// Save saves the current configuration.
	// Command: system configuration save
	Save(ctx context.Context) error

	// DNS routing (object-group fqdn)

	// RCIPost sends a JSON payload to RCI via HTTP POST.
	// Used for batch configuration commands.
	RCIPost(ctx context.Context, payload interface{}) (json.RawMessage, error)

	// ShowObjectGroupFQDN returns all FQDN object groups from the router.
	ShowObjectGroupFQDN(ctx context.Context) ([]ObjectGroupFQDN, error)

	// ShowDnsProxyRoute returns all dns-proxy route entries from the router.
	ShowDnsProxyRoute(ctx context.Context) ([]DnsProxyRoute, error)

	// ListWireguardInterfaces returns all Wireguard interfaces with descriptions.
	ListWireguardInterfaces(ctx context.Context) ([]WireguardInterfaceInfo, error)

	// System WireGuard tunnels (native Keenetic nwgX)

	// ListSystemWireguardTunnels returns all system Wireguard interfaces with full peer info.
	// Complements ListWireguardInterfaces (used for DNS routing) with richer data.
	ListSystemWireguardTunnels(ctx context.Context) ([]SystemWireguardTunnel, error)

	// GetSystemWireguardTunnel returns details for a single system Wireguard interface.
	GetSystemWireguardTunnel(ctx context.Context, name string) (*SystemWireguardTunnel, error)

	// GetASCParams returns AWG obfuscation parameters for a system Wireguard interface.
	// Returns ASCParams (9 fields) or ASCParamsExtended (16 fields) serialized as json.RawMessage.
	GetASCParams(ctx context.Context, name string) (json.RawMessage, error)

	// SetASCParams sets AWG obfuscation parameters on a system Wireguard interface.
	// Accepts json.RawMessage with 9 or 16 fields; determines ndmc command length by key presence.
	SetASCParams(ctx context.Context, name string, params json.RawMessage) error

	// VPN Server support

	// GetWireguardServer returns a server view of a WireGuard interface with all peers.
	GetWireguardServer(ctx context.Context, name string) (*WireguardServer, error)

	// GetWireguardServerConfig returns RC configuration for .conf generation.
	GetWireguardServerConfig(ctx context.Context, name string) (*WireguardServerConfig, error)

	// ListAllWireguardServers returns all WireGuard interfaces as server views (with all peers).
	// Unlike ListSystemWireguardTunnels, this does NOT filter out VPN Server.
	ListAllWireguardServers(ctx context.Context) ([]WireguardServer, error)

	// Managed server support

	// Ndmc executes an arbitrary ndmc command. Used by the managed server service
	// for WireGuard-specific commands (listen-port, peer, etc.).
	Ndmc(ctx context.Context, command string) (string, error)

	// FindFreeWireguardIndex returns the next free WireguardN index.
	FindFreeWireguardIndex(ctx context.Context) (int, error)

	// Ping-check profile management (NativeWG)

	// ConfigurePingCheck creates/updates a ping-check profile and binds it to an interface.
	ConfigurePingCheck(ctx context.Context, profile, ifaceName string, cfg PingCheckConfig) error

	// RemovePingCheck removes a ping-check profile and its interface binding.
	RemovePingCheck(ctx context.Context, profile, ifaceName string) error

	// ShowPingCheck returns the current status of a ping-check profile.
	ShowPingCheck(ctx context.Context, profile string) (*PingCheckStatus, error)
}

// PingCheckConfig holds configuration for an NDMS ping-check profile.
type PingCheckConfig struct {
	Host           string `json:"host"`
	Mode           string `json:"mode"`           // "icmp", "connect", "tls", "uri"
	UpdateInterval int    `json:"updateInterval"` // seconds (3-3600)
	MaxFails       int    `json:"maxFails"`        // 1-10
	MinSuccess     int    `json:"minSuccess"`      // 1-10
	Timeout        int    `json:"timeout"`         // seconds (1-10)
	Port           int    `json:"port,omitempty"`  // for connect/tls mode
	URI            string `json:"uri,omitempty"`   // for uri mode
	Restart        bool   `json:"restart"`         // auto-restart interface on fail
}

// PingCheckStatus holds the current state of an NDMS ping-check profile.
type PingCheckStatus struct {
	Exists       bool   `json:"exists"`
	Host         string `json:"host"`
	Mode         string `json:"mode"`
	Interval     int    `json:"interval"`
	MaxFails     int    `json:"maxFails"`
	MinSuccess   int    `json:"minSuccess"`
	Timeout      int    `json:"timeout"`
	Port         int    `json:"port,omitempty"`
	Restart      bool   `json:"restart"`
	Bound        bool   `json:"bound"`
	Status       string `json:"status"`       // "pass" | "fail" | ""
	FailCount    int    `json:"failCount"`
	SuccessCount int    `json:"successCount"`
}

// WireguardInterfaceInfo holds basic info about a Wireguard interface from NDMS.
type WireguardInterfaceInfo struct {
	Name        string // NDMS interface name (e.g. "Wireguard0")
	Description string // User-set description (e.g. "Home VPN")
}

// AllInterface represents any router interface for the "all interfaces" UI.
type AllInterface struct {
	Name  string `json:"name"`  // Kernel name (e.g., "br0", "eth3")
	Label string `json:"label"` // Human-readable label
	Up    bool   `json:"up"`    // IPv4 layer running
}

// HotspotClient represents a LAN device known to the router.
type HotspotClient struct {
	IP       string `json:"ip"`
	MAC      string `json:"mac"`
	Hostname string `json:"hostname"`
	Online   bool   `json:"online"`
}

// ObjectGroupFQDN represents an FQDN object group in the router.
// Parsed from "show object-group fqdn" which returns nested JSON:
//
//	{"group": [{"group-name": "...", "entry": [{"fqdn": "..."}], "excluded-fqdns": [{"address": "..."}]}]}
type ObjectGroupFQDN struct {
	Name     string
	Includes []string
	Excludes []string
}

// DnsProxyRoute represents a dns-proxy route entry.
// Parsed from "show rc dns-proxy route" which returns:
//
//	[{"group": "...", "interface": "..."}]
type DnsProxyRoute struct {
	Group     string `json:"group"`
	Interface string `json:"interface"`
	Auto      bool   `json:"auto,omitempty"`
	Reject    bool   `json:"reject,omitempty"`
}
