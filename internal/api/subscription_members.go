package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/singbox/subscription"
)

// ActiveMember handles POST /api/singbox/subscriptions/active-member?id=
//
//	@Summary		Set active member
//	@Tags			subscriptions
//	@Accept			json
//	@Produce		json
//	@Param			id	query		string				true	"subscription id"
//	@Param			req	body		ActiveMemberRequest	true	"member tag"
//	@Success		200	{object}	OkResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/subscriptions/active-member [post]
func (h *SubscriptionHandler) ActiveMember(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	id, ok := requireQueryID(w, r)
	if !ok {
		return
	}
	h.log.Info("subscription-active-member", id, "requested via API")
	var req ActiveMemberRequest
	if err := decodeBody(r, &req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "bad request body", "INVALID_JSON")
		return
	}
	if err := h.svc.SetActiveMember(r.Context(), id, req.MemberTag); err != nil {
		if errors.Is(err, subscription.ErrActiveMemberOnURLTest) {
			response.ErrorWithStatus(w, http.StatusConflict, err.Error(),
				"ACTIVE_MEMBER_ON_URLTEST")
			return
		}
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, struct {
		OK bool `json:"ok"`
	}{true})
}

// ActiveNow handles GET /api/singbox/subscriptions/active-now?id=
//
//	@Summary		Live active member from Clash
//	@Description	Returns the currently-active member tag as reported by the running sing-box Clash API. For urltest mode this reflects the auto-selected fastest member. Empty `now` means Clash is unreachable or no member selected yet.
//	@Tags			subscriptions
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id	query	string	true	"Subscription id"
//	@Success		200	{object}	OkResponse{data=ActiveNowResponse}
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		404	{object}	APIErrorEnvelope
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/subscriptions/active-now [get]
func (h *SubscriptionHandler) ActiveNow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	id, ok := requireQueryID(w, r)
	if !ok {
		return
	}
	h.log.Info("subscription-active-now", id, "requested via API")
	now, err := h.svc.GetActiveNow(r.Context(), id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			response.ErrorWithStatus(w, http.StatusNotFound, err.Error(), "NOT_FOUND")
			return
		}
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, ActiveNowResponse{Now: now})
}

// GetStream handles GET /api/singbox/subscriptions/get-stream?id=
//
//	@Summary		Stream subscription details progressively (SSE)
//	@Description	Streams a subscription as Server-Sent Events: one `meta` event with subscription header (incl. total member count), then one `member` event per server member, then a `done` event with finalisation (orphans, active member). Frontend uses this to show a progress bar and render cards as they arrive instead of waiting for the full payload. Service.Get is itself sync; the streaming is the handler's contract — it writes events with Flush() between, so TCP + browser deliver progressively.
//	@Tags			subscriptions
//	@Produce		text/event-stream
//	@Security		CookieAuth
//	@Param			id	query	string	true	"Subscription id"
//	@Success		200	"Stream of SSE events: meta, member×N, done"
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		404	{object}	APIErrorEnvelope
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/subscriptions/get-stream [get]
func (h *SubscriptionHandler) GetStream(w http.ResponseWriter, r *http.Request) {
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
		if strings.Contains(err.Error(), "not found") {
			response.ErrorWithStatus(w, http.StatusNotFound, err.Error(), "NOT_FOUND")
			return
		}
		response.InternalError(w, err.Error())
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		response.InternalError(w, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	metaJSON, _ := json.Marshal(buildSubscriptionMetaDTO(*sub, h.ndmsProxyEnabled()))
	fmt.Fprintf(w, "event: meta\ndata: %s\n\n", metaJSON)
	flusher.Flush()

	for i, m := range sub.Members {
		payload, _ := json.Marshal(SubscriptionStreamMemberDTO{
			Index:  i,
			Member: subscriptionMemberToDTO(m),
		})
		fmt.Fprintf(w, "event: member\ndata: %s\n\n", payload)
		flusher.Flush()
	}

	orphans := sub.OrphanTags
	if orphans == nil {
		orphans = []string{}
	}
	excludedTags, excludedMemberDTOs := buildExcludedDTO(*sub)
	var filteredMemberDTOs []SubscriptionMemberDTO
	if len(sub.FilteredMembers) > 0 {
		filteredMemberDTOs = make([]SubscriptionMemberDTO, len(sub.FilteredMembers))
		for i, m := range sub.FilteredMembers {
			filteredMemberDTOs[i] = subscriptionMemberToDTO(m)
		}
	}
	doneJSON, _ := json.Marshal(SubscriptionStreamDoneDTO{
		OrphanTags:      orphans,
		ActiveMember:    sub.ActiveMember,
		RejectedMembers: rejectedMembersToDTO(sub.RejectedMembers),
		InfoItems:       infoItemsToDTO(sub.InfoItems),
		ExcludedTags:    excludedTags,
		ExcludedMembers: excludedMemberDTOs,
		FilteredMembers: filteredMemberDTOs,
	})
	fmt.Fprintf(w, "event: done\ndata: %s\n\n", doneJSON)
	flusher.Flush()
}

// OrphansDelete handles POST /api/singbox/subscriptions/orphans/delete?id=
//
//	@Summary		Delete orphan members from subscription
//	@Tags			subscriptions
//	@Produce		json
//	@Param			id	query		string	true	"subscription id"
//	@Success		200	{object}	OkResponse
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/subscriptions/orphans/delete [post]
func (h *SubscriptionHandler) OrphansDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	id, ok := requireQueryID(w, r)
	if !ok {
		return
	}
	h.log.Info("subscription-orphans-delete", id, "requested via API")
	if err := h.svc.DeleteOrphans(r.Context(), id); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, struct {
		OK bool `json:"ok"`
	}{true})
}

// RejectedToInfo handles POST /api/singbox/subscriptions/rejected/to-info?id=
func (h *SubscriptionHandler) RejectedToInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	id, ok := requireQueryID(w, r)
	if !ok {
		return
	}
	var req MoveRejectedToInfoRequest
	if err := decodeBody(r, &req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "bad request body", "INVALID_JSON")
		return
	}
	if strings.TrimSpace(req.MemberTag) == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "memberTag required", "MISSING_MEMBER_TAG")
		return
	}
	sub, err := h.svc.MoveRejectedToInfo(r.Context(), id, req.MemberTag)
	if err != nil {
		switch {
		case errors.Is(err, subscription.ErrRejectedMemberNotFound):
			response.ErrorWithStatus(w, http.StatusNotFound, err.Error(), "REJECTED_NOT_FOUND")
		case errors.Is(err, subscription.ErrInfoItemsFull):
			response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "INFO_ITEMS_FULL")
		default:
			h.respondServiceError(w, "subscription-rejected-to-info", err)
		}
		return
	}
	response.Success(w, toSubscriptionDTO(*sub, h.ndmsProxyEnabled()))
}

// InfoRemove handles POST /api/singbox/subscriptions/info/remove?id=
func (h *SubscriptionHandler) InfoRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	id, ok := requireQueryID(w, r)
	if !ok {
		return
	}
	var req RemoveInfoItemRequest
	if err := decodeBody(r, &req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "bad request body", "INVALID_JSON")
		return
	}
	if strings.TrimSpace(req.ItemID) == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "itemId required", "MISSING_ITEM_ID")
		return
	}
	sub, err := h.svc.RemoveInfoItem(r.Context(), id, req.ItemID)
	if err != nil {
		switch {
		case errors.Is(err, subscription.ErrInfoItemNotFound):
			response.ErrorWithStatus(w, http.StatusNotFound, err.Error(), "INFO_ITEM_NOT_FOUND")
		default:
			h.respondServiceError(w, "subscription-info-remove", err)
		}
		return
	}
	response.Success(w, toSubscriptionDTO(*sub, h.ndmsProxyEnabled()))
}

// AddMember handles POST /api/singbox/subscriptions/members/add?id=
//
//	@Summary		Add a manual member to an inline subscription
//	@Tags			subscriptions
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id		query		string			true	"Subscription ID"
//	@Param			body	body		AddMemberRequest	true	"Share-link"
//	@Success		200		{object}	SubscriptionResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		409		{object}	APIErrorEnvelope
//	@Failure		422		{object}	APIErrorEnvelope	"sing-box validation rejected the new member"
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/subscriptions/members/add [post]
func (h *SubscriptionHandler) AddMember(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	id, ok := requireQueryID(w, r)
	if !ok {
		return
	}
	h.log.Info("subscription-member-add", id, "requested via API")
	var req AddMemberRequest
	if err := decodeBody(r, &req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "bad request body", "INVALID_JSON")
		return
	}
	sub, err := h.svc.AddManualMember(r.Context(), id, req.ShareLink)
	if err != nil {
		switch {
		case errors.Is(err, subscription.ErrManualMemberOnURLSub):
			response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "MEMBER_CRUD_ON_URL_SUB")
		case errors.Is(err, subscription.ErrShareLinkInvalid):
			response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "INVALID_SHARE_LINK")
		case errors.Is(err, subscription.ErrMemberDuplicate):
			response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "MEMBER_DUPLICATE")
		default:
			h.respondServiceError(w, "subscription-add-member", err)
		}
		return
	}
	response.Success(w, toSubscriptionDTO(*sub, h.ndmsProxyEnabled()))
}

// RemoveMember handles POST /api/singbox/subscriptions/members/remove?id=
//
//	@Summary		Remove a member from an inline subscription
//	@Description	Removing the last member tears the whole subscription
//	@Description	down. The response indicates whether the subscription
//	@Description	itself was deleted via deleted=true.
//	@Tags			subscriptions
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id		query		string				true	"Subscription ID"
//	@Param			body	body		RemoveMemberRequest	true	"Member tag"
//	@Success		200		{object}	APIEnvelope
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		404		{object}	APIErrorEnvelope
//	@Failure		422		{object}	APIErrorEnvelope	"sing-box validation rejected the remainder"
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/subscriptions/members/remove [post]
func (h *SubscriptionHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	id, ok := requireQueryID(w, r)
	if !ok {
		return
	}
	h.log.Info("subscription-member-remove", id, "requested via API")
	var req RemoveMemberRequest
	if err := decodeBody(r, &req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "bad request body", "INVALID_JSON")
		return
	}
	sub, err := h.svc.RemoveMember(r.Context(), id, req.MemberTag)
	if err != nil {
		switch {
		case errors.Is(err, subscription.ErrManualMemberOnURLSub):
			response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "MEMBER_CRUD_ON_URL_SUB")
		case errors.Is(err, subscription.ErrMemberNotFound):
			response.ErrorWithStatus(w, http.StatusNotFound, err.Error(), "MEMBER_NOT_FOUND")
		default:
			h.respondServiceError(w, "subscription-remove-member", err)
		}
		return
	}
	resp := RemoveMemberResponseData{Deleted: sub == nil}
	if sub != nil {
		dto := toSubscriptionDTO(*sub, h.ndmsProxyEnabled())
		resp.Subscription = &dto
	}
	response.Success(w, resp)
}

// ExcludeMembers handles POST /api/singbox/subscriptions/members/exclude?id=
//
//	@Summary		Exclude members from a URL subscription
//	@Description	Marks the given member tags as excluded: rebuilds the
//	@Description	selector without them, drops their outbounds and survives
//	@Description	refresh. Reversible via restore. URL subscriptions only.
//	@Tags			subscriptions
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id		query		string					true	"Subscription ID"
//	@Param			body	body		ExcludeMembersRequest	true	"Member tags to exclude"
//	@Success		200		{object}	SubscriptionResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		404		{object}	APIErrorEnvelope
//	@Failure		409		{object}	APIErrorEnvelope	"excluding all members is not allowed"
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/subscriptions/members/exclude [post]
func (h *SubscriptionHandler) ExcludeMembers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	id, ok := requireQueryID(w, r)
	if !ok {
		return
	}
	h.log.Info("subscription-member-exclude", id, "requested via API")
	var req ExcludeMembersRequest
	if err := decodeBody(r, &req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "bad request body", "INVALID_JSON")
		return
	}
	sub, err := h.svc.ExcludeMembers(r.Context(), id, req.MemberTags)
	if err != nil {
		h.respondServiceError(w, "subscription-exclude-members", err)
		return
	}
	response.Success(w, toSubscriptionDTO(*sub, h.ndmsProxyEnabled()))
}

// RestoreMembers handles POST /api/singbox/subscriptions/members/restore?id=
//
//	@Summary		Restore previously excluded members
//	@Description	Removes the given tags from the exclusion set and runs a
//	@Description	refresh, re-materializing the returned servers.
//	@Tags			subscriptions
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id		query		string					true	"Subscription ID"
//	@Param			body	body		RestoreMembersRequest	true	"Member tags to restore"
//	@Success		200		{object}	SubscriptionResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		404		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/subscriptions/members/restore [post]
func (h *SubscriptionHandler) RestoreMembers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	id, ok := requireQueryID(w, r)
	if !ok {
		return
	}
	h.log.Info("subscription-member-restore", id, "requested via API")
	var req RestoreMembersRequest
	if err := decodeBody(r, &req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "bad request body", "INVALID_JSON")
		return
	}
	sub, err := h.svc.RestoreMembers(r.Context(), id, req.MemberTags)
	if err != nil {
		h.respondServiceError(w, "subscription-restore-members", err)
		return
	}
	response.Success(w, toSubscriptionDTO(*sub, h.ndmsProxyEnabled()))
}

// PreviewURL handles POST /api/singbox/subscriptions/preview
//
//	@Summary		Preview a subscription URL without creating it
//	@Description	Read-only fetch + parse of a subscription URL. Returns the
//	@Description	parsed members (with subID-independent keys) so the import
//	@Description	wizard can offer per-server exclusion before creation.
//	@Tags			subscriptions
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		PreviewURLRequest	true	"URL and optional headers"
//	@Success		200		{object}	APIEnvelope
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		502		{object}	APIErrorEnvelope	"fetch or parse failed"
//	@Router			/singbox/subscriptions/preview [post]
func (h *SubscriptionHandler) PreviewURL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req PreviewURLRequest
	if err := decodeBody(r, &req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "bad request body", "INVALID_JSON")
		return
	}
	members, err := h.svc.PreviewURL(r.Context(), req.URL, fromSubscriptionHeaders(req.Headers))
	if err != nil {
		response.ErrorWithStatus(w, http.StatusBadGateway, err.Error(), "PREVIEW_FAILED")
		return
	}
	response.Success(w, members)
}

type DetectHeadersRequest struct {
	URL     string               `json:"url"`
	Headers []SubscriptionHeader `json:"headers"`
}

// DetectHeaders handles POST /api/singbox/subscriptions/detect-headers
//
//	@Summary		Auto-detect required HTTP headers for a subscription URL
//	@Description	Probes a subscription URL with different client profiles to auto-detect working headers
//	@Tags			subscriptions
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		DetectHeadersRequest	true	"Subscription URL and user-configured headers"
//	@Success		200		{object}	APIEnvelope
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		502		{object}	APIErrorEnvelope
//	@Router			/singbox/subscriptions/detect-headers [post]
func (h *SubscriptionHandler) DetectHeaders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req DetectHeadersRequest
	if err := decodeBody(r, &req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "bad request body", "INVALID_JSON")
		return
	}
	profile, err := h.svc.DetectHeaders(r.Context(), req.URL, fromSubscriptionHeaders(req.Headers))
	if err != nil {
		response.ErrorWithStatus(w, http.StatusBadGateway, err.Error(), "DETECT_FAILED")
		return
	}
	response.Success(w, profile)
}

type HappKeysRequest struct {
	Keys []string `json:"keys,omitempty"`
	Text string   `json:"text,omitempty"`
}

type HappKeysResponse struct {
	Configured bool `json:"configured"`
	Count      int  `json:"count"`
}

// HappKeys handles GET, POST, and DELETE /api/singbox/subscriptions/happ-keys
//
//	@Summary		Manage HAPP RSA decryption keys
//	@Description	GET returns key status; POST saves user-provided RSA keys (JSON array or raw text); DELETE removes stored keys
//	@Tags			subscriptions
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		HappKeysRequest	false	"RSA keys array or raw text (POST only)"
//	@Success		200		{object}	APIEnvelope
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/subscriptions/happ-keys [get]
//	@Router			/singbox/subscriptions/happ-keys [post]
//	@Router			/singbox/subscriptions/happ-keys [delete]
func (h *SubscriptionHandler) HappKeys(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		response.Success(w, HappKeysResponse{
			Configured: subscription.HasHappKeys(),
			Count:      subscription.GetHappKeysCount(),
		})
	case http.MethodPost:
		var req HappKeysRequest
		if err := decodeBody(r, &req); err != nil {
			response.ErrorWithStatus(w, http.StatusBadRequest, "bad request body", "INVALID_JSON")
			return
		}
		var keys []string
		var err error
		if len(req.Keys) > 0 {
			keys = req.Keys
		} else if strings.TrimSpace(req.Text) != "" {
			keys, err = subscription.ParseHappKeysInput(req.Text)
			if err != nil {
				response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "INVALID_KEYS")
				return
			}
		}
		if len(keys) == 0 {
			response.ErrorWithStatus(w, http.StatusBadRequest, "no valid keys found in payload", "INVALID_KEYS")
			return
		}
		if err := h.svc.SaveHappKeys(keys); err != nil {
			response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "SAVE_KEYS_FAILED")
			return
		}
		response.Success(w, HappKeysResponse{
			Configured: true,
			Count:      len(keys),
		})
	case http.MethodDelete:
		if err := h.svc.ClearHappKeys(); err != nil {
			response.InternalError(w, err.Error())
			return
		}
		response.Success(w, HappKeysResponse{
			Configured: false,
			Count:      0,
		})
	default:
		response.MethodNotAllowed(w)
	}
}
