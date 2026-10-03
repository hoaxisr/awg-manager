package traffic

import "time"

// DomainKnowledge contains user-friendly metadata about a domain or IP owner.
type DomainKnowledge struct {
	Title       string `json:"title,omitempty"`       // Friendly service name: "Google Cloud", "Одноклассники (VK)", "Яндекс.Облако"
	Description string `json:"description,omitempty"` // Friendly description: "Почтовые серверы Mail.ru", "CDN видеопотоков YouTube"
	Org         string `json:"org,omitempty"`         // Organization / AS: "VK LLC", "Google LLC", "Selectel"
	Country     string `json:"country,omitempty"`     // "Россия", "Беларусь", "США", "Германия"
	CountryCode string `json:"countryCode,omitempty"` // "RU", "BY", "US", "DE"
	Icon        string `json:"icon,omitempty"`        // icon slug: "youtube", "telegram", "vk", "google", "cloud", "server", "smartphone"
	Category    string `json:"category,omitempty"`    // "social", "media", "cloud", "communication", "gaming", "system", "isp"
}

// ItemRouteStatus describes which active routing rule, policy, or catalog preset a domain/IP already belongs to.
type ItemRouteStatus struct {
	Target         string `json:"target"`                   // "mihomo", "singbox", "catalog", "ndms", "hydraroute"
	TargetLabel    string `json:"targetLabel"`              // "Mihomo", "Sing-box", "Каталог", "NDMS", "HydraRoute Neo"
	RuleName       string `json:"ruleName"`                 // e.g. "Задний вход :)", "Telegram", "Policy0", "Smart TV"
	RuleID         string `json:"ruleId,omitempty"`         // Identifier of rule/group/preset
	MatchedPattern string `json:"matchedPattern,omitempty"` // e.g. "DOMAIN-SUFFIX telegram.org", "IP-CIDR 149.154.160.0/20", "t.me"
	IsDirect       bool   `json:"isDirect,omitempty"`       // true if routed to DIRECT
}

// TrafficDevice represents a LAN client device for sniffing.
type TrafficDevice struct {
	IP             string `json:"ip"`
	MAC            string `json:"mac"`
	Name           string `json:"name"`
	Hostname       string `json:"hostname"`
	Active         bool   `json:"active"`
	ActiveSessions int    `json:"activeSessions"`
	Policy         string `json:"policy,omitempty"`
}

// TrafficSession represents an individual TCP/UDP flow from the device.
type TrafficSession struct {
	ID              string            `json:"id"`
	Protocol        string            `json:"protocol"`
	SrcIP           string            `json:"srcIp"`
	SrcPort         int               `json:"srcPort"`
	DstIP           string            `json:"dstIp"`
	DstPort         int               `json:"dstPort"`
	Domain          string            `json:"domain,omitempty"`
	State           string            `json:"state"`
	Packets         int64             `json:"packets"`
	BytesIn         int64             `json:"bytesIn"`
	BytesOut        int64             `json:"bytesOut"`
	TotalBytes      int64             `json:"totalBytes"`
	TTL             int64             `json:"ttl"`
	ServiceName     string            `json:"serviceName,omitempty"`
	ServiceCategory string            `json:"serviceCategory,omitempty"`
	Knowledge       *DomainKnowledge  `json:"knowledge,omitempty"`
	DomainRoutes    []ItemRouteStatus `json:"domainRoutes,omitempty"`
	IPRoutes        []ItemRouteStatus `json:"ipRoutes,omitempty"`
	IsConfigured    bool              `json:"isConfigured"`
}

// TrafficDomainGroup groups sessions by consolidated service / domain.
type TrafficDomainGroup struct {
	GroupKey        string                       `json:"groupKey"`
	Title           string                       `json:"title"`
	Domain          string                       `json:"domain"`
	Domains         []string                     `json:"domains"`
	ServiceName     string                       `json:"serviceName,omitempty"`
	ServiceCategory string                       `json:"serviceCategory,omitempty"`
	Knowledge       *DomainKnowledge             `json:"knowledge,omitempty"`
	SessionCount    int                          `json:"sessionCount"`
	TotalBytes      int64                        `json:"totalBytes"`
	BytesIn         int64                        `json:"bytesIn"`
	BytesOut        int64                        `json:"bytesOut"`
	IPs             []string                     `json:"ips"`
	Ports           []int                        `json:"ports"`
	Sessions        []TrafficSession             `json:"sessions,omitempty"`
	// Route Presence Metadata
	DomainStatuses  map[string][]ItemRouteStatus `json:"domainStatuses,omitempty"` // map[domain] -> matched routes
	IPStatuses      map[string][]ItemRouteStatus `json:"ipStatuses,omitempty"`     // map[ip] -> matched routes
	ExistingRules   []string                     `json:"existingRules,omitempty"` // e.g. ["Mihomo: Задний вход :)", "Каталог: Telegram"]
	OverallStatus   string                       `json:"overallStatus"`           // "routed" | "partial" | "new"
	NewDomainsCount int                          `json:"newDomainsCount"`
	NewIPsCount     int                          `json:"newIpsCount"`
}

// ActiveEngineInfo gives information about an active routing subsystem.
type ActiveEngineInfo struct {
	ID          string `json:"id"`          // "catalog", "mihomo", "singbox", "hydraroute", "static_route"
	Label       string `json:"label"`       // "Каталог сервисов", "Mihomo", "Sing-box", "HydraRoute Neo", "Статический IP"
	Description string `json:"description"` // User-friendly description
	Active      bool   `json:"active"`      // Whether this engine is active/enabled right now
}

// TrafficSnapshot is the full snapshot returned for a device.
type TrafficSnapshot struct {
	Device        TrafficDevice        `json:"device"`
	TotalSessions int                  `json:"totalSessions"`
	ActiveCount   int                  `json:"activeCount"`
	TotalBytesIn  int64                `json:"totalBytesIn"`
	TotalBytesOut int64                `json:"totalBytesOut"`
	DomainGroups  []TrafficDomainGroup `json:"domainGroups"`
	Sessions      []TrafficSession     `json:"sessions"`
	ActiveEngines []ActiveEngineInfo   `json:"activeEngines"`
	Timestamp     time.Time            `json:"timestamp"`
}

// TrafficExportRequest is the payload to export selected items.
type TrafficExportRequest struct {
	Target         string   `json:"target"` // "catalog", "mihomo", "singbox", "hydraroute", "static_route"
	Mode           string   `json:"mode"`   // "append" | "create"
	TargetRuleID   string   `json:"targetRuleId,omitempty"`
	TargetPresetID string   `json:"targetPresetId,omitempty"`
	ServiceName    string   `json:"serviceName,omitempty"`
	Domains        []string `json:"domains,omitempty"`
	IPs            []string `json:"ips,omitempty"`
	Outbound       string   `json:"outbound,omitempty"`
}

// TrafficExportResponse is the result of an export action.
type TrafficExportResponse struct {
	Success        bool     `json:"success"`
	Message        string   `json:"message"`
	Count          int      `json:"count"`
	AddedDomains   []string `json:"addedDomains,omitempty"`
	AddedIPs       []string `json:"addedIps,omitempty"`
	SkippedDomains []string `json:"skippedDomains,omitempty"`
	SkippedIPs     []string `json:"skippedIps,omitempty"`
}
