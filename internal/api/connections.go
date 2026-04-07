package api

import (
	"net/http"
	"strconv"

	"github.com/hoaxisr/awg-manager/internal/connections"
	"github.com/hoaxisr/awg-manager/internal/response"
)

// ConnectionsHandler handles GET /api/connections.
type ConnectionsHandler struct {
	svc *connections.Service
}

// NewConnectionsHandler creates a new connections handler.
func NewConnectionsHandler(svc *connections.Service) *ConnectionsHandler {
	return &ConnectionsHandler{svc: svc}
}

// List returns filtered and paginated conntrack connections.
func (h *ConnectionsHandler) List(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	q := r.URL.Query()
	params := connections.ListParams{
		Tunnel:   q.Get("tunnel"),
		Protocol: q.Get("protocol"),
		Search:   q.Get("search"),
	}

	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			response.BadRequest(w, "invalid offset parameter")
			return
		}
		params.Offset = n
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			response.BadRequest(w, "invalid limit parameter")
			return
		}
		params.Limit = n
	}

	resp, err := h.svc.List(r.Context(), params)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "Conntrack not available", "CONNTRACK_UNAVAILABLE")
		return
	}

	response.Success(w, resp)
}
