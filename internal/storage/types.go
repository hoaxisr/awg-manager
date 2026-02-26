package storage

// Settings represents /opt/etc/awg-manager/settings.json
type Settings struct {
	SchemaVersion       int               `json:"schemaVersion,omitempty"`
	AuthEnabled         bool              `json:"authEnabled"`
	Server              ServerSettings    `json:"server"`
	PingCheck           PingCheckSettings `json:"pingCheck"`
	Logging             LoggingSettings   `json:"logging"`
	DisableMemorySaving bool              `json:"disableMemorySaving"` // false = auto, true = soft mode
	BackendMode         string            `json:"backendMode"`         // "auto", "kernel", "userspace"
	KmodVersion         string            `json:"kmodVersion,omitempty"` // "", "latest", "1.0.2", "1.0.3"
	BootDelaySeconds    int               `json:"bootDelaySeconds"`    // 0 = default (180), min 120, seconds
	Updates             UpdateSettings    `json:"updates"`
	OnboardingCompleted bool              `json:"onboardingCompleted"`
}

// ServerSettings contains HTTP server configuration.
type ServerSettings struct {
	Port      int    `json:"port"`
	Interface string `json:"interface"`
}

// PingCheckSettings contains global ping check configuration.
type PingCheckSettings struct {
	Enabled  bool              `json:"enabled"`
	Defaults PingCheckDefaults `json:"defaults"`
}

// PingCheckDefaults contains default values for tunnel ping checks.
type PingCheckDefaults struct {
	Method        string `json:"method"`        // "http" or "icmp"
	Target        string `json:"target"`        // ICMP target, default "8.8.8.8"
	Interval      int    `json:"interval"`      // check interval in seconds, default 45
	DeadInterval  int    `json:"deadInterval"`  // dead tunnel check interval in seconds, default 120
	FailThreshold int    `json:"failThreshold"` // failures before marking dead, default 3
}

// LoggingSettings contains application logging configuration.
type LoggingSettings struct {
	Enabled bool `json:"enabled"` // default: false
	MaxAge  int  `json:"maxAge"`  // hours, default: 2
}

// UpdateSettings contains auto-update configuration.
type UpdateSettings struct {
	CheckEnabled bool `json:"checkEnabled"` // default: true
}

// AWGTunnel represents AmneziaWG tunnel metadata.
type AWGTunnel struct {
	ID                string           `json:"id"`
	Name              string           `json:"name"`
	Type              string           `json:"type,omitempty"` // "awg"
	Status            string           `json:"status"`         // runtime-only, always "stopped" in file
	Enabled           bool             `json:"enabled"`
	DefaultRoute    bool `json:"defaultRoute"`              // Create NDMS default route (ip route default OpkgTunX)
	DefaultRouteSet bool `json:"defaultRouteSet,omitempty"` // Migration sentinel: false = field never saved, default to true
	ISPInterface       string           `json:"ispInterface,omitempty"`        // Override ISP interface for endpoint route (empty = auto-detect)
	ISPInterfaceLabel  string           `json:"ispInterfaceLabel,omitempty"`   // Human-readable name for UI display
	ResolvedEndpointIP string           `json:"resolvedEndpointIP,omitempty"` // Persisted resolved endpoint IP for reliable cleanup
	ActiveWAN          string           `json:"activeWAN,omitempty"`          // Persisted resolved WAN for WAN event matching
	StartedAt         string           `json:"startedAt,omitempty"`          // RFC3339 timestamp of last successful start
	CreatedAt         string           `json:"createdAt"`
	Interface         AWGInterface     `json:"interface"`
	Peer              AWGPeer          `json:"peer"`
	PingCheck         *TunnelPingCheck `json:"pingCheck,omitempty"`
}

// TunnelPingCheck contains per-tunnel ping check configuration.
type TunnelPingCheck struct {
	Enabled            bool    `json:"enabled"`
	UseCustomSettings  bool    `json:"useCustomSettings"`
	Method             string  `json:"method"`
	Target             string  `json:"target"`
	Interval           int     `json:"interval"`
	DeadInterval       int     `json:"deadInterval"`
	FailThreshold      int     `json:"failThreshold"`
	IsDeadByMonitoring bool    `json:"isDeadByMonitoring"`
	DeadSince          *string `json:"deadSince"` // ISO timestamp or null
}

// AWGInterface contains AmneziaWG interface configuration.
type AWGInterface struct {
	PrivateKey string `json:"privateKey"`
	Address    string `json:"address"`
	MTU        int    `json:"mtu"`
	Qlen       int    `json:"qlen"`
	// AmneziaWG obfuscation parameters
	Jc   int    `json:"jc"`
	Jmin int    `json:"jmin"`
	Jmax int    `json:"jmax"`
	S1   int    `json:"s1"`
	S2   int    `json:"s2"`
	S3   int    `json:"s3"`
	S4   int    `json:"s4"`
	H1   string `json:"h1"`
	H2   string `json:"h2"`
	H3   string `json:"h3"`
	H4   string `json:"h4"`
	I1   string `json:"i1,omitempty"`
	I2   string `json:"i2,omitempty"`
	I3   string `json:"i3,omitempty"`
	I4   string `json:"i4,omitempty"`
	I5   string `json:"i5,omitempty"`
}

// AWGPeer contains AmneziaWG peer configuration.
type AWGPeer struct {
	PublicKey           string   `json:"publicKey"`
	PresharedKey        string   `json:"presharedKey,omitempty"`
	Endpoint            string   `json:"endpoint"`
	AllowedIPs          []string `json:"allowedIPs"`
	PersistentKeepalive int      `json:"persistentKeepalive"`
}
