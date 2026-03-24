package clientroute

import "context"

// Service manages per-device VPN routing rules.
type Service interface {
	List() ([]ClientRoute, error)
	Create(ctx context.Context, route ClientRoute) (*ClientRoute, error)
	Update(ctx context.Context, route ClientRoute) (*ClientRoute, error)
	Delete(ctx context.Context, id string) error
	SetEnabled(ctx context.Context, id string, enabled bool) error

	// Tunnel lifecycle hooks.
	OnTunnelStart(ctx context.Context, tunnelID string, kernelIface string) error
	OnTunnelStop(ctx context.Context, tunnelID string) error
	OnTunnelDelete(ctx context.Context, tunnelID string) error

	// Reconcile re-applies routing rules for all running tunnels.
	Reconcile(ctx context.Context, runningTunnels map[string]string) error
	// CleanupAll removes all routing tables and deletes storage.
	CleanupAll(ctx context.Context) error
}

// Operator is the narrow interface for OS-level routing operations.
type Operator interface {
	SetupClientRouteTable(ctx context.Context, kernelIface string, tableNum int) error
	AddClientRule(ctx context.Context, clientIP string, tableNum int) error
	RemoveClientRule(ctx context.Context, clientIP string, tableNum int) error
	CleanupClientRouteTable(ctx context.Context, tableNum int) error
	ListUsedRoutingTables(ctx context.Context) ([]int, error)
}

// Store is the narrow interface for client route persistence.
type Store interface {
	List() []ClientRoute
	Get(id string) *ClientRoute
	FindByClientIP(ip string) *ClientRoute
	FindByTunnel(tunnelID string) []ClientRoute
	Add(r ClientRoute) error
	Update(r ClientRoute) error
	Remove(id string) error
	RemoveByTunnel(tunnelID string) error
	GetTableForTunnel(tunnelID string) (int, bool)
	AllocateTable(tunnelID string, usedTables []int) (int, error)
	FreeTable(tunnelID string) error
	GetAllTables() map[string]int
	DeleteFile() error
}

// GetKernelIfaceFunc returns the kernel interface name for a running tunnel.
// Returns ("", false) if the tunnel is not running.
type GetKernelIfaceFunc func(ctx context.Context, tunnelID string) (string, bool)

// TunnelExistsFunc checks whether a tunnel exists in storage.
type TunnelExistsFunc func(tunnelID string) bool
