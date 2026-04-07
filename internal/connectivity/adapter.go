package connectivity

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/testing"
	"github.com/hoaxisr/awg-manager/internal/traffic"
)

// Adapter implements CheckLister and Checker by delegating to the tunnel service
// and testing service.
type Adapter struct {
	lister  traffic.TunnelLister
	store   *storage.AWGTunnelStore
	testSvc *testing.Service
}

// NewAdapter creates a connectivity Adapter.
func NewAdapter(lister traffic.TunnelLister, store *storage.AWGTunnelStore, testSvc *testing.Service) *Adapter {
	return &Adapter{lister: lister, store: store, testSvc: testSvc}
}

// ListCheckableTunnels returns running tunnels with their connectivity check method.
func (a *Adapter) ListCheckableTunnels(ctx context.Context) []TunnelForCheck {
	running := a.lister.RunningTunnels(ctx)
	result := make([]TunnelForCheck, 0, len(running))
	for _, t := range running {
		method := "http" // default
		var target string
		if stored, err := a.store.Get(t.ID); err == nil && stored.ConnectivityCheck != nil {
			if stored.ConnectivityCheck.Method != "" {
				method = stored.ConnectivityCheck.Method
			}
			target = stored.ConnectivityCheck.PingTarget
		}
		result = append(result, TunnelForCheck{
			ID:        t.ID,
			IfaceName: t.IfaceName,
			Method:    method,
			Target:    target,
		})
	}
	return result
}

// Check performs a single connectivity check for a tunnel.
func (a *Adapter) Check(ctx context.Context, tunnelID string) (bool, *int, error) {
	res, err := a.testSvc.CheckConnectivity(ctx, tunnelID)
	if err != nil {
		return false, nil, err
	}
	return res.Connected, res.Latency, nil
}

// HasHandshake checks if a tunnel has completed WireGuard handshake.
func (a *Adapter) HasHandshake(ctx context.Context, tunnelID string) bool {
	running := a.lister.RunningTunnels(ctx)
	for _, t := range running {
		if t.ID == tunnelID {
			return !t.LastHandshake.IsZero()
		}
	}
	return false
}
