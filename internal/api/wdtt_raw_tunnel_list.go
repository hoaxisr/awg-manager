package api

import (
	"context"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/pingcheck"
	"github.com/hoaxisr/awg-manager/internal/wdtt"
)

type wdttListSource interface {
	Status() wdtt.Status
	GetConfig() (wdtt.Config, error)
}

func (h *TunnelsHandler) appendWdttRawListItems(ctx context.Context, items []tunnelItem) []tunnelItem {
	if h.store == nil || h.wdttSvc == nil {
		return items
	}
	stored, err := h.store.List()
	if err != nil {
		return items
	}
	cfg, err := h.wdttSvc.GetConfig()
	if err != nil {
		return items
	}
	st := h.wdttSvc.Status()
	now := time.Now()
	seen := make(map[string]struct{}, len(items))
	for _, it := range items {
		seen[it.ID] = struct{}{}
	}

	for _, tun := range stored {
		if tun.Backend != wdtt.BackendWdttRaw {
			continue
		}
		if _, dup := seen[tun.ID]; dup {
			continue
		}
		clientID := strings.TrimSpace(tun.WdttClientID)
		kernelIface := strings.TrimSpace(tun.RawKernelIface)
		ndmsName := strings.TrimSpace(tun.RawNdmsIface)
		mtu := tun.Interface.MTU
		for _, c := range cfg.Clients {
			if c.ID != clientID {
				continue
			}
			if raw := strings.TrimSpace(c.Config.RawIface); raw != "" {
				kernelIface = raw
			}
			ndmsName = strings.TrimSpace(c.Config.NdmsIface)
			if addr := strings.TrimSpace(c.Config.RawClientIP); addr != "" {
				tun.Interface.Address = addr
				if !strings.Contains(addr, "/") {
					tun.Interface.Address = addr + "/32"
				}
			}
			break
		}
		running := false
		var startedAt string
		for _, c := range st.Clients {
			if c.ID != clientID {
				continue
			}
			running = c.Status.Running
			if raw := strings.TrimSpace(c.Status.RawIface); raw != "" {
				kernelIface = raw
			}
			if ndms := strings.TrimSpace(c.Status.NdmsIface); ndms != "" {
				ndmsName = ndms
			}
			if ip := strings.TrimSpace(c.Status.RawClientIP); ip != "" {
				tun.Interface.Address = ip
				if !strings.Contains(ip, "/") {
					tun.Interface.Address = ip + "/32"
				}
			}
			if c.Status.StartedAt != nil {
				startedAt = c.Status.StartedAt.UTC().Format(time.RFC3339)
			}
			break
		}
		status := "stopped"
		if running {
			status = "running"
		}
		if startedAt == "" {
			startedAt = strings.TrimSpace(tun.StartedAt)
		}
		var rxBytes, txBytes int64
		if h.svc != nil {
			si := h.svc.GetState(ctx, tun.ID)
			rxBytes = si.RxBytes
			txBytes = si.TxBytes
			if startedAt == "" && si.ConnectedAt != "" {
				startedAt = si.ConnectedAt
			}
		}
		ifaceName := kernelIface
		if ifaceName == "" {
			ifaceName = tun.ID
		}
		if mtu <= 0 {
			mtu = 1300
		}
		connCheck := wdttRawConnectivityCheck(&tun)
		seen[tun.ID] = struct{}{}
		items = append(items, tunnelItem{
			ID:                tun.ID,
			Name:              tun.Name,
			Type:              "awg",
			Status:            status,
			Enabled:           running,
			DefaultRoute:      tun.DefaultRoute,
			Endpoint:          tun.Peer.Endpoint,
			Address:           tun.Interface.Address,
			InterfaceName:     ifaceName,
			NDMSName:          ndmsName,
			Backend:           wdtt.BackendWdttRaw,
			BackendType:       wdtt.BackendWdttRaw,
			AWGVersion:        "raw",
			MTU:               mtu,
			RxBytes:           rxBytes,
			TxBytes:           txBytes,
			StartedAt:         startedAt,
			PingCheck:         pingcheck.TunnelPingInfo{Status: "idle"},
			ConnectivityCheck: connCheck,
			WdttClientID:      clientID,
		})
		_ = ctx
		_ = now
	}
	return items
}

func (h *TunnelsHandler) SetWdttListSource(s wdttListSource) {
	h.wdttSvc = s
}
