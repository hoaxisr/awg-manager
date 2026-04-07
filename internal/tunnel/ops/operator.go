// Package ops provides tunnel operations (create, start, stop, delete, recover).
// Operations are low-level and assume the caller has already verified state.
package ops

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// Operator is the interface for tunnel lifecycle operations.
// All operations use direct ip commands for kernel interface management.
type Operator interface {
	// Create creates system resources for a tunnel without starting it.
	// No-op for kernel tunnels (interface created by process).
	Create(ctx context.Context, cfg tunnel.Config) error

	// ColdStart creates a tunnel from scratch: ip link add + ip addr add +
	// wg setconf + ip link set up + firewall.
	// Used for: NotCreated, Broken, boot.
	ColdStart(ctx context.Context, cfg tunnel.Config) error

	// Start brings up an existing amneziawg interface after Stop.
	// Interface already exists with address and WG config loaded.
	// ip link set up + firewall.
	// Used for: Disabled (after Stop), Dead (after PingCheck stop).
	Start(ctx context.Context, cfg tunnel.Config) error

	// Stop brings down a tunnel: kills backend process + removes firewall rules.
	// Used for: user Stop, PingCheck dead.
	Stop(ctx context.Context, tunnelID string) error

	// Delete completely removes a tunnel.
	// Receives the full stored tunnel for reliable cleanup (persisted endpoint IP, etc.).
	Delete(ctx context.Context, stored *storage.AWGTunnel) error

	// Recover attempts to bring a broken tunnel into a consistent state.
	// Based on current state, may kill zombie processes, clean up orphaned resources, etc.
	Recover(ctx context.Context, tunnelID string, state tunnel.StateInfo) error

	// Reconcile re-applies system configuration around an already-running process.
	// Used when the process survived a daemon restart.
	// Skips process start; applies WG config, IP config, and firewall.
	Reconcile(ctx context.Context, cfg tunnel.Config) error

	// Suspend sets the tunnel link down without removing the interface.
	// Used for WAN failover. NDMS handles routing automatically.
	Suspend(ctx context.Context, tunnelID string) error

	// Resume sets the tunnel link up after Suspend.
	Resume(ctx context.Context, tunnelID string) error

	// ApplyConfig applies a new WireGuard config to a running tunnel.
	ApplyConfig(ctx context.Context, tunnelID, configPath string) error

	// SetDefaultRoute adds a default route through the tunnel interface.
	SetDefaultRoute(ctx context.Context, tunnelID string) error

	// RemoveDefaultRoute removes the default route through the tunnel interface.
	RemoveDefaultRoute(ctx context.Context, tunnelID string) error

	// SetupEndpointRoute adds a route to the VPN endpoint via kernel device.
	// kernelDevice is the kernel interface name (e.g., "eth3") for oif constraint;
	// empty string means no constraint (ip route get picks the best route).
	// Returns the resolved endpoint IP on success (empty string on non-fatal failure).
	SetupEndpointRoute(ctx context.Context, tunnelID, endpoint, kernelDevice, ispName string) (string, error)

	// CleanupEndpointRoute removes the endpoint route for a tunnel.
	CleanupEndpointRoute(ctx context.Context, tunnelID string) error

	// RestoreEndpointTracking restores endpoint route tracking without creating the route.
	// Used on daemon restart for tunnels that are already running.
	// ispInterface is the resolved ISP interface name (for dashboard display).
	// Returns the resolved endpoint IP on success (empty string on non-fatal failure).
	RestoreEndpointTracking(ctx context.Context, tunnelID, endpoint, ispInterface string) (string, error)

	// GetTrackedEndpointIP returns the currently tracked endpoint IP for a tunnel.
	// Returns empty string if no endpoint route is tracked.
	GetTrackedEndpointIP(tunnelID string) string

	// SetMTU sets MTU on a running tunnel interface via ip link set.
	SetMTU(ctx context.Context, tunnelID string, mtu int) error

	// SyncDNS updates DNS servers for a tunnel via RCI.
	SyncDNS(ctx context.Context, tunnelID string, dns []string) error

	// SyncAddress updates IPv4/IPv6 address on a running tunnel via ip commands.
	SyncAddress(ctx context.Context, tunnelID string, address, ipv6 string) error

	// UpdateDescription updates the tunnel description in RCI.
	UpdateDescription(ctx context.Context, tunnelID, description string) error

	// GetDefaultGatewayInterface returns the current default gateway interface name.
	// Used by resolveWAN for auto-mode tunnels.
	GetDefaultGatewayInterface(ctx context.Context) (string, error)

	// GetResolvedISP returns the resolved ISP interface name for a running tunnel.
	// For auto-mode tunnels, this is the WAN picked during SetupEndpointRoute.
	// Returns empty string if no resolved ISP is tracked.
	GetResolvedISP(tunnelID string) string

	// HasWANIPv6 checks if a WAN interface has IPv6 connectivity via RCI.
	HasWANIPv6(ctx context.Context, ifaceName string) bool

	// GetSystemName resolves a router interface ID (e.g., "PPPoE0") to its kernel
	// interface name (e.g., "ppp0") via RCI. Returns the ID unchanged if resolution fails.
	GetSystemName(ctx context.Context, ndmsID string) string

	// SetAppLogger sets the web UI logger for operator events.
	SetAppLogger(logger logging.AppLogger)

	// Client VPN routing (ip rule / ip route tables)
	SetupClientRouteTable(ctx context.Context, kernelIface string, tableNum int) error
	AddClientRule(ctx context.Context, clientIP string, tableNum int) error
	RemoveClientRule(ctx context.Context, clientIP string, tableNum int) error
	CleanupClientRouteTable(ctx context.Context, tableNum int) error
	ListUsedRoutingTables(ctx context.Context) ([]int, error)
}
