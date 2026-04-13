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
	UpdatedAt        string         `json:"updatedAt"`
	LastDedupeReport *DedupeReport  `json:"lastDedupeReport,omitempty"`
	Backend          string         `json:"backend,omitempty"`  // "" or "ndms" = NDMS, "hydraroute" = HydraRoute Neo
	HRRouteMode      string         `json:"hrRouteMode,omitempty"`  // "interface" or "policy" (hydraroute only)
	HRPolicyName     string         `json:"hrPolicyName,omitempty"` // policy name for policy mode
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

// DedupeReport contains information about domains removed during deduplication.
type DedupeReport struct {
	TotalInput    int          `json:"totalInput"`
	TotalKept     int          `json:"totalKept"`
	TotalRemoved  int          `json:"totalRemoved"`
	ExactDupes    int          `json:"exactDupes"`
	WildcardDupes int          `json:"wildcardDupes"`
	Items         []DedupeItem `json:"items,omitempty"`
}

// DedupeItem describes a single domain or subnet removed during deduplication.
type DedupeItem struct {
	Domain    string `json:"domain"`
	Reason    string `json:"reason"`    // "exact", "wildcard", "subnet_covered"
	CoveredBy string `json:"coveredBy"`
	ListID    string `json:"listId"`
	ListName  string `json:"listName"`
}

// StoreData is the top-level dns-routes.json structure.
type StoreData struct {
	Lists []DomainList `json:"lists"`
}

func isHydraRoute(backend string) bool {
	return backend == "hydraroute"
}

func isNDMS(backend string) bool {
	return backend == "" || backend == "ndms"
}
