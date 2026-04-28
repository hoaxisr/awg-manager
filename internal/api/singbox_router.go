package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

type SingboxRouterHandler struct {
	svc router.Service
	log *logging.ScopedLogger
}

func NewSingboxRouterHandler(svc router.Service, appLogger logging.AppLogger) *SingboxRouterHandler {
	return &SingboxRouterHandler{
		svc: svc,
		log: logging.NewScopedLogger(appLogger, logging.GroupRouting, logging.SubSingboxRouter),
	}
}

// GetStatus returns the current sing-box router engine status.
//
//	@Summary		Get sing-box router status
//	@Description	Returns the singbox-router status snapshot (running, mode, policy/iptables state, rule/ruleset/outbound counts).
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	map[string]interface{}
//	@Failure		405	{object}	map[string]interface{}
//	@Failure		500	{object}	map[string]interface{}
//	@Router			/singbox/router/status [get]
func (h *SingboxRouterHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	st, err := h.svc.GetStatus(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, st)
}

// Enable starts the singbox-router engine and installs iptables/policy rules.
//
//	@Summary		Enable singbox-router
//	@Description	Starts the singbox-router engine and installs iptables/policy rules. Returns 400 with code POLICY_NOT_CONFIGURED or POLICY_MISSING when the router policy mode is incomplete.
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	map[string]interface{}
//	@Failure		400	{object}	map[string]interface{}
//	@Failure		405	{object}	map[string]interface{}
//	@Failure		500	{object}	map[string]interface{}
//	@Router			/singbox/router/enable [post]
func (h *SingboxRouterHandler) Enable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if err := h.svc.Enable(r.Context()); err != nil {
		if errors.Is(err, router.ErrPolicyNotConfigured) {
			response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "POLICY_NOT_CONFIGURED")
			return
		}
		if errors.Is(err, router.ErrPolicyMissing) {
			response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "POLICY_MISSING")
			return
		}
		h.handleErr(w, "request", err)
		return
	}
	response.Success(w, nil)
}

// Disable stops the singbox-router engine and uninstalls iptables/policy rules.
//
//	@Summary		Disable singbox-router
//	@Description	Stops the singbox-router engine and uninstalls iptables/policy rules. Idempotent.
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	map[string]interface{}
//	@Failure		405	{object}	map[string]interface{}
//	@Failure		500	{object}	map[string]interface{}
//	@Router			/singbox/router/disable [post]
func (h *SingboxRouterHandler) Disable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if err := h.svc.Disable(r.Context()); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, nil)
}

// GetSettings reads singbox-router settings (policy-mode, defaults, etc.).
//
//	@Summary		Get singbox-router settings
//	@Description	Reads the current singbox-router settings (policy mode, defaults, ...).
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	map[string]interface{}
//	@Failure		405	{object}	map[string]interface{}
//	@Failure		500	{object}	map[string]interface{}
//	@Router			/singbox/router/settings [get]
func (h *SingboxRouterHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	s, err := h.svc.GetSettings(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, s)
}

// PutSettings persists singbox-router settings.
//
//	@Summary		Update singbox-router settings
//	@Description	Persists singbox-router settings. The router is restarted only when fields that affect the running config change.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		map[string]interface{}	true	"storage.SingboxRouterSettings"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		405		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/singbox/router/settings [post]
//	@Router			/singbox/router/settings [put]
func (h *SingboxRouterHandler) PutSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		response.MethodNotAllowed(w)
		return
	}
	var sr storage.SingboxRouterSettings
	if err := decodeBody(r, &sr); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.UpdateSettings(r.Context(), sr); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	response.Success(w, nil)
}

// ListRules returns all singbox-router routing rules in priority order.
//
//	@Summary		List singbox-router rules
//	@Description	Returns all routing rules in priority (top-first) order.
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	map[string]interface{}
//	@Failure		405	{object}	map[string]interface{}
//	@Failure		500	{object}	map[string]interface{}
//	@Router			/singbox/router/rules/list [get]
func (h *SingboxRouterHandler) ListRules(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	rules, err := h.svc.ListRules(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, rules)
}

// AddRule appends a new singbox-router routing rule.
//
//	@Summary		Add singbox-router rule
//	@Description	Appends a new routing rule. Rule conditions reference rulesets/outbounds that must already exist.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		map[string]interface{}	true	"router.Rule"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/singbox/router/rules/add [post]
func (h *SingboxRouterHandler) AddRule(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var rule router.Rule
	if err := decodeBody(r, &rule); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.AddRule(r.Context(), rule); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	response.Success(w, nil)
}

// UpdateRule replaces a rule at the given index with the provided one.
//
//	@Summary		Update singbox-router rule
//	@Description	Replaces the rule at index with the provided one. Index is the priority slot (0-based).
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		map[string]interface{}	true	"{index, rule}"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/singbox/router/rules/update [post]
func (h *SingboxRouterHandler) UpdateRule(w http.ResponseWriter, r *http.Request) {
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
	if err := h.svc.UpdateRule(r.Context(), body.Index, body.Rule); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	response.Success(w, nil)
}

// DeleteRule removes the rule at the given index.
//
//	@Summary		Delete singbox-router rule
//	@Description	Removes the rule at the given index (0-based priority slot).
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		map[string]interface{}	true	"{index}"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/singbox/router/rules/delete [post]
func (h *SingboxRouterHandler) DeleteRule(w http.ResponseWriter, r *http.Request) {
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
	if err := h.svc.DeleteRule(r.Context(), body.Index); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	response.Success(w, nil)
}

// MoveRule moves the rule from one priority slot to another.
//
//	@Summary		Move singbox-router rule
//	@Description	Moves the rule from index `from` to index `to` (both 0-based). Adjusts other rules' indices accordingly.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		map[string]interface{}	true	"{from, to}"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/singbox/router/rules/move [post]
func (h *SingboxRouterHandler) MoveRule(w http.ResponseWriter, r *http.Request) {
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
	if err := h.svc.MoveRule(r.Context(), body.From, body.To); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	response.Success(w, nil)
}

// ListRuleSets returns all configured rulesets.
//
//	@Summary		List singbox-router rulesets
//	@Description	Returns all configured rulesets (downloaded geo files / inline lists), with their tag, type, and freshness metadata.
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	map[string]interface{}
//	@Failure		405	{object}	map[string]interface{}
//	@Failure		500	{object}	map[string]interface{}
//	@Router			/singbox/router/rulesets/list [get]
func (h *SingboxRouterHandler) ListRuleSets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	rs, err := h.svc.ListRuleSets(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, rs)
}

// AddRuleSet registers a new ruleset (downloads if remote).
//
//	@Summary		Add singbox-router ruleset
//	@Description	Registers a new ruleset. For remote rulesets the file is downloaded synchronously.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		map[string]interface{}	true	"router.RuleSet"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/singbox/router/rulesets/add [post]
func (h *SingboxRouterHandler) AddRuleSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var rs router.RuleSet
	if err := decodeBody(r, &rs); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.AddRuleSet(r.Context(), rs); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	response.Success(w, nil)
}

// DeleteRuleSet removes the ruleset identified by tag.
//
//	@Summary		Delete singbox-router ruleset
//	@Description	Removes the ruleset identified by tag. Refuses if any rule references it; pass force=true to ignore references.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		map[string]interface{}	true	"{tag, force}"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		409		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/singbox/router/rulesets/delete [post]
func (h *SingboxRouterHandler) DeleteRuleSet(w http.ResponseWriter, r *http.Request) {
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
	if err := h.svc.DeleteRuleSet(r.Context(), body.Tag, body.Force); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	response.Success(w, nil)
}

// RefreshRuleSet re-downloads the ruleset identified by tag.
//
//	@Summary		Refresh singbox-router ruleset
//	@Description	Re-downloads the remote ruleset identified by tag and updates its content/timestamp.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		map[string]interface{}	true	"{tag}"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/singbox/router/rulesets/refresh [post]
func (h *SingboxRouterHandler) RefreshRuleSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var body struct {
		Tag string `json:"tag"`
	}
	if err := decodeBody(r, &body); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.RefreshRuleSet(r.Context(), body.Tag); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	response.Success(w, nil)
}

// ListOutbounds returns all composite outbounds.
//
//	@Summary		List singbox-router outbounds
//	@Description	Returns all composite outbounds (sing-box selectors/urltests over multiple base outbounds).
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	map[string]interface{}
//	@Failure		405	{object}	map[string]interface{}
//	@Failure		500	{object}	map[string]interface{}
//	@Router			/singbox/router/outbounds/list [get]
func (h *SingboxRouterHandler) ListOutbounds(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	o, err := h.svc.ListCompositeOutbounds(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, o)
}

// AddOutbound creates a new composite outbound.
//
//	@Summary		Add singbox-router outbound
//	@Description	Creates a new composite outbound. The base outbounds it references must already exist.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		map[string]interface{}	true	"router.Outbound"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/singbox/router/outbounds/add [post]
func (h *SingboxRouterHandler) AddOutbound(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var o router.Outbound
	if err := decodeBody(r, &o); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.AddCompositeOutbound(r.Context(), o); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	response.Success(w, nil)
}

// UpdateOutbound replaces the composite outbound identified by tag.
//
//	@Summary		Update singbox-router outbound
//	@Description	Replaces the composite outbound identified by tag with the provided one.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		map[string]interface{}	true	"{tag, outbound}"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/singbox/router/outbounds/update [post]
func (h *SingboxRouterHandler) UpdateOutbound(w http.ResponseWriter, r *http.Request) {
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
	if err := h.svc.UpdateCompositeOutbound(r.Context(), body.Tag, body.Outbound); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	response.Success(w, nil)
}

// DeleteOutbound removes the composite outbound identified by tag.
//
//	@Summary		Delete singbox-router outbound
//	@Description	Removes the composite outbound identified by tag. Refuses if any rule references it; pass force=true to override.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		map[string]interface{}	true	"{tag, force}"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		409		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/singbox/router/outbounds/delete [post]
func (h *SingboxRouterHandler) DeleteOutbound(w http.ResponseWriter, r *http.Request) {
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
	if err := h.svc.DeleteCompositeOutbound(r.Context(), body.Tag, body.Force); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	response.Success(w, nil)
}

// ListPresets returns the catalog of built-in singbox-router presets.
//
//	@Summary		List singbox-router presets
//	@Description	Returns the catalog of built-in presets the user can apply (each preset = a curated bundle of rules + rulesets).
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	map[string]interface{}
//	@Failure		405	{object}	map[string]interface{}
//	@Router			/singbox/router/presets/list [get]
func (h *SingboxRouterHandler) ListPresets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	response.Success(w, router.ListPresets())
}

// ApplyPreset materialises the named preset against the chosen outbound.
//
//	@Summary		Apply singbox-router preset
//	@Description	Materialises the preset (id) into rules + rulesets, routing matched traffic via the selected outbound. Existing rules with the same tag are overwritten.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		map[string]interface{}	true	"{id, outbound}"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/singbox/router/presets/apply [post]
func (h *SingboxRouterHandler) ApplyPreset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var body struct {
		ID       string `json:"id"`
		Outbound string `json:"outbound"`
	}
	if err := decodeBody(r, &body); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.ApplyPreset(r.Context(), body.ID, body.Outbound); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	response.Success(w, nil)
}

// PoliciesCollection routes by HTTP method:
//
//	GET  → ListPolicies (returns []router.PolicyInfo)
//	POST → CreatePolicy (body: {description}, returns router.PolicyInfo)
func (h *SingboxRouterHandler) PoliciesCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listPolicies(w, r)
	case http.MethodPost:
		h.createPolicy(w, r)
	default:
		response.MethodNotAllowed(w)
	}
}

// listPolicies returns all NDMS policies known to the singbox-router engine.
//
//	@Summary		List singbox-router policies
//	@Description	Returns all NDMS policies known to the singbox-router engine. Always a JSON array, never null.
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	map[string]interface{}
//	@Failure		500	{object}	map[string]interface{}
//	@Router			/singbox/router/policies [get]
func (h *SingboxRouterHandler) listPolicies(w http.ResponseWriter, r *http.Request) {
	policies, err := h.svc.ListPolicies(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	if policies == nil {
		policies = []router.PolicyInfo{}
	}
	response.Success(w, policies)
}

// createPolicy creates a new NDMS policy with the given description.
//
//	@Summary		Create singbox-router policy
//	@Description	Creates a new NDMS policy with the given description. Returns the created policy.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		map[string]interface{}	true	"{description}"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/singbox/router/policies [post]
func (h *SingboxRouterHandler) createPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Description string `json:"description"`
	}
	if err := decodeBody(r, &req); err != nil {
		response.BadRequest(w, "invalid body")
		return
	}
	policy, err := h.svc.CreatePolicy(r.Context(), req.Description)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, policy)
}

// ListPolicyDevices handles GET /api/singbox/router/policy-devices?name=X
//
//	@Summary		List singbox-router policy devices
//	@Description	Returns the LAN devices currently bound to the named policy. Always a JSON array, never null.
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Param			name	query		string	true	"Policy name"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/singbox/router/policy-devices [get]
func (h *SingboxRouterHandler) ListPolicyDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	policyName := r.URL.Query().Get("name")
	if policyName == "" {
		response.Error(w, "missing name parameter", "MISSING_NAME")
		return
	}
	devices, err := h.svc.ListPolicyDevices(r.Context(), policyName)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	if devices == nil {
		devices = []router.PolicyDevice{}
	}
	response.Success(w, devices)
}

// BindDevice handles POST /api/singbox/router/policy-devices/bind
//
//	@Summary		Bind device to singbox-router policy
//	@Description	Binds the LAN device (MAC) to the named policy. Replaces any existing binding.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		map[string]interface{}	true	"{mac, policyName}"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/singbox/router/policy-devices/bind [post]
func (h *SingboxRouterHandler) BindDevice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req struct {
		MAC        string `json:"mac"`
		PolicyName string `json:"policyName"`
	}
	if err := decodeBody(r, &req); err != nil {
		response.BadRequest(w, "invalid body")
		return
	}
	if err := h.svc.BindDevice(r.Context(), req.MAC, req.PolicyName); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]any{"success": true})
}

// UnbindDevice handles POST /api/singbox/router/policy-devices/unbind
//
//	@Summary		Unbind device from singbox-router policy
//	@Description	Removes any policy binding for the LAN device identified by MAC.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		map[string]interface{}	true	"{mac}"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/singbox/router/policy-devices/unbind [post]
func (h *SingboxRouterHandler) UnbindDevice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req struct {
		MAC string `json:"mac"`
	}
	if err := decodeBody(r, &req); err != nil {
		response.BadRequest(w, "invalid body")
		return
	}
	if err := h.svc.UnbindDevice(r.Context(), req.MAC); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]any{"success": true})
}

func decodeBody(r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	defer r.Body.Close()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, dst)
}

func (h *SingboxRouterHandler) handleErr(w http.ResponseWriter, action string, err error) {
	h.log.Warn(action, "", err.Error())
	switch {
	case errors.Is(err, router.ErrNetfilterComponentMissing),
		errors.Is(err, router.ErrIPTablesModTProxyMissing):
		response.Error(w, err.Error(), "NETFILTER_MISSING")
	case errors.Is(err, router.ErrRuleSetReferenced),
		errors.Is(err, router.ErrOutboundReferenced),
		errors.Is(err, router.ErrRuleSetTagConflict),
		errors.Is(err, router.ErrOutboundTagConflict),
		errors.Is(err, router.ErrDNSServerTagConflict),
		errors.Is(err, router.ErrDNSServerReferenced):
		response.Error(w, err.Error(), "CONFLICT")
	case errors.Is(err, router.ErrRuleIndexOutOfRange),
		errors.Is(err, router.ErrDNSRuleIndexOutOfRange),
		errors.Is(err, router.ErrDNSServerNotFound):
		response.Error(w, err.Error(), "NOT_FOUND")
	case errors.Is(err, router.ErrInvalidMatchers),
		errors.Is(err, router.ErrDNSInvalidServer):
		response.Error(w, err.Error(), "INVALID_MATCHERS")
	default:
		response.InternalError(w, err.Error())
	}
}
