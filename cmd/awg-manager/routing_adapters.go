package main

import (
	"context"
	"errors"

	"github.com/hoaxisr/awg-manager/internal/dnsroute"
	"github.com/hoaxisr/awg-manager/internal/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/exitreg"
	"github.com/hoaxisr/awg-manager/internal/routing"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// tunnelProviderAdapter adapts service.Service to routing.TunnelProvider.
type tunnelProviderAdapter struct {
	svc   service.Service
	store *storage.AWGTunnelStore
}

func (a *tunnelProviderAdapter) ListTunnels(ctx context.Context) ([]routing.TunnelWithStatus, error) {
	tunnels, err := a.svc.List(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]routing.TunnelWithStatus, len(tunnels))
	for i, t := range tunnels {
		entry := routing.TunnelWithStatus{
			ID:      t.ID,
			Name:    t.Name,
			Backend: t.Backend,
			State:   t.State,
		}
		// NativeWG tunnels need NWGIndex from storage.
		if t.Backend == "nativewg" {
			if stored, err := a.store.Get(t.ID); err == nil {
				entry.NWGIndex = stored.NWGIndex
			}
		}
		result[i] = entry
	}
	return result, nil
}

func (a *tunnelProviderAdapter) GetState(ctx context.Context, tunnelID string) tunnel.StateInfo {
	return a.svc.GetState(ctx, tunnelID)
}

func (a *tunnelProviderAdapter) WANModel() *wan.Model {
	return a.svc.WANModel()
}

// dnsRouteCountAdapter adapts dnsroute.Store to dnscheck.DnsRouteProvider.
type dnsRouteCountAdapter struct {
	store *dnsroute.Store
}

func (a *dnsRouteCountAdapter) ListEnabledCount(_ context.Context) (int, int) {
	data := a.store.GetCached()
	if data == nil {
		return 0, 0
	}
	total := len(data.Lists)
	enabled := 0
	for _, l := range data.Lists {
		if l.Enabled {
			enabled++
		}
	}
	return total, enabled
}

// runningTunnelAdapter adapts service.Service to dnscheck.TunnelStateProvider.
type runningTunnelAdapter struct {
	svc service.Service
}

func (a *runningTunnelAdapter) RunningTunnelNames(ctx context.Context) []string {
	list, err := a.svc.List(ctx)
	if err != nil {
		return nil
	}
	var names []string
	for _, t := range list {
		if t.State == tunnel.StateRunning {
			names = append(names, t.Name)
		}
	}
	return names
}

// storeAdapter adapts storage.AWGTunnelStore to routing.StoreClient.
type storeAdapter struct {
	store *storage.AWGTunnelStore
}

func (a *storeAdapter) Get(id string) (routing.StoreEntry, error) {
	t, err := a.store.Get(id)
	if err != nil {
		return routing.StoreEntry{}, err
	}
	return routing.StoreEntry{
		Backend:        t.Backend,
		NWGIndex:       t.NWGIndex,
		RawKernelIface: t.RawKernelIface,
		RawNdmsIface:   t.RawNdmsIface,
	}, nil
}

func (a *storeAdapter) Exists(id string) bool {
	return a.store.Exists(id)
}

// exitRegistryAdapter adapts exitreg.Registry to routing.ExitRegistry.
// Типы каталога и рантайма намеренно разные: каталог объявляет свой узкий
// интерфейс и потому не зависит от пакетов, которые переставляет волна.
type exitRegistryAdapter struct{ reg *exitreg.Registry }

func (a exitRegistryAdapter) LookupExit(id string) (routing.ExitEntry, bool) {
	info, ok := a.reg.Lookup(id)
	if !ok {
		return routing.ExitEntry{}, false
	}
	return routing.ExitEntry{
		NDMSName:    info.NDMSName,
		KernelIface: info.KernelIface,
		Ready:       info.Ready,
	}, true
}

// orchLifecycleAdapter routes accesspolicy.SetInterfaceUp for managed tunnels
// to the orchestrator lifecycle (full start/stop incl. NativeWG kmod proxy).
type orchLifecycleAdapter struct{ orch *orchestrator.Orchestrator }

func (a orchLifecycleAdapter) Start(ctx context.Context, id string) error {
	err := a.orch.HandleEvent(ctx, orchestrator.Event{Type: orchestrator.EventStart, Tunnel: id})
	if errors.Is(err, tunnel.ErrAlreadyRunning) {
		return nil // already running — user's "on" intent fulfilled
	}
	return err
}

func (a orchLifecycleAdapter) Stop(ctx context.Context, id string) error {
	return a.orch.HandleEvent(ctx, orchestrator.Event{Type: orchestrator.EventStop, Tunnel: id})
}

// storeManagedTunnelResolver maps an NDMS interface name to a managed tunnel
// ID by scanning the AWG tunnel store. ok=false for plain system interfaces.
type storeManagedTunnelResolver struct{ store *storage.AWGTunnelStore }

func (r storeManagedTunnelResolver) ManagedTunnelByNDMSName(_ context.Context, ndmsName string) (string, bool) {
	tunnels, err := r.store.List()
	if err != nil {
		return "", false
	}
	for _, t := range tunnels {
		var nm string
		if t.Backend == "nativewg" {
			nm = nwg.NewNWGNames(t.NWGIndex).NDMSName
		} else {
			nm = tunnel.NewNames(t.ID).NDMSName
		}
		if nm != "" && nm == ndmsName {
			return t.ID, true
		}
	}
	return "", false
}
