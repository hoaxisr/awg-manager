package router

import (
	"bytes"
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
	FakeIPTunAddr string  `json:"fakeipTunAddr,omitempty"`
	Issues        []Issue `json:"issues,omitempty"`
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
	// AwgmManaged marks auto-generated route rules owned by AWG Manager.
	// "selective-ip" rules map resolved domain IPs to proxy outbounds for
	// selective TPROXY mode; they are replaced on each ipset rebuild.
	AwgmManaged string `json:"awgm_managed,omitempty"`
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

// RuleSetHTTPClient — параметры скачивания remote rule-set (поле
// `http_client`, sing-box ≥1.14; заменяет download_detour, который
// удаляется в 1.16). Upstream-схема (option/rule_set.go:
// `HTTPClient *HTTPClientOptions json:"http_client,omitempty"`) принимает
// либо строку — тег top-level `http_clients`, либо объект с Dial-полями.
// Нам из объекта нужен только `detour`; прочие upstream-поля
// (engine/version/headers/tls) наши генераторы не пишут.
type RuleSetHTTPClient struct {
	// Tag — строковая форма (`"http_client": "tag"`): ссылка на клиент из
	// top-level `http_clients`. Заполняется только при разборе слота,
	// маршалится обратно строкой. Наши генераторы её не создают.
	Tag string `json:"-"`
	// Detour — outbound, через который скачивается набор
	// (наследник download_detour).
	Detour string `json:"detour,omitempty"`
}

// MarshalJSON эмитит строковую форму, когда клиент задан тегом.
func (h RuleSetHTTPClient) MarshalJSON() ([]byte, error) {
	if h.Tag != "" {
		return json.Marshal(h.Tag)
	}
	type alias RuleSetHTTPClient
	return json.Marshal(alias(h))
}

// UnmarshalJSON принимает обе upstream-формы: строку (тег клиента) и объект.
func (h *RuleSetHTTPClient) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		*h = RuleSetHTTPClient{}
		return json.Unmarshal(trimmed, &h.Tag)
	}
	type alias RuleSetHTTPClient
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*h = RuleSetHTTPClient(a)
	return nil
}

type RuleSet struct {
	Tag            string `json:"tag"`
	Type           string `json:"type"`
	Format         string `json:"format,omitempty"`
	URL            string `json:"url,omitempty"`
	UpdateInterval string `json:"update_interval,omitempty"`
	// HTTPClient — способ скачивания remote-набора (sing-box ≥1.14).
	// nil = явный default HTTP-клиент (top-level `http_clients` в
	// 00-base.json, системный дайлер ≈ direct). Legacy download_detour
	// принимается на входе (UnmarshalJSON) и нормализуется сюда; наружу
	// download_detour больше не эмитится — с 1.16 sing-box его отвергает,
	// а одновременно с http_client отвергает уже сейчас.
	HTTPClient *RuleSetHTTPClient `json:"http_client,omitempty"`
	Path       string             `json:"path,omitempty"`
	Rules      []map[string]any   `json:"rules,omitempty"`
	// MaterializedSRS is set by ListRuleSets when a compiled .srs sibling
	// exists for an inline ruleset. Not persisted in router JSON.
	MaterializedSRS bool `json:"materialized_srs,omitempty"`
}

// UnmarshalJSON принимает legacy-поле download_detour (слоты и API-клиенты
// времён sing-box <1.14) и нормализует его в http_client.detour.
func (rs *RuleSet) UnmarshalJSON(data []byte) error {
	type ruleSetAlias RuleSet
	var raw struct {
		ruleSetAlias
		LegacyDownloadDetour string `json:"download_detour,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*rs = RuleSet(raw.ruleSetAlias)
	if rs.HTTPClient == nil && raw.LegacyDownloadDetour != "" {
		rs.HTTPClient = &RuleSetHTTPClient{Detour: raw.LegacyDownloadDetour}
	}
	rs.normalizeHTTPClient()
	return nil
}

// normalizeHTTPClient приводит http_client к канону: пустой объект и
// detour на встроенный пустой direct-outbound эквивалентны default-клиенту
// — храним отсутствие поля. detour на пустой direct нельзя эмитить:
// sing-box ≥1.14 отвергает его на dial («detour to an empty direct
// outbound makes no sense»); legacy download_detour:"direct" работал лишь
// потому, что совместимый путь отключал эту проверку.
func (rs *RuleSet) normalizeHTTPClient() {
	if rs.HTTPClient == nil {
		return
	}
	if rs.HTTPClient.Tag == "" && (rs.HTTPClient.Detour == "" || rs.HTTPClient.Detour == "direct") {
		rs.HTTPClient = nil
	}
}

// DownloadDetourTag возвращает outbound, через который скачивается набор
// ("" — default HTTP-клиент). Единая точка чтения для валидации и ссылок.
func (rs RuleSet) DownloadDetourTag() string {
	if rs.HTTPClient == nil {
		return ""
	}
	return rs.HTTPClient.Detour
}

// setDownloadDetourTag заменяет detour свежим значением (без алиасинга
// указателя) с той же нормализацией, что и при разборе.
func (rs *RuleSet) setDownloadDetourTag(tag string) {
	if rs.HTTPClient != nil && rs.HTTPClient.Tag != "" {
		// Именованный клиент (строковая форма) — не наша ссылка на outbound.
		return
	}
	if tag == "" || tag == "direct" {
		rs.HTTPClient = nil
		return
	}
	rs.HTTPClient = &RuleSetHTTPClient{Detour: tag}
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
	Inet4Range     string          `json:"inet4_range,omitempty"`
	Inet6Range     string          `json:"inet6_range,omitempty"`
}

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
	Experimental *Experimental `json:"experimental,omitempty"`
}
