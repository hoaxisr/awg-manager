package api

import (
	"context"
	"fmt"
	"net"
	"strconv"
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
// Разбор — локальный: прежние freeturn.LocalListenPort и wdtt.ListenPortFromAddr
// живут в пакетах, которые умирают вместе со старым движком (Н1 плана 5).
// Хост обязан быть 127.0.0.1 либо пустым — паритет обеих старых функций по
// намерению; их фолбэк «неразобранный адрес → порт 9000» не воспроизводится:
// он молча переписывал endpoint связанного туннеля на чужой порт.
func localEndpointFromListen(listen string) (string, bool) {
	host, portStr, err := net.SplitHostPort(strings.TrimSpace(listen))
	if err != nil {
		return "", false
	}
	if host != "" && host != "127.0.0.1" {
		return "", false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return "", false
	}
	return fmt.Sprintf("127.0.0.1:%d", port), true
}

// LinkedField — поле связи записи туннеля с прокси-клиентом.
type LinkedField int

const (
	LinkedWdtt     LinkedField = iota // storage.AWGTunnel.WdttClientID
	LinkedFreeTurn                    // storage.AWGTunnel.FreeTurnClientID
)

// SyncLinkedProxyEndpoints — экспорт для адаптера прокси-рантайма (план 5):
// pred строится по полю связи и clientID; th=nil — публикацию списка туннелей
// делает вызывающий (адаптер шлёт resource:invalidated через шину сам).
func SyncLinkedProxyEndpoints(ctx context.Context, store *storage.AWGTunnelStore,
	svc TunnelService, field LinkedField, clientID, listen string) (updated, failed []string) {
	var pred linkedTunnelPredicate
	switch field {
	case LinkedWdtt:
		pred = func(tun storage.AWGTunnel) bool { return tunnelLinkedToWdttClient(tun, clientID) }
	case LinkedFreeTurn:
		pred = func(tun storage.AWGTunnel) bool { return tunnelLinkedToFreeTurnClient(tun, clientID) }
	default:
		return nil, []string{fmt.Sprintf("неизвестное поле связи %d", int(field))}
	}
	return syncLinkedAwgTunnelEndpoints(ctx, store, svc, nil, pred, listen)
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
