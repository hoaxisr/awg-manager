package subscription

import "time"

// MemberInfo is parsed metadata for one selector member, persisted alongside
// the subscription so the UI can display useful info per card without
// re-parsing the raw outbound JSON each time.
type MemberInfo struct {
	Tag       string `json:"tag"`
	Label     string `json:"label,omitempty"`     // human-readable name from Clash "name", empty for share-links
	Protocol  string `json:"protocol"`            // "vless" | "trojan" | "shadowsocks" | "hysteria2" | "naive"
	Server    string `json:"server"`              // host
	Port      uint16 `json:"port"`
	SNI       string `json:"sni,omitempty"`
	Transport string `json:"transport,omitempty"` // "tcp" | "ws" | "grpc" | "http" — empty if N/A
	Security  string `json:"security,omitempty"`  // "tls" | "reality" | "" (none/Hy2 always tls)
}

// SubscriptionMode picks which sing-box outbound type wraps the
// subscription's members.
//
//   - "selector" (default, back-compat) — manual switch via Clash API.
//   - "urltest"  — sing-box probes each member at URLTestConfig.URL
//     every IntervalSec and routes through the fastest. The Clash API
//     SelectClashProxy still works as a temporary override but auto-test
//     re-evaluates on the next interval.
type SubscriptionMode string

const (
	ModeSelector SubscriptionMode = "selector"
	ModeURLTest  SubscriptionMode = "urltest"
)

// URLTestConfig holds the urltest-specific tuning. All fields have
// safe defaults; an empty struct yields gstatic 60s 50ms.
type URLTestConfig struct {
	URL         string `json:"url"`         // probe URL (default https://www.gstatic.com/generate_204)
	IntervalSec int    `json:"intervalSec"` // probe interval seconds (default 60)
	ToleranceMs int    `json:"toleranceMs"` // RTT tolerance ms (default 50)
}

// DefaultURLTestConfig is what newly-created urltest subscriptions get
// when the caller does not override the fields. Centralised so backend
// and frontend can use the same numbers.
func DefaultURLTestConfig() URLTestConfig {
	return URLTestConfig{
		URL:         "https://www.gstatic.com/generate_204",
		IntervalSec: 60,
		ToleranceMs: 50,
	}
}

// Subscription is the persisted shape of a VPN subscription. A
// subscription is either URL-backed (Inline == "") or inline (URL == "");
// IsInline is the canonical predicate.
type Subscription struct {
	ID           string           `json:"id"`                  // uuid
	Label        string           `json:"label"`               // user-facing
	URL          string           `json:"url"`                 // subscription URL ("" when inline)
	Inline       string           `json:"inline,omitempty"`    // raw paste of share-links / clash YAML / sing-box JSON
	Headers      []Header         `json:"headers"`             // custom HTTP headers for fetch (URL-backed only)
	RefreshHours int              `json:"refreshHours"`        // 0 = manual only; ignored for inline
	LastFetched  time.Time        `json:"lastFetched"`
	LastError    string           `json:"lastError,omitempty"`
	SelectorTag  string           `json:"selectorTag"`           // "sub-<id-short>"
	InboundTag   string           `json:"inboundTag"`            // "sub-<id-short>-in"
	ListenPort   uint16           `json:"listenPort"`            // localhost port for the mixed inbound
	ProxyIndex   int              `json:"proxyIndex"`            // NDMS ProxyN index, -1 if not yet allocated
	MemberTags   []string         `json:"memberTags"`            // every member outbound tag (kept for back-compat)
	Members      []MemberInfo     `json:"members,omitempty"`     // per-member parsed metadata
	OrphanTags   []string         `json:"orphanTags"`            // tags missing on last refresh
	ActiveMember string           `json:"activeMember,omitempty"` // currently-active selector member tag
	Enabled      bool             `json:"enabled"`
	Mode         SubscriptionMode `json:"mode,omitempty"`        // "" treated as ModeSelector for back-compat
	URLTest      *URLTestConfig   `json:"urlTest,omitempty"`     // populated when Mode == ModeURLTest
}

// IsInline reports whether the subscription's content is paste-supplied
// rather than fetched from a remote URL. URL and Inline are mutually
// exclusive at create time and the source type is frozen for the
// lifetime of the subscription (Update rejects switching).
func (s Subscription) IsInline() bool {
	return s.URL == "" && s.Inline != ""
}

// EffectiveMode returns the subscription's mode with the empty-string
// back-compat shim applied. Stored entries that predate the mode field
// implicitly behave as ModeSelector.
func (s Subscription) EffectiveMode() SubscriptionMode {
	if s.Mode == "" {
		return ModeSelector
	}
	return s.Mode
}

// EffectiveURLTest returns URLTest with defaults filled in. Safe to
// call regardless of Mode — caller should only consume the result when
// EffectiveMode() == ModeURLTest.
func (s Subscription) EffectiveURLTest() URLTestConfig {
	def := DefaultURLTestConfig()
	if s.URLTest == nil {
		return def
	}
	out := *s.URLTest
	if out.URL == "" {
		out.URL = def.URL
	}
	if out.IntervalSec <= 0 {
		out.IntervalSec = def.IntervalSec
	}
	if out.ToleranceMs < 0 {
		out.ToleranceMs = def.ToleranceMs
	}
	return out
}

// Header is a single name:value pair sent on the fetch request.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// CreateInput is the input to Service.Create. Exactly one of URL or
// Inline must be set; setting both or neither is rejected.
type CreateInput struct {
	Label        string
	URL          string
	Inline       string
	Headers      []Header
	RefreshHours int
	Enabled      bool
	Mode         SubscriptionMode // "" = ModeSelector
	URLTest      *URLTestConfig   // ignored when Mode != ModeURLTest; defaults applied otherwise
}

// UpdatePatch is partial update; nil pointers mean "leave as-is".
type UpdatePatch struct {
	Label        *string
	URL          *string
	Headers      *[]Header
	RefreshHours *int
	Enabled      *bool
	Mode         *SubscriptionMode
	URLTest      *URLTestConfig // overwrites stored URLTest when non-nil
}

// RefreshResult is the outcome of a single refresh cycle.
type RefreshResult struct {
	When             time.Time `json:"when"`
	Err              error     `json:"-"`
	Added            int       `json:"added"`
	Updated          int       `json:"updated"`
	Orphaned         int       `json:"orphaned"`
	SkippedVmess     int       `json:"skippedVmess"`
	SkippedOther     int       `json:"skippedOther"`
	SkippedDuplicate int       `json:"skippedDuplicate"`
	ParseErrors      []string  `json:"parseErrors,omitempty"`
}
