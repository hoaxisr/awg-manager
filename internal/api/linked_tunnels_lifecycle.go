package api

import (
	"context"
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/freeturn"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/wdtt"
)

type linkedTunnelPredicate func(storage.AWGTunnel) bool

func listLinkedAwgTunnels(store *storage.AWGTunnelStore, pred linkedTunnelPredicate) ([]storage.AWGTunnel, error) {
	if store == nil {
		return nil, nil
	}
	all, err := store.List()
	if err != nil {
		return nil, err
	}
	var out []storage.AWGTunnel
	for _, tun := range all {
		if pred(tun) {
			out = append(out, tun)
		}
	}
	return out, nil
}

func startLinkedAwgTunnels(
	ctx context.Context,
	store *storage.AWGTunnelStore,
	svc TunnelService,
	th *TunnelsHandler,
	pred linkedTunnelPredicate,
) (started []string, errs []string) {
	if store == nil || svc == nil {
		return nil, nil
	}
	tunnels, err := listLinkedAwgTunnels(store, pred)
	if err != nil {
		return nil, []string{err.Error()}
	}
	for _, tun := range tunnels {
		state := svc.GetState(ctx, tun.ID)
		if state.State == tunnel.StateRunning || state.State == tunnel.StateStarting {
			continue
		}
		if err := svc.Start(ctx, tun.ID); err != nil {
			errs = append(errs, fmt.Sprintf("%s (%s): %v", tun.Name, tun.ID, err))
			continue
		}
		started = append(started, tun.ID)
	}
	if th != nil && len(started) > 0 {
		th.publishTunnelList(ctx)
	}
	return started, errs
}

func stopLinkedAwgTunnels(
	ctx context.Context,
	store *storage.AWGTunnelStore,
	svc TunnelService,
	th *TunnelsHandler,
	pred linkedTunnelPredicate,
) (stopped []string, errs []string) {
	if store == nil || svc == nil {
		return nil, nil
	}
	tunnels, err := listLinkedAwgTunnels(store, pred)
	if err != nil {
		return nil, []string{err.Error()}
	}
	for _, tun := range tunnels {
		state := svc.GetState(ctx, tun.ID)
		switch state.State {
		case tunnel.StateRunning, tunnel.StateStarting:
			// stop below
		default:
			continue
		}
		if err := svc.Stop(ctx, tun.ID); err != nil {
			errs = append(errs, fmt.Sprintf("%s (%s): %v", tun.Name, tun.ID, err))
			continue
		}
		stopped = append(stopped, tun.ID)
	}
	if th != nil && len(stopped) > 0 {
		th.publishTunnelList(ctx)
	}
	return stopped, errs
}

func tryStartAwgTunnel(ctx context.Context, svc TunnelService, th *TunnelsHandler, tunnelID string) error {
	if svc == nil || tunnelID == "" {
		return nil
	}
	state := svc.GetState(ctx, tunnelID)
	if state.State == tunnel.StateRunning || state.State == tunnel.StateStarting {
		return nil
	}
	if err := svc.Start(ctx, tunnelID); err != nil {
		return err
	}
	if th != nil {
		th.publishTunnelList(ctx)
	}
	return nil
}

func syncLinkedAwgTunnelNames(
	ctx context.Context,
	store *storage.AWGTunnelStore,
	th *TunnelsHandler,
	pred linkedTunnelPredicate,
	newName string,
) (renamed []string, errs []string) {
	newName = strings.TrimSpace(newName)
	if store == nil || newName == "" {
		return nil, nil
	}
	tunnels, err := listLinkedAwgTunnels(store, pred)
	if err != nil {
		return nil, []string{err.Error()}
	}
	for _, tun := range tunnels {
		if tun.Name == newName {
			continue
		}
		stored, err := store.Get(tun.ID)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s (%s): %v", tun.Name, tun.ID, err))
			continue
		}
		stored.Name = newName
		if err := store.Save(stored); err != nil {
			errs = append(errs, fmt.Sprintf("%s (%s): %v", tun.Name, tun.ID, err))
			continue
		}
		renamed = append(renamed, tun.ID)
	}
	if th != nil && len(renamed) > 0 {
		th.publishTunnelList(ctx)
	}
	return renamed, errs
}

func clientStartStopResponse(message string, tunnelIDs []string, tunnelErrors []string) map[string]any {
	resp := map[string]any{"message": message}
	if len(tunnelIDs) > 0 {
		resp["linkedTunnels"] = tunnelIDs
	}
	if len(tunnelErrors) > 0 {
		resp["tunnelErrors"] = tunnelErrors
	}
	return resp
}

func appendLinkedTunnelSync(resp map[string]any, synced []string, syncErrs []string) {
	if len(synced) > 0 {
		resp["syncedTunnelEndpoints"] = synced
	}
	if len(syncErrs) > 0 {
		if existing, ok := resp["tunnelErrors"].([]string); ok {
			resp["tunnelErrors"] = append(existing, syncErrs...)
		} else {
			resp["tunnelErrors"] = syncErrs
		}
	}
}

// localEndpointFromListen maps proxy client listen (127.0.0.1:9001) to AWG Peer.Endpoint.
func localEndpointFromListen(listen string) (string, bool) {
	port, ok := freeturn.LocalListenPort(listen)
	if !ok || port <= 0 {
		if p := wdtt.ListenPortFromAddr(listen); p > 0 {
			port = p
		} else {
			return "", false
		}
	}
	return fmt.Sprintf("127.0.0.1:%d", port), true
}

// syncLinkedAwgTunnelEndpoints updates linked AWG tunnels when proxy listen port changes.
func syncLinkedAwgTunnelEndpoints(
	ctx context.Context,
	store *storage.AWGTunnelStore,
	svc TunnelService,
	th *TunnelsHandler,
	pred linkedTunnelPredicate,
	listen string,
) (updated []string, errs []string) {
	want, ok := localEndpointFromListen(listen)
	if !ok || store == nil {
		return nil, nil
	}
	tunnels, err := listLinkedAwgTunnels(store, pred)
	if err != nil {
		return nil, []string{err.Error()}
	}
	for _, tun := range tunnels {
		if strings.TrimSpace(tun.Peer.Endpoint) == want {
			continue
		}
		existing, err := store.Get(tun.ID)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s (%s): %v", tun.Name, tun.ID, err))
			continue
		}
		updatedStored := *existing
		updatedStored.Peer.Endpoint = want
		if svc != nil {
			if err := svc.Update(ctx, existing, &updatedStored); err != nil {
				errs = append(errs, fmt.Sprintf("%s (%s): %v", tun.Name, tun.ID, err))
				continue
			}
		}
		if err := store.Save(&updatedStored); err != nil {
			errs = append(errs, fmt.Sprintf("%s (%s): %v", tun.Name, tun.ID, err))
			continue
		}
		updated = append(updated, tun.ID)
	}
	if th != nil && len(updated) > 0 {
		th.publishTunnelList(ctx)
	}
	return updated, errs
}
