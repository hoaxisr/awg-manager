package api

import (
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/routing"
)

// RoutingHandler handles routing API endpoints.
type RoutingHandler struct {
	catalog routing.Catalog
}

// NewRoutingHandler creates a new routing handler.
func NewRoutingHandler(catalog routing.Catalog) *RoutingHandler {
	return &RoutingHandler{catalog: catalog}
}

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
