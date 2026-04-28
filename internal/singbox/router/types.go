package router

type Status struct {
	Enabled                bool    `json:"enabled"`
	Installed              bool    `json:"installed"`
	NetfilterAvailable     bool    `json:"netfilterAvailable"`
	NetfilterComponentName string  `json:"netfilterComponentName,omitempty"`
	TProxyTargetAvailable  bool    `json:"tproxyTargetAvailable"`
	PolicyName             string  `json:"policyName"`
	PolicyMark             string  `json:"policyMark,omitempty"`
	PolicyExists           bool    `json:"policyExists"`
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
	DomainSuffix []string `json:"domain_suffix,omitempty"`
	IPCIDR       []string `json:"ip_cidr,omitempty"`
	SourceIPCIDR []string `json:"source_ip_cidr,omitempty"`
	Port         []int    `json:"port,omitempty"`
	RuleSet      []string `json:"rule_set,omitempty"`
	Protocol     string   `json:"protocol,omitempty"`
	Action       string   `json:"action"`
	Outbound     string   `json:"outbound,omitempty"`
}

type RuleSet struct {
	Tag            string `json:"tag"`
	Type           string `json:"type"`
	Format         string `json:"format"`
	URL            string `json:"url,omitempty"`
	UpdateInterval string `json:"update_interval,omitempty"`
	DownloadDetour string `json:"download_detour,omitempty"`
	Path           string `json:"path,omitempty"`
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

type Inbound struct {
	Type        string `json:"type"`
	Tag         string `json:"tag"`
	Listen      string `json:"listen"`
	ListenPort  int    `json:"listen_port"`
	Network     string `json:"network,omitempty"`
	UDPTimeout  string `json:"udp_timeout,omitempty"`
	RoutingMark int    `json:"routing_mark,omitempty"`
}

type Route struct {
	RuleSet []RuleSet `json:"rule_set,omitempty"`
	Rules   []Rule    `json:"rules,omitempty"`
	Final   string    `json:"final,omitempty"`
}

type DomainResolver struct {
	Server   string `json:"server"`
	Strategy string `json:"strategy,omitempty"`
}

type DNSServer struct {
	Tag            string          `json:"tag"`
	Type           string          `json:"type"`
	Server         string          `json:"server"`
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
	QueryType     []string `json:"query_type,omitempty"`
	Server        string   `json:"server,omitempty"`
	Action        string   `json:"action,omitempty"`
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
