package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/singbox/subscription"
)

// SubscriptionGroupMemberDTO — превью одного разрешённого члена группы
// (дёшево собирается из store, без обращения к sing-box).
type SubscriptionGroupMemberDTO struct {
	Tag   string `json:"tag" example:"sub-abc12345-aabbccdd"`
	Label string `json:"label,omitempty" example:"🇩🇪 Frankfurt-1"`
}

// SubscriptionGroupDTO mirrors subscription.AggregateGroup for OpenAPI exposure.
// MemberCount / Members — серверное разрешение состава на момент запроса.
type SubscriptionGroupDTO struct {
	ID                 string                       `json:"id" example:"a1b2c3d4e5f60718293a4b5c"`
	Label              string                       `json:"label" example:"Все европейские"`
	Tag                string                       `json:"tag" example:"agg-a1b2c3d4"`
	InboundTag         string                       `json:"inboundTag" example:"agg-a1b2c3d4-in"`
	ListenPort         int                          `json:"listenPort" example:"11002"`
	ProxyIndex         int                          `json:"proxyIndex" example:"2" description:"NDMS ProxyN группы. -1 когда прокси не выделен ИЛИ глобальный тумблер NDMS Proxy выключен (гейтится как у подписок)."`
	Mode               string                       `json:"mode" example:"urltest"`
	URLTest            *SubscriptionURLTestDTO      `json:"urlTest,omitempty"`
	UseSubscriptionIDs []string                     `json:"useSubscriptionIds"`
	FilterInclude      string                       `json:"filterInclude,omitempty" example:"(?i)(DE|NL)"`
	FilterExclude      string                       `json:"filterExclude,omitempty" example:"(?i)(RU|Russia)"`
	Enabled            bool                         `json:"enabled" example:"true"`
	MemberCount        int                          `json:"memberCount" example:"12"`
	Members            []SubscriptionGroupMemberDTO `json:"members"`
}

// SubscriptionGroupListResponse is the envelope for GET /api/singbox/subscriptions/groups.
type SubscriptionGroupListResponse struct {
	Success bool                   `json:"success" example:"true"`
	Data    []SubscriptionGroupDTO `json:"data"`
}

// SubscriptionGroupResponse is the envelope for single-group responses.
type SubscriptionGroupResponse struct {
	Success bool                 `json:"success" example:"true"`
	Data    SubscriptionGroupDTO `json:"data"`
}

// CreateSubscriptionGroupRequest is the body for POST /api/singbox/subscriptions/groups/create.
type CreateSubscriptionGroupRequest struct {
	Label string `json:"label" example:"Все европейские"`
	// Tag — пользовательский outbound-тег (#572); пусто = авто "agg-<id8>".
	Tag                string                  `json:"tag,omitempty" example:"eu-all"`
	Mode               string                  `json:"mode,omitempty" example:"urltest"` // "urltest" (default) | "selector"
	URLTest            *SubscriptionURLTestDTO `json:"urlTest,omitempty"`
	UseSubscriptionIDs []string                `json:"useSubscriptionIds"`
	FilterInclude      string                  `json:"filterInclude,omitempty"`
	FilterExclude      string                  `json:"filterExclude,omitempty"`
	Enabled            bool                    `json:"enabled" example:"true"`
}

// UpdateSubscriptionGroupRequest is the body for PUT /api/singbox/subscriptions/groups/update.
// All fields are optional; absent fields leave the stored value unchanged.
type UpdateSubscriptionGroupRequest struct {
	Label              *string                 `json:"label,omitempty"`
	Mode               *string                 `json:"mode,omitempty" example:"selector"`
	URLTest            *SubscriptionURLTestDTO `json:"urlTest,omitempty"`
	UseSubscriptionIDs *[]string               `json:"useSubscriptionIds,omitempty"`
	FilterInclude      *string                 `json:"filterInclude,omitempty"`
	FilterExclude      *string                 `json:"filterExclude,omitempty"`
	Enabled            *bool                   `json:"enabled,omitempty"`
}

// DeleteSubscriptionGroupRequest is the body for POST /api/singbox/subscriptions/groups/delete.
type DeleteSubscriptionGroupRequest struct {
	ID string `json:"id" example:"a1b2c3d4e5f60718293a4b5c"`
}

// parseGroupMode валидирует mode группы: пустая строка = urltest
// (основной кейс — авто-выбор быстрейшего среди всех подписок).
func parseGroupMode(s string) (subscription.SubscriptionMode, error) {
	switch s {
	case "":
		return subscription.ModeURLTest, nil
	case string(subscription.ModeSelector):
		return subscription.ModeSelector, nil
	case string(subscription.ModeURLTest):
		return subscription.ModeURLTest, nil
	default:
		return "", errors.New("invalid mode (expected \"selector\" or \"urltest\")")
	}
}

// toSubscriptionGroupDTO конвертирует доменную группу в API-представление.
// resolved — серверное разрешение состава (nil при битом фильтре из
// руками отредактированного store — тогда memberCount=0).
func (h *SubscriptionHandler) toSubscriptionGroupDTO(g subscription.AggregateGroup) SubscriptionGroupDTO {
	resolved, err := h.svc.ResolveGroupMembers(g)
	if err != nil {
		resolved = nil
	}
	members := make([]SubscriptionGroupMemberDTO, len(resolved))
	for i, m := range resolved {
		members[i] = SubscriptionGroupMemberDTO{Tag: m.Tag, Label: m.Label}
	}
	var urltest *SubscriptionURLTestDTO
	if g.EffectiveMode() == subscription.ModeURLTest {
		ut := g.EffectiveURLTest()
		urltest = &SubscriptionURLTestDTO{
			URL:         ut.URL,
			IntervalSec: ut.IntervalSec,
			ToleranceMs: ut.ToleranceMs,
		}
	}
	proxyIdx := g.ProxyIndex
	if !h.ndmsProxyEnabled() {
		proxyIdx = -1
	}
	useIDs := g.UseSubscriptionIDs
	if useIDs == nil {
		useIDs = []string{}
	}
	return SubscriptionGroupDTO{
		ID:                 g.ID,
		Label:              g.Label,
		Tag:                g.Tag,
		InboundTag:         g.InboundTag,
		ListenPort:         int(g.ListenPort),
		ProxyIndex:         proxyIdx,
		Mode:               string(g.EffectiveMode()),
		URLTest:            urltest,
		UseSubscriptionIDs: useIDs,
		FilterInclude:      g.FilterInclude,
		FilterExclude:      g.FilterExclude,
		Enabled:            g.Enabled,
		MemberCount:        len(members),
		Members:            members,
	}
}

// respondGroupServiceError маппит ошибки Group-CRUD на HTTP-статусы
// и журналит сбой мутации.
func (h *SubscriptionHandler) respondGroupServiceError(w http.ResponseWriter, action string, err error) {
	// См. respondServiceError: клиентские 4xx — Debug, внутренние — Warn.
	var filterErr *subscription.FilterError
	isInternal := !errors.As(err, &filterErr) &&
		!errors.Is(err, subscription.ErrGroupSubscriptionNotFound) &&
		!errors.Is(err, subscription.ErrValidation) &&
		!errors.Is(err, subscription.ErrGroupNotFound)
	if isInternal {
		h.log.Warn(action, "", err.Error())
	} else {
		h.log.Debug(action, "", err.Error())
	}
	switch {
	case errors.As(err, &filterErr):
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "INVALID_FILTER")
	case errors.Is(err, subscription.ErrGroupSubscriptionNotFound):
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "GROUP_SUBSCRIPTION_NOT_FOUND")
	case errors.Is(err, subscription.ErrValidation):
		response.ErrorWithStatus(w, http.StatusUnprocessableEntity, err.Error(), "VALIDATION_FAILED")
	case errors.Is(err, subscription.ErrGroupNotFound):
		response.ErrorWithStatus(w, http.StatusNotFound, err.Error(), "NOT_FOUND")
	default:
		response.InternalError(w, err.Error())
	}
}

// ListGroups handles GET /api/singbox/subscriptions/groups
//
//	@Summary		List aggregate subscription groups
//	@Description	Returns configured aggregate groups (one selector/urltest across several subscriptions) with server-side resolved member previews.
//	@Tags			subscriptions
//	@Produce		json
//	@Success		200	{object}	SubscriptionGroupListResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Router			/singbox/subscriptions/groups [get]
func (h *SubscriptionHandler) ListGroups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	all := []SubscriptionGroupDTO{}
	for _, g := range h.svc.ListGroups() {
		all = append(all, h.toSubscriptionGroupDTO(g))
	}
	response.Success(w, all)
}

// CreateGroup handles POST /api/singbox/subscriptions/groups/create
//
//	@Summary		Create aggregate subscription group
//	@Description	Creates a selector/urltest group over members of several subscriptions with optional regex filters. Invalid regex or unknown subscription id → 400.
//	@Tags			subscriptions
//	@Accept			json
//	@Produce		json
//	@Param			req	body		CreateSubscriptionGroupRequest	true	"create request"
//	@Success		200	{object}	SubscriptionGroupResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		422	{object}	APIErrorEnvelope	"sing-box validation rejected the group"
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/subscriptions/groups/create [post]
func (h *SubscriptionHandler) CreateGroup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req CreateSubscriptionGroupRequest
	if err := decodeBody(r, &req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "bad request body", "INVALID_JSON")
		return
	}
	if strings.TrimSpace(req.Label) == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "название группы не может быть пустым", "INVALID_LABEL")
		return
	}
	mode, err := parseGroupMode(req.Mode)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "INVALID_MODE")
		return
	}
	g, err := h.svc.CreateGroup(r.Context(), subscription.GroupCreateInput{
		Label:              req.Label,
		Tag:                strings.TrimSpace(req.Tag),
		Mode:               mode,
		URLTest:            urlTestDTOToConfig(req.URLTest),
		UseSubscriptionIDs: req.UseSubscriptionIDs,
		FilterInclude:      req.FilterInclude,
		FilterExclude:      req.FilterExclude,
		Enabled:            req.Enabled,
	})
	if err != nil {
		h.respondGroupServiceError(w, "subscription-group-create", err)
		return
	}
	h.log.Info("subscription-group-create", g.Label, "Subscription group created: "+g.Label)
	response.Success(w, h.toSubscriptionGroupDTO(*g))
}

// UpdateGroup handles PUT /api/singbox/subscriptions/groups/update?id=
//
//	@Summary		Update aggregate subscription group
//	@Description	Applies a partial patch (label, mode, urltest, subscriptions, filters, enabled) and re-materializes the group with a single reload.
//	@Tags			subscriptions
//	@Accept			json
//	@Produce		json
//	@Param			id	query		string							true	"group id"
//	@Param			req	body		UpdateSubscriptionGroupRequest	true	"update request"
//	@Success		200	{object}	SubscriptionGroupResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		404	{object}	APIErrorEnvelope
//	@Failure		422	{object}	APIErrorEnvelope	"sing-box validation rejected the group"
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/subscriptions/groups/update [put]
func (h *SubscriptionHandler) UpdateGroup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		response.MethodNotAllowed(w)
		return
	}
	id, ok := requireQueryID(w, r)
	if !ok {
		return
	}
	var req UpdateSubscriptionGroupRequest
	if err := decodeBody(r, &req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "bad request body", "INVALID_JSON")
		return
	}
	patch := subscription.GroupUpdatePatch{
		Label:              req.Label,
		UseSubscriptionIDs: req.UseSubscriptionIDs,
		FilterInclude:      req.FilterInclude,
		FilterExclude:      req.FilterExclude,
		Enabled:            req.Enabled,
	}
	if req.Mode != nil {
		mode, err := parseGroupMode(*req.Mode)
		if err != nil {
			response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "INVALID_MODE")
			return
		}
		patch.Mode = &mode
	}
	if req.URLTest != nil {
		patch.URLTest = urlTestDTOToConfig(req.URLTest)
	}
	g, err := h.svc.UpdateGroup(r.Context(), id, patch)
	if err != nil {
		h.respondGroupServiceError(w, "subscription-group-update", err)
		return
	}
	h.log.Info("subscription-group-update", g.Label, "Subscription group updated: "+g.Label)
	response.Success(w, h.toSubscriptionGroupDTO(*g))
}

// DeleteGroup handles POST /api/singbox/subscriptions/groups/delete
//
//	@Summary		Delete aggregate subscription group
//	@Description	Removes the group outbound, its mixed inbound / route rule and the NDMS ProxyN, then drops the group from storage.
//	@Tags			subscriptions
//	@Accept			json
//	@Produce		json
//	@Param			req	body		DeleteSubscriptionGroupRequest	true	"group id"
//	@Success		200	{object}	APIEnvelope
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		404	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/subscriptions/groups/delete [post]
func (h *SubscriptionHandler) DeleteGroup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req DeleteSubscriptionGroupRequest
	if err := decodeBody(r, &req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "bad request body", "INVALID_JSON")
		return
	}
	if strings.TrimSpace(req.ID) == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "id required", "MISSING_ID")
		return
	}
	label := req.ID
	if g, err := h.svc.GetGroup(req.ID); err == nil && g.Label != "" {
		label = g.Label
	}
	if err := h.svc.DeleteGroup(r.Context(), req.ID); err != nil {
		h.respondGroupServiceError(w, "subscription-group-delete", err)
		return
	}
	h.log.Info("subscription-group-delete", req.ID, "Subscription group deleted: "+label)
	response.Success(w, struct {
		OK bool `json:"ok"`
	}{true})
}
