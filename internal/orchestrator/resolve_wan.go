package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
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
		// Родитель не поднят или сам в цепочке — WAN неизвестен, и это не
		// отказ: маршрут ребёнка идёт в интерфейс родителя (RouteDevice), а
		// не в WAN, и встанет с подъёмом родителя (refreshChainedChildren).
		// Отказ здесь ронял старт ребёнка, поднятого раньше родителя (F806).
		parentState := o.stateMgr.GetState(ctx, parentID)
		if parentState.State != tunnel.StateRunning || tunnel.IsTunnelRoute(parentStored.ISPInterface) {
			return "", nil
		}
		o.appLog.Info("resolve-wan", parentID, "ActiveWAN empty, resolving from stored config")
		return o.resolveWAN(ctx, parentStored.ISPInterface)
	}

	// Explicit WAN — after migration this is already a kernel name
	return ispInterface, nil
}

// RouteDevice — устройство (oif) и подпись для маршрута до endpoint.
//
// Цепочка `tunnel:<id>` ведёт маршрут в интерфейс РОДИТЕЛЯ. resolveWAN для неё
// отдаёт WAN родителя — это ActiveWAN ребёнка и его реакция на WAN-события, —
// но маршрут через этот WAN означал бы, что выбор «через туннель» не действует:
// так и было с 2.6.3 (F806/#1233). Подпись `tunnel:<id>` говорит оператору, что
// маршрут через туннель заказан явно (см. SetupEndpointRoute).
func RouteDevice(store *storage.AWGTunnelStore, ispInterface, resolvedWAN string) (device, ispName string, err error) {
	if !tunnel.IsTunnelRoute(ispInterface) {
		return resolvedWAN, resolvedWAN, nil
	}
	parent, err := store.Get(tunnel.TunnelRouteID(ispInterface))
	if err != nil {
		return "", "", fmt.Errorf("parent tunnel %s not found", tunnel.TunnelRouteID(ispInterface))
	}
	if parent.Backend == "wdtt-raw" && strings.TrimSpace(parent.RawKernelIface) == "" {
		// Без выделенного интерфейса ifaceNameForTunnel дал бы opkgtunN по
		// ID — устройство, которого нет.
		return "", "", fmt.Errorf("parent tunnel %s: kernel interface not allocated", parent.ID)
	}
	return ifaceNameForTunnel(parent), ispInterface, nil
}
