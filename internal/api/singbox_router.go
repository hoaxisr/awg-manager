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

// ── Response DTOs ────────────────────────────────────────────────

// SingboxRouterIssueDTO mirrors router.Issue (one entry of Status.Issues).
type SingboxRouterIssueDTO struct {
	Severity  string `json:"severity" example:"warning"`
	Kind      string `json:"kind" example:"missing-outbound"`
	RuleIndex int    `json:"ruleIndex,omitempty" example:"0"`
	Tag       string `json:"tag,omitempty" example:"selector"`
	Message   string `json:"message" example:"outbound 'selector' is referenced but does not exist"`
}

// SingboxRouterStatusData mirrors router.Status.
type SingboxRouterStatusData struct {
	Enabled                bool                    `json:"enabled" example:"true"`
	Installed              bool                    `json:"installed" example:"true"`
	NetfilterAvailable     bool                    `json:"netfilterAvailable" example:"true"`
	NetfilterComponentName string                  `json:"netfilterComponentName,omitempty" example:"iptables-mod-tproxy"`
	TProxyTargetAvailable  bool                    `json:"tproxyTargetAvailable" example:"true"`
	PolicyName             string                  `json:"policyName" example:"awgm-router"`
	PolicyMark             string                  `json:"policyMark,omitempty" example:"0xffffaaa"`
	PolicyExists           bool                    `json:"policyExists" example:"true"`
	DeviceCount            int                     `json:"deviceCount" example:"3"`
	RuleCount              int                     `json:"ruleCount" example:"12"`
	RuleSetCount           int                     `json:"ruleSetCount" example:"4"`
	OutboundAWGCount       int                     `json:"outboundAwgCount" example:"2"`
	OutboundCompositeCount int                     `json:"outboundCompositeCount" example:"1"`
	Final                  string                  `json:"final" example:"direct"`
	Issues                 []SingboxRouterIssueDTO `json:"issues,omitempty"`
}

// SingboxRouterStatusResponse is the envelope for GET /singbox/router/status.
type SingboxRouterStatusResponse struct {
	Success bool                    `json:"success" example:"true"`
	Data    SingboxRouterStatusData `json:"data"`
}

// SingboxRouterSettingsData mirrors storage.SingboxRouterSettings.
type SingboxRouterSettingsData struct {
	Enabled         bool   `json:"enabled" example:"true"`
	PolicyName      string `json:"policyName" example:"awgm-router"`
	RefreshMode     string `json:"refreshMode,omitempty" example:"interval"`
	RefreshInterval int    `json:"refreshIntervalHours,omitempty" example:"24"`
	RefreshDaily    string `json:"refreshDailyTime,omitempty" example:"03:00"`
}

// SingboxRouterSettingsResponse is the envelope for GET /singbox/router/settings.
type SingboxRouterSettingsResponse struct {
	Success bool                      `json:"success" example:"true"`
	Data    SingboxRouterSettingsData `json:"data"`
}

// SingboxRouterRuleDTO mirrors router.Rule (a routing rule in priority order).
type SingboxRouterRuleDTO struct {
	DomainSuffix []string `json:"domain_suffix,omitempty" example:".example.com"`
	IPCIDR       []string `json:"ip_cidr,omitempty" example:"10.0.0.0/8"`
	SourceIPCIDR []string `json:"source_ip_cidr,omitempty" example:"192.168.1.100/32"`
	Port         []int    `json:"port,omitempty" example:"443"`
	RuleSet      []string `json:"rule_set,omitempty" example:"geosite-cn"`
	Protocol     string   `json:"protocol,omitempty" example:"tcp"`
	Action       string   `json:"action" example:"route"`
	Outbound     string   `json:"outbound,omitempty" example:"selector"`
}

// SingboxRouterRulesListResponse is the envelope for GET /singbox/router/rules/list.
type SingboxRouterRulesListResponse struct {
	Success bool                   `json:"success" example:"true"`
	Data    []SingboxRouterRuleDTO `json:"data"`
}

// SingboxRouterRuleSetDTO mirrors router.RuleSet.
type SingboxRouterRuleSetDTO struct {
	Tag            string `json:"tag" example:"geosite-cn"`
	Type           string `json:"type" example:"remote"`
	Format         string `json:"format" example:"binary"`
	URL            string `json:"url,omitempty" example:"https://cdn.example.com/geosite-cn.srs"`
	UpdateInterval string `json:"update_interval,omitempty" example:"24h"`
	DownloadDetour string `json:"download_detour,omitempty" example:"direct"`
	Path           string `json:"path,omitempty" example:"/opt/etc/singbox/rulesets/geosite-cn.srs"`
}

// SingboxRouterRuleSetsListResponse is the envelope for GET /singbox/router/rulesets/list.
type SingboxRouterRuleSetsListResponse struct {
	Success bool                      `json:"success" example:"true"`
	Data    []SingboxRouterRuleSetDTO `json:"data"`
}

// SingboxRouterOutboundDTO mirrors router.Outbound (composite outbound).
type SingboxRouterOutboundDTO struct {
	Type          string   `json:"type" example:"selector"`
	Tag           string   `json:"tag" example:"my-selector"`
	BindInterface string   `json:"bind_interface,omitempty" example:"awg-vpn0"`
	Outbounds     []string `json:"outbounds,omitempty" example:"awg-vpn0"`
	URL           string   `json:"url,omitempty" example:"https://www.gstatic.com/generate_204"`
	Interval      string   `json:"interval,omitempty" example:"3m"`
	Tolerance     int      `json:"tolerance,omitempty" example:"50"`
	Default       string   `json:"default,omitempty" example:"awg-vpn0"`
	Strategy      string   `json:"strategy,omitempty" example:"prefer_ipv4"`
}

// SingboxRouterOutboundsListResponse is the envelope for GET /singbox/router/outbounds/list.
type SingboxRouterOutboundsListResponse struct {
	Success bool                       `json:"success" example:"true"`
	Data    []SingboxRouterOutboundDTO `json:"data"`
}

// SingboxRouterPresetRuleRefDTO mirrors internalpresets.RuleRef.
type SingboxRouterPresetRuleRefDTO struct {
	Tag string `json:"tag" example:"geosite-cn"`
}

// SingboxRouterPresetRuleLinkDTO mirrors internalpresets.RuleLink.
type SingboxRouterPresetRuleLinkDTO struct {
	RuleSet      []string `json:"rule_set,omitempty" example:"geosite-cn"`
	DomainSuffix []string `json:"domain_suffix,omitempty" example:".cn"`
	Action       string   `json:"action,omitempty" example:"route"`
}

// SingboxRouterPresetDTO mirrors router.Preset (one entry of the preset catalog).
type SingboxRouterPresetDTO struct {
	ID        string                           `json:"id" example:"china-direct"`
	Name      string                           `json:"name" example:"China Direct"`
	IconSlug  string                           `json:"iconSlug,omitempty" example:"china"`
	RuleSets  []SingboxRouterPresetRuleRefDTO  `json:"ruleSets"`
	Rules     []SingboxRouterPresetRuleLinkDTO `json:"rules"`
	Notice    string                           `json:"notice,omitempty" example:"Routes mainland China traffic via the direct outbound."`
	Featured  bool                             `json:"featured,omitempty" example:"true"`
	Sensitive bool                             `json:"sensitive,omitempty" example:"false"`
}

// SingboxRouterPresetsListResponse is the envelope for GET /singbox/router/presets/list.
type SingboxRouterPresetsListResponse struct {
	Success bool                     `json:"success" example:"true"`
	Data    []SingboxRouterPresetDTO `json:"data"`
}

// SingboxRouterPolicyInfoDTO mirrors router.PolicyInfo (NDMS policy projection).
type SingboxRouterPolicyInfoDTO struct {
	Name         string `json:"name" example:"Policy0"`
	Description  string `json:"description" example:"Default policy"`
	Mark         string `json:"mark,omitempty" example:"0xffffaaa"`
	DeviceCount  int    `json:"deviceCount" example:"3"`
	IsOurDefault bool   `json:"isOurDefault" example:"false"`
}

// SingboxRouterPoliciesListResponse is the envelope for GET /singbox/router/policies.
type SingboxRouterPoliciesListResponse struct {
	Success bool                         `json:"success" example:"true"`
	Data    []SingboxRouterPolicyInfoDTO `json:"data"`
}

// SingboxRouterPolicyResponse is the envelope for POST /singbox/router/policies (single policy).
type SingboxRouterPolicyResponse struct {
	Success bool                       `json:"success" example:"true"`
	Data    SingboxRouterPolicyInfoDTO `json:"data"`
}

// SingboxRouterPolicyDeviceDTO mirrors router.PolicyDevice.
type SingboxRouterPolicyDeviceDTO struct {
	MAC   string `json:"mac" example:"aa:bb:cc:dd:ee:ff"`
	IP    string `json:"ip" example:"192.168.1.100"`
	Name  string `json:"name,omitempty" example:"My Phone"`
	Bound bool   `json:"bound" example:"true"`
}

// SingboxRouterPolicyDevicesListResponse is the envelope for GET /singbox/router/policy-devices.
type SingboxRouterPolicyDevicesListResponse struct {
	Success bool                           `json:"success" example:"true"`
	Data    []SingboxRouterPolicyDeviceDTO `json:"data"`
}

// ── Request DTOs ─────────────────────────────────────────────────

// SingboxRouterRuleUpdateRequest is the body for POST /singbox/router/rules/update.
type SingboxRouterRuleUpdateRequest struct {
	Index int                  `json:"index" example:"0"`
	Rule  SingboxRouterRuleDTO `json:"rule"`
}

// SingboxRouterRuleDeleteRequest is the body for POST /singbox/router/rules/delete.
type SingboxRouterRuleDeleteRequest struct {
	Index int `json:"index" example:"0"`
}

// SingboxRouterRuleMoveRequest is the body for POST /singbox/router/rules/move.
type SingboxRouterRuleMoveRequest struct {
	From int `json:"from" example:"3"`
	To   int `json:"to" example:"0"`
}

// SingboxRouterRuleSetDeleteRequest is the body for POST /singbox/router/rulesets/delete.
type SingboxRouterRuleSetDeleteRequest struct {
	Tag   string `json:"tag" example:"geosite-cn"`
	Force bool   `json:"force" example:"false"`
}

// SingboxRouterRuleSetRefreshRequest is the body for POST /singbox/router/rulesets/refresh.
type SingboxRouterRuleSetRefreshRequest struct {
	Tag string `json:"tag" example:"geosite-cn"`
}

// SingboxRouterOutboundUpdateRequest is the body for POST /singbox/router/outbounds/update.
type SingboxRouterOutboundUpdateRequest struct {
	Tag      string                   `json:"tag" example:"my-selector"`
	Outbound SingboxRouterOutboundDTO `json:"outbound"`
}

// SingboxRouterOutboundDeleteRequest is the body for POST /singbox/router/outbounds/delete.
type SingboxRouterOutboundDeleteRequest struct {
	Tag   string `json:"tag" example:"my-selector"`
	Force bool   `json:"force" example:"false"`
}

// SingboxRouterApplyPresetRequest is the body for POST /singbox/router/presets/apply.
type SingboxRouterApplyPresetRequest struct {
	ID       string `json:"id" example:"china-direct"`
	Outbound string `json:"outbound" example:"awg-vpn0"`
}

// SingboxRouterCreatePolicyRequest is the body for POST /singbox/router/policies.
type SingboxRouterCreatePolicyRequest struct {
	Description string `json:"description" example:"My VPN policy"`
}

// SingboxRouterBindDeviceRequest is the body for POST /singbox/router/policy-devices/bind.
type SingboxRouterBindDeviceRequest struct {
	MAC        string `json:"mac" example:"aa:bb:cc:dd:ee:ff"`
	PolicyName string `json:"policyName" example:"Policy0"`
}

// SingboxRouterUnbindDeviceRequest is the body for POST /singbox/router/policy-devices/unbind.
type SingboxRouterUnbindDeviceRequest struct {
	MAC string `json:"mac" example:"aa:bb:cc:dd:ee:ff"`
}

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
//	@Success		200	{object}	SingboxRouterStatusResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
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
//	@Success		200	{object}	OkResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
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
	response.Success(w, map[string]bool{"ok": true})
}

// Disable stops the singbox-router engine and uninstalls iptables/policy rules.
//
//	@Summary		Disable singbox-router
//	@Description	Stops the singbox-router engine and uninstalls iptables/policy rules. Idempotent.
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	OkResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
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
	response.Success(w, map[string]bool{"ok": true})
}

// GetSettings reads singbox-router settings (policy-mode, defaults, etc.).
//
//	@Summary		Get singbox-router settings
//	@Description	Reads the current singbox-router settings (policy mode, defaults, ...).
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	SingboxRouterSettingsResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
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
//	@Param			body	body		SingboxRouterSettingsData	true	"Singbox-router settings payload"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		405		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
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
	response.Success(w, map[string]bool{"ok": true})
}

// ListRules returns all singbox-router routing rules in priority order.
//
//	@Summary		List singbox-router rules
//	@Description	Returns all routing rules in priority (top-first) order.
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	SingboxRouterRulesListResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
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
//	@Param			body	body		SingboxRouterRuleDTO	true	"Routing rule payload"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
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
	response.Success(w, map[string]bool{"ok": true})
}

// UpdateRule replaces a rule at the given index with the provided one.
//
//	@Summary		Update singbox-router rule
//	@Description	Replaces the rule at index with the provided one. Index is the priority slot (0-based).
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxRouterRuleUpdateRequest	true	"Index + replacement rule"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
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
	response.Success(w, map[string]bool{"ok": true})
}

// DeleteRule removes the rule at the given index.
//
//	@Summary		Delete singbox-router rule
//	@Description	Removes the rule at the given index (0-based priority slot).
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxRouterRuleDeleteRequest	true	"Index of the rule to remove"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
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
	response.Success(w, map[string]bool{"ok": true})
}

// MoveRule moves the rule from one priority slot to another.
//
//	@Summary		Move singbox-router rule
//	@Description	Moves the rule from index `from` to index `to` (both 0-based). Adjusts other rules' indices accordingly.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxRouterRuleMoveRequest	true	"From-index and to-index"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
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
	response.Success(w, map[string]bool{"ok": true})
}

// ListRuleSets returns all configured rulesets.
//
//	@Summary		List singbox-router rulesets
//	@Description	Returns all configured rulesets (downloaded geo files / inline lists), with their tag, type, and freshness metadata.
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	SingboxRouterRuleSetsListResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
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
//	@Param			body	body		SingboxRouterRuleSetDTO	true	"RuleSet payload"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
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
	response.Success(w, map[string]bool{"ok": true})
}

// DeleteRuleSet removes the ruleset identified by tag.
//
//	@Summary		Delete singbox-router ruleset
//	@Description	Removes the ruleset identified by tag. Refuses if any rule references it; pass force=true to ignore references.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxRouterRuleSetDeleteRequest	true	"Tag + optional force flag"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		409		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
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
	response.Success(w, map[string]bool{"ok": true})
}

// RefreshRuleSet re-downloads the ruleset identified by tag.
//
//	@Summary		Refresh singbox-router ruleset
//	@Description	Re-downloads the remote ruleset identified by tag and updates its content/timestamp.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxRouterRuleSetRefreshRequest	true	"Ruleset tag to re-download"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
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
	response.Success(w, map[string]bool{"ok": true})
}

// ListOutbounds returns all composite outbounds.
//
//	@Summary		List singbox-router outbounds
//	@Description	Returns all composite outbounds (sing-box selectors/urltests over multiple base outbounds).
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	SingboxRouterOutboundsListResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
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
//	@Param			body	body		SingboxRouterOutboundDTO	true	"Composite outbound payload"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
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
	response.Success(w, map[string]bool{"ok": true})
}

// UpdateOutbound replaces the composite outbound identified by tag.
//
//	@Summary		Update singbox-router outbound
//	@Description	Replaces the composite outbound identified by tag with the provided one.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxRouterOutboundUpdateRequest	true	"Tag + replacement outbound"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
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
	response.Success(w, map[string]bool{"ok": true})
}

// DeleteOutbound removes the composite outbound identified by tag.
//
//	@Summary		Delete singbox-router outbound
//	@Description	Removes the composite outbound identified by tag. Refuses if any rule references it; pass force=true to override.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxRouterOutboundDeleteRequest	true	"Tag + optional force flag"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		409		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
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
	response.Success(w, map[string]bool{"ok": true})
}

// ListPresets returns the catalog of built-in singbox-router presets.
//
//	@Summary		List singbox-router presets
//	@Description	Returns the catalog of built-in presets the user can apply (each preset = a curated bundle of rules + rulesets).
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	SingboxRouterPresetsListResponse
//	@Failure		405	{object}	APIErrorEnvelope
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
//	@Param			body	body		SingboxRouterApplyPresetRequest	true	"Preset id + target outbound"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
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
	response.Success(w, map[string]bool{"ok": true})
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
//	@Success		200	{object}	SingboxRouterPoliciesListResponse
//	@Failure		500	{object}	APIErrorEnvelope
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
//	@Param			body	body		SingboxRouterCreatePolicyRequest	true	"Policy description"
//	@Success		200		{object}	SingboxRouterPolicyResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
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
//	@Success		200		{object}	SingboxRouterPolicyDevicesListResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
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
//	@Param			body	body		SingboxRouterBindDeviceRequest	true	"Device MAC + target policy name"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
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
	response.Success(w, map[string]bool{"ok": true})
}

// UnbindDevice handles POST /api/singbox/router/policy-devices/unbind
//
//	@Summary		Unbind device from singbox-router policy
//	@Description	Removes any policy binding for the LAN device identified by MAC.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxRouterUnbindDeviceRequest	true	"Device MAC"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
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
	response.Success(w, map[string]bool{"ok": true})
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
