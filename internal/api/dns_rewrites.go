package api

import (
	"net/http"
	"strconv"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/singbox/dnsrewrite"
)

// ── DTOs (swagger) ───────────────────────────────────────────────

// SingboxDNSRewriteDTO mirrors dnsrewrite.DNSRewrite — a glob domain
// pattern rewritten to one or more IPs.
type SingboxDNSRewriteDTO struct {
	Pattern string   `json:"pattern" example:"finland10*.discord.media"`
	IPs     []string `json:"ips" example:"104.25.158.178"`
	Managed string   `json:"managed,omitempty" example:"keendns"`
}

// SingboxDNSRewritesListResponse is the envelope for
// GET /singbox/router/dns/rewrites/list.
type SingboxDNSRewritesListResponse struct {
	Success bool                   `json:"success" example:"true"`
	Data    []SingboxDNSRewriteDTO `json:"data"`
}

// SingboxDNSRewriteUpdateRequest is the body for
// POST /singbox/router/dns/rewrites/update.
type SingboxDNSRewriteUpdateRequest struct {
	Index   int                  `json:"index" example:"0"`
	Rewrite SingboxDNSRewriteDTO `json:"rewrite"`
}

// SingboxDNSRewriteDeleteRequest is the body for
// POST /singbox/router/dns/rewrites/delete.
type SingboxDNSRewriteDeleteRequest struct {
	Index int `json:"index" example:"0"`
}

// SingboxDNSRewriteMoveRequest is the body for
// POST /singbox/router/dns/rewrites/move.
type SingboxDNSRewriteMoveRequest struct {
	From int `json:"from" example:"3"`
	To   int `json:"to" example:"0"`
}

type DNSRewritesService interface {
	List() ([]dnsrewrite.DNSRewrite, error)
	Add(dnsrewrite.DNSRewrite) error
	Update(int, dnsrewrite.DNSRewrite) error
	Delete(int) error
	Move(from, to int) error
}

type DNSRewritesHandler struct {
	svc DNSRewritesService
	log *logging.ScopedLogger
}

// NewDNSRewritesHandler constructs the handler.
// appLogger may be nil — the scoped logger is nil-safe.
func NewDNSRewritesHandler(svc DNSRewritesService, appLogger logging.AppLogger) *DNSRewritesHandler {
	return &DNSRewritesHandler{
		svc: svc,
		log: logging.NewScopedLogger(appLogger, logging.GroupRouting, logging.SubSingboxRouter),
	}
}

// List returns all DNS rewrites in priority order.
//
//	@Summary		List singbox-router DNS rewrites
//	@Description	Returns all DNS rewrites (glob pattern → IPs) in priority order. Always a JSON array, never null.
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	SingboxDNSRewritesListResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/router/dns/rewrites/list [get]
func (h *DNSRewritesHandler) List(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	items, err := h.svc.List()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	if items == nil {
		items = []dnsrewrite.DNSRewrite{}
	}
	response.Success(w, items)
}

// Add registers a new DNS rewrite.
//
//	@Summary		Add singbox-router DNS rewrite
//	@Description	Appends a new DNS rewrite (glob pattern → IPs). The pattern must be unique.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxDNSRewriteDTO	true	"DNS rewrite descriptor"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/router/dns/rewrites/add [post]
func (h *DNSRewritesHandler) Add(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var rw dnsrewrite.DNSRewrite
	if err := decodeBody(r, &rw); err != nil {
		h.log.Warn("dns-rewrite-add", "", err.Error())
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.Add(rw); err != nil {
		h.log.Warn("dns-rewrite-add", "", err.Error())
		response.BadRequest(w, err.Error())
		return
	}
	h.log.Info("dns-rewrite-add", rw.Pattern, "DNS rewrite added: "+rw.Pattern)
	response.Success(w, map[string]bool{"ok": true})
}

// Update replaces the DNS rewrite at the given index.
//
//	@Summary		Update singbox-router DNS rewrite
//	@Description	Replaces the DNS rewrite at the given index (0-based priority slot) with the provided one.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxDNSRewriteUpdateRequest	true	"Index + replacement rewrite"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/router/dns/rewrites/update [post]
func (h *DNSRewritesHandler) Update(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req struct {
		Index   int                   `json:"index"`
		Rewrite dnsrewrite.DNSRewrite `json:"rewrite"`
	}
	if err := decodeBody(r, &req); err != nil {
		h.log.Warn("dns-rewrite-update", "", err.Error())
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.Update(req.Index, req.Rewrite); err != nil {
		h.log.Warn("dns-rewrite-update", "", err.Error())
		response.BadRequest(w, err.Error())
		return
	}
	h.log.Info("dns-rewrite-update", req.Rewrite.Pattern, "DNS rewrite updated: "+req.Rewrite.Pattern)
	response.Success(w, map[string]bool{"ok": true})
}

// Delete removes the DNS rewrite at the given index.
//
//	@Summary		Delete singbox-router DNS rewrite
//	@Description	Removes the DNS rewrite at the given index (0-based priority slot).
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxDNSRewriteDeleteRequest	true	"Index of the rewrite to remove"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/router/dns/rewrites/delete [post]
func (h *DNSRewritesHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req struct {
		Index int `json:"index"`
	}
	if err := decodeBody(r, &req); err != nil {
		h.log.Warn("dns-rewrite-delete", "", err.Error())
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.Delete(req.Index); err != nil {
		h.log.Warn("dns-rewrite-delete", "", err.Error())
		response.BadRequest(w, err.Error())
		return
	}
	h.log.Info("dns-rewrite-delete", strconv.Itoa(req.Index), "DNS rewrite deleted at index "+strconv.Itoa(req.Index))
	response.Success(w, map[string]bool{"ok": true})
}

// Move reorders a DNS rewrite from one priority slot to another.
//
//	@Summary		Move singbox-router DNS rewrite
//	@Description	Moves the DNS rewrite from index `from` to index `to` (both 0-based).
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxDNSRewriteMoveRequest	true	"from/to indices"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/router/dns/rewrites/move [post]
func (h *DNSRewritesHandler) Move(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req struct {
		From int `json:"from"`
		To   int `json:"to"`
	}
	if err := decodeBody(r, &req); err != nil {
		h.log.Warn("dns-rewrite-move", "", err.Error())
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.Move(req.From, req.To); err != nil {
		h.log.Warn("dns-rewrite-move", "", err.Error())
		response.BadRequest(w, err.Error())
		return
	}
	h.log.Info("dns-rewrite-move", strconv.Itoa(req.From),
		"DNS rewrite moved from index "+strconv.Itoa(req.From)+" to "+strconv.Itoa(req.To))
	response.Success(w, map[string]bool{"ok": true})
}
