package router

import (
	"encoding/json"
	"fmt"
)

type Status struct {
	Enabled   bool `json:"enabled"`
	Installed bool `json:"installed"`
	// Active reports whether the interception path is actually live: both
	// AWGM chains exist AND PREROUTING jumps into them. Installed alone only
	// proves the chains exist — the jumps can be wiped (NDMS PREROUTING
	// rebuild) while chains survive, so the engine looks "installed" but
	// routes nothing. The UI keys its "working" badge on Active, not Enabled
	// (intent) or Installed (chains only).
	Active                 bool    `json:"active"`
	NetfilterAvailable     bool    `json:"netfilterAvailable"`
	NetfilterComponentName string  `json:"netfilterComponentName,omitempty"`
	TProxyTargetAvailable  bool    `json:"tproxyTargetAvailable"`
	PolicyName             string  `json:"policyName"`
	PolicyMark             string  `json:"policyMark,omitempty"`
	PolicyExists           bool    `json:"policyExists"`
	DeviceMode             string  `json:"deviceMode"`
	SnifferEnabled         bool    `json:"snifferEnabled"`
	DeviceCount            int     `json:"deviceCount"`
	RuleCount              int     `json:"ruleCount"`
	RuleSetCount           int     `json:"ruleSetCount"`
	OutboundAWGCount       int     `json:"outboundAwgCount"`
	OutboundCompositeCount int     `json:"outboundCompositeCount"`
	Final                  string  `json:"final"`
	Issues                 []Issue `json:"issues,omitempty"`
}

type Issue struct {
	Severity  string `json:"severity"`
	Kind      string `json:"kind"`
	RuleIndex int    `json:"ruleIndex,omitempty"`
	Tag       string `json:"tag,omitempty"`
	Message   string `json:"message"`
}

type Rule struct {
	// Type+Mode+Rules express a sing-box logical rule (`type:"logical"`,
	// `mode:"or"|"and"`) when set. System hijack-dns uses this form to
	// match either `protocol:dns` (sniffed) or `port:53` (direct) so a
	// LAN client setting an explicit DNS server reaches sing-box's hijack
	// path even when sniffing missed the protocol. Nested entries inside
	// `Rules` have no Action (the parent owns it); Action is omitempty
	// so nested marshaling stays clean.
	Type         string   `json:"type,omitempty"`
	Mode         string   `json:"mode,omitempty"`
	Rules        []Rule   `json:"rules,omitempty"`
	DomainSuffix []string `json:"domain_suffix,omitempty"`
	IPCIDR       []string `json:"ip_cidr,omitempty"`
	SourceIPCIDR []string `json:"source_ip_cidr,omitempty"`
	Port         []int    `json:"port,omitempty"`
	RuleSet      []string `json:"rule_set,omitempty"`
	Protocol     string   `json:"protocol,omitempty"`
	// IPIsPrivate, when set, matches packets whose destination is an
	// RFC1918/loopback/link-local/CGNAT/multicast address. Pointer so
	// the zero value (unset) stays out of JSON — `{"ip_is_private":false}`
	// would change sing-box semantics. System rule from EnsureSystemRules
	// uses `*IPIsPrivate = true` as defense-in-depth: even when iptables
	// PolicyMark filter correctly keeps non-policy traffic out of
	// AWGM-TPROXY, the `hijack-dns` route action creates a kernel-level
	// transparent listener on every router LAN IP; a side-effect packet
	// that slips into sing-box from there gets routed `direct` instead
	// of ending up at `final: proxy` and being silently dropped.
	IPIsPrivate *bool  `json:"ip_is_private,omitempty"`
	Action      string `json:"action,omitempty"`
	Outbound    string `json:"outbound,omitempty"`
}

// UnmarshalJSON implements json.Unmarshaler for Rule. It accepts both
// `"port": 53` (scalar) and `"port": [53]` (array) forms so that
// older or hand-edited sing-box configs deserialize without error.
func (r *Rule) UnmarshalJSON(data []byte) error {
	// Use an alias to prevent infinite recursion.
	type ruleAlias Rule
	type ruleRaw struct {
		ruleAlias
		RawPort json.RawMessage `json:"port,omitempty"`
	}
	var raw ruleRaw
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*r = Rule(raw.ruleAlias)
	if len(raw.RawPort) == 0 || string(raw.RawPort) == "null" {
		return nil
	}
	// Try array first (common case).
	var ports []int
	if err := json.Unmarshal(raw.RawPort, &ports); err == nil {
		r.Port = ports
		return nil
	}
	// Fall back to scalar.
	var single int
	if err := json.Unmarshal(raw.RawPort, &single); err != nil {
		return fmt.Errorf("port: expected int or []int, got %s", string(raw.RawPort))
	}
	r.Port = []int{single}
	return nil
}

type RuleSet struct {
	Tag            string           `json:"tag"`
	Type           string           `json:"type"`
	Format         string           `json:"format,omitempty"`
	URL            string           `json:"url,omitempty"`
	UpdateInterval string           `json:"update_interval,omitempty"`
	DownloadDetour string           `json:"download_detour,omitempty"`
	Path           string           `json:"path,omitempty"`
	Rules          []map[string]any `json:"rules,omitempty"`
	// MaterializedSRS is set by ListRuleSets when a compiled .srs sibling
	// exists for an inline ruleset. Not persisted in router JSON.
	MaterializedSRS bool `json:"materialized_srs,omitempty"`
}

type Outbound struct {
	Type          string   `json:"type"`
	Tag           string   `json:"tag"`
	BindInterface string   `json:"bind_interface,omitempty"`
	Outbounds     []string `json:"outbounds,omitempty"`
	URL           string   `json:"url,omitempty"`
	Interval      string   `json:"interval,omitempty"`
	Tolerance     int      `json:"tolerance,omitempty"`
	Default       string   `json:"default,omitempty"`
	Strategy      string   `json:"strategy,omitempty"`
}

// CompositeOutboundView is the API/list projection of a composite
// outbound — the canonical Outbound plus a Source tag identifying which
// orchestrator slot owns it. "router" entries come from 20-router.json
// (mutable via this service); "subscription" entries come from
// 40-subscriptions.json (managed by the subscription service — the UI
// renders them read-only).
type CompositeOutboundView struct {
	Outbound
	Source string `json:"source"`
}

type Inbound struct {
	Type        string `json:"type"`
	Tag         string `json:"tag"`
	Listen      string `json:"listen"`
	ListenPort  int    `json:"listen_port"`
	Network     string `json:"network,omitempty"`
	UDPTimeout  string `json:"udp_timeout,omitempty"`
	UDPFragment bool   `json:"udp_fragment,omitempty"`
	TCPFastOpen bool   `json:"tcp_fast_open,omitempty"`
	RoutingMark int    `json:"routing_mark,omitempty"`
}

type Route struct {
	RuleSet []RuleSet `json:"rule_set,omitempty"`
	Rules   []Rule    `json:"rules,omitempty"`
	Final   string    `json:"final,omitempty"`
	// AutoDetectInterface controls whether sing-box picks the outbound
	// interface from the system default route. Pointer so the unset
	// value stays out of JSON — an explicit `false` would override the
	// sing-box default for users who haven't opted in to the new field
	// (configs written before v2.10.6).
	AutoDetectInterface *bool `json:"auto_detect_interface,omitempty"`
	// DefaultInterface pins outbound traffic to a specific kernel
	// interface (e.g. "ppp0", "eth3"). Mutually exclusive with
	// AutoDetectInterface in EnsureRouteWAN: setting one clears the
	// other so the emitted config never carries both. NEVER stores
	// NDMS interface ID — kernel name is the stable identifier.
	DefaultInterface string `json:"default_interface,omitempty"`
}

type DomainResolver struct {
	Server   string `json:"server"`
	Strategy string `json:"strategy,omitempty"`
}

type DNSServer struct {
	Tag  string `json:"tag"`
	Type string `json:"type"`
	// Server is omitted when empty so type=local marshals to {"type":"local","tag":"X"}
	// — sing-box 1.13's `local` server has no `server` field and FATALs the whole
	// config with `unknown field "server"` if we emit `"server": ""`. Validator
	// already permits empty Server for type=local (config_dns.go:validateDNSServer).
	Server         string          `json:"server,omitempty"`
	ServerPort     int             `json:"server_port,omitempty"`
	Path           string          `json:"path,omitempty"`
	Detour         string          `json:"detour,omitempty"`
	Strategy       string          `json:"domain_strategy,omitempty"`
	DomainResolver *DomainResolver `json:"domain_resolver,omitempty"`
}

type DNSRule struct {
	RuleSet       []string `json:"rule_set,omitempty"`
	DomainSuffix  []string `json:"domain_suffix,omitempty"`
	Domain        []string `json:"domain,omitempty"`
	DomainKeyword []string `json:"domain_keyword,omitempty"`
	DomainRegex   []string `json:"domain_regex,omitempty"`
	QueryType     []string `json:"query_type,omitempty"`
	Server        string   `json:"server,omitempty"`
	Action        string   `json:"action,omitempty"`
	Rcode         string   `json:"rcode,omitempty"`
	RejectMethod  string   `json:"method,omitempty"`
}

type DNS struct {
	Servers  []DNSServer `json:"servers,omitempty"`
	Rules    []DNSRule   `json:"rules,omitempty"`
	Final    string      `json:"final,omitempty"`
	Strategy string      `json:"strategy,omitempty"`
}

type RouterConfig struct {
	Inbounds  []Inbound  `json:"inbounds"`
	Outbounds []Outbound `json:"outbounds"`
	DNS       DNS        `json:"dns,omitempty"`
	Route     Route      `json:"route"`
}
