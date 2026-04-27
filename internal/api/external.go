package api

import (
	"context"
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
	svc                ExternalTunnelService
	tunnelSvc          TunnelService
	store              *storage.AWGTunnelStore
	log                *logging.ScopedLogger
	publishTunnelList  func(ctx context.Context)
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

// SetTunnelListPublisher sets the function that publishes the full tunnel list via SSE.
func (h *ExternalTunnelsHandler) SetTunnelListPublisher(fn func(ctx context.Context)) {
	h.publishTunnelList = fn
}

// listExternal builds the external tunnel list for API response and SSE snapshots.
func (h *ExternalTunnelsHandler) listExternal(ctx context.Context) ([]external.TunnelInfo, error) {
	tunnels, err := h.svc.List(ctx)
	if err != nil {
		return nil, err
	}
	if tunnels == nil {
		tunnels = []external.TunnelInfo{}
	}
	return tunnels, nil
}

// List returns all external (unmanaged) tunnels.
// Endpoint: GET /api/external-tunnels
//
//	@Summary		List external tunnels
//	@Tags			tunnels
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{array}	object
//	@Router			/external-tunnels [get]
func (h *ExternalTunnelsHandler) List(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	tunnels, err := h.listExternal(r.Context())
	if err != nil {
		response.Error(w, err.Error(), "LIST_FAILED")
		return
	}

	response.Success(w, tunnels)
}

// AdoptRequest is the request body for adopting an external tunnel.
type AdoptRequest struct {
	Content string `json:"content"`
	Name    string `json:"name"`
}

// Adopt takes control of an external tunnel.
// Endpoint: POST /api/external-tunnels/adopt?interface=opkgtunX
//
//	@Summary		Adopt external tunnel
//	@Tags			tunnels
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			interface	query		string			true	"NDMS interface name"
//	@Param			body		body		AdoptRequest	true	"Tunnel config body"
//	@Success		200	{object}	map[string]interface{}
//	@Router			/external-tunnels/adopt [post]
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
	req, ok := parseJSON[AdoptRequest](w, r, http.MethodPost)
	if !ok {
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

	if h.publishTunnelList != nil {
		h.publishTunnelList(r.Context())
	}

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
