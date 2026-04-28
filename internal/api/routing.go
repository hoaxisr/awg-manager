package api

import (
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/events"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/routing"
)

// ── Response DTOs ────────────────────────────────────────────────

// RoutingTunnelDTO mirrors frontend RoutingTunnel.
type RoutingTunnelDTO struct {
	ID        string `json:"id" example:"tun_abc123"`
	Name      string `json:"name" example:"My VPN"`
	Iface     string `json:"iface,omitempty" example:"nwg0"`
	Type      string `json:"type" example:"managed"`
	Status    string `json:"status" example:"connected"`
	Available bool   `json:"available" example:"true"`
}

// RoutingTunnelsResponse is the envelope for GET /routing/tunnels.
type RoutingTunnelsResponse struct {
	Success bool               `json:"success" example:"true"`
	Data    []RoutingTunnelDTO `json:"data"`
}

// RoutingRefreshData is the payload for POST /routing/refresh.
type RoutingRefreshData struct {
	Missing []string `json:"missing" example:""`
}

// RoutingRefreshResponse is the envelope for POST /routing/refresh.
type RoutingRefreshResponse struct {
	Success bool               `json:"success" example:"true"`
	Data    RoutingRefreshData `json:"data"`
}

// RoutingHandler handles routing API endpoints.
type RoutingHandler struct {
	catalog routing.Catalog
	queries *ndmsquery.Queries
	bus     *events.Bus
}

// NewRoutingHandler creates a new routing handler.
func NewRoutingHandler(catalog routing.Catalog, queries *ndmsquery.Queries) *RoutingHandler {
	return &RoutingHandler{catalog: catalog, queries: queries}
}

// SetEventBus wires the SSE bus so refresh can rebroadcast a fresh snapshot
// to every connected client after invalidating NDMS caches.
func (h *RoutingHandler) SetEventBus(bus *events.Bus) { h.bus = bus }

// Tunnels returns available tunnels for routing dropdowns.
// GET /api/routing/tunnels
//
//	@Summary		Routing tunnel catalog
//	@Tags			routing
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{array}	object
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/routing/tunnels [get]
func (h *RoutingHandler) Tunnels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	entries := h.catalog.ListAll(r.Context())
	response.Success(w, entries)
}

// Refresh drops every NDMS list cache that feeds the routing sections,
// then posts resource:invalidated hints so every routing polling store
// refetches on the next tick (or immediately if subscribed). Returns the
// Missing list from a freshly-built snapshot so the caller can tell
// whether the retry succeeded.
// POST /api/routing/refresh
//
//	@Summary		Invalidate routing caches
//	@Tags			routing
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	RoutingRefreshResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/routing/refresh [post]
func (h *RoutingHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	if h.queries != nil {
		if h.queries.Policies != nil {
			h.queries.Policies.InvalidateAll()
		}
		if h.queries.Hotspot != nil {
			h.queries.Hotspot.InvalidateAll()
		}
		if h.queries.Interfaces != nil {
			h.queries.Interfaces.InvalidateAll()
		}
		if h.queries.RunningConfig != nil {
			h.queries.RunningConfig.InvalidateAll()
		}
	}

	snap := h.catalog.SnapshotAll(r.Context())
	// Notify every routing polling store to refetch.
	publishInvalidated(h.bus, ResourceRoutingDnsRoutes, "refresh")
	publishInvalidated(h.bus, ResourceRoutingStaticRoutes, "refresh")
	publishInvalidated(h.bus, ResourceRoutingAccessPolicies, "refresh")
	publishInvalidated(h.bus, ResourceRoutingPolicyDevices, "refresh")
	publishInvalidated(h.bus, ResourceRoutingPolicyInterfaces, "refresh")
	publishInvalidated(h.bus, ResourceRoutingClientRoutes, "refresh")
	publishInvalidated(h.bus, ResourceRoutingTunnels, "refresh")
	response.Success(w, map[string]any{"missing": snap.Missing})
}

// ServeOS4EmptyAccessPolicies returns an empty access-policies list on OS4 (no NDMS policies API).
//
//	@Summary		OS4 access policies stub
//	@Description	Always {"data":[]}. Real data on KeeneticOS 5 only.
//	@Tags			routing
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	AccessPoliciesListResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/routing/access-policies [get]
func ServeOS4EmptyAccessPolicies(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"data":[]}`))
}

// ServeOS4EmptyPolicyInterfaces returns an empty policy-interfaces list on OS4.
//
//	@Summary		OS4 policy interfaces stub
//	@Description	Always {"data":[]}. Real data on KeeneticOS 5 only.
//	@Tags			routing
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	PolicyInterfacesListResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/routing/policy-interfaces [get]
func ServeOS4EmptyPolicyInterfaces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"data":[]}`))
}
