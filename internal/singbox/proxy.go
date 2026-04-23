package singbox

import (
	"context"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
)

// ErrProxyComponentMissing is returned when the router lacks the NDMS
// "proxy" component. Without it, no ProxyN interface can be created, so
// sing-box cannot route any traffic. Surfaced to the UI as a distinct
// state (separate from generic RCI errors) so we can show the user how
// to fix it instead of a raw NDMS error string.
var ErrProxyComponentMissing = fmt.Errorf("NDMS 'proxy' component is not installed — sing-box integration unavailable")

// ProxyManager orchestrates NDMS Proxy interfaces for sing-box tunnels.
// Reads go through queries.Interfaces (GetProxy helper); writes through
// commands.Proxies.
type ProxyManager struct {
	queries  *query.Queries
	commands *command.Commands
}

func NewProxyManager(q *query.Queries, c *command.Commands) *ProxyManager {
	return &ProxyManager{queries: q, commands: c}
}

// EnsureProxy creates or refreshes ProxyN pointing at 127.0.0.1:port.
// Idempotent: re-creating with same params is safe. Returns
// ErrProxyComponentMissing before talking to NDMS when the required
// component is absent.
func (pm *ProxyManager) EnsureProxy(ctx context.Context, index, port int, description string) error {
	if !ndmsinfo.HasProxyComponent() {
		return ErrProxyComponentMissing
	}
	name := fmt.Sprintf("%s%d", proxyIfacePrefix, index)
	return pm.commands.Proxies.CreateProxy(ctx, name, description, "127.0.0.1", port, true)
}

// RemoveProxy tears down ProxyN.
func (pm *ProxyManager) RemoveProxy(ctx context.Context, index int) error {
	name := fmt.Sprintf("%s%d", proxyIfacePrefix, index)
	_ = pm.commands.Proxies.ProxyDown(ctx, name) // ignore error — may be already down
	return pm.commands.Proxies.DeleteProxy(ctx, name)
}

// SyncProxies reconciles NDMS Proxy interfaces with current config.json tunnels.
// Creates missing Proxy for each tunnel and brings existing Proxy up if Down.
// Removal of proxies for absent tunnels is the Operator's responsibility.
func (pm *ProxyManager) SyncProxies(ctx context.Context, tunnels []TunnelInfo) error {
	for _, t := range tunnels {
		var idx int
		if _, err := fmt.Sscanf(t.ProxyInterface, proxyIfacePrefix+"%d", &idx); err != nil {
			return fmt.Errorf("bad proxy iface name %q: %w", t.ProxyInterface, err)
		}
		info, err := pm.queries.Interfaces.GetProxy(ctx, t.ProxyInterface)
		if err != nil || !info.Exists {
			if err := pm.EnsureProxy(ctx, idx, t.ListenPort, t.Tag); err != nil {
				return err
			}
			continue
		}
		if !info.Up {
			if err := pm.commands.Proxies.ProxyUp(ctx, t.ProxyInterface); err != nil {
				return err
			}
		}
	}
	return nil
}
