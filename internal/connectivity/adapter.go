package connectivity

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/traffic"
)

// Adapter implements HandshakeChecker by delegating to the traffic lister.
// Latency probing has moved to internal/monitoring — this stays minimal so
// the connectivity package only gates the post-handshake matrix tick.
type Adapter struct {
	lister traffic.TunnelLister
}

// NewAdapter creates a connectivity Adapter.
func NewAdapter(lister traffic.TunnelLister) *Adapter {
	return &Adapter{lister: lister}
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
