package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/dnsroute"
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
	SetEnabled(ctx context.Context, id string, enabled bool) error
	RefreshSubscriptions(ctx context.Context, id string) error
	RefreshAllSubscriptions(ctx context.Context) error
	GetAvailableTunnels(ctx context.Context) ([]dnsroute.TunnelInfo, error)
}

// DNSRouteHandler handles DNS route API endpoints.
type DNSRouteHandler struct {
	svc DNSRouteService
	log *logging.ScopedLogger
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
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)

	var list dnsroute.DomainList
	if err := json.NewDecoder(r.Body).Decode(&list); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "Invalid JSON", "INVALID_JSON")
		return
	}

	created, err := h.svc.Create(r.Context(), list)
	if err != nil {
		response.Error(w, err.Error(), "DNS_ROUTE_CREATE_ERROR")
		return
	}

	h.log.Info("dns-route-create", created.ID, "DNS route list created: "+created.Name)

	response.Success(w, created)
}

// Update updates an existing domain list.
func (h *DNSRouteHandler) Update(w http.ResponseWriter, r *http.Request) {
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

	var list dnsroute.DomainList
	if err := json.NewDecoder(r.Body).Decode(&list); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "Invalid JSON", "INVALID_JSON")
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
}

// Delete deletes a domain list by ID.
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

	response.Success(w, map[string]bool{"success": true})
}

// SetEnabled toggles the enabled state of a domain list.
func (h *DNSRouteHandler) SetEnabled(w http.ResponseWriter, r *http.Request) {
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
		response.Error(w, err.Error(), "DNS_ROUTE_SET_ENABLED_ERROR")
		return
	}

	action := "disabled"
	if body.Enabled {
		action = "enabled"
	}
	h.log.Info("dns-route-toggle", id, "DNS route list "+action)

	response.Success(w, map[string]bool{"success": true})
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

	response.Success(w, map[string]bool{"success": true})
}

// Tunnels returns available tunnels for DNS routing.
func (h *DNSRouteHandler) Tunnels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	tunnels, err := h.svc.GetAvailableTunnels(r.Context())
	if err != nil {
		response.Error(w, err.Error(), "DNS_ROUTE_TUNNELS_ERROR")
		return
	}

	response.Success(w, tunnels)
}
