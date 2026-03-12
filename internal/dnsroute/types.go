package dnsroute

// MaxDomainsPerGroup is the maximum number of domains in a single NDMS object-group fqdn.
const MaxDomainsPerGroup = 300

// GroupPrefix is the prefix for all AWG-managed NDMS object-group names.
const GroupPrefix = "AWG_"

// DomainList represents a user-defined list of domains to route through specific tunnels.
type DomainList struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Domains       []string       `json:"domains"`
	Excludes      []string       `json:"excludes,omitempty"`
	Subnets       []string       `json:"subnets,omitempty"`
	ManualDomains []string       `json:"manualDomains"`
	Subscriptions []Subscription `json:"subscriptions,omitempty"`
	Routes        []RouteTarget  `json:"routes"`
	Enabled       bool           `json:"enabled"`
	CreatedAt     string         `json:"createdAt"`
	UpdatedAt     string         `json:"updatedAt"`
}

// Subscription represents a remote domain list URL that is periodically fetched.
type Subscription struct {
	URL         string `json:"url"`
	Name        string `json:"name"`
	LastFetched string `json:"lastFetched,omitempty"`
	LastCount   int    `json:"lastCount,omitempty"`
	LastError   string `json:"lastError,omitempty"`
}

// RouteTarget specifies which tunnel interface to route matched domains through.
type RouteTarget struct {
	Interface string `json:"interface"`
	TunnelID  string `json:"tunnelId"`
	Fallback  string `json:"fallback,omitempty"`
}

// TunnelInfo provides tunnel metadata for the UI.
type TunnelInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	NDMSName string `json:"ndmsName"`
	Status   string `json:"status"`
	System   bool   `json:"system,omitempty"` // true for unmanaged WireGuard interfaces
}

// StoreData is the top-level dns-routes.json structure.
type StoreData struct {
	Lists []DomainList `json:"lists"`
}
