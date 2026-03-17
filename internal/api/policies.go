package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
)

// PolicyService defines what the policy handler needs from the policy service.
type PolicyService interface {
	List() ([]storage.Policy, error)
	Get(id string) (*storage.Policy, error)
	Create(ctx context.Context, p storage.Policy) (*storage.Policy, error)
	Update(ctx context.Context, p storage.Policy) (*storage.Policy, error)
	Delete(ctx context.Context, id string) error
}

// PolicyHandler handles policy and hotspot API endpoints.
type PolicyHandler struct {
	svc    PolicyService
	ndms   ndms.Client
	logger AppLogger
}

// NewPolicyHandler creates a new policy handler.
func NewPolicyHandler(svc PolicyService, ndmsClient ndms.Client) *PolicyHandler {
	return &PolicyHandler{svc: svc, ndms: ndmsClient}
}

// SetLoggingService sets the logging service for the handler.
func (h *PolicyHandler) SetLoggingService(logger LoggingService) {
	h.logger = logger
}

// List returns all policies.
func (h *PolicyHandler) List(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	policies, err := h.svc.List()
	if err != nil {
		response.Error(w, err.Error(), "POLICY_LIST_ERROR")
		return
	}

	response.Success(w, policies)
}

// Create creates a new policy.
func (h *PolicyHandler) Create(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)

	var p storage.Policy
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "Invalid JSON", "INVALID_JSON")
		return
	}

	created, err := h.svc.Create(r.Context(), p)
	if err != nil {
		response.Error(w, err.Error(), "POLICY_CREATE_ERROR")
		return
	}

	if h.logger != nil {
		h.logger.Log(logging.CategorySettings, "policy", created.ID,
			"Policy created: "+created.Name+" ("+created.ClientIP+" \u2192 "+created.TunnelID+")")
	}

	response.Success(w, created)
}

// Update updates an existing policy.
func (h *PolicyHandler) Update(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)

	var p storage.Policy
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "Invalid JSON", "INVALID_JSON")
		return
	}

	updated, err := h.svc.Update(r.Context(), p)
	if err != nil {
		response.Error(w, err.Error(), "POLICY_UPDATE_ERROR")
		return
	}

	if h.logger != nil {
		h.logger.Log(logging.CategorySettings, "policy", updated.ID,
			"Policy updated: "+updated.Name+" ("+updated.ClientIP+" \u2192 "+updated.TunnelID+")")
	}

	response.Success(w, updated)
}

// Delete deletes a policy by ID.
func (h *PolicyHandler) Delete(w http.ResponseWriter, r *http.Request) {
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
		response.Error(w, err.Error(), "POLICY_DELETE_ERROR")
		return
	}

	if h.logger != nil {
		h.logger.Log(logging.CategorySettings, "policy", id, "Policy deleted")
	}

	response.Success(w, map[string]bool{"deleted": true})
}

// Hotspot returns LAN devices from the router's hotspot table.
func (h *PolicyHandler) Hotspot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	clients, err := h.ndms.GetHotspotClients(r.Context())
	if err != nil {
		response.Error(w, err.Error(), "HOTSPOT_ERROR")
		return
	}

	response.Success(w, clients)
}
