package api

import (
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	wanpkg "github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// ── Response DTOs ────────────────────────────────────────────────

// WANStatusEnvelope is the swagger-friendly envelope for GET /wan/status.
// (The actual handler uses the local WANStatusResponse which embeds wanpkg types.)
type WANStatusEnvelope struct {
	Success bool   `json:"success" example:"true"`
	Data    struct {
		AnyWANUp bool `json:"anyWANUp" example:"true"`
	} `json:"data"`
}

// WANHandler serves WAN status queries. WAN up/down events are handled
// by HookHandler (/api/hook/ndms layer=ipv4).
type WANHandler struct {
	svc    TunnelService
	log    *logger.Logger
	appLog *logging.ScopedLogger
}

// NewWANHandler creates a new WAN status handler.
func NewWANHandler(svc TunnelService, log *logger.Logger, appLogger logging.AppLogger) *WANHandler {
	return &WANHandler{
		svc:    svc,
		log:    log,
		appLog: logging.NewScopedLogger(appLogger, logging.GroupSystem, logging.SubWan),
	}
}

// WANStatusResponse is the response format for WAN status queries.
type WANStatusResponse struct {
	Interfaces map[string]wanpkg.InterfaceStatus `json:"interfaces"`
	AnyWANUp   bool                              `json:"anyWANUp"`
}

// GetStatus returns current WAN interface state.
// GET /api/wan/status
//
//	@Summary		WAN status
//	@Tags			wan
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	WANStatusEnvelope
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/wan/status [get]
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
