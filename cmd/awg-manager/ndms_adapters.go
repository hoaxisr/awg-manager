package main

import (
	"context"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/events"
	"github.com/hoaxisr/awg-manager/internal/ndms/metrics"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
	trafficpkg "github.com/hoaxisr/awg-manager/internal/traffic"
)

// systemTunnelLister returns non-managed WireGuard tunnels known to NDMS.
// The subset the MetricsPoller cares about is the running ones.
type systemTunnelLister interface {
	List(ctx context.Context) ([]ndms.SystemWireguardTunnel, error)
}

// ndmsLogAdapter bridges the Warnf-only interfaces from internal/ndms/query
// and internal/ndms/events onto the project's UI-visible logging service.
// Warnings from NDMS Stores (stale-cache fallbacks) and Dispatcher (hook
// delivery problems) surface in the in-app log view, not stderr.
type ndmsLogAdapter struct {
	log *logging.ScopedLogger
}

func (a *ndmsLogAdapter) Warnf(format string, args ...any) {
	if a == nil || a.log == nil {
		return
	}
	a.log.Warn("warn", "", fmt.Sprintf(format, args...))
}

func queryLogger(appLog logging.AppLogger) query.Logger {
	return &ndmsLogAdapter{log: logging.NewScopedLogger(appLog, logging.GroupSystem, "ndms-query")}
}

func eventsLogger(appLog logging.AppLogger) events.Logger {
	return &ndmsLogAdapter{log: logging.NewScopedLogger(appLog, logging.GroupSystem, "ndms-events")}
}

func metricsLogger(appLog logging.AppLogger) metrics.Logger {
	return &ndmsLogAdapter{log: logging.NewScopedLogger(appLog, logging.GroupSystem, "ndms-metrics")}
}

// runningInterfacesAdapter implements metrics.RunningInterfacesProvider
// by combining tunnelService's running tunnels, running system tunnels,
// the user-configured VPN-server interface list, and the managed WG-server.
type runningInterfacesAdapter struct {
	tunnels       trafficpkg.TunnelLister
	systemTunnels systemTunnelLister
	settings      *storage.SettingsStore
}

func newRunningInterfacesAdapter(tunnels trafficpkg.TunnelLister, systemTunnels systemTunnelLister, settings *storage.SettingsStore) *runningInterfacesAdapter {
	return &runningInterfacesAdapter{
		tunnels:       tunnels,
		systemTunnels: systemTunnels,
		settings:      settings,
	}
}

func (a *runningInterfacesAdapter) RunningInterfaces(ctx context.Context) []metrics.InterfaceRef {
	out := make([]metrics.InterfaceRef, 0, 8)

	for _, rt := range a.tunnels.RunningTunnels(ctx) {
		id := tunnelNDMSName(rt)
		if id == "" {
			continue
		}
		out = append(out, metrics.InterfaceRef{
			ID:       id,
			IsServer: false,
		})
	}

	if a.systemTunnels != nil {
		if list, err := a.systemTunnels.List(ctx); err == nil {
			for _, st := range list {
				if st.Status != "up" {
					continue
				}
				out = append(out, metrics.InterfaceRef{ID: st.ID, IsServer: false})
			}
		}
	}

	for _, id := range a.settings.GetServerInterfaces() {
		out = append(out, metrics.InterfaceRef{ID: id, IsServer: true})
	}

	if ms := a.settings.GetManagedServer(); ms != nil && ms.InterfaceName != "" {
		out = append(out, metrics.InterfaceRef{ID: ms.InterfaceName, IsServer: true})
	}

	return dedupeRefs(out)
}

// tunnelNDMSName returns the NDMS logical name (e.g. "Wireguard3",
// "OpkgTun0") for use with RCI endpoints such as
// /show/interface/<name>/wireguard/peer. The KERNEL name (rt.IfaceName,
// e.g. "nwg0") is NOT a valid NDMS identifier — passing it produces 404s.
// Returns "" when no NDMS identity exists, signalling the caller to skip
// the interface.
func tunnelNDMSName(rt trafficpkg.RunningTunnel) string {
	if rt.NDMSName != "" {
		return rt.NDMSName
	}
	return rt.ID
}

func dedupeRefs(refs []metrics.InterfaceRef) []metrics.InterfaceRef {
	seen := make(map[string]struct{}, len(refs))
	out := refs[:0]
	for _, r := range refs {
		if _, ok := seen[r.ID]; ok {
			continue
		}
		seen[r.ID] = struct{}{}
		out = append(out, r)
	}
	return out
}
