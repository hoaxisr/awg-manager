package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/response"
	wanpkg "github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// WANHandler handles WAN hook events.
type WANHandler struct {
	svc    TunnelService
	orch   *orchestrator.Orchestrator
	log    *logger.Logger
	appLog *logging.ScopedLogger
}

// NewWANHandler creates a new WAN event handler.
func NewWANHandler(svc TunnelService, orch *orchestrator.Orchestrator, log *logger.Logger, appLogger logging.AppLogger) *WANHandler {
	return &WANHandler{
		svc:    svc,
		orch:   orch,
		log:    log,
		appLog: logging.NewScopedLogger(appLogger, logging.GroupSystem, logging.SubWan),
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

	// Ignore VPN tunnel interfaces — not real ISP WAN connections.
	if isVPNInterface(iface) {
		response.Success(w, map[string]string{"action": action, "interface": iface, "status": "ignored"})
		return
	}

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

	var evType orchestrator.EventType
	switch action {
	case "up":
		evType = orchestrator.EventWANUp
	case "down":
		evType = orchestrator.EventWANDown
	}

	h.orch.HandleEvent(ctx, orchestrator.Event{
		Type:     evType,
		WANIface: iface,
	})

	h.appLog.Info("wan-"+action, "", fmt.Sprintf("WAN %s: %s processed", action, iface))
}

// isVPNInterface returns true if the interface is a VPN tunnel, not a real ISP WAN.
func isVPNInterface(name string) bool {
	n := strings.ToLower(name)
	return strings.HasPrefix(n, "nwg") ||
		strings.HasPrefix(n, "opkgtun") ||
		strings.HasPrefix(n, "awg") ||
		strings.HasPrefix(n, "wg") ||
		strings.HasPrefix(n, "wireguard") ||
		strings.HasPrefix(n, "ipsec") ||
		strings.HasPrefix(n, "sstp") ||
		strings.HasPrefix(n, "openvpn")
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
