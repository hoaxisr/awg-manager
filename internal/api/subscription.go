package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

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
		log:      logging.NewScopedLogger(lg, logging.GroupSingbox, logging.SubSBRuntime),
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
func (h *SubscriptionHandler) respondServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, subscription.ErrValidation):
		response.ErrorWithStatus(w, http.StatusUnprocessableEntity, err.Error(), "VALIDATION_FAILED")
	case errors.Is(err, subscription.ErrExcludeOnInline):
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "EXCLUDE_ON_INLINE")
	case errors.Is(err, subscription.ErrAllMembersExcluded):
		response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "ALL_MEMBERS_EXCLUDED")
	case errors.Is(err, subscription.ErrMemberNotFound):
		response.ErrorWithStatus(w, http.StatusNotFound, err.Error(), "MEMBER_NOT_FOUND")
	default:
		response.InternalError(w, err.Error())
	}
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
	ID           string                  `json:"id" example:"sub-demo"`
	Label        string                  `json:"label" example:"Demo Provider"`
	URL          string                  `json:"url" example:"https://example.com/subscriptions/demo.txt"`
	IsInline     bool                    `json:"isInline" example:"false"`
	Headers      []SubscriptionHeader    `json:"headers"`
	RefreshHours int                     `json:"refreshHours" example:"24"`
	LastFetched  string                  `json:"lastFetched" example:"2026-05-14T21:30:00Z"`
	LastError    string                  `json:"lastError,omitempty" example:""`
	SelectorTag  string                  `json:"selectorTag" example:"sub-demo"`
	InboundTag   string                  `json:"inboundTag" example:"sub-demo-in"`
	ListenPort   int                     `json:"listenPort" example:"11000"`
	ProxyIndex   int                     `json:"proxyIndex" example:"1" description:"NDMS ProxyN index for this subscription. -1 when no proxy is allocated yet OR when global 'Create NDMS Proxy for sing-box' is disabled (the composite interface does not exist in that mode — UI should hide t2sN/ProxyN labels and disable per-subscription speedtest)."`
	MemberTags   []string                `json:"memberTags" example:"sub-demo-001,sub-demo-002,sub-demo-003"`
	Members      []SubscriptionMemberDTO `json:"members"`
	OrphanTags        []string                      `json:"orphanTags" example:""`
	RejectedMembers   []SubscriptionRejectedDTO   `json:"rejectedMembers"`
	InfoItems         []SubscriptionInfoItemDTO   `json:"infoItems"`
	ActiveMember      string                      `json:"activeMember" example:"sub-demo-001"`
	ExcludedTags      []string                    `json:"excludedTags"`
	ExcludedMembers   []SubscriptionMemberDTO     `json:"excludedMembers,omitempty"`
	Enabled      bool                    `json:"enabled" example:"true"`
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
	Success bool              `json:"success" example:"true"`
	Data    []SubscriptionDTO `json:"data"`
}

// SubscriptionResponse is the envelope for single-subscription responses.
type SubscriptionResponse struct {
	Success bool            `json:"success" example:"true"`
	Data    SubscriptionDTO `json:"data"`
}

// CreateSubscriptionRequest is the body for POST /api/singbox/subscriptions/create.
// Exactly one of URL or Inline must be provided.
type CreateSubscriptionRequest struct {
	Label        string                  `json:"label" example:"Demo Provider"`
	URL          string                  `json:"url,omitempty" example:"https://example.com/subscriptions/demo.txt"`
	Inline       string                  `json:"inline,omitempty" example:"vless://11111111-2222-3333-4444-555555555555@demo.example.com:443?type=tcp&encryption=none&security=reality&pbk=EXAMPLE_PUBLIC_KEY&fp=chrome&sni=cdn.example.com&sid=abcd1234&spx=%2F&flow=xtls-rprx-vision#Demo-vless-reality"`
	Headers      []SubscriptionHeader    `json:"headers"`
	RefreshHours int                     `json:"refreshHours" example:"24"`
	Enabled      bool                    `json:"enabled" example:"true"`
	Mode         string                  `json:"mode,omitempty"` // "selector" (default) | "urltest"
	URLTest      *SubscriptionURLTestDTO `json:"urlTest,omitempty"`
	ExcludedKeys []string                `json:"excludedKeys,omitempty"` // identity-суффиксы серверов, снятых в import-preview
}

// UpdateSubscriptionRequest is the body for PUT /api/singbox/subscriptions/update.
// All fields are optional; absent fields leave the stored value unchanged.
type UpdateSubscriptionRequest struct {
	Label        *string                 `json:"label,omitempty" example:"Demo Provider Updated"`
	URL          *string                 `json:"url,omitempty" example:"https://example.com/subscriptions/demo.txt"`
	Headers      *[]SubscriptionHeader   `json:"headers,omitempty"`
	RefreshHours *int                    `json:"refreshHours,omitempty"`
	Enabled      *bool                   `json:"enabled,omitempty"`
	Mode         *string                 `json:"mode,omitempty" example:"selector"`
	URLTest      *SubscriptionURLTestDTO `json:"urlTest,omitempty"`
}

// ActiveMemberRequest is the body for POST /api/singbox/subscriptions/active-member.
type ActiveMemberRequest struct {
	MemberTag string `json:"memberTag" example:"sub-demo-001"`
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

// SubscriptionRejectedDTO is a parsed share-link that was not added to sing-box.
type SubscriptionRejectedDTO struct {
	Tag      string `json:"tag,omitempty"`
	Label    string `json:"label,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Server   string `json:"server,omitempty"`
	Port     int    `json:"port,omitempty"`
	Reason   string `json:"reason"`
}

// SubscriptionInfoItemDTO is a provider info banner (not a proxy).
type SubscriptionInfoItemDTO struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Tag    string `json:"tag,omitempty"`
	Source string `json:"source,omitempty"`
}

// MoveRejectedToInfoRequest is the body for POST .../rejected/to-info.
type MoveRejectedToInfoRequest struct {
	MemberTag string `json:"memberTag"`
}

// RemoveInfoItemRequest is the body for POST .../info/remove.
type RemoveInfoItemRequest struct {
	ItemID string `json:"itemId"`
}

// RemoveMemberRequest is the body for POST /api/singbox/subscriptions/members/remove.
// Removing the last member tears the whole subscription down (no
// meaningful empty subscription); the response carries deleted=true in
// that case so the UI can navigate away.
type RemoveMemberRequest struct {
	MemberTag string `json:"memberTag" example:"sub-demo-003"`
}

// RemoveMemberResponseData is the data envelope for remove-member.
type RemoveMemberResponseData struct {
	Deleted      bool             `json:"deleted" example:"false"`
	Subscription *SubscriptionDTO `json:"subscription,omitempty"`
}

// ExcludeMembersRequest is the body for POST /api/singbox/subscriptions/members/exclude.
// Excluding members is only allowed on URL-backed subscriptions and is reversible
// via restore.
type ExcludeMembersRequest struct {
	MemberTags []string `json:"memberTags" example:"sub-demo-002,sub-demo-003"`
}

// RestoreMembersRequest is the body for POST /api/singbox/subscriptions/members/restore.
type RestoreMembersRequest struct {
	MemberTags []string `json:"memberTags" example:"sub-demo-002"`
}

// PreviewURLRequest is the body for POST /api/singbox/subscriptions/preview.
// Read-only fetch + parse of a subscription URL without creating anything.
type PreviewURLRequest struct {
	URL     string               `json:"url" example:"https://example.com/subscriptions/demo.txt"`
	Headers []SubscriptionHeader `json:"headers"`
}

// toDTO converts a domain Subscription to its API representation.
// ndmsProxyEnabled gates ProxyIndex: when false the field is surfaced
// as -1 to match the rest of the API contract (no NDMS Proxy → no
// composite interface → no proxyIndex to display). Mirrors the
// ProxyInterface/KernelInterface stripping ListTunnels already does
// for tunnels in disabled mode.
func toSubscriptionDTO(s subscription.Subscription, ndmsProxyEnabled bool) SubscriptionDTO {
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
	rejected := rejectedMembersToDTO(s.RejectedMembers)
	info := infoItemsToDTO(s.InfoItems)
	memberDTOs := make([]SubscriptionMemberDTO, len(s.Members))
	for i, m := range s.Members {
		memberDTOs[i] = subscriptionMemberToDTO(m)
	}
	excludedTags := s.ExcludedTags
	if excludedTags == nil {
		excludedTags = []string{}
	}
	var excludedMemberDTOs []SubscriptionMemberDTO
	if len(s.ExcludedMembers) > 0 {
		excludedMemberDTOs = make([]SubscriptionMemberDTO, len(s.ExcludedMembers))
		for i, m := range s.ExcludedMembers {
			excludedMemberDTOs[i] = subscriptionMemberToDTO(m)
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
	proxyIdx := s.ProxyIndex
	if !ndmsProxyEnabled {
		proxyIdx = -1
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
		ProxyIndex:   proxyIdx,
		MemberTags:   memberTags,
		Members:      memberDTOs,
		OrphanTags:        orphans,
		RejectedMembers:   rejected,
		InfoItems:         info,
		ActiveMember:      s.ActiveMember,
		ExcludedTags:      excludedTags,
		ExcludedMembers:   excludedMemberDTOs,
		Enabled:      s.Enabled,
		Mode:         mode,
		URLTest:      urltest,
	}
}

// SubscriptionMetaDTO is the meta-event payload for the streaming
// /get-stream endpoint. Mirrors SubscriptionDTO minus Members (those
// arrive as separate member events). The `total` field tells the UI
// how many member events to expect for progress.
type SubscriptionMetaDTO struct {
	ID           string                  `json:"id"`
	Label        string                  `json:"label"`
	URL          string                  `json:"url"`
	IsInline     bool                    `json:"isInline"`
	Headers      []SubscriptionHeader    `json:"headers"`
	RefreshHours int                     `json:"refreshHours"`
	LastFetched  string                  `json:"lastFetched" example:"2026-05-14T21:30:00Z"`
	LastError    string                  `json:"lastError,omitempty" example:""`
	SelectorTag  string                  `json:"selectorTag"`
	InboundTag   string                  `json:"inboundTag"`
	ListenPort   int                     `json:"listenPort"`
	ProxyIndex   int                     `json:"proxyIndex" description:"See SubscriptionDTO.ProxyIndex — gated identically (-1 when NDMS Proxy disabled)."`
	Enabled      bool                    `json:"enabled"`
	Mode         string                  `json:"mode"`
	URLTest           *SubscriptionURLTestDTO     `json:"urlTest,omitempty"`
	Total             int                         `json:"total"`
	RejectedMembers   []SubscriptionRejectedDTO   `json:"rejectedMembers"`
	InfoItems         []SubscriptionInfoItemDTO   `json:"infoItems"`
}

// SubscriptionStreamMemberDTO wraps a single member with its index for
// the member-event payload. Index lets the frontend reason about
// progress (i+1 / total) and detect gaps if events arrive out of order.
type SubscriptionStreamMemberDTO struct {
	Index  int                   `json:"index"`
	Member SubscriptionMemberDTO `json:"member"`
}

// SubscriptionStreamDoneDTO is the done-event payload — finalisation
// fields that don't fit the meta header but the frontend needs to
// complete the rendering.
type SubscriptionStreamDoneDTO struct {
	OrphanTags      []string                    `json:"orphanTags"`
	ActiveMember    string                      `json:"activeMember"`
	RejectedMembers []SubscriptionRejectedDTO   `json:"rejectedMembers"`
	InfoItems       []SubscriptionInfoItemDTO   `json:"infoItems"`
}

// buildSubscriptionMetaDTO extracts the meta-event payload from a
// domain Subscription. Same field semantics as toSubscriptionDTO but
// no Members slice (those stream as member events). ndmsProxyEnabled
// gates ProxyIndex identically to toSubscriptionDTO.
func buildSubscriptionMetaDTO(s subscription.Subscription, ndmsProxyEnabled bool) SubscriptionMetaDTO {
	hh := make([]SubscriptionHeader, len(s.Headers))
	for i, h := range s.Headers {
		hh[i] = SubscriptionHeader{Name: h.Name, Value: h.Value}
	}
	last := ""
	if !s.LastFetched.IsZero() {
		last = s.LastFetched.Format("2006-01-02T15:04:05Z07:00")
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
	proxyIdx := s.ProxyIndex
	if !ndmsProxyEnabled {
		proxyIdx = -1
	}
	return SubscriptionMetaDTO{
		ID:              s.ID,
		Label:           s.Label,
		URL:             s.URL,
		IsInline:        s.IsInline(),
		Headers:         hh,
		RefreshHours:    s.RefreshHours,
		LastFetched:     last,
		LastError:       s.LastError,
		SelectorTag:     s.SelectorTag,
		InboundTag:      s.InboundTag,
		ListenPort:      int(s.ListenPort),
		ProxyIndex:      proxyIdx,
		Enabled:         s.Enabled,
		Mode:            mode,
		URLTest:         urltest,
		Total:           len(s.Members),
		RejectedMembers: rejectedMembersToDTO(s.RejectedMembers),
		InfoItems:       infoItemsToDTO(s.InfoItems),
	}
}

// subscriptionMemberToDTO extracts the per-member DTO. Same shape as
// the Members slice element in toSubscriptionDTO.
func rejectedMembersToDTO(in []subscription.RejectedMember) []SubscriptionRejectedDTO {
	if len(in) == 0 {
		return []SubscriptionRejectedDTO{}
	}
	out := make([]SubscriptionRejectedDTO, len(in))
	for i, r := range in {
		out[i] = SubscriptionRejectedDTO{
			Tag:      r.Tag,
			Label:    r.Label,
			Protocol: r.Protocol,
			Server:   r.Server,
			Port:     int(r.Port),
			Reason:   r.Reason,
		}
	}
	return out
}

func infoItemsToDTO(in []subscription.SubscriptionInfoItem) []SubscriptionInfoItemDTO {
	if len(in) == 0 {
		return []SubscriptionInfoItemDTO{}
	}
	out := make([]SubscriptionInfoItemDTO, len(in))
	for i, it := range in {
		out[i] = SubscriptionInfoItemDTO{
			ID:     it.ID,
			Label:  it.Label,
			Tag:    it.Tag,
			Source: it.Source,
		}
	}
	return out
}

func subscriptionMemberToDTO(m subscription.MemberInfo) SubscriptionMemberDTO {
	return SubscriptionMemberDTO{
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
		ExcludedKeys: req.ExcludedKeys,
	}
	sub, err := h.svc.Create(r.Context(), in)
	if err != nil {
		h.log.Warn("subscription-create", req.Label, "failed: "+err.Error())
		h.respondServiceError(w, err)
		return
	}
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
//	@Failure		422	{object}	APIErrorEnvelope	"sing-box validation rejected the subscription"
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/subscriptions/update [put]
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
		h.respondServiceError(w, err)
		return
	}
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
	id := r.URL.Query().Get("id")
	if id == "" {
		response.BadRequest(w, "id required")
		return
	}
	if sub, err := h.svc.Get(id); err == nil {
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
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, struct {
		OK bool `json:"ok"`
	}{true})
}

// Refresh handles POST /api/singbox/subscriptions/refresh?id=
//
//	@Summary		Refresh sing-box subscription
//	@Description	Re-fetches the provider URL and re-runs Pass 1 / Pass 2 validation. Returns 422 VALIDATION_FAILED when the refreshed config is rejected by `sing-box check`.
//	@Tags			subscriptions
//	@Produce		json
//	@Param			id	query		string	false	"subscription id"
//	@Success		200	{object}	SubscriptionResponse
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
		h.respondServiceError(w, err)
		return
	}
	response.Success(w, res)
}

// ActiveMember handles POST /api/singbox/subscriptions/active-member?id=
//
//	@Summary		Set active member
//	@Tags			subscriptions
//	@Accept			json
//	@Produce		json
//	@Param			id	query		string				true	"subscription id"
//	@Param			req	body		ActiveMemberRequest	true	"member tag"
//	@Success		200	{object}	SubscriptionResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/subscriptions/active-member [post]
func (h *SubscriptionHandler) ActiveMember(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	id := r.URL.Query().Get("id")
	h.log.Info("subscription-active-member", id, "requested via API")
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
	id := r.URL.Query().Get("id")
	if id == "" {
		response.Error(w, "missing id parameter", "MISSING_ID")
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
	doneJSON, _ := json.Marshal(SubscriptionStreamDoneDTO{
		OrphanTags:      orphans,
		ActiveMember:    sub.ActiveMember,
		RejectedMembers: rejectedMembersToDTO(sub.RejectedMembers),
		InfoItems:       infoItemsToDTO(sub.InfoItems),
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
//	@Success		200	{object}	SubscriptionResponse
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/subscriptions/orphans/delete [post]
func (h *SubscriptionHandler) OrphansDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	id := r.URL.Query().Get("id")
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
	id := r.URL.Query().Get("id")
	if id == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "id required", "MISSING_ID")
		return
	}
	var req MoveRejectedToInfoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
			h.respondServiceError(w, err)
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
	id := r.URL.Query().Get("id")
	if id == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "id required", "MISSING_ID")
		return
	}
	var req RemoveInfoItemRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
			h.respondServiceError(w, err)
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
	id := r.URL.Query().Get("id")
	if id == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "id required", "MISSING_ID")
		return
	}
	h.log.Info("subscription-member-add", id, "requested via API")
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
			h.respondServiceError(w, err)
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
	id := r.URL.Query().Get("id")
	if id == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "id required", "MISSING_ID")
		return
	}
	h.log.Info("subscription-member-remove", id, "requested via API")
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
			h.respondServiceError(w, err)
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
	id := r.URL.Query().Get("id")
	if id == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "id required", "MISSING_ID")
		return
	}
	h.log.Info("subscription-member-exclude", id, "requested via API")
	var req ExcludeMembersRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "bad request body", "INVALID_JSON")
		return
	}
	sub, err := h.svc.ExcludeMembers(r.Context(), id, req.MemberTags)
	if err != nil {
		h.respondServiceError(w, err)
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
	id := r.URL.Query().Get("id")
	if id == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "id required", "MISSING_ID")
		return
	}
	h.log.Info("subscription-member-restore", id, "requested via API")
	var req RestoreMembersRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "bad request body", "INVALID_JSON")
		return
	}
	sub, err := h.svc.RestoreMembers(r.Context(), id, req.MemberTags)
	if err != nil {
		h.respondServiceError(w, err)
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
