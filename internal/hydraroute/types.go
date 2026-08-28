package hydraroute

// Status represents the current state of HydraRoute Neo daemon.
type ProcessState string

const (
	StateNotInstalled ProcessState = "not_installed"
	StateStopped      ProcessState = "stopped"
	StateRunning      ProcessState = "running"
	StateDead         ProcessState = "dead"
)

type Status struct {
	Installed    bool         `json:"installed"`
	Version      string       `json:"version,omitempty"`
	Running      bool         `json:"running"`
	PID          int          `json:"pid,omitempty"`
	StalePID     int          `json:"stalePid,omitempty"`
	ProcessState ProcessState `json:"processState"`
	LastError    string       `json:"lastError,omitempty"`
}

// ManagedEntry represents a single DNS list to be written into HydraRoute config files.
// The list name is the identity — there's no separate ID.
type ManagedEntry struct {
	ListName string   // human-readable name, unique per file (== identity)
	Domains  []string // regular domains + geosite: tags
	Subnets  []string // CIDR ranges + geoip: tags
	Iface    string   // kernel interface name or policy name (DirectRoute target)
	Disabled bool     // rule is commented out in HR files with a leading '#'
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
	PolicyOrder        []string `json:"policyOrder"`
}

func (c *Config) EffectiveMaxElem() int {
	if c.IpsetMaxElem <= 0 {
		return defaultMaxElem
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
	// External is true when the .dat file lives under /opt/etc/HydraRoute
	// (downloaded via HR Neo). awg-manager tracks the path only until the
	// user takes control or deletes the file.
	External bool `json:"external,omitempty"`
	// Mtime (RFC3339 UTC) is the file's modification time at the moment
	// TagCount was computed. Used to detect stale cached TagCount without
	// re-parsing the file: stat() returns the same mtime+size → cache hit.
	Mtime string `json:"mtime,omitempty"`
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
	maxGeoFiles = 16

	// Дефолты демона (PARAMS в params.c) для ключей, которых нет в
	// стоковом hrneo.conf: демон подставляет их сам, и наши значения
	// обязаны совпадать — иначе WriteConfig молча переключит поведение.
	defaultMaxElem      = 262144
	defaultIpsetTimeout = 21600
	defaultLogLevel     = "off"
	defaultLogFile      = "/opt/var/log/LOGhrneo.log"
)

// hrConfPath and hrDir are vars so tests can override them via t.TempDir().
var (
	hrConfPath = "/opt/etc/HydraRoute/hrneo.conf" //nolint:gochecknoglobals
	hrDir      = "/opt/etc/HydraRoute"            //nolint:gochecknoglobals
)
