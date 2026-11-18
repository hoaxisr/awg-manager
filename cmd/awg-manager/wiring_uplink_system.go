package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/uplink"
	"github.com/hoaxisr/awg-manager/internal/uplink/systemwg"
)

func (a *app) setupSystemUplink() uplink.Adapter {
	if a.systemTunnelSvc != nil {
		a.systemTunnelSvc.SetUplinkGuard(func(ctx context.Context, id string) (func(), error) {
			if c := a.uplinkController.Load(); c != nil {
				return c.GuardNativeMutation(ctx, id, "transport")
			}
			return nil, fmt.Errorf("%w: system uplink controller not ready", uplink.ErrConflict)
		})
	}
	adapter, err := systemwg.New(systemwg.Deps{Directory: a.dataDir, Transport: a.ndmsTransportClient, Queries: a.ndmsQueries, Commands: a.ndmsCommands,
		Providers: func() []uplink.Provider {
			if a.wanModel == nil {
				return nil
			}
			return (uplinkWANResolver{a.wanModel}).Providers()
		},
		Excluded: func(ctx context.Context, id string) (bool, error) {
			if a.settingsStore != nil && a.settingsStore.IsServerInterface(id) {
				return true, nil
			}
			if a.awgStore != nil {
				list, err := a.awgStore.ListStrict()
				if err != nil {
					return true, err
				}
				for _, t := range list {
					if t.Backend == "nativewg" && "Wireguard"+strconv.Itoa(t.NWGIndex) == id {
						return true, nil
					}
				}
			}
			if a.ndmsQueries == nil {
				return true, fmt.Errorf("%w: NDMS queries uninitialized", uplink.ErrUnsupported)
			}
			list, err := a.ndmsQueries.WGServers.ListSystemTunnelsFresh(ctx)
			if err != nil {
				return true, err
			}
			for _, t := range list {
				if t.ID == id {
					return t.External != "" || t.Description == ndms.BuiltInVPNServerDescription || strings.HasPrefix(t.Description, "Phobos-"), nil
				}
			}
			return true, nil
		},
	})
	if err != nil {
		return unavailableNativeUplink{err: fmt.Errorf("%w: system WireGuard: %v", uplink.ErrConflict, err), applies: func(id string) bool { return strings.HasPrefix(id, "Wireguard") }}
	}
	return adapter
}
