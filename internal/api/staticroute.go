package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/events"
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
	svc StaticRouteService
	bus *events.Bus
	log *logging.ScopedLogger
}

// SetEventBus sets the event bus for SSE publishing.
func (h *StaticRouteHandler) SetEventBus(bus *events.Bus) { h.bus = bus }

// publishStaticUpdated publishes the full static route list via SSE (best-effort).
func (h *StaticRouteHandler) publishStaticUpdated() {
	if h.bus == nil {
		return
	}
	list, err := h.svc.List()
	if err != nil {
		return
	}
	h.bus.Publish("routing:static-updated", list)
}

// NewStaticRouteHandler creates a new static route handler.
func NewStaticRouteHandler(svc StaticRouteService, appLogger logging.AppLogger) *StaticRouteHandler {
	return &StaticRouteHandler{
		svc: svc,
		log: logging.NewScopedLogger(appLogger, logging.GroupRouting, logging.SubStaticRoute),
	}
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
	rl, ok := parseJSON[storage.StaticRouteList](w, r, http.MethodPost)
	if !ok {
		return
	}

	created, err := h.svc.Create(r.Context(), rl)
	if err != nil {
		response.Error(w, err.Error(), "STATIC_ROUTE_CREATE_ERROR")
		return
	}

	h.log.Info("static-route", created.ID, "Route list created: "+created.Name)

	response.Success(w, created)
	h.publishStaticUpdated()
}

// Update updates an existing static route list.
func (h *StaticRouteHandler) Update(w http.ResponseWriter, r *http.Request) {
	rl, ok := parseJSON[storage.StaticRouteList](w, r, http.MethodPost)
	if !ok {
		return
	}

	updated, err := h.svc.Update(r.Context(), rl)
	if err != nil {
		response.Error(w, err.Error(), "STATIC_ROUTE_UPDATE_ERROR")
		return
	}

	h.log.Info("static-route", updated.ID, "Route list updated: "+updated.Name)

	response.Success(w, updated)
	h.publishStaticUpdated()
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

	h.log.Info("static-route", id, "Route list deleted")

	response.Success(w, map[string]bool{"deleted": true})
	h.publishStaticUpdated()
}

// SetEnabled toggles the enabled state of a static route list.
func (h *StaticRouteHandler) SetEnabled(w http.ResponseWriter, r *http.Request) {
	body, ok := parseJSON[enabledToggle](w, r, http.MethodPost)
	if !ok {
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "Missing id parameter", "MISSING_ID")
		return
	}

	if err := h.svc.SetEnabled(r.Context(), id, body.Enabled); err != nil {
		response.Error(w, err.Error(), "STATIC_ROUTE_SET_ENABLED_ERROR")
		return
	}

	action := "disabled"
	if body.Enabled {
		action = "enabled"
	}
	h.log.Info("static-route", id, "Route list "+action)

	response.Success(w, map[string]bool{"success": true})
	h.publishStaticUpdated()
}

// staticRouteImportReq is the shape of /api/static-route/import body.
type staticRouteImportReq struct {
	TunnelID string `json:"tunnelID"`
	Name     string `json:"name"`
	Content  string `json:"content"`
}

// Import imports subnets from a .bat file content.
func (h *StaticRouteHandler) Import(w http.ResponseWriter, r *http.Request) {
	body, ok := parseJSON[staticRouteImportReq](w, r, http.MethodPost)
	if !ok {
		return
	}

	created, err := h.svc.Import(r.Context(), body.TunnelID, body.Name, body.Content)
	if err != nil {
		response.Error(w, err.Error(), "STATIC_ROUTE_IMPORT_ERROR")
		return
	}

	h.log.Info("static-route", created.ID,
		fmt.Sprintf("Route list imported: %s (%d subnets)", created.Name, len(created.Subnets)))

	response.Success(w, created)
	h.publishStaticUpdated()
}
