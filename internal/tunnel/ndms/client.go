// Package ndms provides an interface for Keenetic NDMS operations.
// NDMS (Network Device Management System) is Keenetic's configuration management system.
// All operations are performed via the ndmc CLI tool.
package ndms

import (
	"context"

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

	// IsInterfaceUp checks if an interface is in UP state.
	IsInterfaceUp(ctx context.Context, name string) bool

	// IPv4 Routing

	// SetDefaultRoute sets the default IPv4 route via an interface.
	// Command: ip route default <name>
	SetDefaultRoute(ctx context.Context, name string) error

	// RemoveDefaultRoute removes the default IPv4 route for an interface.
	// Command: no ip route default <name>
	RemoveDefaultRoute(ctx context.Context, name string) error

	// AddHostRoute adds a route to a specific host via a gateway IP.
	// Command: ip route <host> <gateway>
	// The gateway must be an IP address (not interface name) to ensure
	// proper next-hop resolution. Using interface name creates a connected
	// route without gateway, which fails for remote hosts.
	AddHostRoute(ctx context.Context, host, gateway string) error

	// RemoveHostRoute removes a host route.
	// Command: no ip route <host>
	RemoveHostRoute(ctx context.Context, host string) error

	// IPv6 Routing

	// SetIPv6DefaultRoute sets the default IPv6 route via an interface.
	SetIPv6DefaultRoute(ctx context.Context, name string) error

	// RemoveIPv6DefaultRoute removes the default IPv6 route for an interface.
	RemoveIPv6DefaultRoute(ctx context.Context, name string)

	// GetDefaultIPv6Gateway returns the IPv6 gateway and interface name for the default route.
	// Mirrors GetDefaultGateway but uses "show ipv6 route".
	GetDefaultIPv6Gateway(ctx context.Context) (gateway, iface string, err error)

	// GetDefaultIPv6GatewayWithRetry returns the IPv6 gateway and interface with retry logic.
	GetDefaultIPv6GatewayWithRetry(ctx context.Context) (gateway, iface string, err error)

	// GetIPv6GatewayForInterface returns the IPv6 gateway used by a specific interface.
	// Mirrors GetGatewayForInterface but uses "show ipv6 route".
	GetIPv6GatewayForInterface(ctx context.Context, iface string) (string, error)

	// WAN interface detection

	// GetDefaultGatewayInterface returns the current default gateway interface.
	// Filters out tunnel interfaces to find the real ISP.
	GetDefaultGatewayInterface(ctx context.Context) (string, error)

	// GetDefaultGatewayInterfaceWithRetry returns the default gateway with retry logic.
	// Used during WAN up when routing table may not be immediately available.
	GetDefaultGatewayInterfaceWithRetry(ctx context.Context) (string, error)

	// GetDefaultGateway returns the gateway IP and interface name for the default route.
	// Filters out tunnel interfaces. Used to get the actual next-hop for endpoint routes.
	GetDefaultGateway(ctx context.Context) (gateway, iface string, err error)

	// GetDefaultGatewayWithRetry returns the gateway IP and interface name with retry logic.
	GetDefaultGatewayWithRetry(ctx context.Context) (gateway, iface string, err error)

	// GetGatewayForInterface returns the gateway IP used by a specific interface.
	// Parses routing table to find a route with a non-zero gateway via the given interface.
	// For DHCP interfaces with 0.0.0.0 gateway, computes the real gateway from IP + mask.
	GetGatewayForInterface(ctx context.Context, iface string) (string, error)

	// IsDHCPClientBound checks if a DHCP client is active (bound/renew) for the given interface.
	// Matches by interface id or name. Returns false on any error.
	IsDHCPClientBound(ctx context.Context, iface string) bool

	// GetInterfaceAddress returns the IPv4 address and mask of an interface.
	GetInterfaceAddress(ctx context.Context, iface string) (address, mask string, err error)

	// QueryAllWANInterfaces returns all WAN interfaces.
	// Used at boot to populate wan.Model. Exclusion filtering is the model's job.
	QueryAllWANInterfaces(ctx context.Context) ([]wan.Interface, error)

	// LAN client discovery

	// HasWANIPv6 checks if a WAN interface has a global IPv6 address.
	// Returns true when the interface's ipv6 layer is "running".
	HasWANIPv6(ctx context.Context, ifaceName string) bool

	// GetHotspotClients returns LAN devices from the router's hotspot table.
	// Used for access policy UI to let users select clients by hostname+IP.
	GetHotspotClients(ctx context.Context) ([]HotspotClient, error)

	// Configuration persistence

	// Save saves the current configuration.
	// Command: system configuration save
	Save(ctx context.Context) error
}

// HotspotClient represents a LAN device known to the router.
type HotspotClient struct {
	IP       string `json:"ip"`
	MAC      string `json:"mac"`
	Hostname string `json:"hostname"`
	Online   bool   `json:"online"`
}
