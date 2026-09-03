package api

import (
	"time"
)

// SingboxRouterIssueDTO mirrors router.Issue (one entry of Status.Issues).
type SingboxRouterIssueDTO struct {
	Severity  string `json:"severity" example:"warning"`
	Kind      string `json:"kind" example:"missing-outbound"`
	RuleIndex int    `json:"ruleIndex,omitempty" example:"0"`
	Tag       string `json:"tag,omitempty" example:"selector"`
	Message   string `json:"message" example:"outbound 'selector' is referenced but does not exist"`
}

// SingboxRouterStatusData mirrors router.Status.
type SingboxRouterStatusData struct {
	Enabled                bool   `json:"enabled" example:"true"`
	Installed              bool   `json:"installed" example:"true"`
	Active                 bool   `json:"active" example:"true"`
	NetfilterAvailable     bool   `json:"netfilterAvailable" example:"true"`
	NetfilterComponentName string `json:"netfilterComponentName,omitempty" example:"iptables-mod-tproxy"`
	TProxyTargetAvailable  bool   `json:"tproxyTargetAvailable" example:"true"`
	// XtDscpAvailable reports whether iptables DSCP matching is usable
	// (xt_dscp kernel module present AND iptables `-m dscp` extension works).
	// The QoS-DSCP settings UI keys its "supported" badge on this field.
	XtDscpAvailable        bool   `json:"xtDscpAvailable" example:"true"`
	PolicyName             string `json:"policyName" example:"awgm-router"`
	PolicyMark             string `json:"policyMark,omitempty" example:"0xffffaaa"`
	PolicyExists           bool   `json:"policyExists" example:"true"`
	DeviceMode             string `json:"deviceMode" example:"policy" enums:"policy,all"`
	SnifferEnabled         bool   `json:"snifferEnabled" example:"true"`
	DeviceCount            int    `json:"deviceCount" example:"3"`
	RuleCount              int    `json:"ruleCount" example:"12"`
	RuleSetCount           int    `json:"ruleSetCount" example:"4"`
	OutboundAWGCount       int    `json:"outboundAwgCount" example:"2"`
	OutboundCompositeCount int    `json:"outboundCompositeCount" example:"1"`
	Final                  string `json:"final" example:"direct"`
	FakeIPIface            string `json:"fakeipIface,omitempty" example:"opkgtun0"`
	FakeIPDns              string `json:"fakeipDns,omitempty" example:"172.18.0.2"`
	FakeIPTunAddr          string `json:"fakeipTunAddr,omitempty" example:"172.18.0.1"`
	// CacheDBPath — эффективный путь cache.db: по cacheFileLocation, при пустой
	// настройке — из 00-base.json (рукописный путь сохраняется).
	CacheDBPath string `json:"cacheDbPath,omitempty" example:"/opt/etc/awg-manager/singbox/cache.db"`
	// PolicyTunIface / PolicyTunNDMSName — kernel- и NDMS-имена policy-tun
	// интерфейса. Заполнены при Enabled+Provisioned (ДО того как режим стал
	// active): по NDMS-имени пользователь разрешает интерфейс в политике
	// доступа. Пусто вне policy-tun и при выключенном движке.
	PolicyTunIface    string `json:"policyTunIface,omitempty" example:"opkgtun0"`
	PolicyTunNDMSName string `json:"policyTunNdmsName,omitempty" example:"OpkgTun0"`
	// PolicyTunSourcePreserve — применённый режим NAT сегментов (static-NAT
	// вместо маскарада). Указатель: отсутствует = «неприменимо» (не policy-tun
	// или движок выключен), false = «применимо и выключено».
	PolicyTunSourcePreserve *bool  `json:"policyTunSourcePreserve,omitempty" example:"true"`
	LastError               string `json:"lastError,omitempty" example:"engine start failed"`
	// CrashCount — падения sing-box за последние 10 минут (issue #456).
	CrashCount int `json:"crashCount,omitempty" example:"2"`
	// LastCrashReason — причина последнего падения в окне (например, OOM-kill).
	LastCrashReason string `json:"lastCrashReason,omitempty" example:"sing-box убит OOM-killer'ом"`
	// RestartSuppressedUntil — RFC3339-время окончания паузы авто-перезапуска
	// (анти crash-loop); пусто, когда авто-перезапуск не подавлен.
	RestartSuppressedUntil string                  `json:"restartSuppressedUntil,omitempty" example:"2026-07-06T12:34:56+03:00"`
	Issues                 []SingboxRouterIssueDTO `json:"issues,omitempty"`
}

// SingboxRouterStatusResponse is the envelope for GET /singbox/router/status.
type SingboxRouterStatusResponse struct {
	Success bool                    `json:"success" example:"true"`
	Data    SingboxRouterStatusData `json:"data"`
}

// SingboxRouterSettingsData mirrors storage.SingboxRouterSettings.
type SingboxRouterSettingsData struct {
	Enabled        bool   `json:"enabled" example:"true"`
	PolicyName     string `json:"policyName" example:"awgm-router"`
	DeviceMode     string `json:"deviceMode,omitempty" example:"policy" enums:"policy,all"`
	RoutingMode    string `json:"routingMode,omitempty" example:"tproxy" enums:"tproxy,fakeip-tun,policy-tun"`
	SnifferEnabled bool   `json:"snifferEnabled" example:"true"`
	// WANAutoDetect / WANInterface form a two-field discriminator:
	//   true  + ""    → sing-box auto_detect_interface
	//   false + "ppp0"→ sing-box default_interface=ppp0
	// Other combinations are rejected by the backend validator.
	// Example below shows the PINNED case as it's the more interesting
	// shape to document (auto case has WANInterface omitted via omitempty
	// and wanAutoDetect=true); both examples are intentionally consistent.
	WANAutoDetect bool   `json:"wanAutoDetect" example:"false"`
	WANInterface  string `json:"wanInterface,omitempty" example:"ppp0"`
	// BypassPresets lists active named bypass presets.
	// Valid values: "l2tp", "ntp", "netbios-smb" (port-based), "keendns"
	// (имена KeenDNS/CrazeDNS резолвит сам роутер, плюс обход его адресов).
	// Default includes "keendns".
	BypassPresets []string `json:"bypassPresets,omitempty" example:"keendns"`
	// BypassExtraPorts is a user-defined comma-separated list of extra
	// port exclusions in "PORT UDP|TCP" format (e.g. "51820 UDP, 1194 TCP").
	BypassExtraPorts string `json:"bypassExtraPorts,omitempty" example:"51820 UDP"`
	// BypassExtraSubnets is a user-defined comma/space-separated list of IPv4
	// IP/CIDR destinations whose traffic bypasses sing-box entirely (incl.
	// DNS/53). Bare IP is treated as /32. E.g. "203.0.113.0/24, 10.8.0.5".
	BypassExtraSubnets string `json:"bypassExtraSubnets,omitempty" example:"203.0.113.0/24"`
	// BypassGeoipTags lists geoip tags from the configured .dat files whose
	// CIDRs bypass sing-box entirely via the AWGM-BYPASS ipset (same full
	// bypass semantics as bypassExtraSubnets, ipset-backed because of scale).
	BypassGeoipTags []string `json:"bypassGeoipTags,omitempty" example:"geoip:ru"`
	// IngressInterfaces lists interface refs whose ingress traffic is
	// redirected through the sing-box router (e.g. "managed:Wireguard3").
	IngressInterfaces []string `json:"ingressInterfaces,omitempty" example:"managed:Wireguard3"`
	// --- fakeip-tun engine settings (user-editable) ---
	// FakeIPStack selects the sing-tun stack: "gvisor" (default) or "system".
	FakeIPStack string `json:"fakeipStack,omitempty" example:"gvisor" enums:"gvisor,system"`
	// FakeIPPool4 is the fakeip v4 pool CIDR (default "198.18.0.0/15").
	FakeIPPool4 string `json:"fakeipPool4,omitempty" example:"198.18.0.0/15"`
	// FakeIPPool6 is the fakeip v6 pool CIDR (default "fc00::/18").
	// Explicit "" disables v6; an OMITTED field keeps the stored value
	// (absent != empty — the handler substitutes the current setting).
	FakeIPPool6 string `json:"fakeipPool6,omitempty" example:"fc00::/18"`
	// FakeIPMTU is the tun MTU (default 1500; valid range 576-9000).
	FakeIPMTU int `json:"fakeipMtu,omitempty" example:"1500"`
	// FakeIPRealServer is the upstream resolver the engine-managed "real" DNS
	// server forwards to (default "1.1.1.1"). Must be a plain IP address. Also
	// captured from a user edit of the "real" server address in the fakeip DNS
	// panel.
	FakeIPRealServer string `json:"fakeipRealServer,omitempty" example:"1.1.1.1"`
	// UDPTimeout sets the UDP session timeout for the tproxy-in / fakeip tun-in
	// inbound (Go duration string, e.g. "5m0s", "10m0s"). Empty = use default
	// (5m0s). Increase to prevent long-quiet UDP applications (games, etc.) from
	// having their sessions silently dropped mid-game.
	UDPTimeout string `json:"udpTimeout,omitempty" example:"10m0s"`
	// QoSClasses lists DSCP-based QoS traffic classes routed to dedicated
	// outbounds. At most 8 classes; DSCP must be 0-63 and unique across
	// classes; outbound is required; name is limited to 32 characters.
	// Only effective when routingMode is "tproxy" or "policy-tun" (DSCP-only
	// hybrid) and xt_dscp is available (see status.xtDscpAvailable).
	// Empty = feature off.
	QoSClasses []SingboxRouterQoSClassDTO `json:"qosClasses,omitempty"`
	// PolicyTunSourcePreserve переводит выбранные сегменты на static-NAT в WAN,
	// чтобы sing-box видел реальные адреса клиентов, а не маскарад в tun.
	// Требует непустого policyTunNatSegments (иначе 400).
	PolicyTunSourcePreserve bool `json:"policyTunSourcePreserve,omitempty" example:"true"`
	// PolicyTunNATSegments — выбранные пользователем сегменты для
	// source-preserve (см. GET /singbox/router/policy-tun/nat-preview).
	// Обнуляется бэкендом при policyTunSourcePreserve=false.
	PolicyTunNATSegments []string `json:"policyTunNatSegments,omitempty" example:"Home"`
	// CacheFileLocation — место хранения cache.db sing-box: "flash" —
	// /opt/etc/awg-manager/singbox/cache.db, "tmp" — /tmp/singbox-cache.db в RAM,
	// записи кэша перестают изнашивать флеш, но кэш не переживает перезагрузку.
	// Пусто — не задано: путь из 00-base.json как есть (см. cacheDbPath в статусе).
	CacheFileLocation string `json:"cacheFileLocation,omitempty" example:"flash" enums:"flash,tmp"`
}

// SingboxRouterQoSClassDTO mirrors storage.SingboxQoSClass — one DSCP-based
// QoS traffic class.
type SingboxRouterQoSClassDTO struct {
	DSCP     int    `json:"dscp" example:"46" minimum:"0" maximum:"63"`
	Name     string `json:"name,omitempty" example:"VoIP" maxLength:"32"`
	Outbound string `json:"outbound" example:"my-selector"`
	Enabled  bool   `json:"enabled" example:"true"`
	// Slot is the backend-assigned stable listen-port slot (0-7). Read-only:
	// clients send classes without it and the backend re-associates each
	// class with its persisted slot by DSCP, so a class keeps its ports when
	// other classes are edited, disabled or removed. Any value sent by a
	// client is ignored.
	Slot int `json:"slot" example:"0" minimum:"0" maximum:"7" readonly:"true"`
}

// SingboxRouterSettingsResponse is the envelope for GET /singbox/router/settings.
type SingboxRouterSettingsResponse struct {
	Success bool                      `json:"success" example:"true"`
	Data    SingboxRouterSettingsData `json:"data"`
}

// SingboxRouterRuleDTO mirrors router.Rule (a routing rule in priority order).
type SingboxRouterRuleDTO struct {
	DomainSuffix []string `json:"domain_suffix,omitempty" example:".example.com"`
	IPCIDR       []string `json:"ip_cidr,omitempty" example:"10.0.0.0/8"`
	SourceIPCIDR []string `json:"source_ip_cidr,omitempty" example:"192.168.1.100/32"`
	Port         []int    `json:"port,omitempty" example:"443"`
	RuleSet      []string `json:"rule_set,omitempty" example:"geosite-cn"`
	Protocol     string   `json:"protocol,omitempty" example:"tcp"`
	// Inbound matches sing-box listener tags the connection entered through.
	// Managed QoS-DSCP rules use the reserved tproxy-qos-N / redirect-qos-N
	// tag pair (N = DSCP codepoint).
	Inbound  []string `json:"inbound,omitempty" example:"tproxy-qos-46"`
	Action   string   `json:"action" example:"route"`
	Outbound string   `json:"outbound,omitempty" example:"selector"`
}

// SingboxRouterRulesListResponse is the envelope for GET /singbox/router/rules/list.
type SingboxRouterRulesListResponse struct {
	Success bool                   `json:"success" example:"true"`
	Data    []SingboxRouterRuleDTO `json:"data"`
}

// SingboxRouterRuleSetDTO mirrors router.RuleSet.
type SingboxRouterRuleSetDTO struct {
	Tag             string           `json:"tag" example:"geosite-cn"`
	Type            string           `json:"type" example:"remote"`
	Format          string           `json:"format,omitempty" example:"binary"`
	URL             string           `json:"url,omitempty" example:"https://cdn.example.com/geosite-cn.srs"`
	UpdateInterval  string           `json:"update_interval,omitempty" example:"24h"`
	DownloadDetour  string           `json:"download_detour,omitempty" example:"direct"`
	Path            string           `json:"path,omitempty" example:"/opt/etc/singbox/rulesets/geosite-cn.srs"`
	Rules           []map[string]any `json:"rules,omitempty"`
	MaterializedSRS bool             `json:"materialized_srs,omitempty" example:"true"`
}

// SingboxRouterRuleSetUpdateRequest is the body for POST /singbox/router/rulesets/update.
type SingboxRouterRuleSetUpdateRequest struct {
	Tag     string                  `json:"tag" example:"geosite-cn"`
	RuleSet SingboxRouterRuleSetDTO `json:"ruleSet"`
}

// SingboxRouterRuleSetsListResponse is the envelope for GET /singbox/router/rulesets/list.
type SingboxRouterRuleSetsListResponse struct {
	Success bool                      `json:"success" example:"true"`
	Data    []SingboxRouterRuleSetDTO `json:"data"`
}

// SingboxRouterDatRuleSetURLData is the payload for GET /singbox/router/rulesets/dat-url.
type SingboxRouterDatRuleSetURLData struct {
	URL string `json:"url" example:"http://127.0.0.1:2222/api/singbox/router/rulesets/dat-srs?kind=geosite&tag=GOOGLE&token=..."`
}

// SingboxRouterDatRuleSetURLResponse is the envelope for dat rule-set URL metadata.
type SingboxRouterDatRuleSetURLResponse struct {
	Success bool                           `json:"success" example:"true"`
	Data    SingboxRouterDatRuleSetURLData `json:"data"`
}

// SingboxRouterOutboundDTO mirrors router.Outbound (composite outbound).
type SingboxRouterOutboundDTO struct {
	Type          string   `json:"type" example:"selector"`
	Tag           string   `json:"tag" example:"my-selector"`
	BindInterface string   `json:"bind_interface,omitempty" example:"awg-vpn0"`
	Outbounds     []string `json:"outbounds,omitempty" example:"awg-vpn0"`
	URL           string   `json:"url,omitempty" example:"https://www.gstatic.com/generate_204"`
	Interval      string   `json:"interval,omitempty" example:"3m"`
	Tolerance     int      `json:"tolerance,omitempty" example:"50"`
	Default       string   `json:"default,omitempty" example:"awg-vpn0"`
	Strategy      string   `json:"strategy,omitempty" example:"prefer_ipv4"`
	Source        string   `json:"source" example:"router" enums:"router,subscription"`
}

// SingboxRouterOutboundsListResponse is the envelope for GET /singbox/router/outbounds/list.
type SingboxRouterOutboundsListResponse struct {
	Success bool                       `json:"success" example:"true"`
	Data    []SingboxRouterOutboundDTO `json:"data"`
}

// SingboxRouterPresetRuleRefDTO mirrors router.RuleRef.
type SingboxRouterPresetRuleRefDTO struct {
	Tag string `json:"tag" example:"geosite-cn"`
}

// SingboxRouterPresetRuleLinkDTO mirrors router.RuleLink.
type SingboxRouterPresetRuleLinkDTO struct {
	RuleSet      []string `json:"rule_set,omitempty" example:"geosite-cn"`
	DomainSuffix []string `json:"domain_suffix,omitempty" example:".cn"`
	Action       string   `json:"action,omitempty" example:"route"`
}

// SingboxRouterPresetDTO mirrors router.Preset (one entry of the preset catalog).
type SingboxRouterPresetDTO struct {
	ID        string                           `json:"id" example:"china-direct"`
	Name      string                           `json:"name" example:"China Direct"`
	Category  string                           `json:"category,omitempty" example:"social"`
	IconSlug  string                           `json:"iconSlug,omitempty" example:"china"`
	RuleSets  []SingboxRouterPresetRuleRefDTO  `json:"ruleSets"`
	Rules     []SingboxRouterPresetRuleLinkDTO `json:"rules"`
	Notice    string                           `json:"notice,omitempty" example:"Routes mainland China traffic via the direct outbound."`
	Covers    []string                         `json:"covers,omitempty" example:"instagram,whatsapp"`
	Featured  bool                             `json:"featured,omitempty" example:"true"`
	Sensitive bool                             `json:"sensitive,omitempty" example:"false"`
}

// SingboxRouterPresetsListResponse is the envelope for GET /singbox/router/presets/list.
type SingboxRouterPresetsListResponse struct {
	Success bool                     `json:"success" example:"true"`
	Data    []SingboxRouterPresetDTO `json:"data"`
}

// SingboxRouterPolicyInfoDTO mirrors router.PolicyInfo (NDMS policy projection).
type SingboxRouterPolicyInfoDTO struct {
	Name         string `json:"name" example:"Policy0"`
	Description  string `json:"description" example:"Default policy"`
	Mark         string `json:"mark,omitempty" example:"0xffffaaa"`
	DeviceCount  int    `json:"deviceCount" example:"3"`
	IsOurDefault bool   `json:"isOurDefault" example:"false"`
}

// SingboxRouterPoliciesListResponse is the envelope for GET /singbox/router/policies.
type SingboxRouterPoliciesListResponse struct {
	Success bool                         `json:"success" example:"true"`
	Data    []SingboxRouterPolicyInfoDTO `json:"data"`
}

// SingboxRouterPolicyResponse is the envelope for POST /singbox/router/policies (single policy).
type SingboxRouterPolicyResponse struct {
	Success bool                       `json:"success" example:"true"`
	Data    SingboxRouterPolicyInfoDTO `json:"data"`
}

// SingboxRouterWANInterfaceDTO mirrors router.WANInterfaceInfo for the
// WAN-binding picker.
type SingboxRouterWANInterfaceDTO struct {
	Name     string `json:"name" example:"ppp0"`
	ID       string `json:"id" example:"PPPoE0"`
	Label    string `json:"label" example:"Резервный канал"`
	Up       bool   `json:"up" example:"true"`
	Priority int    `json:"priority" example:"700000"`
}

// SingboxRouterWANInterfacesListResponse is the envelope for
// GET /singbox/router/wan-interfaces and GET /singbox/router/bindable-interfaces.
// For the bindable-interfaces endpoint, id and priority are always zero (only name, label, up are populated).
type SingboxRouterWANInterfacesListResponse struct {
	Success bool                           `json:"success" example:"true"`
	Data    []SingboxRouterWANInterfaceDTO `json:"data"`
}

// SingboxRouterNATSegmentDTO mirrors router.NATSegmentInfo — один сегмент
// роутера с текущим режимом NAT. staticWan заполнен только для mode=static.
type SingboxRouterNATSegmentDTO struct {
	Name      string `json:"name" example:"Home"`
	Mode      string `json:"mode" example:"dynamic" enums:"dynamic,static,none"`
	StaticWAN string `json:"staticWan,omitempty" example:"PPPoE0"`
	// Label — описание сегмента из NDMS, Subnet — его подсеть. Пустые, когда
	// NDMS описания не дал или адресации у сегмента нет.
	Label  string `json:"label,omitempty" example:"Домашняя сеть"`
	Subnet string `json:"subnet,omitempty" example:"192.168.1.0/24"`
}

// SingboxRouterNATEgressDTO mirrors router.NATEgress — выход роутера, на
// котором подмена адреса сохранится.
type SingboxRouterNATEgressDTO struct {
	Name  string `json:"name" example:"PPPoE0"`
	Label string `json:"label,omitempty" example:"Провайдер"`
}

// SingboxRouterNATPreviewData is the payload of
// GET /singbox/router/policy-tun/nat-preview.
type SingboxRouterNATPreviewData struct {
	Segments []SingboxRouterNATSegmentDTO `json:"segments"`
	// Egresses — выходы, на которых подмена адреса СОХРАНИТСЯ (туда встанет
	// static-NAT). Их несколько: `ip static` — общероутерная настройка, и
	// правило вешается на каждый интерфейс с `ip global`. Пусто, когда выходы
	// определить нельзя.
	Egresses []SingboxRouterNATEgressDTO `json:"egresses,omitempty"`
}

// SingboxRouterNATPreviewResponse is the envelope for
// GET /singbox/router/policy-tun/nat-preview.
type SingboxRouterNATPreviewResponse struct {
	Success bool                        `json:"success" example:"true"`
	Data    SingboxRouterNATPreviewData `json:"data"`
}

// SingboxRouterPolicyDeviceDTO mirrors router.PolicyDevice.
type SingboxRouterPolicyDeviceDTO struct {
	MAC   string `json:"mac" example:"aa:bb:cc:dd:ee:ff"`
	IP    string `json:"ip" example:"192.168.1.100"`
	Name  string `json:"name,omitempty" example:"My Phone"`
	Bound bool   `json:"bound" example:"true"`
}

// SingboxRouterPolicyDevicesListResponse is the envelope for GET /singbox/router/policy-devices.
type SingboxRouterPolicyDevicesListResponse struct {
	Success bool                           `json:"success" example:"true"`
	Data    []SingboxRouterPolicyDeviceDTO `json:"data"`
}

// SingboxRouterModeRequest is the body for POST /singbox/router/mode.
type SingboxRouterModeRequest struct {
	Mode string `json:"mode" example:"fakeip-tun" enums:"off,tproxy,fakeip-tun,policy-tun"`
}

// SingboxRouterTransitionStepDTO mirrors router.TransitionStep — one milestone
// of a routing-mode switch.
type SingboxRouterTransitionStepDTO struct {
	Step    string `json:"step" example:"provision" enums:"start,teardown,provision,readiness,ready,rollback,error"`
	Status  string `json:"status" example:"current" enums:"current,done,error"`
	Message string `json:"message,omitempty" example:"restored tproxy"`
}

// SingboxRouterTransitionData mirrors router.TransitionEvent. It documents the
// payload of the "singbox-router:transition" events emitted on the GET /events
// SSE stream during a POST /singbox/router/mode switch (the UI progress screen).
type SingboxRouterTransitionData struct {
	TransitionID string                         `json:"transitionId" example:"switch-7"`
	From         string                         `json:"from" example:"tproxy" enums:"off,tproxy,fakeip-tun,policy-tun"`
	To           string                         `json:"to" example:"fakeip-tun" enums:"off,tproxy,fakeip-tun,policy-tun"`
	Step         SingboxRouterTransitionStepDTO `json:"step"`
	Done         bool                           `json:"done,omitempty" example:"true"`
	FinalState   string                         `json:"finalState,omitempty" example:"fakeip-tun" enums:"off,tproxy,fakeip-tun,policy-tun"`
	Error        string                         `json:"error,omitempty" example:""`
}

// SingboxRouterRuleUpdateRequest is the body for POST /singbox/router/rules/update.
type SingboxRouterRuleUpdateRequest struct {
	Index int                  `json:"index" example:"0"`
	Rule  SingboxRouterRuleDTO `json:"rule"`
}

// SingboxRouterRuleBulkOutboundRequest is the body for POST
// /singbox/router/rules/bulk-outbound (and its fakeip-config counterpart).
type SingboxRouterRuleBulkOutboundRequest struct {
	Indices  []int  `json:"indices" example:"0,2"`
	Outbound string `json:"outbound" example:"my-selector"`
}

// SingboxRouterRuleSetBulkDetourRequest is the body for POST
// /singbox/router/rulesets/bulk-detour (and its fakeip-config counterpart).
type SingboxRouterRuleSetBulkDetourRequest struct {
	Tags           []string `json:"tags" example:"geosite-cn"`
	DownloadDetour string   `json:"downloadDetour" example:"direct"`
}

// SingboxRouterBulkUpdatedData is the payload for bulk rule/ruleset mutation
// endpoints — reports how many entries were updated.
type SingboxRouterBulkUpdatedData struct {
	Updated int `json:"updated" example:"2"`
}

// SingboxRouterBulkUpdatedResponse is the typed envelope for bulk mutation
// endpoints. Use as @Success {object} SingboxRouterBulkUpdatedResponse.
type SingboxRouterBulkUpdatedResponse struct {
	Success bool                         `json:"success" example:"true"`
	Data    SingboxRouterBulkUpdatedData `json:"data"`
}

// SingboxRouterRuleDeleteRequest is the body for POST /singbox/router/rules/delete.
type SingboxRouterRuleDeleteRequest struct {
	Index int `json:"index" example:"0"`
}

// SingboxRouterRuleMoveRequest is the body for POST /singbox/router/rules/move.
type SingboxRouterRuleMoveRequest struct {
	From int `json:"from" example:"3"`
	To   int `json:"to" example:"0"`
}

// SingboxRouterRuleSetDeleteRequest is the body for POST /singbox/router/rulesets/delete.
type SingboxRouterRuleSetDeleteRequest struct {
	Tag   string `json:"tag" example:"geosite-cn"`
	Force bool   `json:"force" example:"false"`
}

// SingboxRouterOutboundUpdateRequest is the body for POST /singbox/router/outbounds/update.
type SingboxRouterOutboundUpdateRequest struct {
	Tag      string                   `json:"tag" example:"my-selector"`
	Outbound SingboxRouterOutboundDTO `json:"outbound"`
}

// SingboxRouterOutboundDeleteRequest is the body for POST /singbox/router/outbounds/delete.
type SingboxRouterOutboundDeleteRequest struct {
	Tag   string `json:"tag" example:"my-selector"`
	Force bool   `json:"force" example:"false"`
}

// SingboxRouterApplyPresetRequest is the body for POST /singbox/router/presets/apply.
type SingboxRouterApplyPresetRequest struct {
	ID       string `json:"id" example:"china-direct"`
	Outbound string `json:"outbound" example:"awg-vpn0"`
}

// SingboxRouterCreatePolicyRequest is the body for POST /singbox/router/policies.
type SingboxRouterCreatePolicyRequest struct {
	Description string `json:"description" example:"My VPN policy"`
}

// SingboxRouterBindDeviceRequest is the body for POST /singbox/router/policy-devices/bind.
type SingboxRouterBindDeviceRequest struct {
	MAC        string `json:"mac" example:"aa:bb:cc:dd:ee:ff"`
	PolicyName string `json:"policyName" example:"Policy0"`
}

// SingboxRouterUnbindDeviceRequest is the body for POST /singbox/router/policy-devices/unbind.
type SingboxRouterUnbindDeviceRequest struct {
	MAC string `json:"mac" example:"aa:bb:cc:dd:ee:ff"`
}

// SingboxRouterRouteFinalRequest is the body for POST /singbox/router/route/final.
type SingboxRouterRouteFinalRequest struct {
	Final string `json:"final" example:"direct"`
}

// RouterStagingStatusResponse is the body of GET /api/singbox/router/staging.
// HasDraft=false means DraftedAt and Validation are absent.
type RouterStagingStatusResponse struct {
	HasDraft   bool                 `json:"hasDraft"`
	DraftedAt  *time.Time           `json:"draftedAt,omitempty"`
	Validation *RouterValidationDTO `json:"validation,omitempty"`
}

// RouterValidationDTO carries a structured list of cross-slot validation errors.
// Used by the staging-status payload (preview) and by 422 responses from /staging/apply.
type RouterValidationDTO struct {
	Errors []RouterValidationErrorDTO `json:"errors"`
}

// RouterValidationErrorDTO mirrors orchestrator.ValidationError.
type RouterValidationErrorDTO struct {
	Slot    string `json:"slot"`
	Kind    string `json:"kind"`
	Tag     string `json:"tag,omitempty"`
	InRule  string `json:"inRule,omitempty"`
	Message string `json:"message"`
}

// RouterStagingValidationError is the body of 422 responses from /staging/apply.
// Either Validation or SbCheck is populated (exclusive).
type RouterStagingValidationError struct {
	Validation *RouterValidationDTO `json:"validation,omitempty"`
	SbCheck    string               `json:"sbCheck,omitempty"`
}
