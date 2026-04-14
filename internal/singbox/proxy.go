package singbox

import (
	"context"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
)

// ProxyManager wraps ndms.Client for Proxy interface operations
// specific to sing-box tunnels.
type ProxyManager struct {
	ndms ndms.Client
}

func NewProxyManager(n ndms.Client) *ProxyManager {
	return &ProxyManager{ndms: n}
}

// EnsureProxy creates or refreshes ProxyN pointing at 127.0.0.1:port.
// Idempotent: re-creating with same params is safe.
func (pm *ProxyManager) EnsureProxy(ctx context.Context, index, port int, description string) error {
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
