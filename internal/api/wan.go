package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	wanpkg "github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// WANHandler handles WAN hook events.
type WANHandler struct {
	svc    TunnelService
	log    *logger.Logger
	logger AppLogger
}

// SetLoggingService sets the logging service for the handler.
func (h *WANHandler) SetLoggingService(logger LoggingService) {
	h.logger = logger
}

// NewWANHandler creates a new WAN event handler.
func NewWANHandler(svc TunnelService, log *logger.Logger) *WANHandler {
	return &WANHandler{
		svc: svc,
		log: log,
	}
}

// HandleEvent processes WAN up/down events.
// Returns 200 immediately; processes event in background goroutine.
func (h *WANHandler) HandleEvent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	action := r.URL.Query().Get("action")
	if action != "up" && action != "down" {
		response.BadRequest(w, "action must be 'up' or 'down'")
		return
	}

	iface := r.URL.Query().Get("interface")

	// Update WAN model synchronously (fast, in-memory)
	switch action {
	case "up":
		h.svc.WANModel().SetUp(iface, true)
	case "down":
		h.svc.WANModel().SetUp(iface, false)
	}

	// Return 200 immediately — curl gets response before processing starts
	response.Success(w, map[string]string{"action": action, "interface": iface, "status": "accepted"})

	// Process in background (decoupled from HTTP context)
	go h.processEvent(action, iface)
}

// processEvent handles WAN up/down in a background goroutine.
func (h *WANHandler) processEvent(action, iface string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	switch action {
	case "up":
		h.svc.HandleWANUp(ctx, iface)
	case "down":
		h.svc.HandleWANDown(ctx, iface)
	}

	if h.logger != nil {
		h.logger.Log(logging.CategorySystem, "wan-"+action, "",
			fmt.Sprintf("WAN %s: %s processed", action, iface))
	}
}

// WANStatusResponse is the response format for WAN status queries.
type WANStatusResponse struct {
	Interfaces map[string]wanpkg.InterfaceStatus `json:"interfaces"`
	AnyWANUp   bool                              `json:"anyWANUp"`
}

// GetStatus returns current WAN interface state.
// GET /api/wan/status
func (h *WANHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	model := h.svc.WANModel()
	resp := WANStatusResponse{
		Interfaces: model.Status(),
		AnyWANUp:   model.AnyUp(),
	}
	response.Success(w, resp)
}
