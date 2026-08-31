package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/pingcheck"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/config"
)

// stateToStatus converts a tunnel State to the status string sent to the frontend.
func stateToStatus(s tunnel.State) string {
	switch s {
	case tunnel.StateNotCreated:
		return "not_created"
	case tunnel.StateRunning:
		return "running"
	case tunnel.StateStarting:
		return "starting"
	case tunnel.StateStopping:
		return "stopping"
	case tunnel.StateStopped:
		return "stopped"
	case tunnel.StateBroken:
		return "broken"
	case tunnel.StateNeedsStart:
		return "needs_start"
	case tunnel.StateNeedsStop:
		return "needs_stop"
	case tunnel.StateDisabled:
		return "disabled"
	default:
		return "stopped"
	}
}

// overlayPendingStatus refines the display status for a NativeWG tunnel the RCI
// classifier reports as Broken. The classifier cannot tell "coming up right
// now" from "stuck": a tunnel that never handshook carries no timestamp to
// measure from (the router reports link=down, connected="no" and no uptime at
// all), so it is classified Broken from the first poll. The orchestrator's
// per-tunnel bring-up window is the only source of "right now", hence the
// overlay: inside the window the tunnel shows as starting, past it as broken.
// quiescentUntil is that window (zero if no bring-up was attempted this daemon
// session, e.g. right after a restart); now is injected for tests.
// A zero window keeps its old kmod-proxy meaning on non-ASC firmware only —
// NDMS raised the WireGuard interface but awg-manager has not attached the
// proxy yet, which is "needs start", not a fault. On ASC there is no such
// intermediate state, so a zero window leaves Broken as is (#702).
// Kernel and every non-Broken state pass through unchanged.
func overlayPendingStatus(rawState tunnel.State, backend string, supportsASC bool, quiescentUntil, now time.Time) string {
	base := stateToStatus(rawState)
	if backend != "nativewg" || rawState != tunnel.StateBroken {
		return base
	}
	if now.Before(quiescentUntil) {
		return stateToStatus(tunnel.StateStarting) // actively bringing up
	}
	if quiescentUntil.IsZero() && !supportsASC {
		return stateToStatus(tunnel.StateNeedsStart) // kmod proxy not attached yet
	}
	return base // window elapsed (#183), or nothing to wait for on ASC (#702)
}

// displayStatus is the single point that turns a tunnel's canonical state into
// the UI status string: it applies the boot-pending overlay (see
// overlayPendingStatus), deriving backend from StateInfo so list and detail
// stay consistent. quiescentUntil is the orchestrator bring-up window (zero
// when unknown); the ASC flag comes from the firmware, as the overlay treats a
// missing window differently on the two paths.
func displayStatus(info tunnel.StateInfo, quiescentUntil, now time.Time) string {
	return overlayPendingStatus(info.State, info.BackendType, ndmsinfo.SupportsWireguardASC(), quiescentUntil, now)
}

// quiescentFor returns the orchestrator bring-up window for a tunnel, or zero
// when the orchestrator is not wired (tests/edge).
func (h *TunnelsHandler) quiescentFor(id string) time.Time {
	if h.orch != nil {
		return h.orch.QuiescentUntil(id)
	}
	return time.Time{}
}

// statusForDisplay returns the UI status string for a tunnel via displayStatus.
func (h *TunnelsHandler) statusForDisplay(id string, info tunnel.StateInfo) string {
	return displayStatus(info, h.quiescentFor(id), time.Now())
}

// formatHandshake converts time to human-readable format.
func formatHandshake(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// PublishTunnelList emits resource:invalidated hints for tunnels and
// routing.tunnels so polling stores refetch immediately. Exported for
// cross-handler use (Import, ExternalAdopt, Control).
func (h *TunnelsHandler) PublishTunnelList(ctx context.Context) { h.publishTunnelList(ctx) }

// publishTunnelList emits resource:invalidated hints after any mutation
// that changes the managed-tunnel list (Create / Update / Delete /
// Start / Stop / Restart / Import / Adopt / Replace).
//
//   - ResourceTunnels         — the {tunnels, external, system} snapshot
//     now served by /api/tunnels/all.
//   - ResourceRoutingTunnels  — the routing-page catalog, served by
//     /api/routing/tunnels (Task 11).
//
// Also refreshes the pingcheck snapshot so monitoring picks up
// new/deleted tunnels.
func (h *TunnelsHandler) publishTunnelList(ctx context.Context) {
	_ = ctx
	if h.bus == nil {
		return
	}
	h.bus.PublishInvalidated(events.ResourceTunnels, "list-changed")
	if h.catalog != nil {
		h.bus.PublishInvalidated(events.ResourceRoutingTunnels, "list-changed")
	}

	// Also refresh pingcheck (new/deleted tunnels appear/disappear on monitoring page)
	if h.pingCheckSnapshot != nil {
		h.pingCheckSnapshot()
	}
}

// BuildTunnelResponse builds a consistent tunnel response with stored data.
// Exported so Import and External handlers can reuse the same response format.
// quiescentUntil is the orchestrator bring-up window (zero when unknown) so the
// "state" string carries the same boot-pending overlay the list shows.
func BuildTunnelResponse(r *http.Request, svc TunnelService, store *storage.AWGTunnelStore, id string, quiescentUntil time.Time) (map[string]interface{}, error) {
	t, err := svc.Get(r.Context(), id)
	if err != nil {
		return nil, err
	}

	stored, _ := store.Get(id)

	ispIface := t.ISPInterface
	// NativeWG stores NDMS IDs (e.g. "ISP"), but frontend uses kernel names (e.g. "eth3").
	if stored != nil && stored.Backend == "nativewg" && ispIface != "" && ispIface != "auto" {
		if kernelName := svc.WANModel().NameForID(ispIface); kernelName != "" {
			ispIface = kernelName
		}
	}

	resp := map[string]interface{}{
		"id":            t.ID,
		"name":          t.Name,
		"type":          "awg",
		"enabled":       t.Enabled,
		"defaultRoute":  t.DefaultRoute,
		"ispInterface":  ispIface,
		"interfaceName": t.InterfaceName,
		"ndmsName":      t.NDMSName,
		"state":         displayStatus(t.StateInfo, quiescentUntil, time.Now()),
		"stateInfo":     t.StateInfo,
	}

	if stored != nil {
		// Ключевой материал наружу не отдаём НИ В КАКОМ виде: GET читают три
		// модалки, и каждая шлёт весь объект обратно в update. Сохранность
		// обоих ключей держится не на эхе, а на merge-семантике — пустое
		// значение в теле значит «оставить прежний» (mergedInterface для
		// PrivateKey, mergedPeer для PresharedKey, F56+F70).
		iface := stored.Interface
		iface.PrivateKey = ""
		resp["interface"] = iface
		peer := stored.Peer
		peer.PresharedKey = ""
		resp["peer"] = peer
		resp["pingCheck"] = stored.PingCheck
		resp["connectivityCheck"] = stored.ConnectivityCheck
		resp["ispInterfaceLabel"] = stored.ISPInterfaceLabel
		backend := stored.Backend
		if backend == "" {
			backend = "kernel"
		}
		resp["backend"] = backend
	}

	return resp, nil
}

// tunnelItem is the list-item DTO returned by List and used by SSE snapshots.
type tunnelItem struct {
	ID                        string                           `json:"id"`
	Name                      string                           `json:"name"`
	Type                      string                           `json:"type"`
	Status                    string                           `json:"status"`
	Enabled                   bool                             `json:"enabled"`
	DefaultRoute              bool                             `json:"defaultRoute"`
	ISPInterface              string                           `json:"ispInterface,omitempty"`
	ISPInterfaceLabel         string                           `json:"ispInterfaceLabel,omitempty"`
	ResolvedISPInterface      string                           `json:"resolvedIspInterface,omitempty"`
	ResolvedISPInterfaceLabel string                           `json:"resolvedIspInterfaceLabel,omitempty"`
	Endpoint                  string                           `json:"endpoint"`
	Address                   string                           `json:"address"`
	InterfaceName             string                           `json:"interfaceName"`
	NDMSName                  string                           `json:"ndmsName,omitempty"`
	HasAddressConflict        bool                             `json:"hasAddressConflict"`
	RxBytes                   int64                            `json:"rxBytes"`
	TxBytes                   int64                            `json:"txBytes"`
	LastHandshake             string                           `json:"lastHandshake"`
	Backend                   string                           `json:"backend"`
	BackendType               string                           `json:"backendType,omitempty"`
	AWGVersion                string                           `json:"awgVersion"`
	MTU                       int                              `json:"mtu"`
	StartedAt                 string                           `json:"startedAt,omitempty"`
	PingCheck                 pingcheck.TunnelPingInfo         `json:"pingCheck"`
	ConnectivityCheck         *storage.ConnectivityCheckConfig `json:"connectivityCheck,omitempty"`
	// WdttClientID links the tunnel to the WDTT client instance it was
	// created from; empty for tunnels unrelated to WDTT.
	WdttClientID string `json:"wdttClientId,omitempty"`
	// FreeTurnClientID — то же для FreeTurn-клиента. Пара к WdttClientID:
	// список помечает туннели, принадлежащие прокси-выходу, и без второго
	// поля метки не было бы ровно у половины из них.
	FreeTurnClientID string `json:"freeTurnClientId,omitempty"`
}

// listItems builds the tunnel list items for API response and SSE snapshots.
func (h *TunnelsHandler) listItems(ctx context.Context) ([]tunnelItem, error) {
	tunnels, err := h.svc.List(ctx)
	if err != nil {
		return nil, err
	}

	// Build set of addresses used by running tunnels (for conflict detection)
	runningAddresses := make(map[string]string) // address -> tunnelID
	for _, t := range tunnels {
		if t.State == tunnel.StateRunning {
			if stored, _ := h.store.Get(t.ID); stored != nil && stored.Interface.Address != "" {
				runningAddresses[stored.Interface.Address] = t.ID
			}
		}
	}

	items := make([]tunnelItem, 0, len(tunnels))
	for _, t := range tunnels {
		// Get stored tunnel for additional fields
		stored, _ := h.store.Get(t.ID)

		awgVersion := "wg"
		var endpoint, address, wdttClientID, freeTurnClientID string
		var ispInterface, ispInterfaceLabel string
		var resolvedISPInterface, resolvedISPInterfaceLabel string
		var mtu int
		if stored != nil {
			endpoint = stored.Peer.Endpoint
			address = stored.Interface.Address
			mtu = stored.Interface.MTU
			awgVersion = config.ClassifyAWGVersion(&stored.Interface)
			if stored.Backend == backendWdttRaw {
				// У зеркала прокси-выхода нет конфига интерфейса, по
				// которому считается версия: классификатор вернул бы
				// "wg" и карточка получила бы бейдж «WireGuard».
				awgVersion = "raw"
			}
			ispInterface = stored.ISPInterface
			ispInterfaceLabel = stored.ISPInterfaceLabel
			wdttClientID = strings.TrimSpace(stored.WdttClientID)
			freeTurnClientID = strings.TrimSpace(stored.FreeTurnClientID)

			// NativeWG stores NDMS IDs (e.g. "ISP"), but frontend uses kernel names (e.g. "eth3").
			// Convert back so the dropdown can match the stored value.
			if stored.Backend == "nativewg" && ispInterface != "" && ispInterface != "auto" {
				if kernelName := h.svc.WANModel().NameForID(ispInterface); kernelName != "" {
					ispInterface = kernelName
				}
			}

			// For running tunnels, resolve actual WAN from in-memory tracking
			if t.State == tunnel.StateRunning {
				if resolved := h.svc.GetResolvedISP(t.ID); resolved != "" {
					resolvedISPInterface = resolved
					resolvedISPInterfaceLabel = h.svc.WANModel().GetLabel(resolved)
					if resolvedISPInterfaceLabel == "" {
						// Non-WAN interface (bridge mode etc.) — use stored label from routing page
						resolvedISPInterfaceLabel = ispInterfaceLabel
					}
					if resolvedISPInterfaceLabel == "" {
						// Last resort — show kernel interface name
						resolvedISPInterfaceLabel = resolved
					}

				}

				// NativeWG: resolve actual WAN from NDMS peer "via" field
				if resolvedISPInterface == "" && stored.Backend == "nativewg" {
					if via := t.StateInfo.PeerVia; via != "" {
						wanModel := h.svc.WANModel()
						if kernelName := wanModel.NameForID(via); kernelName != "" {
							resolvedISPInterface = kernelName
							resolvedISPInterfaceLabel = wanModel.GetLabel(kernelName)
						}
						if resolvedISPInterfaceLabel == "" {
							resolvedISPInterfaceLabel = via
						}
					}
				}
			}
		}

		// Detect address conflict: another running tunnel uses the same address
		hasConflict := false
		if address != "" && t.State != tunnel.StateRunning {
			if conflictID, ok := runningAddresses[address]; ok && conflictID != t.ID {
				hasConflict = true
			}
		}

		backend := "kernel"
		if stored != nil {
			switch stored.Backend {
			case "nativewg":
				backend = "nativewg"
			case backendWdttRaw:
				backend = backendWdttRaw
			}
		}

		var startedAt string
		if t.StateInfo.ConnectedAt != "" {
			// Use NDMS uptime as source of truth (both kernel and NativeWG)
			startedAt = t.StateInfo.ConnectedAt
		} else if stored != nil && stored.StartedAt != "" {
			startedAt = stored.StartedAt // fallback to storage
		}

		var pcInfo pingcheck.TunnelPingInfo
		if h.pingCheck != nil {
			pcInfo = h.pingCheck.GetTunnelPingStatus(t.ID)
		} else {
			pcInfo = pingcheck.TunnelPingInfo{Status: "disabled"}
		}

		item := tunnelItem{
			ID:                        t.ID,
			Name:                      t.Name,
			Type:                      "awg",
			Status:                    h.statusForDisplay(t.ID, t.StateInfo),
			Enabled:                   t.Enabled,
			DefaultRoute:              t.DefaultRoute,
			ISPInterface:              ispInterface,
			ISPInterfaceLabel:         ispInterfaceLabel,
			ResolvedISPInterface:      resolvedISPInterface,
			ResolvedISPInterfaceLabel: resolvedISPInterfaceLabel,
			Endpoint:                  endpoint,
			Address:                   address,
			InterfaceName:             t.InterfaceName,
			NDMSName:                  t.NDMSName,
			Backend:                   backend,
			HasAddressConflict:        hasConflict,
			RxBytes:                   t.StateInfo.RxBytes,
			TxBytes:                   t.StateInfo.TxBytes,
			LastHandshake:             formatHandshake(t.StateInfo.LastHandshake),
			BackendType:               t.StateInfo.BackendType,
			AWGVersion:                awgVersion,
			MTU:                       mtu,
			StartedAt:                 startedAt,
			PingCheck:                 pcInfo,
			WdttClientID:              wdttClientID,
			FreeTurnClientID:          freeTurnClientID,
		}
		if stored != nil && stored.ConnectivityCheck != nil {
			item.ConnectivityCheck = stored.ConnectivityCheck
		}
		items = append(items, item)
	}

	return items, nil
}

// writeAll writes the composite tunnels snapshot. Used by GetAll
// (REST poll) and by any mutation that wants to return fresh state
// inline (see Task spec — current Create/Update/Delete return a single
// entity instead, so this is reserved for future callers).
func (h *TunnelsHandler) writeAll(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if h.buildTunnelsSnapshot != nil {
		if payload := h.buildTunnelsSnapshot(ctx); payload != nil {
			response.Success(w, payload)
			return
		}
	}
	// Fallback: managed-only (no external / system lists wired).
	items, err := h.listItems(ctx)
	if err != nil {
		response.Error(w, err.Error(), "LIST_FAILED")
		return
	}
	response.Success(w, map[string]interface{}{
		"tunnels":  items,
		"external": []interface{}{},
		"system":   []interface{}{},
	})
}
