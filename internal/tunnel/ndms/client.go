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
