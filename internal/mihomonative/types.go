// Package mihomonative owns proxy nodes and subscriptions that are executed
// natively by Mihomo. It deliberately does not depend on sing-box storage.
package mihomonative

import (
	"time"
)

type EnginePreference string

const (
	EngineAuto    EnginePreference = "auto"
	EngineSingbox EnginePreference = "sing-box"
	EngineMihomo  EnginePreference = "mihomo"
)

type Support struct {
	Supported bool   `json:"supported"`
	Reason    string `json:"reason,omitempty"`
}

type Compatibility struct {
	Singbox Support `json:"sing-box"`
	Mihomo  Support `json:"mihomo"`
}

// ProxyBridge is the stable local egress exported to KeenOS. Mihomo listens
// on ListenPort and routes the listener to exactly one native proxy/group;
// NDMS ProxyN exposes the same path as kernel interface t2sN so the rest of
// AWG Manager (policies, HR Neo and diagnostics) can consume it.
type ProxyBridge struct {
	ListenPort      int    `json:"listenPort"`
	ProxyIndex      int    `json:"proxyIndex"`
	ProxyInterface  string `json:"proxyInterface"`
	KernelInterface string `json:"kernelInterface"`
	LegacyOwner     string `json:"legacyOwner,omitempty"`
}

type BridgeListener struct {
	Name  string
	Port  int
	Proxy string
}

type ProxyNode struct {
	ID               string                 `json:"id"`
	Name             string                 `json:"name"`
	Protocol         string                 `json:"protocol"`
	Transport        string                 `json:"transport"`
	EnginePreference EnginePreference       `json:"enginePreference"`
	SelectedEngine   EnginePreference       `json:"selectedEngine"`
	SourceID         string                 `json:"sourceId,omitempty"`
	RawURI           string                 `json:"rawUri,omitempty"`
	NativeConfig     map[string]interface{} `json:"nativeConfig"`
	Compatibility    Compatibility          `json:"compatibility"`
	Enabled          bool                   `json:"enabled"`
	Bridge           *ProxyBridge           `json:"bridge,omitempty"`
	CreatedAt        time.Time              `json:"createdAt"`
	UpdatedAt        time.Time              `json:"updatedAt"`
}

type SubscriptionFormat string

const (
	FormatAuto           SubscriptionFormat = "auto"
	FormatShareLinks     SubscriptionFormat = "share-links"
	FormatMihomoProvider SubscriptionFormat = "mihomo-provider"
)

type Subscription struct {
	ID               string              `json:"id"`
	Name             string              `json:"name"`
	URL              string              `json:"url,omitempty"`
	Inline           string              `json:"inline,omitempty"`
	Format           SubscriptionFormat  `json:"format"`
	EnginePreference EnginePreference    `json:"enginePreference"`
	ProviderName     string              `json:"providerName,omitempty"`
	GroupName        string              `json:"groupName,omitempty"`
	Headers          map[string][]string `json:"headers,omitempty"`
	RefreshHours     int                 `json:"refreshHours"`
	Enabled          bool                `json:"enabled"`
	Mode             string              `json:"mode,omitempty"`
	TestURL          string              `json:"testUrl,omitempty"`
	TestInterval     int                 `json:"testInterval,omitempty"`
	TestTolerance    int                 `json:"testTolerance,omitempty"`
	FilterInclude    string              `json:"filterInclude,omitempty"`
	FilterExclude    string              `json:"filterExclude,omitempty"`
	BindInterface    string              `json:"bindInterface,omitempty"`
	Bridge           *ProxyBridge        `json:"bridge,omitempty"`
	LastFetched      time.Time           `json:"lastFetched,omitempty"`
	LastError        string              `json:"lastError,omitempty"`
	Members          []MemberInfo        `json:"members,omitempty"`
	CreatedAt        time.Time           `json:"createdAt"`
	UpdatedAt        time.Time           `json:"updatedAt"`
}

type MemberInfo struct {
	Tag          string `json:"tag"`
	Label        string `json:"label,omitempty"`
	Protocol     string `json:"protocol"`
	Server       string `json:"server"`
	Port         uint16 `json:"port"`
	SNI          string `json:"sni,omitempty"`
	Transport    string `json:"transport,omitempty"`
	Security     string `json:"security,omitempty"`
	TransportKey string `json:"transportKey,omitempty"`
}

// ProxyGroup is a Mihomo-native policy group. Provider references live in Use;
// concrete proxies and nested groups live in Proxies.
type ProxyGroup struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	Type                string   `json:"type"`
	Proxies             []string `json:"proxies,omitempty"`
	Use                 []string `json:"use,omitempty"`
	URL                 string   `json:"url,omitempty"`
	Interval            int      `json:"interval,omitempty"`
	Lazy                bool     `json:"lazy"`
	Strategy            string   `json:"strategy,omitempty"`
	Tolerance           int      `json:"tolerance,omitempty"`
	Timeout             int      `json:"timeout,omitempty"`
	MaxFailedTimes      int      `json:"maxFailedTimes,omitempty"`
	DisableUDP          bool     `json:"disableUdp,omitempty"`
	IncludeAll          bool     `json:"includeAll,omitempty"`
	IncludeAllProxies   bool     `json:"includeAllProxies,omitempty"`
	IncludeAllProviders bool     `json:"includeAllProviders,omitempty"`
	Filter              string   `json:"filter,omitempty"`
	ExcludeFilter       string   `json:"excludeFilter,omitempty"`
	ExcludeType         string   `json:"excludeType,omitempty"`
	ExpectedStatus      string   `json:"expectedStatus,omitempty"`
	Hidden              bool     `json:"hidden,omitempty"`
	Icon                string   `json:"icon,omitempty"`
	Enabled             bool     `json:"enabled"`
}

type GroupReference struct {
	Kind string `json:"kind"` // "rule" | "group" | "susanin"
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Rule struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Payload   string `json:"payload,omitempty"`
	Outbound  string `json:"outbound"`
	NoResolve bool   `json:"noResolve,omitempty"`
	Enabled   bool   `json:"enabled"`
}

type RuleInput struct {
	ID        string `json:"id,omitempty"`
	Type      string `json:"type"`
	Payload   string `json:"payload,omitempty"`
	Outbound  string `json:"outbound"`
	NoResolve bool   `json:"noResolve,omitempty"`
	Enabled   *bool  `json:"enabled,omitempty"`
}

type RuleProvider struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	URL      string `json:"url,omitempty"`
	Path     string `json:"path,omitempty"`
	Behavior string `json:"behavior"`
	Format   string `json:"format"`
	Interval int    `json:"interval,omitempty"`
	Proxy    string `json:"proxy,omitempty"`
	Enabled  bool   `json:"enabled"`
}
