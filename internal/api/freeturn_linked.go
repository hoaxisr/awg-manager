package api

import (
	"context"
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// SetLinkedTunnelCleanup wires AWG tunnel store + service so deleting a
// freeturn client also removes tunnels tagged with freeTurnClientId.
func (h *FreeTurnHandler) SetLinkedTunnelCleanup(store *storage.AWGTunnelStore, svc TunnelService) {
	h.awgStore = store
	h.tunnelSvc = svc
}

// SetTunnelsHandler publishes tunnel list SSE after linked tunnel cleanup.
func (h *FreeTurnHandler) SetTunnelsHandler(th *TunnelsHandler) {
	h.tunnelsHandler = th
}

func tunnelLinkedToFreeTurnClient(tun storage.AWGTunnel, clientID string) bool {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return false
	}
	return strings.TrimSpace(tun.FreeTurnClientID) == clientID
}

func (h *FreeTurnHandler) deleteLinkedAwgTunnels(ctx context.Context, clientID string) (deleted []string, errs []string) {
	if h.awgStore == nil || h.tunnelSvc == nil || strings.TrimSpace(clientID) == "" {
		return nil, nil
	}
	tunnels, err := h.awgStore.List()
	if err != nil {
		return nil, []string{err.Error()}
	}
	for _, tun := range tunnels {
		if !tunnelLinkedToFreeTurnClient(tun, clientID) {
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
