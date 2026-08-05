package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
)

// SingboxFakeIPConfigHandler отдаёт DNS режимного слота fakeip (SlotFakeIP)
// ручками под /api/singbox/fakeip/config/dns/... Правила, наборы и outbound'ы
// сюда больше не входят: они живут в общем слоте 21-routing.json и правятся
// ручками SingboxRouterHandler (подэтап 5D0).
//
// DNS-часть по-прежнему повторяет DTO, маппинг ошибок и nil-гарды роутерного
// обработчика — расходится только префикс пути и FakeIP-префикс вызовов
// сервиса.
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
	case errors.Is(err, router.ErrDNSResolverCycle):
		// 400: кольцо domain_resolver. `sing-box check` его пропускает —
		// движок падает на старте, поэтому отвергаем на входе.
		response.Error(w, err.Error(), "DNS_RESOLVER_CYCLE")
	case errors.Is(err, router.ErrRuleSetNotApplied):
		// 400: DNS-правило сослалось на черновичный набор общего слота —
		// применять нечего, пока правка наборов не принята.
		response.Error(w, err.Error(), "RULE_SET_NOT_APPLIED")
	case errors.Is(err, router.ErrOutboundNotApplied):
		// 400: то же самое для detour DNS-сервера — он сослался на выход,
		// который пока только в черновике общего слота.
		response.Error(w, err.Error(), "OUTBOUND_NOT_APPLIED")
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
