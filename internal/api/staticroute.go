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

// publishStaticUpdated posts a resource:invalidated hint so clients refetch
// their static route list. Mutations also return the fresh list inline so
// the caller's store can applyMutationResponse without waiting for the
// hint.
func (h *StaticRouteHandler) publishStaticUpdated(reason string) {
	publishInvalidated(h.bus, ResourceRoutingStaticRoutes, reason)
}

// NewStaticRouteHandler creates a new static route handler.
func NewStaticRouteHandler(svc StaticRouteService, appLogger logging.AppLogger) *StaticRouteHandler {
	return &StaticRouteHandler{
		svc: svc,
		log: logging.NewScopedLogger(appLogger, logging.GroupRouting, logging.SubStaticRoute),
	}
}

// List returns static IP routes.
//
//	@Summary		List static routes
//	@Tags			static-routes
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{array}	map[string]interface{}
//	@Router			/static-routes/list [get]
//	@Router			/routing/static-routes [get]
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
//
//	@Summary		Create static route
//	@Tags			static-routes
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	map[string]interface{}
//	@Router			/static-routes/create [post]
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
	h.publishStaticUpdated("create")
}

// Update updates an existing static route list.
//
//	@Summary		Update static route
//	@Tags			static-routes
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	map[string]interface{}
//	@Router			/static-routes/update [post]
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
	h.publishStaticUpdated("update")
}

// Delete deletes a static route list by ID.
//
//	@Summary		Delete static route
//	@Tags			static-routes
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id	query	string	true	"List id"
//	@Success		200	{object}	map[string]interface{}
//	@Router			/static-routes/delete [post]
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
	h.publishStaticUpdated("delete")
}

// SetEnabled toggles the enabled state of a static route list.
//
//	@Summary		Set static route enabled
//	@Tags			static-routes
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id	query	string	true	"List id"
//	@Success		200	{object}	map[string]interface{}
//	@Router			/static-routes/set-enabled [post]
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
	h.publishStaticUpdated("set-enabled")
}

// staticRouteImportReq is the shape of /api/static-route/import body.
type staticRouteImportReq struct {
	TunnelID string `json:"tunnelID"`
	Name     string `json:"name"`
	Content  string `json:"content"`
}

// Import imports subnets from a .bat file content.
//
//	@Summary		Import static routes
//	@Tags			static-routes
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	map[string]interface{}
//	@Router			/static-routes/import [post]
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
	h.publishStaticUpdated("import")
}
