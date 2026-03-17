package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// StaticRouteService defines what the static route handler needs from the service.
type StaticRouteService interface {
	List() ([]storage.StaticRouteList, error)
	Get(id string) (*storage.StaticRouteList, error)
	Create(ctx context.Context, rl storage.StaticRouteList) (*storage.StaticRouteList, error)
	Update(ctx context.Context, rl storage.StaticRouteList) (*storage.StaticRouteList, error)
	Delete(ctx context.Context, id string) error
	SetEnabled(ctx context.Context, id string, enabled bool) error
	Import(ctx context.Context, tunnelID, name, batContent string) (*storage.StaticRouteList, error)
}

// StaticRouteHandler handles static route API endpoints.
type StaticRouteHandler struct {
	svc    StaticRouteService
	logger AppLogger
}

// NewStaticRouteHandler creates a new static route handler.
func NewStaticRouteHandler(svc StaticRouteService) *StaticRouteHandler {
	return &StaticRouteHandler{svc: svc}
}

// SetLoggingService sets the logging service for the handler.
func (h *StaticRouteHandler) SetLoggingService(logger LoggingService) {
	h.logger = logger
}

// List returns all static route lists.
func (h *StaticRouteHandler) List(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	lists, err := h.svc.List()
	if err != nil {
		response.Error(w, err.Error(), "STATIC_ROUTE_LIST_ERROR")
		return
	}

	response.Success(w, lists)
}

// Create creates a new static route list.
func (h *StaticRouteHandler) Create(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)

	var rl storage.StaticRouteList
	if err := json.NewDecoder(r.Body).Decode(&rl); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "Invalid JSON", "INVALID_JSON")
		return
	}

	created, err := h.svc.Create(r.Context(), rl)
	if err != nil {
		response.Error(w, err.Error(), "STATIC_ROUTE_CREATE_ERROR")
		return
	}

	if h.logger != nil {
		h.logger.Log(logging.CategorySettings, "static-route", created.ID,
			"Route list created: "+created.Name)
	}

	response.Success(w, created)
}

// Update updates an existing static route list.
func (h *StaticRouteHandler) Update(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)

	var rl storage.StaticRouteList
	if err := json.NewDecoder(r.Body).Decode(&rl); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "Invalid JSON", "INVALID_JSON")
		return
	}

	updated, err := h.svc.Update(r.Context(), rl)
	if err != nil {
		response.Error(w, err.Error(), "STATIC_ROUTE_UPDATE_ERROR")
		return
	}

	if h.logger != nil {
		h.logger.Log(logging.CategorySettings, "static-route", updated.ID,
			"Route list updated: "+updated.Name)
	}

	response.Success(w, updated)
}

// Delete deletes a static route list by ID.
func (h *StaticRouteHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "Missing id parameter", "MISSING_ID")
		return
	}

	if err := h.svc.Delete(r.Context(), id); err != nil {
		response.Error(w, err.Error(), "STATIC_ROUTE_DELETE_ERROR")
		return
	}

	if h.logger != nil {
		h.logger.Log(logging.CategorySettings, "static-route", id, "Route list deleted")
	}

	response.Success(w, map[string]bool{"deleted": true})
}

// SetEnabled toggles the enabled state of a static route list.
func (h *StaticRouteHandler) SetEnabled(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "Missing id parameter", "MISSING_ID")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)

	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "Invalid JSON", "INVALID_JSON")
		return
	}

	if err := h.svc.SetEnabled(r.Context(), id, body.Enabled); err != nil {
		response.Error(w, err.Error(), "STATIC_ROUTE_SET_ENABLED_ERROR")
		return
	}

	if h.logger != nil {
		action := "disabled"
		if body.Enabled {
			action = "enabled"
		}
		h.logger.Log(logging.CategorySettings, "static-route", id,
			"Route list "+action)
	}

	response.Success(w, map[string]bool{"success": true})
}

// Import imports subnets from a .bat file content.
func (h *StaticRouteHandler) Import(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var body struct {
		TunnelID string `json:"tunnelID"`
		Name     string `json:"name"`
		Content  string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "Invalid JSON", "INVALID_JSON")
		return
	}

	created, err := h.svc.Import(r.Context(), body.TunnelID, body.Name, body.Content)
	if err != nil {
		response.Error(w, err.Error(), "STATIC_ROUTE_IMPORT_ERROR")
		return
	}

	if h.logger != nil {
		h.logger.Log(logging.CategorySettings, "static-route", created.ID,
			fmt.Sprintf("Route list imported: %s (%d subnets)", created.Name, len(created.Subnets)))
	}

	response.Success(w, created)
}
