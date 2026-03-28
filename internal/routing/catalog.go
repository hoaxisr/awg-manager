// internal/routing/catalog.go
package routing

import "context"

// TunnelEntry represents a tunnel or interface available for routing.
type TunnelEntry struct {
	ID        string `json:"id"`        // "awgm0", "system:Wireguard0", "wan:apcli1"
	Name      string `json:"name"`      // "WARPm2_88", "Wireguard0", "gpon5G_2"
	Type      string `json:"type"`      // "managed", "system", "wan"
	Status    string `json:"status"`    // "running", "stopped", "disabled", "up", "down"
	Available bool   `json:"available"` // can route traffic right now
}

// Catalog provides a unified tunnel listing and ID resolution for all routing subsystems.
type Catalog interface {
	// ListAll returns deduplicated list for UI dropdowns.
	ListAll(ctx context.Context) []TunnelEntry

	// ResolveInterface maps tunnelID to interface name for routing commands.
	// Returns NDMS name on OS5, kernel name on OS4.
	ResolveInterface(ctx context.Context, tunnelID string) (string, error)

	// Exists checks if tunnelID refers to a valid tunnel or interface.
	Exists(ctx context.Context, tunnelID string) bool

	// GetKernelIface resolves tunnelID to kernel interface name.
	// Returns empty string and false if tunnel is not running.
	GetKernelIface(ctx context.Context, tunnelID string) (ifaceName string, running bool)
}
