package api

import (
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/events"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/routing"
)

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
