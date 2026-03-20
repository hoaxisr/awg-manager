package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/external"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
)

// ExternalTunnelService defines the interface for external tunnel operations.
type ExternalTunnelService interface {
	List(ctx context.Context) ([]external.TunnelInfo, error)
	Adopt(ctx context.Context, req external.AdoptRequest) (*service.TunnelWithStatus, error)
}

// ExternalTunnelsHandler handles external tunnel operations.
type ExternalTunnelsHandler struct {
	svc        ExternalTunnelService
	tunnelSvc  TunnelService
	store      *storage.AWGTunnelStore
	log        *logging.ScopedLogger
}

// NewExternalTunnelsHandler creates a new external tunnels handler.
func NewExternalTunnelsHandler(svc ExternalTunnelService, tunnelSvc TunnelService, store *storage.AWGTunnelStore, appLogger logging.AppLogger) *ExternalTunnelsHandler {
	return &ExternalTunnelsHandler{
		svc:       svc,
		tunnelSvc: tunnelSvc,
		store:     store,
		log:       logging.NewScopedLogger(appLogger, logging.GroupTunnel, logging.SubLifecycle),
	}
}

// List returns all external (unmanaged) tunnels.
// Endpoint: GET /api/external-tunnels
func (h *ExternalTunnelsHandler) List(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	tunnels, err := h.svc.List(r.Context())
	if err != nil {
		response.Error(w, err.Error(), "LIST_FAILED")
		return
	}

	response.Success(w, response.MustNotNil(tunnels))
}

// adoptRequest represents the request body for adopting an external tunnel.
type adoptRequest struct {
	Content string `json:"content"`
	Name    string `json:"name"`
}

// Adopt takes control of an external tunnel.
// Endpoint: POST /api/external-tunnels/adopt?interface=opkgtunX
func (h *ExternalTunnelsHandler) Adopt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	interfaceName := r.URL.Query().Get("interface")
	if interfaceName == "" {
		response.Error(w, "missing interface parameter", "MISSING_INTERFACE")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	var req adoptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid request body", "INVALID_BODY")
		return
	}

	if req.Content == "" {
		response.Error(w, "config content is required", "MISSING_CONTENT")
		return
	}

	result, err := h.svc.Adopt(r.Context(), external.AdoptRequest{
		InterfaceName: interfaceName,
		ConfContent:   req.Content,
		TunnelName:    req.Name,
	})
	if err != nil {
		h.log.Warn("adopt", interfaceName, "Failed to adopt external tunnel: "+err.Error())
		response.Error(w, err.Error(), "ADOPT_FAILED")
		return
	}

	h.log.Info("adopt", result.Name, "External tunnel adopted")

	resp, err := BuildTunnelResponse(r, h.tunnelSvc, h.store, result.ID)
	if err != nil {
		response.Error(w, err.Error(), "ADOPT_FAILED")
		return
	}
	if warnings := h.tunnelSvc.CheckAddressConflicts(r.Context(), result.ID); len(warnings) > 0 {
		resp["warnings"] = warnings
	}
	response.Success(w, resp)
}
