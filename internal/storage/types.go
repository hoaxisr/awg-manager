package storage

// Settings represents /opt/etc/awg-manager/settings.json
type Settings struct {
	SchemaVersion       int               `json:"schemaVersion,omitempty"`
	AuthEnabled         bool              `json:"authEnabled"`
	Server              ServerSettings    `json:"server"`
	PingCheck           PingCheckSettings `json:"pingCheck"`
	Logging             LoggingSettings   `json:"logging"`
	DisableMemorySaving bool              `json:"disableMemorySaving"` // false = auto, true = soft mode
	Updates             UpdateSettings    `json:"updates"`
	DNSRoute            DNSRouteSettings  `json:"dnsRoute"`
	OnboardingCompleted    bool              `json:"onboardingCompleted"`
	HiddenSystemTunnels    []string          `json:"hiddenSystemTunnels,omitempty"`
	ServerInterfaces       []string          `json:"serverInterfaces,omitempty"`
	ManagedServer          *ManagedServer    `json:"managedServer,omitempty"`
	ManagedPolicies        []string          `json:"managedPolicies,omitempty"`
}

// ManagedServer represents the user-created WireGuard server interface.
type ManagedServer struct {
	InterfaceName string        `json:"interfaceName"` // e.g. "Wireguard3"
	Address       string        `json:"address"`       // e.g. "10.0.0.1"
	Mask          string        `json:"mask"`          // e.g. "255.255.255.0"
	ListenPort    int           `json:"listenPort"`
	Endpoint      string        `json:"endpoint,omitempty"` // custom endpoint (IP or domain); empty = WAN IP
	DNS           string        `json:"dns,omitempty"`      // custom DNS for client configs; empty = "1.1.1.1, 8.8.8.8"
	MTU           int           `json:"mtu,omitempty"`      // custom MTU for client configs; 0 = 1376
	NATEnabled    bool          `json:"natEnabled,omitempty"`
	Peers         []ManagedPeer `json:"peers"`
	// Signature packets for client configs (not stored on NDMS server)
	I1 string `json:"i1,omitempty"`
	I2 string `json:"i2,omitempty"`
	I3 string `json:"i3,omitempty"`
	I4 string `json:"i4,omitempty"`
	I5 string `json:"i5,omitempty"`
}

// ManagedPeer represents a client peer on the managed server.
type ManagedPeer struct {
	PublicKey    string `json:"publicKey"`
	PrivateKey  string `json:"privateKey"`  // stored for .conf generation
	PresharedKey string `json:"presharedKey"`
	Description string `json:"description"`
	TunnelIP    string `json:"tunnelIP"`    // e.g. "10.0.0.2/32"
	DNS         string `json:"dns,omitempty"` // per-peer DNS for .conf generation
	Enabled     bool   `json:"enabled"`
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
	Enabled  bool   `json:"enabled"`  // default: false
	MaxAge   int    `json:"maxAge"`   // hours, default: 2
	LogLevel string `json:"logLevel"` // "warn", "info", "full", "debug"; default: "info"
}

// UpdateSettings contains auto-update configuration.
type UpdateSettings struct {
	CheckEnabled bool `json:"checkEnabled"` // default: true
}

// DNSRouteSettings contains DNS route auto-refresh configuration.
type DNSRouteSettings struct {
	AutoRefreshEnabled   bool   `json:"autoRefreshEnabled"`             // default: false
	RefreshIntervalHours int    `json:"refreshIntervalHours"`           // default: 0 (user must choose)
	RefreshMode          string `json:"refreshMode,omitempty"`          // "interval" (default/empty) or "daily"
	RefreshDailyTime     string `json:"refreshDailyTime,omitempty"`     // "HH:MM" 24h format, e.g. "03:00"
}

// ConnectivityCheckConfig holds per-tunnel connectivity check settings.
type ConnectivityCheckConfig struct {
	Method     string `json:"method"`              // "http" (default), "ping", "handshake", "disabled"
	PingTarget string `json:"pingTarget,omitempty"` // IP address for ping method
}

// AWGTunnel represents AmneziaWG tunnel metadata.
type AWGTunnel struct {
	ID                string           `json:"id"`
	Name              string           `json:"name"`
	Type              string           `json:"type,omitempty"` // "awg"
	Enabled           bool             `json:"enabled"`
	DefaultRoute    bool `json:"defaultRoute"`              // Create NDMS default route (ip route default OpkgTunX)
	DefaultRouteSet bool `json:"defaultRouteSet,omitempty"` // Migration sentinel: false = field never saved, default to true
	ISPInterface       string           `json:"ispInterface,omitempty"`        // Override ISP interface for endpoint route (empty = auto-detect)
	ISPInterfaceLabel  string           `json:"ispInterfaceLabel,omitempty"`   // Human-readable name for UI display
	ResolvedEndpointIP string           `json:"resolvedEndpointIP,omitempty"` // Persisted resolved endpoint IP for reliable cleanup
	ActiveWAN          string           `json:"activeWAN,omitempty"`          // Persisted resolved WAN for WAN event matching
	StartedAt         string           `json:"startedAt,omitempty"`          // RFC3339 timestamp of last successful start
	Backend           string           `json:"backend,omitempty"`            // "nativewg" | "kernel" | "" (legacy=kernel)
	NWGIndex          int              `json:"nwgIndex"`                     // Wireguard{N} index, nativewg only (0 is valid!)
	CreatedAt         string           `json:"createdAt"`
	Interface         AWGInterface     `json:"interface"`
	Peer              AWGPeer          `json:"peer"`
	PingCheck         *TunnelPingCheck        `json:"pingCheck,omitempty"`
	ConnectivityCheck *ConnectivityCheckConfig `json:"connectivityCheck,omitempty"`
}

// TunnelPingCheck contains per-tunnel ping check configuration.
type TunnelPingCheck struct {
	Enabled            bool    `json:"enabled"`
	Method             string  `json:"method"`              // "icmp", "connect", "tls", "uri"
	Target             string  `json:"target"`              // host to check
	Interval           int     `json:"interval"`            // check interval in seconds
	DeadInterval       int     `json:"deadInterval"`        // dead tunnel check interval (kernel only)
	FailThreshold      int     `json:"failThreshold"`       // max fails before dead
	MinSuccess         int     `json:"minSuccess"`          // min successes to recover (nativewg, default 1)
	Timeout            int     `json:"timeout"`             // check timeout seconds (nativewg, default 5)
	Port               int     `json:"port,omitempty"`      // port for connect/tls modes
	Restart            bool    `json:"restart"`             // restart tunnel on dead (nativewg)
}

// AWGInterface contains AmneziaWG interface configuration.
type AWGInterface struct {
	PrivateKey string `json:"privateKey"`
	Address    string `json:"address"`
	MTU        int    `json:"mtu"`
	DNS        string `json:"dns,omitempty"` // Comma-separated DNS servers (e.g., "1.1.1.1, 8.8.8.8")
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
