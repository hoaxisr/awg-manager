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
	Active                 bool   `json:"active"`
	NetfilterAvailable     bool   `json:"netfilterAvailable"`
	NetfilterComponentName string `json:"netfilterComponentName,omitempty"`
	TProxyTargetAvailable  bool   `json:"tproxyTargetAvailable"`
	// XtDscpAvailable reports whether iptables `-m dscp` matching is usable
	// (xt_dscp kernel module loaded/on-disk AND iptables extension present).
	// The QoS-DSCP settings UI keys its "supported" state on this field.
	XtDscpAvailable        bool   `json:"xtDscpAvailable"`
	PolicyName             string `json:"policyName"`
	PolicyMark             string `json:"policyMark,omitempty"`
	PolicyExists           bool   `json:"policyExists"`
	DeviceMode             string `json:"deviceMode"`
	SnifferEnabled         bool   `json:"snifferEnabled"`
	DeviceCount            int    `json:"deviceCount"`
	RuleCount              int    `json:"ruleCount"`
	RuleSetCount           int    `json:"ruleSetCount"`
	OutboundAWGCount       int    `json:"outboundAwgCount"`
	OutboundCompositeCount int    `json:"outboundCompositeCount"`
	Final                  string `json:"final"`
	// FakeIPIface is the active fakeip-tun kernel interface name ("opkgtun<idx>")
	// when the router is provisioned in fakeip-tun mode; empty otherwise. The UI
	// surfaces it in the «Настройки движка» panel. Populated from the persisted
	// FakeIPState.Index.
	FakeIPIface string `json:"fakeipIface,omitempty"`
	// FakeIPDns is the DNS address clients must configure manually in fakeip-tun
	// mode (DeriveTunDNS of the tun /30 gw, e.g. "172.18.0.2"); "" when not fakeip.
	FakeIPDns string `json:"fakeipDns,omitempty"`
	// FakeIPTunAddr is the fakeip-tun gateway address (the tun /30 host, e.g.
	// "172.18.0.1"); "" when not in fakeip-tun mode. Read-only, for display.
	FakeIPTunAddr string `json:"fakeipTunAddr,omitempty"`
	// CacheDBPath — эффективный путь cache.db (issue #842): по настройке, а при
	// пустой — из 00-base.json. Показывает рукописный путь, который селектор
	// «flash | tmp» выразить не может. "" без шва Deps.CacheDBPath.
	CacheDBPath string `json:"cacheDbPath,omitempty"`
	// PolicyTunIface / PolicyTunNDMSName — kernel- и NDMS-имена policy-tun
	// интерфейса ("opkgtun0" / "OpkgTun0"). Заполняются при Enabled+Provisioned,
	// ДО того как режим стал active: имя OpkgTun нужно пользователю, чтобы
	// разрешить интерфейс в политике доступа. Пусто при Enabled=false.
	PolicyTunIface    string `json:"policyTunIface,omitempty"`
	PolicyTunNDMSName string `json:"policyTunNdmsName,omitempty"`
	// PolicyTunSourcePreserve — ПРИМЕНЁННЫЙ режим NAT сегментов (static-NAT
	// вместо маскарада), а не эхо настроек: применение живёт в подъёме режима,
	// вживую опция только снимается. Указатель: nil = «поле неприменимо» (не
	// policy-tun или движок выключен), false = «применимо и выключено».
	PolicyTunSourcePreserve *bool   `json:"policyTunSourcePreserve,omitempty"`
	Issues                  []Issue `json:"issues,omitempty"`
	// LastError is the last sing-box fatal/exit reason, populated only when
	// the engine is enabled but not active (СБОЙ). Empty otherwise.
	LastError string `json:"lastError,omitempty"`
	// CrashCount — падения sing-box за последнее backoff-окно (10 мин).
	// UI показывает «Падений за 10 мин: N» в панели движка (issue #456).
	CrashCount int `json:"crashCount,omitempty"`
	// LastCrashReason — причина последнего падения в окне (например,
	// распознанный OOM-kill); пусто, когда падений в окне нет.
	LastCrashReason string `json:"lastCrashReason,omitempty"`
	// RestartSuppressedUntil — RFC3339-время, до которого авто-перезапуск
	// приостановлен анти-crash-loop backoff'ом; пусто, когда не подавлен.
	// Ручной «Перезапустить» не подавляется и сбрасывает паузу.
	RestartSuppressedUntil string `json:"restartSuppressedUntil,omitempty"`
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
	Domain       []string `json:"domain,omitempty"`
	IPCIDR       []string `json:"ip_cidr,omitempty"`
	SourceIPCIDR []string `json:"source_ip_cidr,omitempty"`
	Port         []int    `json:"port,omitempty"`
	RuleSet      []string `json:"rule_set,omitempty"`
	Protocol     string   `json:"protocol,omitempty"`
	// Inbound matches the sing-box listener tag the connection entered
	// through (native sing-box route-rule field). Managed QoS-DSCP rules use
	// it to bind a per-class inbound pair (tproxy-qos-N / redirect-qos-N) to
	// the class outbound; user rules may use it too.
	Inbound []string `json:"inbound,omitempty"`
	// IPIsPrivate, when set, matches packets whose destination is an
	// RFC1918/loopback/link-local/multicast/unspecified address — the
	// negation of sing's N.IsPublicAddr. CGNAT (100.64/10) is NOT among
	// them: netip.Addr.IsPrivate() says public, so a CGNAT destination does
	// not match (the fakeip route gate excludes it separately, see
	// excludedAddr). Pointer so
	// the zero value (unset) stays out of JSON — `{"ip_is_private":false}`
	// would change sing-box semantics. System rule from EnsureSystemRules
	// uses `*IPIsPrivate = true` as defense-in-depth: even when iptables
	// PolicyMark filter correctly keeps non-policy traffic out of
	// AWGM-TPROXY, the `hijack-dns` route action creates a kernel-level
	// transparent listener on every router LAN IP; a side-effect packet
	// that slips into sing-box from there gets routed `direct` instead
	// of ending up at `final: proxy` and being silently dropped.
	IPIsPrivate *bool `json:"ip_is_private,omitempty"`
	// Network matches the connection L4 protocol ("tcp" | "udp"). The system
	// route-options rule uses it to scope the udp_timeout override to UDP.
	Network  string `json:"network,omitempty"`
	Action   string `json:"action,omitempty"`
	Outbound string `json:"outbound,omitempty"`
	// UDPTimeout carries the `udp_timeout` route option for an
	// `action:"route-options"` rule. sing-box otherwise applies short
	// per-protocol idle timeouts to sniffed UDP (STUN/DNS 10s, QUIC/DTLS 30s)
	// that ignore the inbound udp_timeout; a route-options rule raising it to the
	// inbound value keeps games/VoIP sessions alive. A route rule action carries
	// no outbound, so this and Action are the only fields it sets.
	UDPTimeout string `json:"udp_timeout,omitempty"`
	// AwgmManaged marks auto-generated route rules owned by AWG Manager.
	// The only value still seen in the wild is the legacy "selective-ip" of
	// the removed selective-TPROXY feature: such rules are stripped from the
	// applied config by the one-shot startup cleanup.
	AwgmManaged string `json:"awgm_managed,omitempty"`
}

// ActionIsRoute reports whether the rule's action is "route" in sing-box
// semantics: an EMPTY action is route too (sing-box defaults it). Readers
// must use this instead of comparing Action == "route" — a rule persisted
// without the field (API callers omit it; the frontend always sends it) is
// executed by sing-box but was invisible to strict comparisons: the fakeip
// loop-safety gate skipped its CIDR tun-routes and the dangling-outbound
// issue detector skipped its warning (стенд-находка #534-расследования).
func (r Rule) ActionIsRoute() bool {
	return r.Action == "" || r.Action == "route"
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

// RuleSetHTTPClient — http_client remote-набора в форме sing-box 1.14
// (замена deprecated download_detour). Только в материализованном слоте:
// restoreHTTPClients возвращает DownloadDetour, API/UI его не видят.
type RuleSetHTTPClient struct {
	Detour string `json:"detour,omitempty"`
}

// HTTPClient — запись верхнеуровневого http_clients (sing-box 1.14). Один
// общий клиент rs-download для загрузки наборов, detour = route.final.
type HTTPClient struct {
	Tag    string `json:"tag"`
	Detour string `json:"detour,omitempty"`
}

type RuleSet struct {
	Tag            string             `json:"tag"`
	Type           string             `json:"type"`
	Format         string             `json:"format,omitempty"`
	URL            string             `json:"url,omitempty"`
	UpdateInterval string             `json:"update_interval,omitempty"`
	DownloadDetour string             `json:"download_detour,omitempty"`
	HTTPClient     *RuleSetHTTPClient `json:"http_client,omitempty"`
	Path           string             `json:"path,omitempty"`
	Rules          []map[string]any   `json:"rules,omitempty"`
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
	// Server and DomainResolver support fakeip-tun's domain_resolver guard:
	// a hostname-bearing proxy outbound resolves its Server via the named
	// resolver instead of the fakeip server. Both omitempty so v1 IP-bound
	// direct outbounds stay clean.
	Server         string          `json:"server,omitempty"`
	DomainResolver *DomainResolver `json:"domain_resolver,omitempty"`
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
	Listen      string `json:"listen,omitempty"`
	ListenPort  int    `json:"listen_port,omitempty"`
	Network     string `json:"network,omitempty"`
	UDPTimeout  string `json:"udp_timeout,omitempty"`
	UDPFragment bool   `json:"udp_fragment,omitempty"`
	TCPFastOpen bool   `json:"tcp_fast_open,omitempty"`
	RoutingMark int    `json:"routing_mark,omitempty"`
	// tun inbound (fakeip-tun mode)
	InterfaceName          string   `json:"interface_name,omitempty"`
	Address                []string `json:"address,omitempty"`
	MTU                    int      `json:"mtu,omitempty"`
	AutoRoute              *bool    `json:"auto_route,omitempty"`
	AutoRedirect           *bool    `json:"auto_redirect,omitempty"`
	StrictRoute            *bool    `json:"strict_route,omitempty"`
	Stack                  string   `json:"stack,omitempty"`
	EndpointIndependentNAT *bool    `json:"endpoint_independent_nat,omitempty"`
	// GSO controls sing-tun's generic-segmentation-offload on the tun device.
	// Pointer + omitempty so it's emitted ONLY when explicitly set: the gvisor
	// stack leaves it nil (omitted), while the system stack MUST set it false —
	// on this router's kernel (4.9) system+GSO panics sing-tun under load, and
	// system+gso:false is the only stable system-stack combo (PoC-proven
	// 2026-06-13; the project's sing-box alpha accepts "gso": false on tun).
	GSO *bool `json:"gso,omitempty"`
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
	// DefaultDomainResolver names the DNS server used to resolve outbound
	// hostnames that no rule pins elsewhere (fakeip-tun: a "real" resolver
	// so proxy server hostnames don't get fakeip addresses).
	DefaultDomainResolver *DomainResolver `json:"default_domain_resolver,omitempty"`
	// DefaultHTTPClient — тег клиента для загрузки наборов без http_client.
	// Ставится материализацией (applyHTTPClients), в хранимой форме пуст.
	DefaultHTTPClient string `json:"default_http_client,omitempty"`
}

type DomainResolver struct {
	Server   string `json:"server"`
	Strategy string `json:"strategy,omitempty"`
}

// DNSClientTLSOptions is the client-side TLS subset supported by the DNS
// server editor. The DNS server type itself enables TLS where applicable.
type DNSClientTLSOptions struct {
	ServerName                 string   `json:"server_name,omitempty"`
	Insecure                   bool     `json:"insecure,omitempty"`
	ALPN                       []string `json:"alpn,omitempty"`
	MinVersion                 string   `json:"min_version,omitempty"`
	MaxVersion                 string   `json:"max_version,omitempty"`
	CertificatePublicKeySHA256 []string `json:"certificate_public_key_sha256,omitempty"`
}

type DNSServer struct {
	Tag  string `json:"tag"`
	Type string `json:"type"`
	// Server is omitted when empty so type=local marshals to {"type":"local","tag":"X"}
	// — sing-box 1.13's `local` server has no `server` field and FATALs the whole
	// config with `unknown field "server"` if we emit `"server": ""`. Validator
	// already permits empty Server for type=local (config_dns.go:validateDNSServer).
	Server         string               `json:"server,omitempty"`
	ServerPort     int                  `json:"server_port,omitempty"`
	Path           string               `json:"path,omitempty"`
	Detour         string               `json:"detour,omitempty"`
	Strategy       string               `json:"domain_strategy,omitempty"`
	DomainResolver *DomainResolver      `json:"domain_resolver,omitempty"`
	TLS            *DNSClientTLSOptions `json:"tls,omitempty"`
	Inet4Range     string               `json:"inet4_range,omitempty"`
	Inet6Range     string               `json:"inet6_range,omitempty"`
}

// DNSMatchResponse — union sing-box `match_response`: true | "<tag>"
// (option/rule_dns.go beta.1). false сериализуется как false — как в upstream.
type DNSMatchResponse struct {
	Enabled bool
	Tag     string
}

func (m *DNSMatchResponse) UnmarshalJSON(b []byte) error {
	var v bool
	if err := json.Unmarshal(b, &v); err == nil {
		*m = DNSMatchResponse{Enabled: v}
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("match_response: ожидается true или строка-тег")
	}
	if s == "" {
		return fmt.Errorf("match_response: пустой тег")
	}
	*m = DNSMatchResponse{Enabled: true, Tag: s}
	return nil
}

func (m DNSMatchResponse) MarshalJSON() ([]byte, error) {
	if m.Tag != "" {
		return json.Marshal(m.Tag)
	}
	return json.Marshal(m.Enabled)
}

// IsEnabled nil-safe: правило без match_response не матчит ответы.
func (m *DNSMatchResponse) IsEnabled() bool { return m != nil && m.Enabled }

type DNSRule struct {
	RuleSet       []string `json:"rule_set,omitempty"`
	SourceIPCIDR  []string `json:"source_ip_cidr,omitempty"`
	DomainSuffix  []string `json:"domain_suffix,omitempty"`
	Domain        []string `json:"domain,omitempty"`
	DomainKeyword []string `json:"domain_keyword,omitempty"`
	DomainRegex   []string `json:"domain_regex,omitempty"`
	QueryType     []string `json:"query_type,omitempty"`
	Server        string   `json:"server,omitempty"`
	Action        string   `json:"action,omitempty"`
	Rcode         string   `json:"rcode,omitempty"`
	RejectMethod  string   `json:"method,omitempty"`

	Tag            string            `json:"tag,omitempty"`
	MatchResponse  *DNSMatchResponse `json:"match_response,omitempty"`
	IPCIDR         []string          `json:"ip_cidr,omitempty"`
	ResponseRcode  string            `json:"response_rcode,omitempty"`
	ResponseAnswer []string          `json:"response_answer,omitempty"`
	ResponseNS     []string          `json:"response_ns,omitempty"`
	ResponseExtra  []string          `json:"response_extra,omitempty"`
	Race           bool              `json:"race,omitempty"`
	Speculative    bool              `json:"speculative,omitempty"`
}

type DNS struct {
	Servers  []DNSServer `json:"servers,omitempty"`
	Rules    []DNSRule   `json:"rules,omitempty"`
	Final    string      `json:"final,omitempty"`
	Strategy string      `json:"strategy,omitempty"`
}

type CacheFile struct {
	Enabled     bool   `json:"enabled"`
	StoreFakeIP bool   `json:"store_fakeip,omitempty"`
	StoreDNS    bool   `json:"store_dns,omitempty"`
	Path        string `json:"path,omitempty"`
}

type Experimental struct {
	CacheFile *CacheFile `json:"cache_file,omitempty"`
}

type RouterConfig struct {
	Inbounds     []Inbound     `json:"inbounds"`
	Outbounds    []Outbound    `json:"outbounds"`
	DNS          DNS           `json:"dns,omitempty"`
	Route        Route         `json:"route"`
	HTTPClients  []HTTPClient  `json:"http_clients,omitempty"`
	Experimental *Experimental `json:"experimental,omitempty"`
}
