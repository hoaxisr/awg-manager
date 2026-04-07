package orchestrator

import (
	"context"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// resolveWAN resolves ISPInterface to a kernel interface name.
// Auto mode (empty): uses WAN model priority or NDMS default gateway.
// Tunnel chaining (tunnel:xxx): resolves to parent tunnel's WAN.
// Explicit: returns as-is (kernel name after migration).
func (o *Orchestrator) resolveWAN(ctx context.Context, ispInterface string) (string, error) {
	if ispInterface == "" {
		// Auto mode: prefer WAN model (priority-based, returns kernel name)
		if iface, ok := o.wanModel.PreferredUp(); ok {
			return iface, nil
		}
		// Fallback: wan.Model not yet populated (early boot)
		// GetDefaultGatewayInterface returns NDMS ID -> translate to kernel name
		ndmsID, err := o.kernelOp.GetDefaultGatewayInterface(ctx)
		if err != nil {
			return "", fmt.Errorf("no default gateway available: %w", err)
		}
		// Try model reverse lookup first
		if kernelName := o.wanModel.NameForID(ndmsID); kernelName != "" {
			return kernelName, nil
		}
		// Model not populated — direct NDMS lookup
		return o.kernelOp.GetSystemName(ctx, ndmsID), nil
	}

	if tunnel.IsTunnelRoute(ispInterface) {
		// Tunnel chaining: resolve to parent's persisted WAN
		parentID := tunnel.TunnelRouteID(ispInterface)
		parentStored, err := o.store.Get(parentID)
		if err != nil {
			return "", fmt.Errorf("parent tunnel %s not found", parentID)
		}
		if parentStored.ActiveWAN != "" {
			return parentStored.ActiveWAN, nil
		}
		// Fallback: ActiveWAN empty (first start or upgrade from old version)
		parentState := o.stateMgr.GetState(ctx, parentID)
		if parentState.State != tunnel.StateRunning {
			return "", fmt.Errorf("parent tunnel %s not running (state: %s)", parentID, parentState.State)
		}
		if tunnel.IsTunnelRoute(parentStored.ISPInterface) {
			return "", fmt.Errorf("parent tunnel %s: nested chain, ActiveWAN not tracked", parentID)
		}
		o.logInfo(parentID, "ActiveWAN empty, resolving from stored config")
		return o.resolveWAN(ctx, parentStored.ISPInterface)
	}

	// Explicit WAN — after migration this is already a kernel name
	return ispInterface, nil
}

// resolveKernelDevice extracts the kernel device name from a resolved WAN.
// resolveWAN already returns kernel names, so this just handles tunnel chaining.
func (o *Orchestrator) resolveKernelDevice(resolvedWAN string) string {
	if resolvedWAN == "" {
		return ""
	}
	if tunnel.IsTunnelRoute(resolvedWAN) {
		return tunnel.NewNames(tunnel.TunnelRouteID(resolvedWAN)).IfaceName
	}
	return resolvedWAN
}
