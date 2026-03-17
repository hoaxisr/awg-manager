package policy

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Service manages access policies — per-client routing through tunnels.
type Service interface {
	// CRUD
	List() ([]storage.Policy, error)
	Get(id string) (*storage.Policy, error)
	Create(ctx context.Context, p storage.Policy) (*storage.Policy, error)
	Update(ctx context.Context, p storage.Policy) (*storage.Policy, error)
	Delete(ctx context.Context, id string) error

	// Tunnel lifecycle hooks
	OnTunnelStart(ctx context.Context, tunnelID, tunnelIface string) error
	OnTunnelStop(ctx context.Context, tunnelID string) error
	OnTunnelDelete(ctx context.Context, tunnelID string) error

	// Reconcile restores all policy routes at daemon startup.
	// runningTunnels maps tunnelID → kernel interface name (e.g. "awgm0").
	Reconcile(ctx context.Context, runningTunnels map[string]string) error
}
