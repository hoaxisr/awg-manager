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

// Handshaked returns the IDs of running tunnels that already have a handshake.
// Одна выборка на вызов независимо от того, сколько туннелей ждут.
func (a *Adapter) Handshaked(ctx context.Context) map[string]bool {
	running := a.lister.RunningTunnels(ctx)
	out := make(map[string]bool, len(running))
	for _, t := range running {
		if !t.LastHandshake.IsZero() {
			out[t.ID] = true
		}
	}
	return out
}
