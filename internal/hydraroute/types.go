package hydraroute

// Status represents the current state of HydraRoute Neo daemon.
type Status struct {
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	Version   string `json:"version,omitempty"`
}

// ManagedEntry represents a single DNS list to be written into HydraRoute config files.
type ManagedEntry struct {
	ListID   string
	ListName string
	Domains  []string // regular domains + geosite: tags
	Subnets  []string // CIDR ranges + geoip: tags
	Iface    string   // kernel interface name (DirectRoute target)
}

// ListInput is the input for BuildEntries — domain list data with tunnel ID to resolve.
type ListInput struct {
	ListID   string
	ListName string
	TunnelID string
	Domains  []string
	Subnets  []string
}

// Config represents the managed subset of hrneo.conf fields.
type Config struct {
	AutoStart          bool     `json:"autoStart"`
	ClearIPSet         bool     `json:"clearIPSet"`
	CIDR               bool     `json:"cidr"`
	IpsetEnableTimeout bool     `json:"ipsetEnableTimeout"`
	IpsetTimeout       int      `json:"ipsetTimeout"`
	IpsetMaxElem       int      `json:"ipsetMaxElem"`
	DirectRouteEnabled bool     `json:"directRouteEnabled"`
	GlobalRouting      bool     `json:"globalRouting"`
	ConntrackFlush     bool     `json:"conntrackFlush"`
	Log                string   `json:"log"`
	LogFile            string   `json:"logFile"`
	GeoIPFiles         []string `json:"geoIPFiles"`
	GeoSiteFiles       []string `json:"geoSiteFiles"`
}

func (c *Config) EffectiveMaxElem() int {
	if c.IpsetMaxElem <= 0 {
		return 65536
	}
	return c.IpsetMaxElem
}

type GeoFileEntry struct {
	Type     string `json:"type"`
	Path     string `json:"path"`
	URL      string `json:"url"`
	Size     int64  `json:"size"`
	TagCount int    `json:"tagCount"`
	Updated  string `json:"updated"`
}

type GeoTag struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type IpsetUsage struct {
	MaxElem int            `json:"maxElem"`
	Usage   map[string]int `json:"usage"`
}

type DnsListInfo struct {
	TunnelID string
	Subnets  []string
}

const (
	maxGeoFiles    = 16
	defaultMaxElem = 65536
)

// hrConfPath and hrDir are vars so tests can override them via t.TempDir().
var (
	hrConfPath = "/opt/etc/HydraRoute/hrneo.conf" //nolint:gochecknoglobals
	hrDir      = "/opt/etc/HydraRoute"            //nolint:gochecknoglobals
)
