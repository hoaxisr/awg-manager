package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/clientroute"
	"github.com/hoaxisr/awg-manager/internal/response"
)

// ClientRouteHandler handles client route CRUD operations.
type ClientRouteHandler struct {
	svc clientroute.Service
}

// NewClientRouteHandler creates a new client route handler.
func NewClientRouteHandler(svc clientroute.Service) *ClientRouteHandler {
	return &ClientRouteHandler{svc: svc}
}

// HandleList returns all client routes.
// GET /api/client-routes
func (h *ClientRouteHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	routes, err := h.svc.List()
	if err != nil {
		response.Error(w, err.Error(), "LIST_FAILED")
		return
	}
	response.Success(w, response.MustNotNil(routes))
}

// HandleCreate creates a new client route.
// POST /api/client-routes/create
// Body: ClientRoute JSON
func (h *ClientRouteHandler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		response.Error(w, "invalid request body", "INVALID_BODY")
		return
	}
	var route clientroute.ClientRoute
	if err := json.Unmarshal(body, &route); err != nil {
		response.Error(w, "invalid JSON", "INVALID_JSON")
		return
	}
	created, err := h.svc.Create(r.Context(), route)
	if err != nil {
		response.Error(w, err.Error(), "CREATE_FAILED")
		return
	}
	response.Success(w, created)
}

// HandleUpdate updates an existing client route.
// POST /api/client-routes/update?id=xxx
// Body: ClientRoute JSON
func (h *ClientRouteHandler) HandleUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		response.Error(w, "missing id parameter", "MISSING_ID")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		response.Error(w, "invalid request body", "INVALID_BODY")
		return
	}
	var route clientroute.ClientRoute
	if err := json.Unmarshal(body, &route); err != nil {
		response.Error(w, "invalid JSON", "INVALID_JSON")
		return
	}
	route.ID = id
	updated, err := h.svc.Update(r.Context(), route)
	if err != nil {
		response.Error(w, err.Error(), "UPDATE_FAILED")
		return
	}
	response.Success(w, updated)
}

// HandleDelete deletes a client route.
// POST /api/client-routes/delete?id=xxx
func (h *ClientRouteHandler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		response.Error(w, "missing id parameter", "MISSING_ID")
		return
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		response.Error(w, err.Error(), "DELETE_FAILED")
		return
	}
	response.Success(w, map[string]bool{"ok": true})
}

// HandleToggle enables or disables a client route.
// POST /api/client-routes/toggle?id=xxx
// Body: {"enabled": bool}
func (h *ClientRouteHandler) HandleToggle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		response.Error(w, "missing id parameter", "MISSING_ID")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		response.Error(w, "invalid request body", "INVALID_BODY")
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		response.Error(w, "invalid JSON", "INVALID_JSON")
		return
	}
	if err := h.svc.SetEnabled(r.Context(), id, req.Enabled); err != nil {
		response.Error(w, err.Error(), "TOGGLE_FAILED")
		return
	}
	response.Success(w, map[string]interface{}{
		"id":      id,
		"enabled": req.Enabled,
	})
}
