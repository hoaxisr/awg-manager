package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
)

// SingboxFakeIPConfigHandler exposes the FakeIPConfigService CRUD surface
// (SlotFakeIP) as REST endpoints under /api/singbox/fakeip/config/...
// It is a pure mechanical mirror of SingboxRouterHandler: same DTO shapes,
// same error mapping, same nil-array guards, different path prefix and
// FakeIP-prefixed service calls.
type SingboxFakeIPConfigHandler struct {
	svc router.FakeIPConfigService
	log *logging.ScopedLogger
}

// NewSingboxFakeIPConfigHandler constructs a handler backed by svc.
// appLogger may be nil — the scoped logger is nil-safe.
func NewSingboxFakeIPConfigHandler(svc router.FakeIPConfigService, appLogger logging.AppLogger) *SingboxFakeIPConfigHandler {
	return &SingboxFakeIPConfigHandler{
		svc: svc,
		log: logging.NewScopedLogger(appLogger, logging.GroupRouting, logging.SubSingboxRouter),
	}
}

// handleErr maps domain sentinel errors to appropriate HTTP statuses.
// ErrFakeIPLockedField → 400 (clear client error, field is engine-managed).
// Reference/conflict errors → 400 CONFLICT (mirror router convention).
// Not-found errors → 400 NOT_FOUND.
// Everything else → 500.
func (h *SingboxFakeIPConfigHandler) handleErr(w http.ResponseWriter, action string, err error) {
	h.log.Warn(action, "", err.Error())
	switch {
	case errors.Is(err, router.ErrFakeIPLockedField):
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "FAKEIP_LOCKED_FIELD")
	case errors.Is(err, router.ErrFakeIPRealServerInvalid):
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "FAKEIP_REAL_SERVER_INVALID")
	case errors.Is(err, router.ErrRuleSetReferenced),
		errors.Is(err, router.ErrOutboundReferenced),
		errors.Is(err, router.ErrRuleSetTagConflict),
		errors.Is(err, router.ErrOutboundTagConflict),
		errors.Is(err, router.ErrDNSServerTagConflict),
		errors.Is(err, router.ErrDNSServerReferenced):
		response.Error(w, err.Error(), "CONFLICT")
	case errors.Is(err, router.ErrRuleIndexOutOfRange),
		errors.Is(err, router.ErrDNSRuleIndexOutOfRange),
		errors.Is(err, router.ErrDNSServerNotFound),
		errors.Is(err, router.ErrRuleSetNotFound),
		errors.Is(err, router.ErrOutboundNotFound):
		response.Error(w, err.Error(), "NOT_FOUND")
	case errors.Is(err, router.ErrInvalidMatchers),
		errors.Is(err, router.ErrDNSInvalidServer):
		response.Error(w, err.Error(), "INVALID_MATCHERS")
	case errors.Is(err, router.ErrBulkEmptyIndices),
		errors.Is(err, router.ErrBulkEmptyTags):
		// 400: empty selection for a bulk rule/ruleset mutation — nothing to do.
		response.Error(w, err.Error(), "BULK_EMPTY_SELECTION")
	case errors.Is(err, router.ErrCompositeMemberUnknown):
		// 400: member-тег композита не существует ни в одном каталоге (#567).
		response.Error(w, err.Error(), "COMPOSITE_MEMBER_UNKNOWN")
	case errors.Is(err, router.ErrBulkInvalidSelection):
		// 400: non-empty but invalid bulk selection (duplicate index/tag,
		// non-route rule, unknown outbound tag, non-remote rule set).
		response.Error(w, err.Error(), "BULK_INVALID_SELECTION")
	default:
		response.InternalError(w, err.Error())
	}
}

// ── DNS servers ───────────────────────────────────────────────────

// ListDNSServers returns all configured DNS servers in the fakeip-tun slot.
//
//	@Summary		List fakeip-config DNS servers
//	@Description	Returns all configured DNS upstreams for the fakeip-tun slot. Always a JSON array, never null.
//	@Tags			singbox-fakeip
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	SingboxDNSServersListResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/fakeip/config/dns/servers/list [get]
func (h *SingboxFakeIPConfigHandler) ListDNSServers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	servers, err := h.svc.FakeIPListDNSServers(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	if servers == nil {
		servers = []router.DNSServer{}
	}
	response.Success(w, servers)
}

// AddDNSServer registers a new DNS upstream in the fakeip-tun slot.
//
//	@Summary		Add fakeip-config DNS server
//	@Description	Registers a new DNS upstream (tag must be unique) in the fakeip-tun slot.
//	@Tags			singbox-fakeip
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxDNSServerDTO	true	"DNS server descriptor"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/fakeip/config/dns/servers/add [post]
func (h *SingboxFakeIPConfigHandler) AddDNSServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var s router.DNSServer
	if err := decodeBody(r, &s); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.FakeIPAddDNSServer(r.Context(), s); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("fakeip-dns-server-add", s.Tag, "fakeip DNS server added: "+s.Tag)
	response.Success(w, map[string]bool{"ok": true})
}

// UpdateDNSServer replaces the DNS upstream identified by tag in the fakeip-tun slot.
//
//	@Summary		Update fakeip-config DNS server
//	@Description	Replaces the DNS upstream identified by tag with the provided one (fakeip-tun slot).
//	@Tags			singbox-fakeip
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxDNSServerUpdateRequest	true	"Tag + replacement server"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/fakeip/config/dns/servers/update [post]
func (h *SingboxFakeIPConfigHandler) UpdateDNSServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var body struct {
		Tag    string           `json:"tag"`
		Server router.DNSServer `json:"server"`
	}
	if err := decodeBody(r, &body); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.FakeIPUpdateDNSServer(r.Context(), body.Tag, body.Server); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("fakeip-dns-server-update", body.Tag, "fakeip DNS server updated: "+body.Tag)
	response.Success(w, map[string]bool{"ok": true})
}

// DeleteDNSServer removes the DNS upstream identified by tag from the fakeip-tun slot.
//
//	@Summary		Delete fakeip-config DNS server
//	@Description	Removes the DNS upstream identified by tag. Engine-locked servers ("real", fakeip-type) are rejected with 400. Pass force=true to bypass the reference guard.
//	@Tags			singbox-fakeip
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxDNSServerDeleteRequest	true	"Tag + optional force flag"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		409		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/fakeip/config/dns/servers/delete [post]
func (h *SingboxFakeIPConfigHandler) DeleteDNSServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var body SingboxDNSServerDeleteRequest
	if err := decodeBody(r, &body); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.FakeIPDeleteDNSServer(r.Context(), body.Tag, body.Force); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("fakeip-dns-server-delete", body.Tag, "fakeip DNS server deleted: "+body.Tag)
	response.Success(w, map[string]bool{"ok": true})
}

// MoveDNSServer reorders a DNS server from one slot to another.
//
//	@Summary		Move fakeip-config DNS server
//	@Description	Moves the DNS server from index `from` to index `to` (both 0-based) in the fakeip-tun slot.
//	@Tags			singbox-fakeip
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxDNSServerMoveRequest	true	"From-index and to-index"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/fakeip/config/dns/servers/move [post]
func (h *SingboxFakeIPConfigHandler) MoveDNSServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var body SingboxDNSServerMoveRequest
	if err := decodeBody(r, &body); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.FakeIPMoveDNSServer(r.Context(), body.From, body.To); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("fakeip-dns-server-move", strconv.Itoa(body.From),
		"fakeip DNS server moved from index "+strconv.Itoa(body.From)+" to "+strconv.Itoa(body.To))
	response.Success(w, map[string]bool{"ok": true})
}

// ── DNS rules ─────────────────────────────────────────────────────

// ListDNSRules returns all DNS routing rules in priority order (fakeip-tun slot).
//
//	@Summary		List fakeip-config DNS rules
//	@Description	Returns all DNS routing rules in priority (top-first) order for the fakeip-tun slot. Always a JSON array, never null.
//	@Tags			singbox-fakeip
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	SingboxDNSRulesListResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/fakeip/config/dns/rules/list [get]
func (h *SingboxFakeIPConfigHandler) ListDNSRules(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	rules, err := h.svc.FakeIPListDNSRules(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	if rules == nil {
		rules = []router.DNSRule{}
	}
	response.Success(w, rules)
}

// AddDNSRule appends a new DNS routing rule to the fakeip-tun slot.
//
//	@Summary		Add fakeip-config DNS rule
//	@Description	Appends a new DNS routing rule. The rule's server tag must already exist (fakeip-tun slot).
//	@Tags			singbox-fakeip
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxDNSRuleDTO	true	"DNS routing rule"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/fakeip/config/dns/rules/add [post]
func (h *SingboxFakeIPConfigHandler) AddDNSRule(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var rule router.DNSRule
	if err := decodeBody(r, &rule); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.FakeIPAddDNSRule(r.Context(), rule); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("fakeip-dns-rule-add", rule.Server, "fakeip DNS rule added (server: "+rule.Server+")")
	response.Success(w, map[string]bool{"ok": true})
}

// UpdateDNSRule replaces the DNS rule at the given index (fakeip-tun slot).
//
//	@Summary		Update fakeip-config DNS rule
//	@Description	Replaces the DNS rule at the given index (0-based priority slot) in the fakeip-tun slot.
//	@Tags			singbox-fakeip
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxDNSRuleUpdateRequest	true	"Index + replacement rule"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/fakeip/config/dns/rules/update [post]
func (h *SingboxFakeIPConfigHandler) UpdateDNSRule(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var body struct {
		Index int            `json:"index"`
		Rule  router.DNSRule `json:"rule"`
	}
	if err := decodeBody(r, &body); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.FakeIPUpdateDNSRule(r.Context(), body.Index, body.Rule); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("fakeip-dns-rule-update", strconv.Itoa(body.Index),
		"fakeip DNS rule updated at index "+strconv.Itoa(body.Index))
	response.Success(w, map[string]bool{"ok": true})
}

// DeleteDNSRule removes the DNS rule at the given index (fakeip-tun slot).
//
//	@Summary		Delete fakeip-config DNS rule
//	@Description	Removes the DNS rule at the given index (0-based priority slot) from the fakeip-tun slot.
//	@Tags			singbox-fakeip
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxDNSRuleDeleteRequest	true	"Index of the rule to remove"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/fakeip/config/dns/rules/delete [post]
func (h *SingboxFakeIPConfigHandler) DeleteDNSRule(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var body SingboxDNSRuleDeleteRequest
	if err := decodeBody(r, &body); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.FakeIPDeleteDNSRule(r.Context(), body.Index); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("fakeip-dns-rule-delete", strconv.Itoa(body.Index),
		"fakeip DNS rule deleted at index "+strconv.Itoa(body.Index))
	response.Success(w, map[string]bool{"ok": true})
}

// MoveDNSRule moves a DNS rule from one priority slot to another (fakeip-tun slot).
//
//	@Summary		Move fakeip-config DNS rule
//	@Description	Moves the DNS rule from index `from` to index `to` (both 0-based) in the fakeip-tun slot.
//	@Tags			singbox-fakeip
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxDNSRuleMoveRequest	true	"From-index and to-index"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/fakeip/config/dns/rules/move [post]
func (h *SingboxFakeIPConfigHandler) MoveDNSRule(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var body SingboxDNSRuleMoveRequest
	if err := decodeBody(r, &body); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.FakeIPMoveDNSRule(r.Context(), body.From, body.To); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("fakeip-dns-rule-move", strconv.Itoa(body.From),
		"fakeip DNS rule moved from index "+strconv.Itoa(body.From)+" to "+strconv.Itoa(body.To))
	response.Success(w, map[string]bool{"ok": true})
}

// ── DNS globals ───────────────────────────────────────────────────

// GetDNSGlobals returns the global DNS final/strategy fields (fakeip-tun slot).
//
//	@Summary		Get fakeip-config DNS globals
//	@Description	Returns the global DNS settings (final, strategy) for the fakeip-tun slot.
//	@Tags			singbox-fakeip
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	SingboxDNSGlobalsResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/fakeip/config/dns/globals [get]
func (h *SingboxFakeIPConfigHandler) GetDNSGlobals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	final, strategy, err := h.svc.FakeIPGetDNSGlobals(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]string{"final": final, "strategy": strategy})
}

// PutDNSGlobals persists global DNS final/strategy fields (fakeip-tun slot).
//
//	@Summary		Update fakeip-config DNS globals
//	@Description	Persists the global DNS settings (final, strategy) for the fakeip-tun slot.
//	@Tags			singbox-fakeip
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxDNSGlobalsData	true	"final + strategy"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		405		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/fakeip/config/dns/globals [post]
//	@Router			/singbox/fakeip/config/dns/globals [put]
func (h *SingboxFakeIPConfigHandler) PutDNSGlobals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		response.MethodNotAllowed(w)
		return
	}
	var body SingboxDNSGlobalsData
	if err := decodeBody(r, &body); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.FakeIPSetDNSGlobals(r.Context(), body.Final, body.Strategy); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("fakeip-dns-globals", body.Final,
		"fakeip DNS globals updated (final: "+body.Final+", strategy: "+body.Strategy+")")
	response.Success(w, map[string]bool{"ok": true})
}

// ── Route rules ───────────────────────────────────────────────────

// ListRules returns all routing rules in priority order (fakeip-tun slot).
//
//	@Summary		List fakeip-config route rules
//	@Description	Returns all routing rules in priority (top-first) order for the fakeip-tun slot.
//	@Tags			singbox-fakeip
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	SingboxRouterRulesListResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/fakeip/config/rules/list [get]
func (h *SingboxFakeIPConfigHandler) ListRules(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	rules, err := h.svc.FakeIPListRules(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	if rules == nil {
		rules = []router.Rule{}
	}
	response.Success(w, rules)
}

// AddRule appends a new routing rule to the fakeip-tun slot.
//
//	@Summary		Add fakeip-config route rule
//	@Description	Appends a new routing rule to the fakeip-tun slot.
//	@Tags			singbox-fakeip
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxRouterRuleDTO	true	"Routing rule payload"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/fakeip/config/rules/add [post]
func (h *SingboxFakeIPConfigHandler) AddRule(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var rule router.Rule
	if err := decodeBody(r, &rule); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.FakeIPAddRule(r.Context(), rule); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("fakeip-rule-add", rule.Outbound, "fakeip routing rule added (outbound: "+rule.Outbound+")")
	response.Success(w, map[string]bool{"ok": true})
}

// UpdateRule replaces a route rule at the given index (fakeip-tun slot).
//
//	@Summary		Update fakeip-config route rule
//	@Description	Replaces the route rule at index with the provided one (fakeip-tun slot).
//	@Tags			singbox-fakeip
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxRouterRuleUpdateRequest	true	"Index + replacement rule"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/fakeip/config/rules/update [post]
func (h *SingboxFakeIPConfigHandler) UpdateRule(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var body struct {
		Index int         `json:"index"`
		Rule  router.Rule `json:"rule"`
	}
	if err := decodeBody(r, &body); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.FakeIPUpdateRule(r.Context(), body.Index, body.Rule); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("fakeip-rule-update", strconv.Itoa(body.Index),
		"fakeip routing rule updated at index "+strconv.Itoa(body.Index)+" (outbound: "+body.Rule.Outbound+")")
	response.Success(w, map[string]bool{"ok": true})
}

// BulkSetRuleOutbound sets Outbound on every route rule at the given indices
// in a single write (fakeip-tun slot).
//
//	@Summary		Bulk-set outbound on fakeip-config route rules
//	@Description	Sets Outbound on every route rule at the given indices in a single config write (fakeip-tun slot). Rejects an empty/duplicate index list, an out-of-range index, a non-route rule, or an unknown outbound tag.
//	@Tags			singbox-fakeip
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxRouterRuleBulkOutboundRequest	true	"Rule indices + new outbound tag"
//	@Success		200		{object}	SingboxRouterBulkUpdatedResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/fakeip/config/rules/bulk-outbound [post]
func (h *SingboxFakeIPConfigHandler) BulkSetRuleOutbound(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var body SingboxRouterRuleBulkOutboundRequest
	if err := decodeBody(r, &body); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.FakeIPBulkSetRuleOutbound(r.Context(), body.Indices, body.Outbound); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("fakeip-rules-bulk-outbound", body.Outbound,
		strconv.Itoa(len(body.Indices))+" fakeip routing rule(s) set to outbound "+body.Outbound)
	response.Success(w, map[string]int{"updated": len(body.Indices)})
}

// DeleteRule removes the route rule at the given index (fakeip-tun slot).
//
//	@Summary		Delete fakeip-config route rule
//	@Description	Removes the route rule at the given index (0-based) from the fakeip-tun slot.
//	@Tags			singbox-fakeip
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxRouterRuleDeleteRequest	true	"Index of the rule to remove"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/fakeip/config/rules/delete [post]
func (h *SingboxFakeIPConfigHandler) DeleteRule(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var body struct {
		Index int `json:"index"`
	}
	if err := decodeBody(r, &body); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.FakeIPDeleteRule(r.Context(), body.Index); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("fakeip-rule-delete", strconv.Itoa(body.Index),
		"fakeip routing rule deleted at index "+strconv.Itoa(body.Index))
	response.Success(w, map[string]bool{"ok": true})
}

// MoveRule moves a route rule from one priority slot to another (fakeip-tun slot).
//
//	@Summary		Move fakeip-config route rule
//	@Description	Moves the route rule from index `from` to index `to` (both 0-based) in the fakeip-tun slot.
//	@Tags			singbox-fakeip
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxRouterRuleMoveRequest	true	"From-index and to-index"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/fakeip/config/rules/move [post]
func (h *SingboxFakeIPConfigHandler) MoveRule(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var body struct {
		From int `json:"from"`
		To   int `json:"to"`
	}
	if err := decodeBody(r, &body); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.FakeIPMoveRule(r.Context(), body.From, body.To); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("fakeip-rule-move", strconv.Itoa(body.From),
		"fakeip routing rule moved from index "+strconv.Itoa(body.From)+" to "+strconv.Itoa(body.To))
	response.Success(w, map[string]bool{"ok": true})
}

// ── Route final ───────────────────────────────────────────────────

// SetRouteFinal sets route.final on the fakeip-tun slot.
//
//	@Summary		Set fakeip-config route.final
//	@Description	Sets route.final on the fakeip-tun slot. The tag must reference a known outbound.
//	@Tags			singbox-fakeip
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxRouterRouteFinalRequest	true	"New final outbound tag"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		405		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/fakeip/config/route/final [post]
func (h *SingboxFakeIPConfigHandler) SetRouteFinal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req SingboxRouterRouteFinalRequest
	if err := decodeBody(r, &req); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.FakeIPSetRouteFinal(r.Context(), req.Final); err != nil {
		h.handleErr(w, "route-final", err)
		return
	}
	h.log.Info("fakeip-route-final", req.Final, "fakeip route.final set to "+req.Final)
	response.Success(w, map[string]bool{"ok": true})
}

// ── Rule sets ─────────────────────────────────────────────────────

// ListRuleSets returns all configured rulesets in the fakeip-tun slot.
//
//	@Summary		List fakeip-config rulesets
//	@Description	Returns all configured rulesets in the fakeip-tun slot.
//	@Tags			singbox-fakeip
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	SingboxRouterRuleSetsListResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/fakeip/config/rulesets/list [get]
func (h *SingboxFakeIPConfigHandler) ListRuleSets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	rs, err := h.svc.FakeIPListRuleSets(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	if rs == nil {
		rs = []router.RuleSet{}
	}
	response.Success(w, rs)
}

// AddRuleSet registers a new ruleset in the fakeip-tun slot.
//
//	@Summary		Add fakeip-config ruleset
//	@Description	Registers a new ruleset in the fakeip-tun slot.
//	@Tags			singbox-fakeip
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxRouterRuleSetDTO	true	"RuleSet payload"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/fakeip/config/rulesets/add [post]
func (h *SingboxFakeIPConfigHandler) AddRuleSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var rs router.RuleSet
	if err := decodeBody(r, &rs); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.FakeIPAddRuleSet(r.Context(), rs); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("fakeip-ruleset-add", rs.Tag, "fakeip ruleset added: "+rs.Tag)
	response.Success(w, map[string]bool{"ok": true})
}

// UpdateRuleSet replaces the ruleset identified by tag in the fakeip-tun slot.
//
//	@Summary		Update fakeip-config ruleset
//	@Description	Replaces the ruleset identified by tag in the fakeip-tun slot.
//	@Tags			singbox-fakeip
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxRouterRuleSetUpdateRequest	true	"Tag + new RuleSet payload"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		404		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/fakeip/config/rulesets/update [post]
func (h *SingboxFakeIPConfigHandler) UpdateRuleSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var body struct {
		Tag     string         `json:"tag"`
		RuleSet router.RuleSet `json:"ruleSet"`
	}
	if err := decodeBody(r, &body); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if body.Tag == "" {
		response.BadRequest(w, "tag is required")
		return
	}
	if err := h.svc.FakeIPUpdateRuleSet(r.Context(), body.Tag, body.RuleSet); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("fakeip-ruleset-update", body.Tag, "fakeip ruleset updated: "+body.Tag)
	response.Success(w, map[string]bool{"ok": true})
}

// BulkSetRuleSetDetour sets download_detour on every rule set with a tag in
// the given list, in a single write (fakeip-tun slot).
//
//	@Summary		Bulk-set download_detour on fakeip-config rulesets
//	@Description	Sets download_detour on every rule set with a tag in the given list, in a single config write (fakeip-tun slot). Rejects an empty/duplicate tag list, an unknown tag, a rule set whose type isn't "remote", or an unknown outbound tag (empty detour clears the field and skips the known-tag check).
//	@Tags			singbox-fakeip
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxRouterRuleSetBulkDetourRequest	true	"Rule set tags + new download_detour"
//	@Success		200		{object}	SingboxRouterBulkUpdatedResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/fakeip/config/rulesets/bulk-detour [post]
func (h *SingboxFakeIPConfigHandler) BulkSetRuleSetDetour(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var body SingboxRouterRuleSetBulkDetourRequest
	if err := decodeBody(r, &body); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.FakeIPBulkSetRuleSetDetour(r.Context(), body.Tags, body.DownloadDetour); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("fakeip-rulesets-bulk-detour", body.DownloadDetour,
		strconv.Itoa(len(body.Tags))+" fakeip rule set(s) detour set to "+body.DownloadDetour)
	response.Success(w, map[string]int{"updated": len(body.Tags)})
}

// DeleteRuleSet removes the ruleset identified by tag from the fakeip-tun slot.
//
//	@Summary		Delete fakeip-config ruleset
//	@Description	Removes the ruleset identified by tag from the fakeip-tun slot. Refuses if referenced; pass force=true to override.
//	@Tags			singbox-fakeip
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxRouterRuleSetDeleteRequest	true	"Tag + optional force flag"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		409		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/fakeip/config/rulesets/delete [post]
func (h *SingboxFakeIPConfigHandler) DeleteRuleSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var body struct {
		Tag   string `json:"tag"`
		Force bool   `json:"force"`
	}
	if err := decodeBody(r, &body); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.FakeIPDeleteRuleSet(r.Context(), body.Tag, body.Force); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("fakeip-ruleset-delete", body.Tag, "fakeip ruleset deleted: "+body.Tag)
	response.Success(w, map[string]bool{"ok": true})
}

// ── Composite outbounds ───────────────────────────────────────────

// ListOutbounds returns all composite outbounds in the fakeip-tun slot.
//
//	@Summary		List fakeip-config outbounds
//	@Description	Returns all composite outbounds (selectors/urltests) in the fakeip-tun slot.
//	@Tags			singbox-fakeip
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	SingboxRouterOutboundsListResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/fakeip/config/outbounds/list [get]
func (h *SingboxFakeIPConfigHandler) ListOutbounds(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	o, err := h.svc.FakeIPListCompositeOutbounds(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	if o == nil {
		o = []router.CompositeOutboundView{}
	}
	response.Success(w, o)
}

// AddOutbound creates a new composite outbound in the fakeip-tun slot.
//
//	@Summary		Add fakeip-config outbound
//	@Description	Creates a new composite outbound in the fakeip-tun slot.
//	@Tags			singbox-fakeip
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxRouterOutboundDTO	true	"Composite outbound payload"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/fakeip/config/outbounds/add [post]
func (h *SingboxFakeIPConfigHandler) AddOutbound(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var o router.Outbound
	if err := decodeBody(r, &o); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.FakeIPAddCompositeOutbound(r.Context(), o); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("fakeip-outbound-add", o.Tag, "fakeip composite outbound added: "+o.Tag)
	response.Success(w, map[string]bool{"ok": true})
}

// UpdateOutbound replaces the composite outbound identified by tag (fakeip-tun slot).
//
//	@Summary		Update fakeip-config outbound
//	@Description	Replaces the composite outbound identified by tag in the fakeip-tun slot.
//	@Tags			singbox-fakeip
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxRouterOutboundUpdateRequest	true	"Tag + replacement outbound"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/fakeip/config/outbounds/update [post]
func (h *SingboxFakeIPConfigHandler) UpdateOutbound(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var body struct {
		Tag      string          `json:"tag"`
		Outbound router.Outbound `json:"outbound"`
	}
	if err := decodeBody(r, &body); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.FakeIPUpdateCompositeOutbound(r.Context(), body.Tag, body.Outbound); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("fakeip-outbound-update", body.Tag, "fakeip composite outbound updated: "+body.Tag)
	response.Success(w, map[string]bool{"ok": true})
}

// DeleteOutbound removes the composite outbound identified by tag (fakeip-tun slot).
//
//	@Summary		Delete fakeip-config outbound
//	@Description	Removes the composite outbound identified by tag from the fakeip-tun slot.
//	@Tags			singbox-fakeip
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxRouterOutboundDeleteRequest	true	"Tag + optional force flag"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		409		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/fakeip/config/outbounds/delete [post]
func (h *SingboxFakeIPConfigHandler) DeleteOutbound(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var body struct {
		Tag   string `json:"tag"`
		Force bool   `json:"force"`
	}
	if err := decodeBody(r, &body); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.FakeIPDeleteCompositeOutbound(r.Context(), body.Tag, body.Force); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("fakeip-outbound-delete", body.Tag, "fakeip composite outbound deleted: "+body.Tag)
	response.Success(w, map[string]bool{"ok": true})
}
