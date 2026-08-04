package api

import (
	"context"
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/wdtt"
)

func (h *WdttHandler) SetLinkedTunnelCleanup(store *storage.AWGTunnelStore, svc TunnelService) {
	h.awgStore = store
	h.tunnelSvc = svc
}

func (h *WdttHandler) SetTunnelsHandler(th *TunnelsHandler) {
	h.tunnelsHandler = th
}

func tunnelLinkedToWdttClient(tun storage.AWGTunnel, clientID string) bool {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return false
	}
	return strings.TrimSpace(tun.WdttClientID) == clientID
}

func (h *WdttHandler) startLinkedAwgTunnels(ctx context.Context, clientID string) ([]string, []string) {
	return startLinkedAwgTunnels(ctx, h.awgStore, h.tunnelSvc, h.tunnelsHandler, func(tun storage.AWGTunnel) bool {
		return tunnelLinkedToWdttClient(tun, clientID)
	})
}

func (h *WdttHandler) stopLinkedAwgTunnels(ctx context.Context, clientID string) ([]string, []string) {
	return stopLinkedAwgTunnels(ctx, h.awgStore, h.tunnelSvc, h.tunnelsHandler, func(tun storage.AWGTunnel) bool {
		return tunnelLinkedToWdttClient(tun, clientID)
	})
}

func (h *WdttHandler) wdttClientRunning(clientID string) bool {
	st := h.svc.Status()
	for _, c := range st.Clients {
		if c.ID == clientID {
			return c.Status.Running
		}
	}
	return false
}

func (h *WdttHandler) syncLinkedTunnelNames(ctx context.Context, clientID, clientName string) ([]string, []string) {
	newName := wdtt.TunnelNameFromClient(wdtt.ClientInstance{Name: clientName})
	return syncLinkedAwgTunnelNames(ctx, h.awgStore, h.tunnelsHandler, func(tun storage.AWGTunnel) bool {
		return tunnelLinkedToWdttClient(tun, clientID)
	}, newName)
}

func (h *WdttHandler) syncLinkedTunnelEndpoints(ctx context.Context, clientID, listen string) ([]string, []string) {
	return syncLinkedAwgTunnelEndpoints(ctx, h.awgStore, h.tunnelSvc, h.tunnelsHandler, func(tun storage.AWGTunnel) bool {
		return tunnelLinkedToWdttClient(tun, clientID)
	}, listen)
}

func wdttClientListen(svc WdttService, id string) string {
	cfg, err := svc.GetConfig()
	if err != nil {
		return ""
	}
	for _, c := range cfg.Clients {
		if c.ID == id {
			return strings.TrimSpace(c.Config.Listen)
		}
	}
	return ""
}

func (h *WdttHandler) deleteLinkedAwgTunnels(ctx context.Context, clientID string) (deleted []string, errs []string) {
	if h.awgStore == nil || h.tunnelSvc == nil || strings.TrimSpace(clientID) == "" {
		return nil, nil
	}
	tunnels, err := h.awgStore.List()
	if err != nil {
		return nil, []string{err.Error()}
	}
	for _, tun := range tunnels {
		if !tunnelLinkedToWdttClient(tun, clientID) {
			continue
		}
		if err := h.tunnelSvc.Delete(ctx, tun.ID); err != nil {
			errs = append(errs, fmt.Sprintf("%s (%s): %v", tun.Name, tun.ID, err))
			continue
		}
		if h.tunnelsHandler != nil && h.tunnelsHandler.traffic != nil {
			h.tunnelsHandler.traffic.Clear(tun.ID)
		}
		deleted = append(deleted, tun.ID)
	}
	return deleted, errs
}
