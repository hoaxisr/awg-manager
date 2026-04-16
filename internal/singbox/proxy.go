package singbox

import (
	"context"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
)

// ErrProxyComponentMissing is returned when the router lacks the NDMS
// "proxy" component. Without it, no ProxyN interface can be created, so
// sing-box cannot route any traffic. Surfaced to the UI as a distinct
// state (separate from generic RCI errors) so we can show the user how
// to fix it instead of a raw NDMS error string.
var ErrProxyComponentMissing = fmt.Errorf("NDMS 'proxy' component is not installed — sing-box integration unavailable")

// ProxyManager wraps ndms.Client for Proxy interface operations
// specific to sing-box tunnels.
type ProxyManager struct {
	ndms ndms.Client
}

func NewProxyManager(n ndms.Client) *ProxyManager {
	return &ProxyManager{ndms: n}
}

// EnsureProxy creates or refreshes ProxyN pointing at 127.0.0.1:port.
// Idempotent: re-creating with same params is safe. Returns
// ErrProxyComponentMissing before talking to NDMS when the required
// component is absent — avoids a confusing "interface type not
// supported" error from the router.
func (pm *ProxyManager) EnsureProxy(ctx context.Context, index, port int, description string) error {
	if !ndmsinfo.HasProxyComponent() {
		return ErrProxyComponentMissing
	}
	name := fmt.Sprintf("%s%d", proxyIfacePrefix, index)
	return pm.ndms.CreateProxy(ctx, name, description, "127.0.0.1", port, true)
}

// RemoveProxy tears down ProxyN.
func (pm *ProxyManager) RemoveProxy(ctx context.Context, index int) error {
	name := fmt.Sprintf("%s%d", proxyIfacePrefix, index)
	_ = pm.ndms.ProxyDown(ctx, name) // ignore error — may be already down
	return pm.ndms.DeleteProxy(ctx, name)
}

// SyncProxies reconciles NDMS Proxy interfaces with current config.json tunnels.
// Creates missing Proxy for each tunnel and brings existing Proxy up if Down.
// Removal of proxies for absent tunnels is the Operator's responsibility.
func (pm *ProxyManager) SyncProxies(ctx context.Context, tunnels []TunnelInfo) error {
	for _, t := range tunnels {
		// Extract index from ProxyN
		var idx int
		if _, err := fmt.Sscanf(t.ProxyInterface, proxyIfacePrefix+"%d", &idx); err != nil {
			return fmt.Errorf("bad proxy iface name %q: %w", t.ProxyInterface, err)
		}
		info, err := pm.ndms.ShowProxy(ctx, t.ProxyInterface)
		if err != nil || !info.Exists {
			// Missing — create
			if err := pm.EnsureProxy(ctx, idx, t.ListenPort, t.Tag); err != nil {
				return err
			}
			continue
		}
		// Exists but down → bring up
		if !info.Up {
			if err := pm.ndms.ProxyUp(ctx, t.ProxyInterface); err != nil {
				return err
			}
		}
	}
	return nil
}
