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
// Different implementations exist for OS5 (NDMS) and OS4 (direct).
type Operator interface {
	// Create creates a tunnel's NDMS/system resources without starting it.
	// For OS5: creates OpkgTun in NDMS, sets address and MTU.
	// For OS4: no-op (interface created by process).
	Create(ctx context.Context, cfg tunnel.Config) error

	// ColdStart creates a tunnel from scratch or recreates from wrong type.
	// For OS5: ip link del (if exists) + ip link add amneziawg + ip addr add +
	//   wg setconf + ip link set up + InterfaceUp + routes + firewall + Save.
	// Used for: BootReady (tun from NDMS), NotCreated, Broken.
	ColdStart(ctx context.Context, cfg tunnel.Config) error

	// Start brings up an existing amneziawg interface after our Stop.
	// Interface already exists with address and WG config loaded.
	// For OS5: ip link set up + InterfaceUp + routes + firewall + Save.
	// Used for: Disabled (after our Stop), Dead (after PingCheck stop).
	Start(ctx context.Context, cfg tunnel.Config) error

	// Stop brings down a tunnel without destroying the interface.
	// ip link set down + InterfaceDown + Save.
	// NDMS handles failover automatically. Routes/firewall stay — NDMS manages them.
	// Used for: user Stop, PingCheck dead.
	Stop(ctx context.Context, tunnelID string) error

	// Delete completely removes a tunnel.
	// Receives the full stored tunnel for reliable cleanup (persisted endpoint IP, etc.).
	Delete(ctx context.Context, stored *storage.AWGTunnel) error

	// Recover attempts to bring a broken tunnel into a consistent state.
	// Based on current state, may kill zombie processes, clean up orphaned resources, etc.
	Recover(ctx context.Context, tunnelID string, state tunnel.StateInfo) error

	// Reconcile re-applies NDMS/system configuration around an already-running process.
	// Used when the process survived a daemon restart but NDMS state was lost.
	// Skips process start; applies WG config, NDMS config, routing, and firewall.
	Reconcile(ctx context.Context, cfg tunnel.Config) error

	// Suspend sets link down without removing the interface or changing NDMS conf.
	// NDMS sees pending state and handles failover automatically.
	// Routes and firewall are NOT touched — NDMS manages failover.
	Suspend(ctx context.Context, tunnelID string) error

	// Resume sets link up after Suspend.
	// NDMS will restore routing automatically.
	Resume(ctx context.Context, tunnelID string) error

	// ApplyConfig applies a new WireGuard config to a running tunnel.
	ApplyConfig(ctx context.Context, tunnelID, configPath string) error

	// SetDefaultRoute adds a default route through the tunnel interface (no-op in kernel mode).
	SetDefaultRoute(ctx context.Context, tunnelID string) error

	// RemoveDefaultRoute removes the default route through the tunnel interface (no-op in kernel mode).
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

	// Static IP routing — adds/removes routes in the main routing table.

	// AddStaticRoutes adds routes for subnets through a tunnel interface.
	// Uses "ip route replace" for idempotency. Individual route errors are non-fatal.
	AddStaticRoutes(ctx context.Context, tunnelIface string, subnets []string) error

	// RemoveStaticRoutes removes routes for subnets from a tunnel interface.
	// Individual route errors are non-fatal (route may already be gone).
	RemoveStaticRoutes(ctx context.Context, tunnelIface string, subnets []string) error

	// SetMTU sets MTU on a running tunnel interface.
	// OS5: via NDMS. OS4: via ip link set.
	SetMTU(ctx context.Context, tunnelID string, mtu int) error

	// UpdateDescription updates the NDMS interface description (OS5 only, no-op for OS4).
	UpdateDescription(ctx context.Context, tunnelID, description string) error

	// GetDefaultGatewayInterface returns the current default gateway interface name.
	// Used by resolveWAN for auto-mode tunnels.
	GetDefaultGatewayInterface(ctx context.Context) (string, error)

	// GetResolvedISP returns the resolved ISP interface name for a running tunnel.
	// For auto-mode tunnels, this is the WAN picked during SetupEndpointRoute.
	// Returns empty string if no resolved ISP is tracked.
	GetResolvedISP(tunnelID string) string

	// HasWANIPv6 checks if a WAN interface has IPv6 connectivity.
	// Uses NDMS RCI to check the ipv6 layer status (works with NDMS interface names).
	HasWANIPv6(ctx context.Context, ifaceName string) bool

	// GetSystemName resolves an NDMS ID (e.g., "PPPoE0") to its kernel interface name
	// (e.g., "ppp0") via NDMS RCI. Returns ndmsID unchanged if resolution fails.
	GetSystemName(ctx context.Context, ndmsID string) string

	// SetAppLogger sets the web UI logger for operator events.
	SetAppLogger(logger logging.AppLogger)
}
