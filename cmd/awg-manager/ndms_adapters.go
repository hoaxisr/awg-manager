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
// for non-managed system WG tunnels, user-configured server interfaces,
// and the managed WG server. Managed AWGM tunnels are driven separately
// by traffic.SysfsPoller via direct sysfs counters and do not pass
// through this adapter.
type runningInterfacesAdapter struct {
	systemTunnels systemTunnelLister
	settings      *storage.SettingsStore
}

func newRunningInterfacesAdapter(systemTunnels systemTunnelLister, settings *storage.SettingsStore) *runningInterfacesAdapter {
	return &runningInterfacesAdapter{
		systemTunnels: systemTunnels,
		settings:      settings,
	}
}

func (a *runningInterfacesAdapter) RunningInterfaces(ctx context.Context) []metrics.InterfaceRef {
	out := make([]metrics.InterfaceRef, 0, 8)

	// Fetch system WG tunnels once — used both for non-managed additions
	// and for filtering server interfaces by up-status below.
	var sysUp map[string]bool
	if a.systemTunnels != nil {
		if list, err := a.systemTunnels.List(ctx); err == nil {
			sysUp = make(map[string]bool, len(list))
			for _, st := range list {
				sysUp[st.ID] = (st.Status == "up")
				if st.Status != "up" {
					continue
				}
				out = append(out, metrics.InterfaceRef{ID: st.ID, IsServer: false})
			}
		}
	}

	for _, id := range a.settings.GetServerInterfaces() {
		// Skip servers that aren't up — polling their /wireguard/peer
		// yields 404 + wasted RCI traffic. sysUp == nil means we couldn't
		// check; include by default to preserve previous behaviour.
		if sysUp != nil && !sysUp[id] {
			continue
		}
		out = append(out, metrics.InterfaceRef{ID: id, IsServer: true})
	}

	if ms := a.settings.GetManagedServer(); ms != nil && ms.InterfaceName != "" {
		if sysUp == nil || sysUp[ms.InterfaceName] {
			out = append(out, metrics.InterfaceRef{ID: ms.InterfaceName, IsServer: true})
		}
	}

	return dedupeRefs(out)
}

// dedupeRefs merges duplicate IDs into a single entry. When an ID is
// added both as a regular interface (IsServer=false) and as a server
// (IsServer=true) — which happens for managed servers that also show
// up via systemTunnels.List() — the server flag wins. Without this,
// the poller routes managed-server peer changes to the tunnel-traffic
// path instead of the server-snapshot path, delaying /servers page
// updates until the next polling tick.
func dedupeRefs(refs []metrics.InterfaceRef) []metrics.InterfaceRef {
	idx := make(map[string]int, len(refs))
	out := make([]metrics.InterfaceRef, 0, len(refs))
	for _, r := range refs {
		if i, ok := idx[r.ID]; ok {
			if r.IsServer {
				out[i].IsServer = true
			}
			continue
		}
		idx[r.ID] = len(out)
		out = append(out, r)
	}
	return out
}
