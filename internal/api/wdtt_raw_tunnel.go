package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/wdtt"
)

// ensureRawTunnel handles POST /api/wdtt/clients/{id}/ensure-raw-tunnel.
func (h *WdttHandler) ensureRawTunnel(w http.ResponseWriter, r *http.Request, clientID string) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	if h.awgStore == nil {
		response.Error(w, "tunnel store not wired", "INTERNAL")
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
	if inst.Config.UsesWireGuard() {
		response.Success(w, EnsureWGTunnelResponse{
			Created: false,
			Message: "Режим WG: используйте ensure-wg-tunnel",
		})
		return
	}

	running := h.wdttClientRunning(clientID)
	st := h.svc.Status()
	var proc wdtt.ProcessStatus
	for _, c := range st.Clients {
		if c.ID != clientID {
			continue
		}
		proc = c.Status
		if ip := strings.TrimSpace(c.Status.RawClientIP); ip != "" {
			inst.Config.RawClientIP = ip
		}
		if ndms := strings.TrimSpace(c.Status.NdmsIface); ndms != "" {
			inst.Config.NdmsIface = ndms
		}
		if raw := strings.TrimSpace(c.Status.RawIface); raw != "" {
			inst.Config.RawIface = raw
		}
		break
	}

	tid := wdtt.RawTunnelID(clientID)
	created := false
	if all, err := h.awgStore.List(); err == nil {
		for _, tun := range all {
			if strings.TrimSpace(tun.WdttClientID) != clientID {
				continue
			}
			if tun.Backend == wdtt.BackendWdttRaw {
				if tun.ID == tid {
					continue
				}
				_ = h.awgStore.Delete(tun.ID)
				continue
			}
			// Raw mode replaces stale kernel AWG imports from ensure-wg-tunnel.
			if h.tunnelSvc != nil {
				_ = h.tunnelSvc.Delete(r.Context(), tun.ID)
			}
			if h.tunnelsHandler != nil && h.tunnelsHandler.traffic != nil {
				h.tunnelsHandler.traffic.Clear(tun.ID)
			}
			_ = h.awgStore.Delete(tun.ID)
		}
	}
	if _, errPrev := h.awgStore.Get(tid); errPrev != nil {
		created = true
	}

	record := wdtt.BuildRawTunnelRecord(*inst, running, proc)
	if prev, err := h.awgStore.Get(tid); err == nil && prev != nil {
		record.CreatedAt = prev.CreatedAt
		if prev.DefaultRoute {
			record.DefaultRoute = true
		}
		if prev.PingCheck != nil {
			record.PingCheck = prev.PingCheck
		}
		if prev.ConnectivityCheck != nil && prev.ConnectivityCheck.Method != "disabled" {
			record.ConnectivityCheck = prev.ConnectivityCheck
		}
	}
	wdtt.EnrichRawTunnelRecord(&record, *inst, proc)

	if err := h.awgStore.Save(&record); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	if h.tunnelsHandler != nil {
		h.tunnelsHandler.publishTunnelList(r.Context())
	}

	msg := "Запись WDTT Raw в AWG-туннелях обновлена"
	if created {
		msg = "WDTT Raw добавлен в список AWG-туннелей"
	}
	response.Success(w, EnsureWGTunnelResponse{
		Created:    created,
		TunnelID:   tid,
		TunnelName: record.Name,
		Message:    msg,
	})
}

func isWdttRawStoredTunnel(tun *storage.AWGTunnel) bool {
	return tun != nil && tun.Backend == wdtt.BackendWdttRaw
}

func wdttRawConnectivityCheck(stored *storage.AWGTunnel) *storage.ConnectivityCheckConfig {
	if stored != nil && stored.ConnectivityCheck != nil && stored.ConnectivityCheck.Method != "" {
		return stored.ConnectivityCheck
	}
	return wdtt.DefaultRawConnectivityCheck()
}

// syncWdttRawLiveFields refreshes kernel/NDMS iface names from the running WDTT client.
func (h *TunnelsHandler) syncWdttRawLiveFields(stored *storage.AWGTunnel) {
	if h == nil || stored == nil || h.wdttSvc == nil {
		return
	}
	clientID := strings.TrimSpace(stored.WdttClientID)
	if clientID == "" {
		return
	}
	st := h.wdttSvc.Status()
	for _, c := range st.Clients {
		if c.ID != clientID {
			continue
		}
		if raw := strings.TrimSpace(c.Status.RawIface); raw != "" {
			stored.RawKernelIface = raw
		}
		if ndms := strings.TrimSpace(c.Status.NdmsIface); ndms != "" {
			stored.RawNdmsIface = ndms
		}
		if ip := strings.TrimSpace(c.Status.RawClientIP); ip != "" {
			stored.Interface.Address = ip
			if !strings.Contains(ip, "/") {
				stored.Interface.Address = ip + "/32"
			}
		}
		if c.Status.StartedAt != nil {
			stored.StartedAt = c.Status.StartedAt.UTC().Format(time.RFC3339)
		}
		stored.Enabled = c.Status.Running
		break
	}
}

func (h *TunnelsHandler) buildWdttRawResponse(stored *storage.AWGTunnel) map[string]interface{} {
	if stored == nil {
		return nil
	}
	updated := *stored
	h.syncWdttRawLiveFields(&updated)

	running := updated.Enabled
	status := "stopped"
	if running {
		status = "running"
	}
	ifaceName := strings.TrimSpace(updated.RawKernelIface)
	if ifaceName == "" {
		ifaceName = updated.ID
	}
	return map[string]interface{}{
		"id":                updated.ID,
		"name":              updated.Name,
		"type":              "awg",
		"enabled":           running,
		"defaultRoute":      updated.DefaultRoute,
		"interfaceName":     ifaceName,
		"ndmsName":          updated.RawNdmsIface,
		"state":             status,
		"backend":           wdtt.BackendWdttRaw,
		"interface":         updated.Interface,
		"peer":              updated.Peer,
		"connectivityCheck": wdttRawConnectivityCheck(&updated),
	}
}
