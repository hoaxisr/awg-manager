package api

import (
	"net/http"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// ServerListenController — управление HTTP-листенерами демона.
// *server.Server реализует его (server/listen.go); интерфейс живёт здесь,
// чтобы api не импортировал server (цикл: server → api).
type ServerListenController interface {
	// ListenState — текущий spec, фактические адреса и confirm-окно.
	ListenState() (port int, interfaces []string, boundAddrs []string, pending bool, deadline time.Time)
	// BeginListenChange живо применяет новый bind (make-before-break) и
	// взводит откат; настройки НЕ персистятся до Confirm.
	BeginListenChange(port int, interfaces []string) (token string, deadline time.Time, addrs []string, err error)
	// ConfirmListenChange гасит откат по одноразовому токену.
	ConfirmListenChange(token string) (port int, interfaces []string, ok bool)
}

// ── DTOs ─────────────────────────────────────────────────────────

// ServerListenStateData is the payload of GET /server/listen.
type ServerListenStateData struct {
	// Port the HTTP server is configured to listen on.
	Port int `json:"port" example:"2222"`
	// Interfaces are kernel interface names the server binds to; empty = all (0.0.0.0).
	Interfaces []string `json:"interfaces" example:"br0"`
	// BoundAddrs are the ip:port addresses with an active listener right now.
	BoundAddrs []string `json:"boundAddrs" example:"192.168.1.1:2222"`
	// PendingConfirm is true while an applied change awaits confirmation (see confirmDeadline).
	PendingConfirm bool `json:"pendingConfirm" example:"false"`
	// ConfirmDeadline is the RFC3339 revert moment; empty when no change is pending.
	ConfirmDeadline string `json:"confirmDeadline,omitempty" example:"2026-07-11T12:00:00+03:00"`
}

// ServerListenStateResponse is the envelope for GET /server/listen.
type ServerListenStateResponse struct {
	Success bool                  `json:"success" example:"true"`
	Data    ServerListenStateData `json:"data"`
}

// ServerListenChangeRequest is the body for POST /server/listen/change.
type ServerListenChangeRequest struct {
	// Port to listen on (1-65535).
	Port int `json:"port" example:"2222"`
	// Interfaces are kernel interface names to bind (each must have an IPv4
	// address); empty/omitted = all interfaces (0.0.0.0). A loopback listener
	// on 127.0.0.1 is always kept regardless of this list.
	Interfaces []string `json:"interfaces,omitempty" example:"br0"`
}

// ServerListenChangeData is the payload of POST /server/listen/change.
type ServerListenChangeData struct {
	// ConfirmToken must be presented to /server/listen/confirm before
	// confirmDeadline, otherwise the change is reverted. The token also
	// authenticates the confirm call by itself — the session cookie does not
	// survive a host (interface) change.
	ConfirmToken string `json:"confirmToken" example:"9f3c..."`
	// ConfirmDeadline is the RFC3339 revert moment.
	ConfirmDeadline string `json:"confirmDeadline" example:"2026-07-11T12:02:00+03:00"`
	// BoundAddrs are the ip:port addresses now listening (new bind applied).
	BoundAddrs []string `json:"boundAddrs" example:"192.168.1.1:8080"`
}

// ServerListenChangeResponse is the envelope for POST /server/listen/change.
type ServerListenChangeResponse struct {
	Success bool                   `json:"success" example:"true"`
	Data    ServerListenChangeData `json:"data"`
}

// ServerListenConfirmRequest is the body for POST /server/listen/confirm.
type ServerListenConfirmRequest struct {
	Token string `json:"token" example:"9f3c..."`
}

// ServerListenHandler exposes live HTTP-listen management.
type ServerListenHandler struct {
	ctrl     ServerListenController
	settings *storage.SettingsStore
	log      *logging.ScopedLogger
}

// NewServerListenHandler constructs the handler.
func NewServerListenHandler(ctrl ServerListenController, settings *storage.SettingsStore, appLogger logging.AppLogger) *ServerListenHandler {
	return &ServerListenHandler{
		ctrl:     ctrl,
		settings: settings,
		log:      logging.NewScopedLogger(appLogger, logging.GroupSystem, "server-listen"),
	}
}

// State returns the current listen configuration and bound addresses.
//
//	@Summary		Get HTTP listen state
//	@Description	Returns the configured port/interfaces, the ip:port addresses with an active listener, and whether an applied change is still awaiting confirmation.
//	@Tags			server
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	ServerListenStateResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Router			/server/listen [get]
func (h *ServerListenHandler) State(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	port, ifaces, addrs, pending, deadline := h.ctrl.ListenState()
	data := ServerListenStateData{
		Port:           port,
		Interfaces:     emptyIfNil(ifaces),
		BoundAddrs:     emptyIfNil(addrs),
		PendingConfirm: pending,
	}
	if pending {
		data.ConfirmDeadline = deadline.Format(time.RFC3339)
	}
	response.Success(w, data)
}

// Change applies a new port/interface bind live (make-before-break) and arms
// a revert unless confirmed in time.
//
//	@Summary		Change HTTP listen address (live, confirm-or-revert)
//	@Description	Applies the new port/interfaces immediately without restarting the daemon: new listeners are bound first, old ones closed after (the server is never left without a listener). The change is NOT persisted yet — call /server/listen/confirm with the returned token before confirmDeadline, otherwise listeners revert to the previous address. A 127.0.0.1 listener is always kept (NDMS reverse proxy, health probes, rescue hatch).
//	@Tags			server
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		ServerListenChangeRequest	true	"New port and interface list (empty list = all interfaces)"
//	@Success		200		{object}	ServerListenChangeResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		405		{object}	APIErrorEnvelope
//	@Router			/server/listen/change [post]
func (h *ServerListenHandler) Change(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req ServerListenChangeRequest
	if err := decodeBody(r, &req); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if req.Port < 1 || req.Port > 65535 {
		response.Error(w, "порт должен быть в диапазоне 1–65535", "INVALID_PORT")
		return
	}
	for _, iface := range req.Interfaces {
		if iface == "" {
			response.Error(w, "пустое имя интерфейса", "INVALID_INTERFACE")
			return
		}
	}
	token, deadline, addrs, err := h.ctrl.BeginListenChange(req.Port, req.Interfaces)
	if err != nil {
		h.log.Warn("change", "", err.Error())
		response.Error(w, err.Error(), "LISTEN_CHANGE_FAILED")
		return
	}
	h.log.Warn("change", "", "HTTP-адрес изменён, ожидаю подтверждения (откат "+deadline.Format(time.RFC3339)+")")
	response.Success(w, ServerListenChangeData{
		ConfirmToken:    token,
		ConfirmDeadline: deadline.Format(time.RFC3339),
		BoundAddrs:      emptyIfNil(addrs),
	})
}

// Confirm finalizes a pending listen change and persists it to settings.
//
// NB: registered WITHOUT the session guard — the one-time 256-bit token from
// /server/listen/change is the credential. The session cookie is scoped to
// the host and does not survive an interface change, so the confirm request
// arriving from the NEW origin may have no session yet.
//
//	@Summary		Confirm a pending HTTP listen change
//	@Description	Cancels the scheduled revert and persists the new port/interfaces to settings. Authenticated by the one-time token returned from /server/listen/change (a session is not required — it does not survive a host change).
//	@Tags			server
//	@Accept			json
//	@Produce		json
//	@Param			body	body		ServerListenConfirmRequest	true	"One-time confirm token"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		405		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/server/listen/confirm [post]
func (h *ServerListenHandler) Confirm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req ServerListenConfirmRequest
	if err := decodeBody(r, &req); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	port, ifaces, ok := h.ctrl.ConfirmListenChange(req.Token)
	if !ok {
		response.Error(w, "токен подтверждения не действителен (окно истекло или уже подтверждено)", "CONFIRM_TOKEN_INVALID")
		return
	}
	if err := h.settings.SetServerListen(port, ifaces); err != nil {
		response.InternalError(w, "настройки применены к листенерам, но не сохранены: "+err.Error())
		return
	}
	h.log.Info("confirm", "", "новый HTTP-адрес подтверждён и сохранён")
	response.Success(w, map[string]bool{"ok": true})
}

func emptyIfNil(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}
