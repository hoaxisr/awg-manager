package mcp

import "time"

// Plain data types crossing the Deps boundary. JSON tags mirror the
// daemon's storage/service types so localdeps can convert by JSON
// round-trip; the fake and cmd/mcp-dev use them directly.

type WANInterface struct {
	Name     string `json:"name"`
	Up       bool   `json:"up"`
	Label    string `json:"label"`
	Priority int    `json:"priority"`
}

type SingboxStatus struct {
	Installed   bool   `json:"installed"`
	Running     bool   `json:"running"`
	Version     string `json:"version,omitempty"`
	TunnelCount int    `json:"tunnelCount"`
	LastError   string `json:"lastError,omitempty"`
}

type SystemStatus struct {
	Version     string         `json:"version"`
	InstanceID  string         `json:"instanceId"`
	BootPhase   string         `json:"bootPhase"`
	AnyWANUp    bool           `json:"anyWANUp"`
	WAN         []WANInterface `json:"wan"`
	Singbox     SingboxStatus  `json:"singbox"`
	AuthEnabled bool           `json:"authEnabled"`
	RouterIP    string         `json:"routerIp,omitempty"`
	// Info is the raw /system/info payload (model, firmware, memory, kernel module…).
	Info map[string]any `json:"info,omitempty"`
}

type TunnelSummary struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Backend       string `json:"backend"`
	Enabled       bool   `json:"enabled"`
	State         string `json:"state"` // unknown|not_created|stopped|starting|running|…
	DefaultRoute  bool   `json:"defaultRoute"`
	InterfaceName string `json:"interfaceName,omitempty"`
	Endpoint      string `json:"endpoint,omitempty"`
	HasHandshake  bool   `json:"hasHandshake"`
}

type TrafficStats struct {
	Points    int     `json:"points"`
	PeakRate  float64 `json:"peakRate"`
	AvgRx     float64 `json:"avgRx"`
	AvgTx     float64 `json:"avgTx"`
	CurrentRx float64 `json:"currentRx"`
	CurrentTx float64 `json:"currentTx"`
	VolumeRx  int64   `json:"volumeRx"`
	VolumeTx  int64   `json:"volumeTx"`
}

type TunnelDetail struct {
	TunnelSummary
	ISPInterface string       `json:"ispInterface,omitempty"`
	AllowedIPs   []string     `json:"allowedIPs,omitempty"`
	Address      string       `json:"address,omitempty"`
	ProcessPID   int          `json:"processPID,omitempty"`
	Traffic1h    TrafficStats `json:"traffic1h"`
}

// TunnelAction values accepted by ControlTunnel.
const (
	ActionStart             = "start"
	ActionStop              = "stop"
	ActionRestart           = "restart"
	ActionEnable            = "enable"
	ActionDisable           = "disable"
	ActionSetDefaultRoute   = "set_default_route"
	ActionUnsetDefaultRoute = "unset_default_route"
)

type RouteTarget struct {
	Interface string `json:"interface,omitempty"`
	TunnelID  string `json:"tunnelId,omitempty"`
	Fallback  string `json:"fallback,omitempty"`
}

type DNSRoute struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	Enabled       bool          `json:"enabled"`
	Domains       []string      `json:"domains"`
	ManualDomains []string      `json:"manualDomains,omitempty"`
	Subnets       []string      `json:"subnets,omitempty"`
	Routes        []RouteTarget `json:"routes"`
	Backend       string        `json:"backend,omitempty"`
}

type DNSRouteInput struct {
	Name     string   `json:"name" jsonschema:"human-readable list name"`
	Domains  []string `json:"domains" jsonschema:"domains to route (suffix match), e.g. [\"youtube.com\"]"`
	TunnelID string   `json:"tunnelId" jsonschema:"target tunnel id from list_tunnels"`
	Enabled  *bool    `json:"enabled,omitempty" jsonschema:"default true"`
}

type StaticRoute struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	TunnelID string   `json:"tunnelID"`
	Subnets  []string `json:"subnets"`
	Fallback string   `json:"fallback,omitempty"`
	Enabled  bool     `json:"enabled"`
}

type StaticRouteInput struct {
	Name     string   `json:"name"`
	TunnelID string   `json:"tunnelId" jsonschema:"target tunnel id from list_tunnels"`
	Subnets  []string `json:"subnets" jsonschema:"CIDR list, e.g. [\"10.0.0.0/8\"]"`
	Enabled  *bool    `json:"enabled,omitempty" jsonschema:"default true"`
}

type ClientRoute struct {
	ID             string `json:"id"`
	ClientIP       string `json:"clientIp"`
	ClientHostname string `json:"clientHostname,omitempty"`
	TunnelID       string `json:"tunnelId"`
	Fallback       string `json:"fallback"`
	Enabled        bool   `json:"enabled"`
}

type ClientRouteInput struct {
	ClientIP string `json:"clientIp" jsonschema:"LAN client IPv4 from list_devices"`
	TunnelID string `json:"tunnelId" jsonschema:"tunnel id, or empty string to remove the route"`
	Fallback string `json:"fallback,omitempty" jsonschema:"drop|bypass when the tunnel is down; default bypass"`
}

type AccessPolicy struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Interfaces  []string `json:"interfaces"`
	DeviceCount int      `json:"deviceCount"`
	IsStandard  bool     `json:"isStandard"`
}

type Device struct {
	MAC      string `json:"mac"`
	IP       string `json:"ip"`
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
	Active   bool   `json:"active"`
	Policy   string `json:"policy"`
}

type LogsQuery struct {
	Bucket   string   `json:"bucket,omitempty" jsonschema:"app|singbox, default app"`
	Groups   []string `json:"groups,omitempty" jsonschema:"e.g. tunnel, routing, system, singbox"`
	Level    string   `json:"level,omitempty" jsonschema:"minimum level: debug|info|warn|error"`
	Lines    int      `json:"lines,omitempty" jsonschema:"1..500, default 100"`
	Contains string   `json:"contains,omitempty" jsonschema:"case-insensitive substring filter on message"`
}

type LogEntry struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Group     string `json:"group"`
	Subgroup  string `json:"subgroup"`
	Action    string `json:"action,omitempty"`
	Target    string `json:"target,omitempty"`
	Message   string `json:"message"`
}

type ConnectivityResult struct {
	TunnelID  string `json:"tunnelId"`
	Connected bool   `json:"connected"`
	LatencyMs *int   `json:"latencyMs,omitempty"`
	Reason    string `json:"reason,omitempty"`
	HTTPCode  *int   `json:"httpCode,omitempty"`
}

type MonitoringCell struct {
	TargetID  string    `json:"targetId"`
	TunnelID  string    `json:"tunnelId"`
	OK        bool      `json:"ok"`
	LatencyMs *int      `json:"latencyMs"`
	TS        time.Time `json:"ts"`
}

type MonitoringTarget struct {
	ID   string `json:"id"`
	Host string `json:"host"`
	Name string `json:"name"`
}

type MonitoringTunnel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type MonitoringMatrix struct {
	Targets   []MonitoringTarget `json:"targets"`
	Tunnels   []MonitoringTunnel `json:"tunnels"`
	Cells     []MonitoringCell   `json:"cells"`
	UpdatedAt time.Time          `json:"updatedAt"`
}

type ManagedServer struct {
	ID            string `json:"id"`
	InterfaceName string `json:"interfaceName"`
	Description   string `json:"description"`
	Status        string `json:"status"`
	Connected     bool   `json:"connected"`
	ListenPort    int    `json:"listenPort"`
	PeerCount     int    `json:"peerCount"`
}

type PingCheckStatus struct {
	TunnelID    string `json:"tunnelId"`
	TunnelName  string `json:"tunnelName"`
	Enabled     bool   `json:"enabled"`
	Status      string `json:"status"`
	Method      string `json:"method"`
	LastLatency int    `json:"lastLatency"`
}
