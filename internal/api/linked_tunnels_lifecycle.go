package api

import (
	"context"
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
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
