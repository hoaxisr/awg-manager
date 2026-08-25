package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/wdtt"
)

// EnsureWGTunnelResponse is returned by POST /api/wdtt/clients/{id}/ensure-wg-tunnel.
type EnsureWGTunnelResponse struct {
	Created    bool   `json:"created"`
	TunnelID   string `json:"tunnelId,omitempty"`
	TunnelName string `json:"tunnelName,omitempty"`
	Message    string `json:"message,omitempty"`
}

// WdttEnsureWGTunnelResponse is the envelope for POST /api/wdtt/clients/{id}/ensure-wg-tunnel.
type WdttEnsureWGTunnelResponse struct {
	Success bool                   `json:"success" example:"true"`
	Data    EnsureWGTunnelResponse `json:"data"`
}

// ensureWGTunnel handles POST /api/wdtt/clients/{id}/ensure-wg-tunnel.
//
//	@Summary	Ensure an AWG tunnel exists for the client's WireGuard config
//	@Tags		wdtt
//	@Param		id	path		string	true	"Client instance id"
//	@Success	200	{object}	WdttEnsureWGTunnelResponse
//	@Failure	500	{object}	APIErrorEnvelope
//	@Router		/wdtt/clients/{id}/ensure-wg-tunnel [post]
func (h *WdttHandler) ensureWGTunnel(w http.ResponseWriter, r *http.Request, clientID string) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	if h.awgStore == nil || h.tunnelSvc == nil {
		response.Error(w, "tunnel import not wired", "INTERNAL")
		return
	}

	cfg, err := h.svc.GetConfig()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	var inst *wdtt.ClientInstance
	for i := range cfg.Clients {
		if cfg.Clients[i].ID == clientID {
			inst = &cfg.Clients[i]
			break
		}
	}
	if inst == nil {
		response.Error(w, "client not found", "NOT_FOUND")
		return
	}
	if !inst.Config.UsesWireGuard() {
		response.Success(w, EnsureWGTunnelResponse{
			Created: false,
			Message: "Режим Raw: AWG-туннель не используется — трафик идёт через OpkgTun (NDMS)",
		})
		return
	}

	st := h.svc.Status()
	var proc wdtt.ProcessStatus
	for _, c := range st.Clients {
		if c.ID == clientID {
			proc = c.Status
			break
		}
	}
	wgRaw := strings.TrimSpace(proc.WgConfig)
	if wgRaw == "" {
		wgRaw = wdtt.ExtractWGConfigFromLog(proc.Log)
	}
	if wgRaw == "" {
		response.Success(w, EnsureWGTunnelResponse{
			Created: false,
			Message: "WireGuard конфиг ещё не получен от wdtt-server — дождитесь успешного подключения клиента",
		})
		return
	}

	tunnels, err := h.awgStore.List()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	port := wdtt.ListenPortFromAddr(inst.Config.Listen)
	patched := wdtt.PatchWgConfEndpoint(wgRaw, port)
	newPeerKey := wdtt.ExtractPeerPublicKey(patched)

	// Drop stale tunnels linked to this client (peer/country changed).
	for _, tun := range tunnels {
		if strings.TrimSpace(tun.WdttClientID) != clientID {
			continue
		}
		storedKey := strings.TrimSpace(tun.Peer.PublicKey)
		if newPeerKey != "" && storedKey != "" && newPeerKey != storedKey {
			if err := h.tunnelSvc.Delete(r.Context(), tun.ID); err != nil {
				if errors.Is(err, tunnel.ErrOperationInProgress) {
					// Занятый замок ретраибелен, а 400 читается как
					// «запрос неверен» — тот же контракт, что у delete.
					response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "OPERATION_IN_PROGRESS")
					return
				}
				response.Error(w, "не удалось удалить устаревший AWG-туннель: "+err.Error(), "WDTT_WG_REPLACE_FAILED")
				return
			}
			if h.tunnelsHandler != nil && h.tunnelsHandler.traffic != nil {
				h.tunnelsHandler.traffic.Clear(tun.ID)
			}
		}
	}

	// Re-list after deletions.
	if tunnels, err = h.awgStore.List(); err != nil {
		response.InternalError(w, err.Error())
		return
	}

	if match := wdtt.MatchingAWGTunnel(tunnels, patched); match != nil {
		stored, err := h.awgStore.Get(match.ID)
		if err != nil {
			response.InternalError(w, err.Error())
			return
		}
		changed := false
		if strings.TrimSpace(stored.WdttClientID) != clientID {
			stored.WdttClientID = clientID
			changed = true
		}
		wantName := wdtt.TunnelNameFromClient(*inst)
		if wantName != "" && stored.Name != wantName {
			stored.Name = wantName
			changed = true
		}
		wantEndpoint := fmt.Sprintf("127.0.0.1:%d", port)
		if strings.TrimSpace(stored.Peer.Endpoint) != wantEndpoint {
			stored.Peer.Endpoint = wantEndpoint
			changed = true
		}
		if changed {
			if err := h.awgStore.Save(stored); err != nil {
				response.InternalError(w, err.Error())
				return
			}
			if h.tunnelsHandler != nil {
				h.tunnelsHandler.publishTunnelList(r.Context())
			}
		}
		if h.wdttClientRunning(clientID) {
			_ = tryStartAwgTunnel(r.Context(), h.tunnelSvc, h.tunnelsHandler, match.ID)
		}
		response.Success(w, EnsureWGTunnelResponse{
			Created:    false,
			TunnelID:   match.ID,
			TunnelName: match.Name,
			Message:    "AWG-туннель с таким WireGuard-конфигом уже существует",
		})
		return
	}

	name := wdtt.TunnelNameFromClient(*inst)

	tunnel, err := h.tunnelSvc.Import(r.Context(), patched, name, "")
	if err != nil {
		response.Error(w, err.Error(), "WDTT_WG_IMPORT_FAILED")
		return
	}
	stored, err := h.awgStore.Get(tunnel.ID)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	stored.WdttClientID = clientID
	if err := h.awgStore.Save(stored); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	if h.tunnelsHandler != nil {
		h.tunnelsHandler.publishTunnelList(r.Context())
	}
	if h.wdttClientRunning(clientID) {
		_ = tryStartAwgTunnel(r.Context(), h.tunnelSvc, h.tunnelsHandler, tunnel.ID)
	}
	response.Success(w, EnsureWGTunnelResponse{
		Created:    true,
		TunnelID:   tunnel.ID,
		TunnelName: tunnel.Name,
		Message:    fmt.Sprintf("Создан AWG-туннель «%s» (Endpoint 127.0.0.1:%d)", tunnel.Name, port),
	})
}
