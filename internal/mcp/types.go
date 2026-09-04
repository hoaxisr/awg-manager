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

// MaxDomainsInOutput caps DNSRoute.Domains. A subscription-backed list
// expands to tens of thousands of domains; shipping them all into a model
// context on every list_dns_routes call is useless to the model and costs
// the router several copies of the list per call. ManualDomains — the
// user's own entries, and what add_dns_route needs to recreate a list —
// is never capped.
const MaxDomainsInOutput = 50

type DNSRoute struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	// Domains is the expanded list (manual + subscriptions), truncated to
	// MaxDomainsInOutput entries; DomainCount is the real size.
	Domains       []string      `json:"domains" jsonschema:"expanded domains, at most the first 50; see domainCount for the full size"`
	DomainCount   int           `json:"domainCount"`
	ManualDomains []string      `json:"manualDomains,omitempty"`
	Subnets       []string      `json:"subnets,omitempty"`
	Routes        []RouteTarget `json:"routes"`
	Backend       string        `json:"backend,omitempty"`
}

// DNSRouteInput has no `enabled` field on purpose: dnsroute.Create always
// creates the list enabled and pushes the routing into NDMS immediately, so
// honouring enabled:false would mean going live and then tearing it down a
// moment later — and leaving the list ENABLED if that second call failed.
// A list created through MCP is always enabled; disable it from the web UI.
type DNSRouteInput struct {
	Name     string   `json:"name" jsonschema:"human-readable list name"`
	Domains  []string `json:"domains" jsonschema:"domains to route (suffix match), e.g. [\"youtube.com\"]; geosite:/geoip: tags and CIDR subnets are also accepted"`
	TunnelID string   `json:"tunnelId" jsonschema:"target tunnel id from list_tunnels"`
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
	Raw      bool     `json:"raw,omitempty" jsonschema:"true returns IPs and domains unmasked; by default they are partially redacted, as in the web UI"`
}

type LogEntry struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Group     string `json:"group"`
	Subgroup  string `json:"subgroup"`
	Action    string `json:"action,omitempty"`
	Target    string `json:"target,omitempty"`
	Message   string `json:"message"`
	// Repeats > 1 means the buffer collapsed identical consecutive lines
	// into this one; LastSeen is when the latest of them arrived.
	Repeats  int    `json:"repeats,omitempty"`
	LastSeen string `json:"lastSeen,omitempty"`
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

// PingCheckRun is what run_pingcheck returns. The sweep itself runs in
// the background — on a router with several tunnels and a 5 s probe
// timeout a synchronous sweep would hold the call for half a minute with
// no way to cancel it — so Tunnels is the status as of the LAST completed
// check, and Triggered says whether this call started a new one (false
// when one was already in flight).
type PingCheckRun struct {
	Triggered bool              `json:"triggered" jsonschema:"true if this call started a new check; false if one was already running"`
	Tunnels   []PingCheckStatus `json:"tunnels" jsonschema:"status as of the last COMPLETED check — call again in ~10 s for the result of the one just triggered"`
}

type PingCheckStatus struct {
	TunnelID    string `json:"tunnelId"`
	TunnelName  string `json:"tunnelName"`
	Enabled     bool   `json:"enabled"`
	Status      string `json:"status"`
	Method      string `json:"method"`
	LastLatency int    `json:"lastLatency"`
}
