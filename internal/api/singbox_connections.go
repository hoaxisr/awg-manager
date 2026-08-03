package api

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/connections"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// HotspotLister is the narrow contract this handler needs from the NDMS
// hotspot cache. Lives here so the handler doesn't import
// internal/ndms/query directly — fake-friendly.
type HotspotLister interface {
	List(ctx context.Context) ([]ndms.Device, error)
}

// WGServerPeersLister is the narrow contract this handler needs from the
// system WG-servers view (ServersHandler.ListServers): the filtered server
// list whose peers carry Description + AllowedIPs. Fake-friendly.
type WGServerPeersLister interface {
	ListServers(ctx context.Context) ([]ndms.WireguardServer, error)
}

// ManagedServersLister is the narrow contract this handler needs from the
// managed WG-server service (managed.Service.List): stored servers whose
// peers carry Description + TunnelIP. Fake-friendly.
type ManagedServersLister interface {
	List() []storage.ManagedServer
}

// SingboxConnectionsClientsData mirrors frontend ClientsByIP map.
type SingboxConnectionsClientsData struct {
	ClientsByIP map[string]string `json:"clientsByIP"`
}

// SingboxConnectionsClientsResponse is the envelope for GET /singbox/connections/clients.
type SingboxConnectionsClientsResponse struct {
	Success bool                          `json:"success" example:"true"`
	Data    SingboxConnectionsClientsData `json:"data"`
}

// SingboxConnectionsHandler serves the narrow IP→display-name lookup used
// by the Connections monitor sub-tab to enrich Clash connection rows.
type SingboxConnectionsHandler struct {
	hotspot HotspotLister
	// Optional WG peer-name sources (issue #435). Either may stay nil —
	// the endpoint then degrades to the hotspot-only behavior.
	wgServers WGServerPeersLister
	managed   ManagedServersLister
}

// NewSingboxConnectionsHandler builds the handler. hotspot may be nil
// during partial bootstrap — handler responds 503 until wired.
func NewSingboxConnectionsHandler(hotspot HotspotLister) *SingboxConnectionsHandler {
	return &SingboxConnectionsHandler{hotspot: hotspot}
}

// SetWGServers wires the system WG-servers peer-name source (optional).
func (h *SingboxConnectionsHandler) SetWGServers(l WGServerPeersLister) {
	h.wgServers = l
}

// SetManagedServers wires the managed WG-servers peer-name source (optional).
func (h *SingboxConnectionsHandler) SetManagedServers(l ManagedServersLister) {
	h.managed = l
}

// Clients returns IP→display-name from the NDMS hotspot cache, merged with
// WireGuard-server peer names so tunnel clients render like LAN devices.
//
//	@Summary		IP → display-name map for sing-box connections
//	@Description	Returns the current mapping of source IPs to display names: NDMS hotspot devices (cached upstream, TTL 30s) merged with WireGuard-server peer names — both system/NDMS VPN servers (peer description keyed by the /32 allowed IP) and awg-manager managed servers (peer description keyed by the tunnel IP). Hotspot names win on collision. On any error returns 200 with whatever subset resolved (best-effort) so the UI keeps working with raw IPs.
//	@Tags			singbox
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	SingboxConnectionsClientsResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		503	{object}	APIErrorEnvelope
//	@Router			/singbox/connections/clients [get]
func (h *SingboxConnectionsHandler) Clients(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	if h.hotspot == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "Hotspot cache not available", "SERVICE_UNAVAILABLE")
		return
	}
	devs, err := h.hotspot.List(r.Context())
	out := make(map[string]string, len(devs))
	// WG peer names go in first; hotspot names below overwrite on
	// collision — the LAN table stays authoritative for LAN IPs.
	h.addWGServerPeers(r.Context(), out)
	h.addManagedPeers(out)
	if err == nil {
		for _, d := range devs {
			name := d.Name
			if name == "" {
				name = d.Hostname
			}
			if name == "" || d.IP == "" {
				continue
			}
			out[strings.ToLower(d.IP)] = name
		}
	}
	response.Success(w, SingboxConnectionsClientsData{ClientsByIP: out})
}

// addWGServerPeers merges system (NDMS) WG-server peer names into out,
// keyed by each peer's single-host allowed IP. Best-effort: a nil source
// or list error leaves the map untouched.
func (h *SingboxConnectionsHandler) addWGServerPeers(ctx context.Context, out map[string]string) {
	if h.wgServers == nil {
		return
	}
	servers, err := h.wgServers.ListServers(ctx)
	if err != nil {
		slog.Default().Warn("singbox-connections: skipping wg server peer names", "error", err)
		return
	}
	for _, srv := range servers {
		for _, p := range srv.Peers {
			if p.Description == "" {
				continue
			}
			// Первая host-запись (/32|/128): у site-to-site пиров впереди
			// могут стоять маршрутизируемые подсети или 0.0.0.0/0.
			for _, entry := range p.AllowedIPs {
				if ip := connections.PeerHostIP(entry); ip != "" {
					out[ip] = p.Description
					break
				}
			}
		}
	}
}

// addManagedPeers merges managed WG-server peer names into out, keyed by
// each peer's tunnel IP. Best-effort: nil source is a no-op.
func (h *SingboxConnectionsHandler) addManagedPeers(out map[string]string) {
	if h.managed == nil {
		return
	}
	for _, srv := range h.managed.List() {
		for _, p := range srv.Peers {
			if p.Description == "" {
				continue
			}
			if ip := connections.PeerHostIP(p.TunnelIP); ip != "" {
				out[ip] = p.Description
			}
		}
	}
}
