package deviceproxy

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/tunnel/systemtunnel"
)

// SystemTunnelQuery returns Keenetic native WireGuard tunnels that
// exist in NDMS but are not managed by awg-manager's own storage.
// Used by the device proxy so its selector can route through them via
// direct+bind_interface.
type SystemTunnelQuery interface {
	List(ctx context.Context) ([]SystemTunnel, error)
}

// SystemTunnel is a minimal projection of ndms.SystemWireguardTunnel.
type SystemTunnel struct {
	ID            string // NDMS id, e.g. "Wireguard0"
	InterfaceName string // kernel name, e.g. "nwg0"
	Description   string // human label
}

// SystemTunnelAdapter satisfies SystemTunnelQuery by delegating to
// systemtunnel.Service. Lives in internal/deviceproxy to keep the
// dependency direction: deviceproxy depends on systemtunnel, not
// the other way around.
type SystemTunnelAdapter struct {
	svc systemtunnel.Service
}

func NewSystemTunnelAdapter(svc systemtunnel.Service) *SystemTunnelAdapter {
	return &SystemTunnelAdapter{svc: svc}
}

func (a *SystemTunnelAdapter) List(ctx context.Context) ([]SystemTunnel, error) {
	tunnels, err := a.svc.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]SystemTunnel, 0, len(tunnels))
	for _, t := range tunnels {
		label := t.Description
		if label == "" {
			label = t.ID
		}
		out = append(out, SystemTunnel{
			ID:            t.ID,
			InterfaceName: t.InterfaceName,
			Description:   label,
		})
	}
	return out, nil
}
