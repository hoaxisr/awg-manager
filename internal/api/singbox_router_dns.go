package api

import (
	"net/http"
	"strconv"

	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// ── Response DTOs ────────────────────────────────────────────────

// SingboxDomainResolverDTO mirrors router.DomainResolver, the optional
// nested resolver descriptor on a DNS server.
type SingboxDomainResolverDTO struct {
	Server   string `json:"server,omitempty" example:"local"`
	Strategy string `json:"strategy,omitempty" example:"ipv4_only"`
}

type SingboxDNSClientTLSOptionsDTO struct {
	ServerName                 string   `json:"server_name,omitempty" example:"cloudflare-dns.com"`
	Insecure                   bool     `json:"insecure,omitempty"`
	ALPN                       []string `json:"alpn,omitempty" example:"h2"`
	MinVersion                 string   `json:"min_version,omitempty" example:"1.2"`
	MaxVersion                 string   `json:"max_version,omitempty" example:"1.3"`
	CertificatePublicKeySHA256 []string `json:"certificate_public_key_sha256,omitempty"`
}

// SingboxDNSServerDTO mirrors router.DNSServer.
type SingboxDNSServerDTO struct {
	Tag            string                         `json:"tag" example:"cloudflare"`
	Type           string                         `json:"type" example:"udp"`
	Server         string                         `json:"server" example:"1.1.1.1"`
	ServerPort     int                            `json:"server_port,omitempty" example:"53"`
	Path           string                         `json:"path,omitempty" example:"/dns-query"`
	Detour         string                         `json:"detour,omitempty" example:"direct"`
	Strategy       string                         `json:"domain_strategy,omitempty" example:"prefer_ipv4"`
	DomainResolver *SingboxDomainResolverDTO      `json:"domain_resolver,omitempty"`
	TLS            *SingboxDNSClientTLSOptionsDTO `json:"tls,omitempty"`
}

// SingboxDNSServersListResponse is the envelope for
// GET /singbox/router/dns/servers/list.
type SingboxDNSServersListResponse struct {
	Success bool                  `json:"success" example:"true"`
	Data    []SingboxDNSServerDTO `json:"data"`
}

// SingboxDNSRuleDTO mirrors router.DNSRule.
type SingboxDNSRuleDTO struct {
	RuleSet       []string `json:"rule_set,omitempty" example:"geosite-cn"`
	DomainSuffix  []string `json:"domain_suffix,omitempty" example:".example.com"`
	Domain        []string `json:"domain,omitempty" example:"example.com"`
	DomainKeyword []string `json:"domain_keyword,omitempty" example:"google"`
	QueryType     []string `json:"query_type,omitempty" example:"A"`
	Server        string   `json:"server,omitempty" example:"cloudflare"`
	Action        string   `json:"action,omitempty" example:"route"`

	// sing-box 1.14: DNS-механизм evaluate/match_response.
	Tag string `json:"tag,omitempty" example:"rd"`
	// MatchResponse — union: true (любой ответ) либо тег правила evaluate ("rd").
	// Тип в swagger намеренно не объявлен: обе формы union'а легальны.
	MatchResponse  any      `json:"match_response,omitempty"`
	IPCIDR         []string `json:"ip_cidr,omitempty" example:"10.0.0.0/8"`
	ResponseRcode  string   `json:"response_rcode,omitempty" example:"NOERROR"`
	ResponseAnswer []string `json:"response_answer,omitempty" example:"example.com. IN A 1.2.3.4"`
	ResponseNS     []string `json:"response_ns,omitempty"`
	ResponseExtra  []string `json:"response_extra,omitempty"`
	Race           bool     `json:"race,omitempty" example:"true"`
	Speculative    bool     `json:"speculative,omitempty" example:"false"`
}

// SingboxDNSRulesListResponse is the envelope for
// GET /singbox/router/dns/rules/list.
type SingboxDNSRulesListResponse struct {
	Success bool                `json:"success" example:"true"`
	Data    []SingboxDNSRuleDTO `json:"data"`
}

// SingboxDNSGlobalsData carries the global DNS settings exposed by
// GET/PUT /singbox/router/dns/globals. Reused as the request body type.
type SingboxDNSGlobalsData struct {
	Final    string `json:"final" example:"cloudflare"`
	Strategy string `json:"strategy" example:"prefer_ipv4"`
	// Timeout is the per-query DNS timeout (Go duration, sing-box 1.14). Empty = engine default (10s).
	Timeout string `json:"timeout,omitempty" example:"5s"`
}

// SingboxDNSGlobalsResponse is the envelope for
// GET/PUT /singbox/router/dns/globals.
type SingboxDNSGlobalsResponse struct {
	Success bool                  `json:"success" example:"true"`
	Data    SingboxDNSGlobalsData `json:"data"`
}

// SingboxDNSChainPresetData carries the DNS-chain preset (sing-box 1.14
// evaluate/match_response chains) exposed by GET/POST
// /singbox/router/dns/chain-preset. Reused as the request body type.
// Mode: "" (off) | "resilient" | "antipoison".
type SingboxDNSChainPresetData struct {
	Mode         string   `json:"mode" example:"resilient"`
	DirectServer string   `json:"directServer,omitempty" example:"dns-direct"`
	ProxyServer  string   `json:"proxyServer,omitempty" example:"dns-tunnel"`
	PoisonCIDRs  []string `json:"poisonCidrs,omitempty" example:"0.0.0.0/32"`
}

// SingboxDNSChainPresetResponse is the envelope for
// GET/POST /singbox/router/dns/chain-preset.
type SingboxDNSChainPresetResponse struct {
	Success bool                      `json:"success" example:"true"`
	Data    SingboxDNSChainPresetData `json:"data"`
}

// ── Request DTOs ─────────────────────────────────────────────────

// SingboxDNSServerUpdateRequest is the body for
// POST /singbox/router/dns/servers/update.
type SingboxDNSServerUpdateRequest struct {
	Tag    string              `json:"tag" example:"cloudflare"`
	Server SingboxDNSServerDTO `json:"server"`
}

// SingboxDNSServerDeleteRequest is the body for
// POST /singbox/router/dns/servers/delete. force=true overrides the
// "still referenced by a rule" guard.
type SingboxDNSServerDeleteRequest struct {
	Tag   string `json:"tag" example:"cloudflare"`
	Force bool   `json:"force" example:"false"`
}

// SingboxDNSServerMoveRequest is the body for
// POST /singbox/router/dns/servers/move.
type SingboxDNSServerMoveRequest struct {
	From int `json:"from" example:"3"`
	To   int `json:"to" example:"0"`
}

// SingboxDNSRuleUpdateRequest is the body for
// POST /singbox/router/dns/rules/update.
type SingboxDNSRuleUpdateRequest struct {
	Index int               `json:"index" example:"0"`
	Rule  SingboxDNSRuleDTO `json:"rule"`
}

// SingboxDNSRuleDeleteRequest is the body for
// POST /singbox/router/dns/rules/delete.
type SingboxDNSRuleDeleteRequest struct {
	Index int `json:"index" example:"0"`
}

// SingboxDNSRuleMoveRequest is the body for
// POST /singbox/router/dns/rules/move.
type SingboxDNSRuleMoveRequest struct {
	From int `json:"from" example:"3"`
	To   int `json:"to" example:"0"`
}

// ListDNSServers returns all configured DNS servers.
//
//	@Summary		List singbox-router DNS servers
//	@Description	Returns all configured DNS upstreams (tag, address, type, ...). Always a JSON array, never null.
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	SingboxDNSServersListResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/router/dns/servers/list [get]
func (h *SingboxRouterHandler) ListDNSServers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	servers, err := h.svc.ListDNSServers(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	if servers == nil {
		servers = []router.DNSServer{}
	}
	response.Success(w, servers)
}

// AddDNSServer registers a new DNS upstream.
//
//	@Summary		Add singbox-router DNS server
//	@Description	Registers a new DNS upstream (tag must be unique).
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxDNSServerDTO	true	"DNS server descriptor"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/router/dns/servers/add [post]
func (h *SingboxRouterHandler) AddDNSServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var s router.DNSServer
	if err := decodeBody(r, &s); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.AddDNSServer(r.Context(), s); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("dns-server-add", s.Tag, "DNS server added: "+s.Tag)
	response.Success(w, map[string]bool{"ok": true})
}

// UpdateDNSServer replaces the DNS upstream identified by tag.
//
//	@Summary		Update singbox-router DNS server
//	@Description	Replaces the DNS upstream identified by tag with the provided one.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxDNSServerUpdateRequest	true	"Tag + replacement server"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/router/dns/servers/update [post]
func (h *SingboxRouterHandler) UpdateDNSServer(w http.ResponseWriter, r *http.Request) {
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
	if err := h.svc.UpdateDNSServer(r.Context(), body.Tag, body.Server); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("dns-server-update", body.Tag, "DNS server updated: "+body.Tag)
	response.Success(w, map[string]bool{"ok": true})
}

// DeleteDNSServer removes the DNS upstream identified by tag.
//
//	@Summary		Delete singbox-router DNS server
//	@Description	Removes the DNS upstream identified by tag. Refuses if any DNS rule references it; pass force=true to override.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxDNSServerDeleteRequest	true	"Tag + optional force flag"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		409		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/router/dns/servers/delete [post]
func (h *SingboxRouterHandler) DeleteDNSServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var body SingboxDNSServerDeleteRequest
	if err := decodeBody(r, &body); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.DeleteDNSServer(r.Context(), body.Tag, body.Force); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("dns-server-delete", body.Tag, "DNS server deleted: "+body.Tag)
	response.Success(w, map[string]bool{"ok": true})
}

// MoveDNSServer reorders a DNS server from one slot to another.
//
//	@Summary		Move singbox-router DNS server
//	@Description	Moves the DNS server from index `from` to index `to` (both 0-based). Server order is cosmetic — sing-box references servers by tag; this exists only for UX-consistent reordering.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxDNSServerMoveRequest	true	"From-index and to-index"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/router/dns/servers/move [post]
func (h *SingboxRouterHandler) MoveDNSServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var body SingboxDNSServerMoveRequest
	if err := decodeBody(r, &body); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.MoveDNSServer(r.Context(), body.From, body.To); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("dns-server-move", strconv.Itoa(body.From),
		"DNS server moved from index "+strconv.Itoa(body.From)+" to "+strconv.Itoa(body.To))
	response.Success(w, map[string]bool{"ok": true})
}

// ListDNSRules returns all DNS routing rules in priority order.
//
//	@Summary		List singbox-router DNS rules
//	@Description	Returns all DNS routing rules in priority (top-first) order. Always a JSON array, never null.
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	SingboxDNSRulesListResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/router/dns/rules/list [get]
func (h *SingboxRouterHandler) ListDNSRules(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	rules, err := h.svc.ListDNSRules(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	if rules == nil {
		rules = []router.DNSRule{}
	}
	response.Success(w, rules)
}

// AddDNSRule appends a new DNS routing rule.
//
//	@Summary		Add singbox-router DNS rule
//	@Description	Appends a new DNS routing rule. The rule's server tag must already exist.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxDNSRuleDTO	true	"DNS routing rule"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/router/dns/rules/add [post]
func (h *SingboxRouterHandler) AddDNSRule(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var rule router.DNSRule
	if err := decodeBody(r, &rule); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.AddDNSRule(r.Context(), rule); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("dns-rule-add", rule.Server, "DNS rule added (server: "+rule.Server+")")
	response.Success(w, map[string]bool{"ok": true})
}

// UpdateDNSRule replaces the DNS rule at the given index.
//
//	@Summary		Update singbox-router DNS rule
//	@Description	Replaces the DNS rule at the given index (0-based priority slot) with the provided one.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxDNSRuleUpdateRequest	true	"Index + replacement rule"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/router/dns/rules/update [post]
func (h *SingboxRouterHandler) UpdateDNSRule(w http.ResponseWriter, r *http.Request) {
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
	if err := h.svc.UpdateDNSRule(r.Context(), body.Index, body.Rule); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("dns-rule-update", strconv.Itoa(body.Index), "DNS rule updated at index "+strconv.Itoa(body.Index))
	response.Success(w, map[string]bool{"ok": true})
}

// DeleteDNSRule removes the DNS rule at the given index.
//
//	@Summary		Delete singbox-router DNS rule
//	@Description	Removes the DNS rule at the given index (0-based priority slot).
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxDNSRuleDeleteRequest	true	"Index of the rule to remove"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/router/dns/rules/delete [post]
func (h *SingboxRouterHandler) DeleteDNSRule(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var body SingboxDNSRuleDeleteRequest
	if err := decodeBody(r, &body); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.DeleteDNSRule(r.Context(), body.Index); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("dns-rule-delete", strconv.Itoa(body.Index), "DNS rule deleted at index "+strconv.Itoa(body.Index))
	response.Success(w, map[string]bool{"ok": true})
}

// MoveDNSRule moves a DNS rule from one priority slot to another.
//
//	@Summary		Move singbox-router DNS rule
//	@Description	Moves the DNS rule from index `from` to index `to` (both 0-based).
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxDNSRuleMoveRequest	true	"From-index and to-index"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/router/dns/rules/move [post]
func (h *SingboxRouterHandler) MoveDNSRule(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var body SingboxDNSRuleMoveRequest
	if err := decodeBody(r, &body); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.MoveDNSRule(r.Context(), body.From, body.To); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("dns-rule-move", strconv.Itoa(body.From),
		"DNS rule moved from index "+strconv.Itoa(body.From)+" to "+strconv.Itoa(body.To))
	response.Success(w, map[string]bool{"ok": true})
}

// GetDNSGlobals returns the global DNS final/strategy fields.
//
//	@Summary		Get singbox-router DNS globals
//	@Description	Returns the global DNS settings: `final` (default server tag), `strategy` (ipv4_only / prefer_ipv4 / etc.) and `timeout` (per-query DNS timeout).
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	SingboxDNSGlobalsResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/router/dns/globals [get]
func (h *SingboxRouterHandler) GetDNSGlobals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	final, strategy, timeout, err := h.svc.GetDNSGlobals(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]string{"final": final, "strategy": strategy, "timeout": timeout})
}

// PutDNSGlobals persists global DNS final/strategy/timeout fields.
//
//	@Summary		Update singbox-router DNS globals
//	@Description	Persists the global DNS settings: `final` (default server tag), `strategy` (ipv4_only / prefer_ipv4 / etc.) and `timeout` (per-query DNS timeout).
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxDNSGlobalsData	true	"final + strategy + timeout"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		405		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/router/dns/globals [post]
//	@Router			/singbox/router/dns/globals [put]
func (h *SingboxRouterHandler) PutDNSGlobals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		response.MethodNotAllowed(w)
		return
	}
	var body SingboxDNSGlobalsData
	if err := decodeBody(r, &body); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.SetDNSGlobals(r.Context(), body.Final, body.Strategy, body.Timeout); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("dns-globals", body.Final, "DNS globals updated (final: "+body.Final+", strategy: "+body.Strategy+")")
	response.Success(w, map[string]bool{"ok": true})
}

// GetDNSChainPreset returns the DNS-chain preset state.
//
//	@Summary		Get singbox-router DNS chain preset
//	@Description	Returns the DNS-chain preset (sing-box 1.14 evaluate/match_response): `mode` ("" = off, resilient, antipoison), the direct/proxy server tags and the antipoison ip_cidr list.
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	SingboxDNSChainPresetResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/router/dns/chain-preset [get]
func (h *SingboxRouterHandler) GetDNSChainPreset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	st, err := h.svc.GetDNSChainPreset(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, st)
}

// PutDNSChainPreset enables, switches or disables the DNS-chain preset.
//
//	@Summary		Update singbox-router DNS chain preset
//	@Description	Enables/switches/disables the DNS-chain preset. `mode: ""` turns it off and removes the managed chain; otherwise the direct/proxy server tags must reference existing DNS servers.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxDNSChainPresetData	true	"mode + servers + poison cidrs"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		405		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/router/dns/chain-preset [post]
func (h *SingboxRouterHandler) PutDNSChainPreset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var body storage.DNSChainPresetState
	if err := decodeBody(r, &body); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.SetDNSChainPreset(r.Context(), body); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("dns-chain-preset", body.Mode, "DNS chain preset updated (mode: "+body.Mode+")")
	response.Success(w, map[string]bool{"ok": true})
}
