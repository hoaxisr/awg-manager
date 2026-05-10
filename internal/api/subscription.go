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

// SingboxPresenceProbe reports whether the managed sing-box binary is
// available on disk. Subscriptions register an NDMS Proxy interface
// pointing at sing-box's inbound listener, so creating one without
// sing-box installed leaves the Proxy slot pointing at nothing.
type SingboxPresenceProbe interface {
	IsPresent() bool
}

// SubscriptionHandler exposes /api/singbox/subscriptions/* endpoints.
type SubscriptionHandler struct {
	svc      *subscription.Service
	presence SingboxPresenceProbe
}

func NewSubscriptionHandler(svc *subscription.Service, presence SingboxPresenceProbe) *SubscriptionHandler {
	return &SubscriptionHandler{svc: svc, presence: presence}
}

// SubscriptionMemberDTO carries per-member parsed metadata for the UI.
type SubscriptionMemberDTO struct {
	Tag       string `json:"tag" example:"sub-abc12345-aabbccdd"`
	Label     string `json:"label,omitempty" example:"🇺🇸 LA-1"`
	Protocol  string `json:"protocol" example:"vless"`
	Server    string `json:"server" example:"de01.example.com"`
	Port      int    `json:"port" example:"443"`
	SNI       string `json:"sni,omitempty" example:"de01.example.com"`
	Transport string `json:"transport,omitempty" example:"ws"`
	Security  string `json:"security,omitempty" example:"tls"`
}

// SubscriptionURLTestDTO carries urltest tuning. Only meaningful when
// SubscriptionDTO.Mode == "urltest"; when absent or mode is "selector"
// the consumer should ignore it.
type SubscriptionURLTestDTO struct {
	URL         string `json:"url" example:"https://www.gstatic.com/generate_204"`
	IntervalSec int    `json:"intervalSec" example:"60"`
	ToleranceMs int    `json:"toleranceMs" example:"50"`
}

// SubscriptionDTO mirrors subscription.Subscription for OpenAPI exposure.
//
// Inline content is deliberately NOT exposed: pasted share-links carry
// the full server address + UUID/password and would otherwise leak into
// every list-all response (i.e. every page load), browser DevTools, and
// any reverse-proxy access log that records response bodies. Frontend
// only needs IsInline to gate UI affordances; raw paste stays
// server-side until a future single-record endpoint requires it.
type SubscriptionDTO struct {
	ID           string                  `json:"id" example:"abc123"`
	Label        string                  `json:"label" example:"Provider X"`
	URL          string                  `json:"url" example:"https://prov.example/sub/a"`
	IsInline     bool                    `json:"isInline" example:"false"`
	Headers      []SubscriptionHeader    `json:"headers"`
	RefreshHours int                     `json:"refreshHours" example:"24"`
	LastFetched  string                  `json:"lastFetched"`
	LastError    string                  `json:"lastError,omitempty"`
	SelectorTag  string                  `json:"selectorTag" example:"sub-abc12345"`
	InboundTag   string                  `json:"inboundTag" example:"sub-abc12345-in"`
	ListenPort   int                     `json:"listenPort" example:"11080"`
	ProxyIndex   int                     `json:"proxyIndex" example:"3"`
	MemberTags   []string                `json:"memberTags"`
	Members      []SubscriptionMemberDTO `json:"members"`
	OrphanTags   []string                `json:"orphanTags"`
	ActiveMember string                  `json:"activeMember" example:"sub-abc-aaaa"`
	Enabled      bool                    `json:"enabled"`
	Mode         string                  `json:"mode" example:"selector"`
	URLTest      *SubscriptionURLTestDTO `json:"urlTest,omitempty"`
}

// SubscriptionHeader is a single custom HTTP header for the fetch request.
type SubscriptionHeader struct {
	Name  string `json:"name" example:"User-Agent"`
	Value string `json:"value" example:"Happ/4.6.0"`
}

// SubscriptionListResponse is the envelope for GET /api/singbox/subscriptions.
type SubscriptionListResponse struct {
	Success bool              `json:"success"`
	Data    []SubscriptionDTO `json:"data"`
}

// SubscriptionResponse is the envelope for single-subscription responses.
type SubscriptionResponse struct {
	Success bool            `json:"success"`
	Data    SubscriptionDTO `json:"data"`
}

// CreateSubscriptionRequest is the body for POST /api/singbox/subscriptions/create.
// Exactly one of URL or Inline must be provided.
type CreateSubscriptionRequest struct {
	Label        string                  `json:"label"`
	URL          string                  `json:"url,omitempty"`
	Inline       string                  `json:"inline,omitempty"`
	Headers      []SubscriptionHeader    `json:"headers"`
	RefreshHours int                     `json:"refreshHours"`
	Enabled      bool                    `json:"enabled"`
	Mode         string                  `json:"mode,omitempty"` // "selector" (default) | "urltest"
	URLTest      *SubscriptionURLTestDTO `json:"urlTest,omitempty"`
}

// UpdateSubscriptionRequest is the body for PUT /api/singbox/subscriptions/update.
// All fields are optional; absent fields leave the stored value unchanged.
type UpdateSubscriptionRequest struct {
	Label        *string                 `json:"label,omitempty"`
	URL          *string                 `json:"url,omitempty"`
	Headers      *[]SubscriptionHeader   `json:"headers,omitempty"`
	RefreshHours *int                    `json:"refreshHours,omitempty"`
	Enabled      *bool                   `json:"enabled,omitempty"`
	Mode         *string                 `json:"mode,omitempty"`
	URLTest      *SubscriptionURLTestDTO `json:"urlTest,omitempty"`
}

// ActiveMemberRequest is the body for POST /api/singbox/subscriptions/active-member.
type ActiveMemberRequest struct {
	MemberTag string `json:"memberTag"`
}

// ActiveNowResponse is the payload for GET /api/singbox/subscriptions/active-now.
// Surface only the live "now" pointer from Clash for urltest mode UI.
type ActiveNowResponse struct {
	Now string `json:"now" example:"sub-abc-aaaa"`
}

// AddMemberRequest is the body for POST /api/singbox/subscriptions/members/add.
// Inline subscriptions only — manual CRUD is rejected on URL-backed
// subscriptions (the URL refresh diff owns the truth there).
type AddMemberRequest struct {
	ShareLink string `json:"shareLink" example:"vless://...@h.example:443?security=tls&sni=h"`
}

// RemoveMemberRequest is the body for POST /api/singbox/subscriptions/members/remove.
// Removing the last member tears the whole subscription down (no
// meaningful empty subscription); the response carries deleted=true in
// that case so the UI can navigate away.
type RemoveMemberRequest struct {
	MemberTag string `json:"memberTag"`
}

// RemoveMemberResponseData is the data envelope for remove-member.
type RemoveMemberResponseData struct {
	Deleted      bool             `json:"deleted"`
	Subscription *SubscriptionDTO `json:"subscription,omitempty"`
}

// toDTO converts a domain Subscription to its API representation.
func toSubscriptionDTO(s subscription.Subscription) SubscriptionDTO {
	hh := make([]SubscriptionHeader, len(s.Headers))
	for i, h := range s.Headers {
		hh[i] = SubscriptionHeader{Name: h.Name, Value: h.Value}
	}
	last := ""
	if !s.LastFetched.IsZero() {
		last = s.LastFetched.Format("2006-01-02T15:04:05Z07:00")
	}
	memberTags := s.MemberTags
	if memberTags == nil {
		memberTags = []string{}
	}
	orphans := s.OrphanTags
	if orphans == nil {
		orphans = []string{}
	}
	memberDTOs := make([]SubscriptionMemberDTO, len(s.Members))
	for i, m := range s.Members {
		memberDTOs[i] = SubscriptionMemberDTO{
			Tag:       m.Tag,
			Label:     m.Label,
			Protocol:  m.Protocol,
			Server:    m.Server,
			Port:      int(m.Port),
			SNI:       m.SNI,
			Transport: m.Transport,
			Security:  m.Security,
		}
	}
	mode := string(s.EffectiveMode())
	var urltest *SubscriptionURLTestDTO
	if s.EffectiveMode() == subscription.ModeURLTest {
		ut := s.EffectiveURLTest()
		urltest = &SubscriptionURLTestDTO{
			URL:         ut.URL,
			IntervalSec: ut.IntervalSec,
			ToleranceMs: ut.ToleranceMs,
		}
	}
	return SubscriptionDTO{
		ID:           s.ID,
		Label:        s.Label,
		URL:          s.URL,
		IsInline:     s.IsInline(),
		Headers:      hh,
		RefreshHours: s.RefreshHours,
		LastFetched:  last,
		LastError:    s.LastError,
		SelectorTag:  s.SelectorTag,
		InboundTag:   s.InboundTag,
		ListenPort:   int(s.ListenPort),
		ProxyIndex:   s.ProxyIndex,
		MemberTags:   memberTags,
		Members:      memberDTOs,
		OrphanTags:   orphans,
		ActiveMember: s.ActiveMember,
		Enabled:      s.Enabled,
		Mode:         mode,
		URLTest:      urltest,
	}
}

// parseSubscriptionMode validates a mode string from a request body. An
// empty string maps to ModeSelector (back-compat default). Anything
// outside the closed set returns an error so the caller can 400.
func parseSubscriptionMode(s string) (subscription.SubscriptionMode, error) {
	switch s {
	case "":
		return subscription.ModeSelector, nil
	case string(subscription.ModeSelector):
		return subscription.ModeSelector, nil
	case string(subscription.ModeURLTest):
		return subscription.ModeURLTest, nil
	default:
		return "", fmt.Errorf("invalid mode %q (expected \"selector\" or \"urltest\")", s)
	}
}

// urlTestDTOToConfig copies a request DTO into the domain config.
// Returns nil when the input is nil so callers can leave URLTest
// unchanged on Update.
func urlTestDTOToConfig(in *SubscriptionURLTestDTO) *subscription.URLTestConfig {
	if in == nil {
		return nil
	}
	return &subscription.URLTestConfig{
		URL:         in.URL,
		IntervalSec: in.IntervalSec,
		ToleranceMs: in.ToleranceMs,
	}
}

func fromSubscriptionHeaders(in []SubscriptionHeader) []subscription.Header {
	out := make([]subscription.Header, len(in))
	for i, h := range in {
		out[i] = subscription.Header{Name: h.Name, Value: h.Value}
	}
	return out
}

// validateSubscriptionHeaders enforces spec limits: <=32 headers, name <=256, value <=2048.
func validateSubscriptionHeaders(hh []SubscriptionHeader) error {
	if len(hh) > 32 {
		return fmt.Errorf("too many headers (%d > 32)", len(hh))
	}
	for _, h := range hh {
		if len(h.Name) > 256 {
			return fmt.Errorf("header name too long: %d > 256", len(h.Name))
		}
		if len(h.Value) > 2048 {
			return fmt.Errorf("header value too long: %d > 2048", len(h.Value))
		}
	}
	return nil
}

// List handles GET /api/singbox/subscriptions
func (h *SubscriptionHandler) List(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	all := []SubscriptionDTO{}
	for _, s := range h.svc.List() {
		all = append(all, toSubscriptionDTO(s))
	}
	response.Success(w, all)
}

// Create handles POST /api/singbox/subscriptions/create
func (h *SubscriptionHandler) Create(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.presence != nil && !h.presence.IsPresent() {
		response.ErrorWithStatus(w, http.StatusPreconditionFailed,
			"Sing-box не установлен — установите перед добавлением подписки",
			"SINGBOX_NOT_INSTALLED")
		return
	}
	var req CreateSubscriptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
		Label:        req.Label,
		URL:          req.URL,
		Inline:       req.Inline,
		Headers:      fromSubscriptionHeaders(req.Headers),
		RefreshHours: req.RefreshHours,
		Enabled:      req.Enabled,
		Mode:         mode,
		URLTest:      urlTestDTOToConfig(req.URLTest),
	}
	sub, err := h.svc.Create(r.Context(), in)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, toSubscriptionDTO(*sub))
}

// Get handles GET /api/singbox/subscriptions/get?id=
func (h *SubscriptionHandler) Get(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "id required", "MISSING_ID")
		return
	}
	sub, err := h.svc.Get(id)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusNotFound, err.Error(), "NOT_FOUND")
		return
	}
	response.Success(w, toSubscriptionDTO(*sub))
}

// Update handles PUT /api/singbox/subscriptions/update?id=
func (h *SubscriptionHandler) Update(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		response.MethodNotAllowed(w)
		return
	}
	id := r.URL.Query().Get("id")
	var req UpdateSubscriptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
		Label:        req.Label,
		URL:          req.URL,
		RefreshHours: req.RefreshHours,
		Enabled:      req.Enabled,
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
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, toSubscriptionDTO(*sub))
}

// Delete handles DELETE /api/singbox/subscriptions/delete?id=  Always performs full cleanup (no cascade flag).
func (h *SubscriptionHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		response.MethodNotAllowed(w)
		return
	}
	id := r.URL.Query().Get("id")
	if err := h.svc.Delete(r.Context(), id); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, struct {
		OK bool `json:"ok"`
	}{true})
}

// Refresh handles POST /api/singbox/subscriptions/refresh?id=
func (h *SubscriptionHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	id := r.URL.Query().Get("id")
	res, err := h.svc.Refresh(r.Context(), id)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, res)
}

// ActiveMember handles POST /api/singbox/subscriptions/active-member?id=
func (h *SubscriptionHandler) ActiveMember(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	id := r.URL.Query().Get("id")
	var req ActiveMemberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
	id := r.URL.Query().Get("id")
	if id == "" {
		response.Error(w, "missing id parameter", "MISSING_ID")
		return
	}
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

// OrphansDelete handles POST /api/singbox/subscriptions/orphans/delete?id=
func (h *SubscriptionHandler) OrphansDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	id := r.URL.Query().Get("id")
	if err := h.svc.DeleteOrphans(r.Context(), id); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, struct {
		OK bool `json:"ok"`
	}{true})
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
//	@Router			/singbox/subscriptions/members/add [post]
func (h *SubscriptionHandler) AddMember(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "id required", "MISSING_ID")
		return
	}
	var req AddMemberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
			response.InternalError(w, err.Error())
		}
		return
	}
	response.Success(w, toSubscriptionDTO(*sub))
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
//	@Router			/singbox/subscriptions/members/remove [post]
func (h *SubscriptionHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "id required", "MISSING_ID")
		return
	}
	var req RemoveMemberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
			response.InternalError(w, err.Error())
		}
		return
	}
	resp := RemoveMemberResponseData{Deleted: sub == nil}
	if sub != nil {
		dto := toSubscriptionDTO(*sub)
		resp.Subscription = &dto
	}
	response.Success(w, resp)
}
