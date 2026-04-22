package ndms

import "time"

// Interface is a snapshot of one NDMS interface observed via
// /show/interface/{name} or extracted from /show/interface/.
type Interface struct {
	ID            string `json:"id"`
	SystemName    string `json:"systemName"`  // kernel name, e.g. nwg3
	Type          string `json:"type"`         // "proxy", "wireguard", "ethernet", ...
	Description   string `json:"description"`
	State         string `json:"state"`        // "up" | "down"
	Link          string `json:"link"`         // "up" | "down"
	Connected     string `json:"connected"`    // "yes" | "no" | ""
	SecurityLevel string `json:"securityLevel"`
	IPv4          string `json:"ipv4,omitempty"`    // summary.layer.ipv4 — "running" | ""
	Address       string `json:"address,omitempty"`
	Mask          string `json:"mask,omitempty"`
	MTU           int    `json:"mtu,omitempty"`
	Uptime        int64  `json:"uptime,omitempty"`
	ConfLayer     string `json:"confLayer,omitempty"` // "running" | "disabled"
	Priority      int    `json:"priority,omitempty"`  // NDMS priority (higher = preferred by user)
}

// AllInterface is a generic interface listing entry used by the
// "choose interface" UI.
type AllInterface struct {
	Name  string `json:"name"`  // Kernel name (e.g., "br0", "eth3")
	Label string `json:"label"` // Human-readable label
	Up    bool   `json:"up"`    // IPv4 layer running
}

// ProxyInfo is the view of an NDMS Proxy interface for sing-box wiring.
type ProxyInfo struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	State       string `json:"state"`
	Link        string `json:"link"`
	Up          bool   `json:"up"`
	Exists      bool   `json:"exists"`
}

// Peer is one peer entry under an NDMS wireguard interface —
// populated from /show/interface/{name}/wireguard/peer.
type Peer struct {
	PublicKey              string    `json:"publicKey"`
	Description            string    `json:"description,omitempty"`
	LocalPort              int       `json:"localPort,omitempty"`
	RemotePort             int       `json:"remotePort,omitempty"`
	Via                    string    `json:"via,omitempty"`
	LocalEndpointAddress   string    `json:"localEndpointAddress,omitempty"`
	RemoteEndpointAddress  string    `json:"remoteEndpointAddress,omitempty"`
	RxBytes                int64     `json:"rxBytes"`
	TxBytes                int64     `json:"txBytes"`
	LastHandshakeSecondsAgo int64    `json:"lastHandshakeSecondsAgo"`
	Online                 bool      `json:"online"`
	Enabled                bool      `json:"enabled"`
	Fwmark                 int64     `json:"fwmark,omitempty"`
}

// Policy — NDMS ip policy (from /show/rc/ip/policy).
type Policy struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Standalone  bool             `json:"standalone"`
	Interfaces  []PermittedIface `json:"interfaces"`
}

// PermittedIface — one interface entry in a policy's permit[] list.
type PermittedIface struct {
	Name   string `json:"name"`
	Label  string `json:"label,omitempty"`
	Order  int    `json:"order"`
	Denied bool   `json:"denied"`
}

// Device — one LAN host from /show/ip/hotspot.
type Device struct {
	MAC      string `json:"mac"`
	IP       string `json:"ip"`
	Name     string `json:"name,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	Active   bool   `json:"active"`
	Link     string `json:"link,omitempty"`
	Policy   string `json:"policy,omitempty"`
}

// Route — one row from /show/ip/route.
type Route struct {
	Destination string `json:"destination"`
	Gateway     string `json:"gateway"`
	Interface   string `json:"interface"`
}

// FQDNGroup — one ip object-group fqdn from /show/rc/object-group/fqdn.
type FQDNGroup struct {
	Name     string   `json:"name"`
	Includes []string `json:"includes"`
	Excludes []string `json:"excludes"`
}

// DNSRouteRule — one entry from /show/sc/dns-proxy/route.
type DNSRouteRule struct {
	Group     string `json:"group"`
	Interface string `json:"interface,omitempty"`
	Auto      bool   `json:"auto,omitempty"`
	Reject    bool   `json:"reject,omitempty"`
	// Index is Keenetic's stable hash for the route, used by the
	// dns-proxy.route.disable command to toggle without delete-recreate.
	// Present in /show/sc/dns-proxy/route; not present in /show/rc/.
	Index string `json:"index,omitempty"`
	// Disabled mirrors the `disable: true` flag NDMS returns for paused
	// routes (absent when enabled).
	Disabled bool `json:"disabled,omitempty"`
}

// PingCheckProfile — one profile from /show/ping-check/. Profiles are the
// static configuration (created/deleted by us); PingCheckStatus holds the
// runtime counters that change continuously.
type PingCheckProfile struct {
	Profile        string   `json:"profile"`
	Host           []string `json:"host"`
	Mode           string   `json:"mode"`
	UpdateInterval int      `json:"updateInterval"`
	MaxFails       int      `json:"maxFails"`
	MinSuccess     int      `json:"minSuccess"`
	Timeout        int      `json:"timeout"`
	Port           int      `json:"port"`
}

// PingCheckStatus — runtime status for one interface under a profile.
type PingCheckStatus struct {
	Profile      string `json:"profile"`
	Interface    string `json:"interface"`
	Status       string `json:"status"`
	SuccessCount int    `json:"successCount"`
	FailCount    int    `json:"failCount"`
}

// WireguardServer is a server view of a WireGuard interface with all peers.
// Equivalent to the legacy ndms.WireguardServer. Used by the VPN-server UI
// and the managed-server package.
type WireguardServer struct {
	ID            string                `json:"id"`
	InterfaceName string                `json:"interfaceName"` // kernel name (e.g. "nwg3")
	Description   string                `json:"description"`
	Status        string                `json:"status"` // "up" | "down" | ...
	Connected     bool                  `json:"connected"`
	MTU           int                   `json:"mtu"`
	Address       string                `json:"address"`
	Mask          string                `json:"mask"`
	PublicKey     string                `json:"publicKey"`
	ListenPort    int                   `json:"listenPort"`
	Peers         []WireguardServerPeer `json:"peers"`
}

// WireguardServerPeer is one peer on a WireguardServer, with runtime data.
type WireguardServerPeer struct {
	PublicKey     string   `json:"publicKey"`
	Description   string   `json:"description"`
	Endpoint      string   `json:"endpoint"`              // "ip:port"
	AllowedIPs    []string `json:"allowedIPs,omitempty"` // enriched from /show/rc/interface
	RxBytes       int64    `json:"rxBytes"`
	TxBytes       int64    `json:"txBytes"`
	LastHandshake string   `json:"lastHandshake"` // seconds-ago-to-RFC3339; "" if never
	Online        bool     `json:"online"`
	Enabled       bool     `json:"enabled"`
}

// WireguardServerConfig is the RC-sourced static config for a WG server,
// used to generate client .conf payloads.
type WireguardServerConfig struct {
	PublicKey  string                      `json:"publicKey"`
	ListenPort int                         `json:"listenPort"`
	MTU        int                         `json:"mtu"`
	Address    string                      `json:"address"`
	Peers      []WireguardServerPeerConfig `json:"peers"`
}

// WireguardServerPeerConfig is the RC-sourced static peer config.
type WireguardServerPeerConfig struct {
	PublicKey    string   `json:"publicKey"`
	Description  string   `json:"description"`
	PresharedKey string   `json:"presharedKey"`
	AllowedIPs   []string `json:"allowedIPs"` // e.g. ["10.0.0.2/32", "0.0.0.0/0"]
	Address      string   `json:"address"`    // first /32 in AllowedIPs
}

// SystemWireguardTunnel is a tunnel-view (single peer) of a WG interface,
// used for the system tunnels UI (external awg tunnels not managed by us).
type SystemWireguardTunnel struct {
	ID            string             `json:"id"`            // "Wireguard0"
	InterfaceName string             `json:"interfaceName"` // kernel "nwg0"
	Description   string             `json:"description"`
	Status        string             `json:"status"`
	Connected     bool               `json:"connected"`
	MTU           int                `json:"mtu"`
	Peer          *WireguardPeerInfo `json:"peer,omitempty"` // FIRST peer only
}

// WireguardPeerInfo is the minimal tunnel-peer view (first peer only, for system-tunnel UI).
type WireguardPeerInfo struct {
	PublicKey     string `json:"publicKey"`
	Endpoint      string `json:"endpoint"` // "ip:port"
	RxBytes       int64  `json:"rxBytes"`
	TxBytes       int64  `json:"txBytes"`
	LastHandshake string `json:"lastHandshake"` // RFC3339 or ""
	Online        bool   `json:"online"`
}

// InterfaceIntent represents what NDMS admin wants for an interface.
// Zero value is IntentDown — safe fallback when ShowInterface fails.
type InterfaceIntent int

const (
	// IntentDown: NDMS has disabled this interface (conf: disabled).
	IntentDown InterfaceIntent = iota
	// IntentUp: NDMS wants this interface running (conf: running).
	IntentUp
)

// InterfaceDetails is the parsed "show interface" output. Used by the
// state.Manager to decide tunnel state. The key insight: state: field is
// unreliable (can show "up" when link is down). Use ConfLayer ("running"
// vs "disabled") to determine NDMS admin intent.
type InterfaceDetails struct {
	State     string // "up", "down", "error"
	Link      string // "up", "down"
	Connected bool
	ConfLayer string // "running", "disabled", "pending"
	Uptime    int    // seconds since interface came up (0 if down)
}

// Intent returns the NDMS admin intent derived from ConfLayer.
func (d InterfaceDetails) Intent() InterfaceIntent {
	if d.ConfLayer == "running" {
		return IntentUp
	}
	return IntentDown
}

// LinkUp returns true if the link layer is up.
func (d InterfaceDetails) LinkUp() bool { return d.Link == "up" }

// Version — NDMS system version from /show/version. Cached write-once at boot.
type Version struct {
	Release     string `json:"release"`     // e.g. "4.2.5"
	Title       string `json:"title"`       // product name
	HardwareID  string `json:"hardwareId"`
	Description string `json:"description"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Vendor       string `json:"vendor,omitempty"`
	Series       string `json:"series,omitempty"`
	Model        string `json:"model,omitempty"`
	Region       string `json:"region,omitempty"`
	Components   []string `json:"components,omitempty"`
	Uptime       int64    `json:"uptime,omitempty"` // seconds
	// LastFetched is set by SystemInfoStore.Init (not from NDMS).
	LastFetched time.Time `json:"lastFetched"`
}

// PingCheckConfig is the configuration for an NDMS ping-check profile,
// passed to tunnel operators when binding a profile to an interface.
type PingCheckConfig struct {
	Host           string `json:"host"`
	Mode           string `json:"mode"`           // "icmp", "connect", "tls", "uri"
	UpdateInterval int    `json:"updateInterval"` // seconds (3-3600)
	MaxFails       int    `json:"maxFails"`       // 1-10
	MinSuccess     int    `json:"minSuccess"`     // 1-10
	Timeout        int    `json:"timeout"`        // seconds (1-10)
	Port           int    `json:"port,omitempty"` // for connect/tls mode
	URI            string `json:"uri,omitempty"`  // for uri mode
	Restart        bool   `json:"restart"`        // auto-restart interface on fail
}

// PingCheckProfileStatus is the aggregated runtime + config view of ONE
// ping-check profile bound to an interface. Returned by nwg operator's
// GetPingCheckStatus. Distinct from PingCheckStatus (flat per-row) which
// is returned by queries.PingCheckStatus.List.
type PingCheckProfileStatus struct {
	Exists       bool   `json:"exists"`
	Host         string `json:"host"`
	Mode         string `json:"mode"`
	Interval     int    `json:"interval"`
	MaxFails     int    `json:"maxFails"`
	MinSuccess   int    `json:"minSuccess"`
	Timeout      int    `json:"timeout"`
	Port         int    `json:"port,omitempty"`
	Restart      bool   `json:"restart"`
	Bound        bool   `json:"bound"`
	Status       string `json:"status"` // "pass" | "fail" | ""
	FailCount    int    `json:"failCount"`
	SuccessCount int    `json:"successCount"`
}

// ASCParams holds base AWG obfuscation parameters (firmware < 5.1 Alpha 3).
type ASCParams struct {
	Jc   int    `json:"jc"`
	Jmin int    `json:"jmin"`
	Jmax int    `json:"jmax"`
	S1   int    `json:"s1"`
	S2   int    `json:"s2"`
	H1   string `json:"h1"`
	H2   string `json:"h2"`
	H3   string `json:"h3"`
	H4   string `json:"h4"`
}

// ASCParamsExtended holds all AWG obfuscation parameters (firmware >= 5.1 Alpha 3).
type ASCParamsExtended struct {
	ASCParams
	S3 int    `json:"s3"`
	S4 int    `json:"s4"`
	I1 string `json:"i1"`
	I2 string `json:"i2"`
	I3 string `json:"i3"`
	I4 string `json:"i4"`
	I5 string `json:"i5"`
}
