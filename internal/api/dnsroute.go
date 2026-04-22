package api

import (
	"context"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/dnsroute"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
)

// DNSRouteService defines what the DNS route handler needs from the service.
type DNSRouteService interface {
	Create(ctx context.Context, list dnsroute.DomainList) (*dnsroute.DomainList, error)
	Get(ctx context.Context, id string) (*dnsroute.DomainList, error)
	List(ctx context.Context) ([]dnsroute.DomainList, error)
	Update(ctx context.Context, list dnsroute.DomainList) (*dnsroute.DomainList, error)
	Delete(ctx context.Context, id string) error
	DeleteBatch(ctx context.Context, ids []string) (int, error)
	CreateBatch(ctx context.Context, lists []dnsroute.DomainList) ([]*dnsroute.DomainList, error)
	SetEnabled(ctx context.Context, id string, enabled bool) error
	RefreshSubscriptions(ctx context.Context, id string) error
	RefreshAllSubscriptions(ctx context.Context) error
}

// DNSRouteHandler handles DNS route API endpoints.
type DNSRouteHandler struct {
	svc DNSRouteService
	bus *events.Bus
	log *logging.ScopedLogger
}

// SetEventBus sets the event bus for SSE publishing.
func (h *DNSRouteHandler) SetEventBus(bus *events.Bus) { h.bus = bus }

// publishDnsUpdated posts a resource:invalidated hint so clients refetch
// their DNS route list. The fresh list is also returned inline from every
// mutation handler (Create/Update return the single entity for its
// lastDedupeReport; Delete/SetEnabled/DeleteBatch/BulkBackend/Refresh
// return the whole list so the caller can apply it without an extra
// round-trip). The hint remains as a safety net for tabs subscribed on
// different pages that did not issue the mutation themselves.
func (h *DNSRouteHandler) publishDnsUpdated(reason string) {
	publishInvalidated(h.bus, ResourceRoutingDnsRoutes, reason)
}

// NewDNSRouteHandler creates a new DNS route handler.
func NewDNSRouteHandler(svc DNSRouteService, appLogger logging.AppLogger) *DNSRouteHandler {
	return &DNSRouteHandler{
		svc: svc,
		log: logging.NewScopedLogger(appLogger, logging.GroupRouting, logging.SubDnsRoute),
	}
}

// List returns all domain lists.
func (h *DNSRouteHandler) List(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	lists, err := h.svc.List(r.Context())
	if err != nil {
		response.Error(w, err.Error(), "DNS_ROUTE_LIST_ERROR")
		return
	}

	response.Success(w, lists)
}

// Get returns a single domain list by ID.
func (h *DNSRouteHandler) Get(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "Missing id parameter", "MISSING_ID")
		return
	}

	list, err := h.svc.Get(r.Context(), id)
	if err != nil {
		response.Error(w, err.Error(), "DNS_ROUTE_GET_ERROR")
		return
	}

	response.Success(w, list)
}

// Create creates a new domain list.
func (h *DNSRouteHandler) Create(w http.ResponseWriter, r *http.Request) {
	list, ok := parseJSON[dnsroute.DomainList](w, r, http.MethodPost)
	if !ok {
		return
	}
	created, err := h.svc.Create(r.Context(), list)
	if err != nil {
		response.Error(w, err.Error(), "DNS_ROUTE_CREATE_ERROR")
		return
	}

	h.log.Info("dns-route-create", created.ID, "DNS route list created: "+created.Name)

	response.Success(w, created)
	h.publishDnsUpdated("create")
}

// Update updates an existing domain list.
func (h *DNSRouteHandler) Update(w http.ResponseWriter, r *http.Request) {
	list, ok := parseJSON[dnsroute.DomainList](w, r, http.MethodPost)
	if !ok {
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "Missing id parameter", "MISSING_ID")
		return
	}
	list.ID = id

	updated, err := h.svc.Update(r.Context(), list)
	if err != nil {
		response.Error(w, err.Error(), "DNS_ROUTE_UPDATE_ERROR")
		return
	}

	h.log.Info("dns-route-update", updated.ID, "DNS route list updated: "+updated.Name)

	response.Success(w, updated)
	h.publishDnsUpdated("update")
}

// Delete deletes a domain list by ID and returns the fresh list so the
// client can call applyMutationResponse without a separate refetch.
func (h *DNSRouteHandler) Delete(w http.ResponseWriter, r *http.Request) {
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
		response.Error(w, err.Error(), "DNS_ROUTE_DELETE_ERROR")
		return
	}

	h.log.Info("dns-route-delete", id, "DNS route list deleted")

	list, err := h.svc.List(r.Context())
	if err != nil {
		response.Error(w, err.Error(), "DNS_ROUTE_LIST_ERROR")
		return
	}

	response.Success(w, list)
	h.publishDnsUpdated("delete")
}

// DeleteBatch deletes multiple domain lists by IDs.
func (h *DNSRouteHandler) DeleteBatch(w http.ResponseWriter, r *http.Request) {
	body, ok := parseJSON[struct {
		IDs []string `json:"ids"`
	}](w, r, http.MethodPost)
	if !ok {
		return
	}

	if len(body.IDs) == 0 {
		response.ErrorWithStatus(w, http.StatusBadRequest, "No IDs provided", "MISSING_IDS")
		return
	}

	if _, err := h.svc.DeleteBatch(r.Context(), body.IDs); err != nil {
		response.Error(w, err.Error(), "DNS_ROUTE_DELETE_BATCH_ERROR")
		return
	}

	h.log.Info("dns-route-delete-batch", "", "DNS route lists deleted in batch")

	list, err := h.svc.List(r.Context())
	if err != nil {
		response.Error(w, err.Error(), "DNS_ROUTE_LIST_ERROR")
		return
	}

	// Mirror Create/Update: fresh list in data so the client can call
	// applyMutationResponse. Callers that need the deleted count can
	// derive it from the length delta.
	response.Success(w, list)
	h.publishDnsUpdated("delete-batch")
}

// CreateBatch creates multiple domain lists at once.
func (h *DNSRouteHandler) CreateBatch(w http.ResponseWriter, r *http.Request) {
	lists, ok := parseJSON[[]dnsroute.DomainList](w, r, http.MethodPost)
	if !ok {
		return
	}

	if len(lists) == 0 {
		response.ErrorWithStatus(w, http.StatusBadRequest, "No lists provided", "MISSING_LISTS")
		return
	}

	created, err := h.svc.CreateBatch(r.Context(), lists)
	if err != nil {
		response.Error(w, err.Error(), "DNS_ROUTE_CREATE_BATCH_ERROR")
		return
	}

	h.log.Info("dns-route-create-batch", "", "DNS route lists created in batch")

	response.Success(w, map[string]any{"created": len(created), "lists": created})
	h.publishDnsUpdated("create-batch")
}

// SetEnabled toggles the enabled state of a domain list.
func (h *DNSRouteHandler) SetEnabled(w http.ResponseWriter, r *http.Request) {
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
		response.Error(w, err.Error(), "DNS_ROUTE_SET_ENABLED_ERROR")
		return
	}

	action := "disabled"
	if body.Enabled {
		action = "enabled"
	}
	h.log.Info("dns-route-toggle", id, "DNS route list "+action)

	list, err := h.svc.List(r.Context())
	if err != nil {
		response.Error(w, err.Error(), "DNS_ROUTE_LIST_ERROR")
		return
	}

	response.Success(w, list)
	h.publishDnsUpdated("set-enabled")
}

// BulkBackend switches the routing backend for multiple lists.
func (h *DNSRouteHandler) BulkBackend(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[struct {
		ListIDs []string `json:"listIDs"`
		Backend string   `json:"backend"`
	}](w, r, http.MethodPost)
	if !ok {
		return
	}
	if req.Backend != "ndms" && req.Backend != "hydraroute" {
		response.Error(w, "Invalid backend: must be 'ndms' or 'hydraroute'", "INVALID_BACKEND")
		return
	}
	if len(req.ListIDs) == 0 {
		response.Error(w, "No list IDs provided", "EMPTY_LIST")
		return
	}

	for _, id := range req.ListIDs {
		list, err := h.svc.Get(r.Context(), id)
		if err != nil {
			continue
		}
		list.Backend = req.Backend
		if _, err := h.svc.Update(r.Context(), *list); err != nil {
			h.log.Warn("bulk-backend", id, "Failed to update backend: "+err.Error())
			continue
		}
	}

	fresh, err := h.svc.List(r.Context())
	if err != nil {
		response.Error(w, err.Error(), "DNS_ROUTE_LIST_ERROR")
		return
	}

	h.publishDnsUpdated("bulk-backend")

	// Return fresh list so clients can call applyMutationResponse.
	response.Success(w, fresh)
}

// Refresh refreshes subscriptions for a single list or all lists.
func (h *DNSRouteHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	id := r.URL.Query().Get("id")

	if id != "" {
		if err := h.svc.RefreshSubscriptions(r.Context(), id); err != nil {
			response.Error(w, err.Error(), "DNS_ROUTE_REFRESH_ERROR")
			return
		}
		h.log.Info("dns-route-refresh", id, "DNS route subscriptions refreshed")
	} else {
		if err := h.svc.RefreshAllSubscriptions(r.Context()); err != nil {
			response.Error(w, err.Error(), "DNS_ROUTE_REFRESH_ALL_ERROR")
			return
		}
		h.log.Info("dns-route-refresh-all", "", "All DNS route subscriptions refreshed")
	}

	list, err := h.svc.List(r.Context())
	if err != nil {
		response.Error(w, err.Error(), "DNS_ROUTE_LIST_ERROR")
		return
	}

	response.Success(w, list)
	h.publishDnsUpdated("refresh-subscriptions")
}

