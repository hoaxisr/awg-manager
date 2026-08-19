package api

import (
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/singbox/subscription"
)

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
	ID              string                    `json:"id" example:"sub-demo"`
	Label           string                    `json:"label" example:"Demo Provider"`
	URL             string                    `json:"url" example:"https://example.com/subscriptions/demo.txt"`
	IsInline        bool                      `json:"isInline" example:"false"`
	Headers         []SubscriptionHeader      `json:"headers"`
	RefreshHours    int                       `json:"refreshHours" example:"24"`
	LastFetched     string                    `json:"lastFetched" example:"2026-05-14T21:30:00Z"`
	LastError       string                    `json:"lastError,omitempty" example:""`
	SelectorTag     string                    `json:"selectorTag" example:"sub-demo"`
	InboundTag      string                    `json:"inboundTag" example:"sub-demo-in"`
	ListenPort      int                       `json:"listenPort" example:"11000"`
	ProxyIndex      int                       `json:"proxyIndex" example:"1" description:"NDMS ProxyN index for this subscription. -1 when no proxy is allocated yet OR when global 'Create NDMS Proxy for sing-box' is disabled (the composite interface does not exist in that mode — UI should hide t2sN/ProxyN labels and disable per-subscription speedtest)."`
	MemberTags      []string                  `json:"memberTags" example:"sub-demo-001,sub-demo-002,sub-demo-003"`
	Members         []SubscriptionMemberDTO   `json:"members"`
	OrphanTags      []string                  `json:"orphanTags" example:""`
	RejectedMembers []SubscriptionRejectedDTO `json:"rejectedMembers"`
	InfoItems       []SubscriptionInfoItemDTO `json:"infoItems"`
	ActiveMember    string                    `json:"activeMember" example:"sub-demo-001"`
	ExcludedTags    []string                  `json:"excludedTags"`
	ExcludedMembers []SubscriptionMemberDTO   `json:"excludedMembers,omitempty"`
	FilterInclude   string                    `json:"filterInclude,omitempty" example:"(?i)(DE|NL)"`
	FilterExclude   string                    `json:"filterExclude,omitempty" example:"(?i)(RU|Russia)"`
	FilteredMembers []SubscriptionMemberDTO   `json:"filteredMembers,omitempty"`
	Enabled         bool                      `json:"enabled" example:"true"`
	Mode            string                    `json:"mode" example:"selector"`
	URLTest         *SubscriptionURLTestDTO   `json:"urlTest,omitempty"`
	BindInterface   string                    `json:"bindInterface,omitempty" example:"eth3"`
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
	Label         string                  `json:"label" example:"Demo Provider"`
	URL           string                  `json:"url,omitempty" example:"https://example.com/subscriptions/demo.txt"`
	Inline        string                  `json:"inline,omitempty" example:"vless://11111111-2222-3333-4444-555555555555@demo.example.com:443?type=tcp&encryption=none&security=reality&pbk=EXAMPLE_PUBLIC_KEY&fp=chrome&sni=cdn.example.com&sid=abcd1234&spx=%2F&flow=xtls-rprx-vision#Demo-vless-reality"`
	Headers       []SubscriptionHeader    `json:"headers"`
	RefreshHours  int                     `json:"refreshHours" example:"24"`
	Enabled       bool                    `json:"enabled" example:"true"`
	Mode          string                  `json:"mode,omitempty"` // "selector" (default) | "urltest"
	URLTest       *SubscriptionURLTestDTO `json:"urlTest,omitempty"`
	ExcludedKeys  []string                `json:"excludedKeys,omitempty"`                            // identity-суффиксы серверов, снятых в import-preview
	FilterInclude string                  `json:"filterInclude,omitempty" example:"(?i)(DE|NL)"`     // regex «включать только» (по имени сервера)
	FilterExclude string                  `json:"filterExclude,omitempty" example:"(?i)(RU|Russia)"` // regex «исключать» (по имени сервера)
	BindInterface string                  `json:"bindInterface,omitempty" example:"eth3"`
}

// UpdateSubscriptionRequest is the body for PUT /api/singbox/subscriptions/update.
// All fields are optional; absent fields leave the stored value unchanged.
type UpdateSubscriptionRequest struct {
	Label         *string                 `json:"label,omitempty" example:"Demo Provider Updated"`
	URL           *string                 `json:"url,omitempty" example:"https://example.com/subscriptions/demo.txt"`
	Headers       *[]SubscriptionHeader   `json:"headers,omitempty"`
	RefreshHours  *int                    `json:"refreshHours,omitempty"`
	Enabled       *bool                   `json:"enabled,omitempty"`
	Mode          *string                 `json:"mode,omitempty" example:"selector"`
	URLTest       *SubscriptionURLTestDTO `json:"urlTest,omitempty"`
	FilterInclude *string                 `json:"filterInclude,omitempty" example:"(?i)(DE|NL)"`     // regex «включать только»; "" снимает фильтр
	FilterExclude *string                 `json:"filterExclude,omitempty" example:"(?i)(RU|Russia)"` // regex «исключать»; "" снимает фильтр
	BindInterface *string                 `json:"bindInterface,omitempty" example:"eth3"`            // "" clears egress bind
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
// buildExcludedDTO извлекает excluded-набор подписки в DTO-форму:
// excludedTags (никогда не nil — пустой срез для стабильного JSON "[]")
// и excludedMembers (nil → omitempty при пустом наборе).
func buildExcludedDTO(s subscription.Subscription) ([]string, []SubscriptionMemberDTO) {
	tags := s.ExcludedTags
	if tags == nil {
		tags = []string{}
	}
	var members []SubscriptionMemberDTO
	if len(s.ExcludedMembers) > 0 {
		members = make([]SubscriptionMemberDTO, len(s.ExcludedMembers))
		for i, m := range s.ExcludedMembers {
			members[i] = subscriptionMemberToDTO(m)
		}
	}
	return tags, members
}

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
	excludedTags, excludedMemberDTOs := buildExcludedDTO(s)
	var filteredMemberDTOs []SubscriptionMemberDTO
	if len(s.FilteredMembers) > 0 {
		filteredMemberDTOs = make([]SubscriptionMemberDTO, len(s.FilteredMembers))
		for i, m := range s.FilteredMembers {
			filteredMemberDTOs[i] = subscriptionMemberToDTO(m)
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
		MemberTags:      memberTags,
		Members:         memberDTOs,
		OrphanTags:      orphans,
		RejectedMembers: rejected,
		InfoItems:       info,
		ActiveMember:    s.ActiveMember,
		ExcludedTags:    excludedTags,
		ExcludedMembers: excludedMemberDTOs,
		FilterInclude:   s.FilterInclude,
		FilterExclude:   s.FilterExclude,
		FilteredMembers: filteredMemberDTOs,
		Enabled:         s.Enabled,
		Mode:            mode,
		URLTest:         urltest,
		BindInterface:   s.BindInterface,
	}
}

// SubscriptionMetaDTO is the meta-event payload for the streaming
// /get-stream endpoint. Mirrors SubscriptionDTO minus Members (those
// arrive as separate member events). The `total` field tells the UI
// how many member events to expect for progress.
type SubscriptionMetaDTO struct {
	ID              string                    `json:"id"`
	Label           string                    `json:"label"`
	URL             string                    `json:"url"`
	IsInline        bool                      `json:"isInline"`
	Headers         []SubscriptionHeader      `json:"headers"`
	RefreshHours    int                       `json:"refreshHours"`
	LastFetched     string                    `json:"lastFetched" example:"2026-05-14T21:30:00Z"`
	LastError       string                    `json:"lastError,omitempty" example:""`
	SelectorTag     string                    `json:"selectorTag"`
	InboundTag      string                    `json:"inboundTag"`
	ListenPort      int                       `json:"listenPort"`
	ProxyIndex      int                       `json:"proxyIndex" description:"See SubscriptionDTO.ProxyIndex — gated identically (-1 when NDMS Proxy disabled)."`
	Enabled         bool                      `json:"enabled"`
	Mode            string                    `json:"mode"`
	URLTest         *SubscriptionURLTestDTO   `json:"urlTest,omitempty"`
	Total           int                       `json:"total"`
	RejectedMembers []SubscriptionRejectedDTO `json:"rejectedMembers"`
	InfoItems       []SubscriptionInfoItemDTO `json:"infoItems"`
	FilterInclude   string                    `json:"filterInclude,omitempty"`
	FilterExclude   string                    `json:"filterExclude,omitempty"`
	BindInterface   string                    `json:"bindInterface,omitempty" example:"eth3"`
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
	OrphanTags      []string                  `json:"orphanTags"`
	ActiveMember    string                    `json:"activeMember"`
	RejectedMembers []SubscriptionRejectedDTO `json:"rejectedMembers"`
	InfoItems       []SubscriptionInfoItemDTO `json:"infoItems"`
	ExcludedTags    []string                  `json:"excludedTags"`
	ExcludedMembers []SubscriptionMemberDTO   `json:"excludedMembers,omitempty"`
	FilteredMembers []SubscriptionMemberDTO   `json:"filteredMembers,omitempty"`
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
		FilterInclude:   s.FilterInclude,
		FilterExclude:   s.FilterExclude,
		BindInterface:   s.BindInterface,
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
