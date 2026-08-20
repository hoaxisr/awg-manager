package api

import (
	"errors"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/singbox/subscription"
	tunnelservice "github.com/hoaxisr/awg-manager/internal/tunnel/service"
)

// SingboxPresenceProbe reports whether the managed sing-box binary is
// available on disk. Subscriptions register an NDMS Proxy interface
// pointing at sing-box's inbound listener, so creating one without
// sing-box installed leaves the Proxy slot pointing at nothing.
type SingboxPresenceProbe interface {
	IsPresent() bool
}

// SubscriptionHandler exposes /api/singbox/subscriptions/* endpoints.
type SubscriptionHandler struct {
	svc             *subscription.Service
	presence        SingboxPresenceProbe
	log             *logging.ScopedLogger
	deviceProxyRefs tunnelservice.DeviceProxyRefChecker
	routerRefs      tunnelservice.RouterRefChecker
	// settings reads the global "create NDMS Proxy for sing-box" flag.
	// When false, response DTOs surface proxyIndex=-1 so subscription
	// cards hide t2sN/ProxyN labels and disable speedtest — the NDMS
	// composite interfaces those rely on no longer exist. nil ⇒ default
	// to "enabled" (back-compat / tests).
	settings ndmsProxyToggler
}

func NewSubscriptionHandler(svc *subscription.Service, presence SingboxPresenceProbe, appLogger ...logging.AppLogger) *SubscriptionHandler {
	var lg logging.AppLogger
	if len(appLogger) > 0 {
		lg = appLogger[0]
	}
	return &SubscriptionHandler{
		svc:      svc,
		presence: presence,
		// Действия пользователя над подписками — в главный app-журнал
		// (routing/subscription), а не в изолированный singbox-бакет.
		log: logging.NewScopedLogger(lg, logging.GroupRouting, logging.SubSubscription),
	}
}

// SetNDMSProxyToggler wires the global NDMS Proxy flag reader. When wired
// and the flag is false, DTO converters surface proxyIndex=-1 so the UI
// (and any other API consumer) sees that the composite NDMS Proxy is
// gone — same shape contract as ListTunnels uses for tunnel ProxyInterface
// fields. Without this setter, every subscription DTO surfaces the stored
// ProxyIndex unconditionally.
func (h *SubscriptionHandler) SetNDMSProxyToggler(s ndmsProxyToggler) { h.settings = s }

// SetOutboundRefCheckers wires device-proxy and router reference guards for
// subscription deletion (refuse when selector/members are still referenced).
func (h *SubscriptionHandler) SetOutboundRefCheckers(dp tunnelservice.DeviceProxyRefChecker, r tunnelservice.RouterRefChecker) {
	h.deviceProxyRefs = dp
	h.routerRefs = r
}

// ndmsProxyEnabled reads the toggler; defaults to true when the toggler
// is not wired (tests, legacy bootstrap paths).
func (h *SubscriptionHandler) ndmsProxyEnabled() bool {
	if h.settings == nil {
		return true
	}
	return h.settings.IsSingboxNDMSProxyEnabled()
}

// respondServiceError routes a subscription.Service mutation error to
// the appropriate HTTP status. subscription.ErrValidation (Pass-2 sing-
// box check rejected the merged config) → 422 VALIDATION_FAILED so the
// frontend can surface a "your subscription has invalid outbound(s)"
// banner instead of a generic 500. Other errors fall through to 500.
func (h *SubscriptionHandler) respondServiceError(w http.ResponseWriter, action string, err error) {
	// Ошибки клиента (4xx: валидация, фильтры, not-found) — Debug: ответ
	// пользователь уже видит в UI, а Warn всегда видим и содержит
	// свободный ввод (не сворачивается коалесцером). Warn — только для
	// внутренних сбоев (default → 500).
	var filterErr *subscription.FilterError
	isInternal := !errors.As(err, &filterErr) &&
		!errors.Is(err, subscription.ErrValidation) &&
		!errors.Is(err, subscription.ErrExcludeOnInline) &&
		!errors.Is(err, subscription.ErrAllMembersExcluded) &&
		!errors.Is(err, subscription.ErrAllMembersFiltered) &&
		!errors.Is(err, subscription.ErrMemberNotFound)
	if isInternal {
		h.log.Warn(action, "", err.Error())
	} else {
		h.log.Debug(action, "", err.Error())
	}
	switch {
	case errors.As(err, &filterErr):
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "INVALID_FILTER")
	case errors.Is(err, subscription.ErrValidation):
		response.ErrorWithStatus(w, http.StatusUnprocessableEntity, err.Error(), "VALIDATION_FAILED")
	case errors.Is(err, subscription.ErrExcludeOnInline):
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "EXCLUDE_ON_INLINE")
	case errors.Is(err, subscription.ErrAllMembersExcluded):
		response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "ALL_MEMBERS_EXCLUDED")
	case errors.Is(err, subscription.ErrAllMembersFiltered):
		response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "ALL_MEMBERS_FILTERED")
	case errors.Is(err, subscription.ErrMemberNotFound):
		response.ErrorWithStatus(w, http.StatusNotFound, err.Error(), "MEMBER_NOT_FOUND")
	default:
		response.InternalError(w, err.Error())
	}
}

// List handles GET /api/singbox/subscriptions
//
//	@Summary		List sing-box subscriptions
//	@Description	Returns configured subscriptions with parsed members. Mocked examples include vless/reality members.
//	@Tags			subscriptions
//	@Produce		json
//	@Success		200	{object}	SubscriptionListResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Router			/singbox/subscriptions [get]
func (h *SubscriptionHandler) List(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	all := []SubscriptionDTO{}
	for _, s := range h.svc.List() {
		all = append(all, toSubscriptionDTO(s, h.ndmsProxyEnabled()))
	}
	response.Success(w, all)
}

// Create handles POST /api/singbox/subscriptions/create
//
//	@Summary		Create sing-box subscription
//	@Description	Creates subscription from URL or inline share links. Returns 422 VALIDATION_FAILED when the merged sing-box config is rejected by `sing-box check` (e.g. reality outbound without uTLS).
//	@Tags			subscriptions
//	@Accept			json
//	@Produce		json
//	@Param			req	body		CreateSubscriptionRequest	true	"create request"
//	@Success		200	{object}	SubscriptionResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		409	{object}	APIErrorEnvelope	"фильтр и исключения скрывают все серверы (ALL_MEMBERS_FILTERED)"
//	@Failure		412	{object}	APIErrorEnvelope
//	@Failure		422	{object}	APIErrorEnvelope	"sing-box validation rejected the subscription"
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/subscriptions/create [post]
func (h *SubscriptionHandler) Create(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.presence != nil && !h.presence.IsPresent() {
		h.log.Warn("subscription-create", "", "rejected: sing-box is not installed")
		response.ErrorWithStatus(w, http.StatusPreconditionFailed,
			"Sing-box не установлен — установите перед добавлением подписки",
			"SINGBOX_NOT_INSTALLED")
		return
	}
	var req CreateSubscriptionRequest
	if err := decodeBody(r, &req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "bad request body", "INVALID_JSON")
		return
	}
	if err := validateSubscriptionHeaders(req.Headers); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "INVALID_HEADERS")
		return
	}
	mode, err := parseSubscriptionMode(req.Mode)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "INVALID_MODE")
		return
	}
	in := subscription.CreateInput{
		Label:         req.Label,
		URL:           req.URL,
		Inline:        req.Inline,
		Headers:       fromSubscriptionHeaders(req.Headers),
		RefreshHours:  req.RefreshHours,
		Enabled:       req.Enabled,
		Mode:          mode,
		URLTest:       urlTestDTOToConfig(req.URLTest),
		ExcludedKeys:  req.ExcludedKeys,
		FilterInclude: req.FilterInclude,
		FilterExclude: req.FilterExclude,
		BindInterface: req.BindInterface,
	}
	sub, err := h.svc.Create(r.Context(), in)
	if err != nil {
		h.respondServiceError(w, "subscription-create", err)
		return
	}
	h.log.Info("subscription-create", sub.Label, "Subscription created: "+sub.Label)
	response.Success(w, toSubscriptionDTO(*sub, h.ndmsProxyEnabled()))
}

// Get handles GET /api/singbox/subscriptions/get?id=
//
//	@Summary		Get sing-box subscription
//	@Tags			subscriptions
//	@Produce		json
//	@Param			id	query		string	true	"subscription id"
//	@Success		200	{object}	SubscriptionResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		404	{object}	APIErrorEnvelope
//	@Router			/singbox/subscriptions/get [get]
func (h *SubscriptionHandler) Get(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	id, ok := requireQueryID(w, r)
	if !ok {
		return
	}
	sub, err := h.svc.Get(id)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusNotFound, err.Error(), "NOT_FOUND")
		return
	}
	response.Success(w, toSubscriptionDTO(*sub, h.ndmsProxyEnabled()))
}

// Update handles PUT /api/singbox/subscriptions/update?id=
//
//	@Summary		Update sing-box subscription
//	@Description	Updates subscription metadata or refreshes from a new URL. Returns 422 VALIDATION_FAILED when the new merged config is rejected by `sing-box check`.
//	@Tags			subscriptions
//	@Accept			json
//	@Produce		json
//	@Param			id	query		string						true	"subscription id"
//	@Param			req	body		UpdateSubscriptionRequest	true	"update request"
//	@Success		200	{object}	SubscriptionResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		409	{object}	APIErrorEnvelope	"фильтр и исключения скрывают все серверы (ALL_MEMBERS_FILTERED)"
//	@Failure		422	{object}	APIErrorEnvelope	"sing-box validation rejected the subscription"
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/subscriptions/update [put]
func (h *SubscriptionHandler) Update(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		response.MethodNotAllowed(w)
		return
	}
	id, ok := requireQueryID(w, r)
	if !ok {
		return
	}
	var req UpdateSubscriptionRequest
	if err := decodeBody(r, &req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "bad request body", "INVALID_JSON")
		return
	}
	if req.Headers != nil {
		if err := validateSubscriptionHeaders(*req.Headers); err != nil {
			response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "INVALID_HEADERS")
			return
		}
	}
	patch := subscription.UpdatePatch{
		Label:         req.Label,
		URL:           req.URL,
		RefreshHours:  req.RefreshHours,
		Enabled:       req.Enabled,
		FilterInclude: req.FilterInclude,
		FilterExclude: req.FilterExclude,
		BindInterface: req.BindInterface,
	}
	if req.Headers != nil {
		hh := fromSubscriptionHeaders(*req.Headers)
		patch.Headers = &hh
	}
	if req.Mode != nil {
		mode, err := parseSubscriptionMode(*req.Mode)
		if err != nil {
			response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "INVALID_MODE")
			return
		}
		patch.Mode = &mode
	}
	if req.URLTest != nil {
		patch.URLTest = urlTestDTOToConfig(req.URLTest)
	}
	sub, err := h.svc.Update(id, patch)
	if err != nil {
		h.respondServiceError(w, "subscription-update", err)
		return
	}
	h.log.Info("subscription-update", sub.Label, "Subscription updated: "+sub.Label)
	response.Success(w, toSubscriptionDTO(*sub, h.ndmsProxyEnabled()))
}

// Delete handles DELETE /api/singbox/subscriptions/delete?id=  Always performs full cleanup (no cascade flag).
//
//	@Summary		Delete sing-box subscription
//	@Tags			subscriptions
//	@Produce		json
//	@Param			id	query		string	true	"subscription id"
//	@Success		200	{object}	APIEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/subscriptions/delete [delete]
func (h *SubscriptionHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		response.MethodNotAllowed(w)
		return
	}
	id, ok := requireQueryID(w, r)
	if !ok {
		return
	}
	label := id
	if sub, err := h.svc.Get(id); err == nil {
		if sub.Label != "" {
			label = sub.Label
		}
		tags := make([]string, 0, 1+len(sub.MemberTags))
		if sub.SelectorTag != "" {
			tags = append(tags, sub.SelectorTag)
		}
		tags = append(tags, sub.MemberTags...)
		if err := tunnelservice.CheckOutboundTagsReferenced(id, tags, h.deviceProxyRefs, h.routerRefs); err != nil {
			var refErr tunnelservice.ErrTunnelReferenced
			if errors.As(err, &refErr) {
				h.log.Info("subscription-delete", id, "Refused: "+refErr.Error())
				WriteTunnelReferenced(w, refErr)
				return
			}
		}
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		if errors.Is(err, subscription.ErrSubscriptionNotFound) {
			h.log.Info("subscription-delete", id, "Subscription already deleted: "+label)
			response.Success(w, struct {
				OK bool `json:"ok"`
			}{true})
			return
		}
		response.InternalError(w, err.Error())
		return
	}
	h.log.Info("subscription-delete", id, "Subscription deleted: "+label)
	response.Success(w, struct {
		OK bool `json:"ok"`
	}{true})
}

// SubscriptionRefreshData mirrors subscription.RefreshResult for swagger
// (the subscription package is outside swag's parse dirs).
type SubscriptionRefreshData struct {
	When             string   `json:"when" example:"2026-07-20T12:00:00Z"`
	Added            int      `json:"added" example:"1"`
	Updated          int      `json:"updated" example:"2"`
	Orphaned         int      `json:"orphaned" example:"0"`
	SkippedVmess     int      `json:"skippedVmess" example:"0"`
	SkippedOther     int      `json:"skippedOther" example:"0"`
	SkippedDuplicate int      `json:"skippedDuplicate" example:"0"`
	ParseErrors      []string `json:"parseErrors,omitempty"`
}

// Refresh handles POST /api/singbox/subscriptions/refresh?id=
//
//	@Summary		Refresh sing-box subscription
//	@Description	Re-fetches the provider URL and re-runs Pass 1 / Pass 2 validation. Returns 422 VALIDATION_FAILED when the refreshed config is rejected by `sing-box check`.
//	@Tags			subscriptions
//	@Produce		json
//	@Param			id	query		string	false	"subscription id"
//	@Success		200	{object}	APIEnvelope{data=SubscriptionRefreshData}
//	@Failure		409	{object}	APIErrorEnvelope	"фильтр и исключения скрывают все серверы (ALL_MEMBERS_FILTERED)"
//	@Failure		422	{object}	APIErrorEnvelope	"sing-box validation rejected the refreshed subscription"
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/subscriptions/refresh [post]
func (h *SubscriptionHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	id := r.URL.Query().Get("id")
	h.log.Info("subscription-refresh", id, "requested via API")
	res, err := h.svc.Refresh(r.Context(), id)
	if err != nil {
		h.respondServiceError(w, "subscription-refresh", err)
		return
	}
	response.Success(w, res)
}
