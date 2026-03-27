package staticroute

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Service manages static IP route lists bound to tunnels.
type Service interface {
	List() ([]storage.StaticRouteList, error)
	Get(id string) (*storage.StaticRouteList, error)
	Create(ctx context.Context, rl storage.StaticRouteList) (*storage.StaticRouteList, error)
	Update(ctx context.Context, rl storage.StaticRouteList) (*storage.StaticRouteList, error)
	Delete(ctx context.Context, id string) error
	SetEnabled(ctx context.Context, id string, enabled bool) error
	Import(ctx context.Context, tunnelID, name, batContent string) (*storage.StaticRouteList, error)

	// Tunnel lifecycle hooks
	OnTunnelStart(ctx context.Context, tunnelID, tunnelIface string) error
	OnTunnelStop(ctx context.Context, tunnelID string) error
	OnTunnelDelete(ctx context.Context, tunnelID string) error

	// Reconcile restores static routes at daemon startup.
	Reconcile(ctx context.Context) error
}
