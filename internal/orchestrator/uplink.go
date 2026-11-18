package orchestrator

import (
	"context"
	"fmt"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// Called during composition, before lifecycle producers start. The callback
// used under state.mu only checks desired policy ownership, never controller or
// adapter locks. WAN resolution runs later under the per-tunnel execution lock.
func (o *Orchestrator) SetManagedKernelUplink(owns func(string) bool, resolve func(context.Context, string) (string, error)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.state.managedUplinkOwns = owns
	o.managedKernelWAN = resolve
}
func (o *Orchestrator) resolveKernelWAN(ctx context.Context, id, original string) (string, error) {
	if o.state.managedUplinkOwns != nil && o.state.managedUplinkOwns(id) {
		return o.managedKernelWAN(ctx, id)
	}
	return o.resolveWAN(ctx, original)
}

// Caller holds the per-tunnel lock. Reinstall only the ordinary lifecycle's
// baseline endpoint route before the owned mark is removed. ISP metadata was
// deliberately never overwritten by fallback.
func (o *Orchestrator) RestoreKernelBaselineRoute(ctx context.Context, id string) error {
	t, err := o.store.Get(id)
	if err != nil {
		return err
	}
	if !t.Enabled {
		return nil
	}
	wan, err := o.resolveWAN(ctx, t.ISPInterface)
	if err != nil {
		return err
	}
	if wan == "" || tunnel.IsTunnelRoute(t.ISPInterface) {
		return fmt.Errorf("original kernel WAN unavailable")
	}
	routeDev, routeISP, err := RouteDevice(o.store, t.ISPInterface, wan)
	if err != nil {
		return err
	}
	_, err = o.kernelOp.SetupEndpointRoute(ctx, id, t.Peer.Endpoint, routeDev, routeISP)
	if err != nil {
		return err
	}
	if err = o.store.Update(id, func(fresh *storage.AWGTunnel) error { fresh.ActiveWAN = wan; return nil }); err != nil {
		return err
	}
	o.RefreshTunnelState(id)
	return nil
}
