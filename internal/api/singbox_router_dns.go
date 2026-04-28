package api

import (
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
)

// ListDNSServers returns all configured DNS servers.
//
//	@Summary		List singbox-router DNS servers
//	@Description	Returns all configured DNS upstreams (tag, address, type, ...). Always a JSON array, never null.
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	map[string]interface{}
//	@Failure		405	{object}	map[string]interface{}
//	@Failure		500	{object}	map[string]interface{}
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
//	@Param			body	body		map[string]interface{}	true	"router.DNSServer"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
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
	response.Success(w, nil)
}

// UpdateDNSServer replaces the DNS upstream identified by tag.
//
//	@Summary		Update singbox-router DNS server
//	@Description	Replaces the DNS upstream identified by tag with the provided one.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		map[string]interface{}	true	"{tag, server}"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
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
	response.Success(w, nil)
}

// DeleteDNSServer removes the DNS upstream identified by tag.
//
//	@Summary		Delete singbox-router DNS server
//	@Description	Removes the DNS upstream identified by tag. Refuses if any DNS rule references it; pass force=true to override.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		map[string]interface{}	true	"{tag, force}"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		409		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/singbox/router/dns/servers/delete [post]
func (h *SingboxRouterHandler) DeleteDNSServer(w http.ResponseWriter, r *http.Request) {
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
	if err := h.svc.DeleteDNSServer(r.Context(), body.Tag, body.Force); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	response.Success(w, nil)
}

// ListDNSRules returns all DNS routing rules in priority order.
//
//	@Summary		List singbox-router DNS rules
//	@Description	Returns all DNS routing rules in priority (top-first) order. Always a JSON array, never null.
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	map[string]interface{}
//	@Failure		405	{object}	map[string]interface{}
//	@Failure		500	{object}	map[string]interface{}
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
//	@Param			body	body		map[string]interface{}	true	"router.DNSRule"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
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
	response.Success(w, nil)
}

// UpdateDNSRule replaces the DNS rule at the given index.
//
//	@Summary		Update singbox-router DNS rule
//	@Description	Replaces the DNS rule at the given index (0-based priority slot) with the provided one.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		map[string]interface{}	true	"{index, rule}"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
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
	response.Success(w, nil)
}

// DeleteDNSRule removes the DNS rule at the given index.
//
//	@Summary		Delete singbox-router DNS rule
//	@Description	Removes the DNS rule at the given index (0-based priority slot).
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		map[string]interface{}	true	"{index}"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/singbox/router/dns/rules/delete [post]
func (h *SingboxRouterHandler) DeleteDNSRule(w http.ResponseWriter, r *http.Request) {
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
	if err := h.svc.DeleteDNSRule(r.Context(), body.Index); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	response.Success(w, nil)
}

// MoveDNSRule moves a DNS rule from one priority slot to another.
//
//	@Summary		Move singbox-router DNS rule
//	@Description	Moves the DNS rule from index `from` to index `to` (both 0-based).
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		map[string]interface{}	true	"{from, to}"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/singbox/router/dns/rules/move [post]
func (h *SingboxRouterHandler) MoveDNSRule(w http.ResponseWriter, r *http.Request) {
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
	if err := h.svc.MoveDNSRule(r.Context(), body.From, body.To); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	response.Success(w, nil)
}

// GetDNSGlobals returns the global DNS final/strategy fields.
//
//	@Summary		Get singbox-router DNS globals
//	@Description	Returns the global DNS settings: `final` (default server tag) and `strategy` (ipv4_only / prefer_ipv4 / etc.).
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	map[string]interface{}
//	@Failure		405	{object}	map[string]interface{}
//	@Failure		500	{object}	map[string]interface{}
//	@Router			/singbox/router/dns/globals [get]
func (h *SingboxRouterHandler) GetDNSGlobals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	final, strategy, err := h.svc.GetDNSGlobals(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]string{"final": final, "strategy": strategy})
}

// PutDNSGlobals persists global DNS final/strategy fields.
//
//	@Summary		Update singbox-router DNS globals
//	@Description	Persists the global DNS settings: `final` (default server tag) and `strategy` (ipv4_only / prefer_ipv4 / etc.).
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		map[string]interface{}	true	"{final, strategy}"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		405		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/singbox/router/dns/globals [post]
//	@Router			/singbox/router/dns/globals [put]
func (h *SingboxRouterHandler) PutDNSGlobals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		response.MethodNotAllowed(w)
		return
	}
	var body struct {
		Final    string `json:"final"`
		Strategy string `json:"strategy"`
	}
	if err := decodeBody(r, &body); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.SetDNSGlobals(r.Context(), body.Final, body.Strategy); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	response.Success(w, nil)
}
