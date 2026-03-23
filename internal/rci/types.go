package rci

import "encoding/json"

// --- From ndms/rci.go ---

// InterfaceInfo represents a single interface from /show/interface/{name}.
type InterfaceInfo struct {
	State         string `json:"state"`
	Link          string `json:"link"`
	Connected     string `json:"connected"`
	InterfaceName string `json:"interface-name"`
	Type          string `json:"type"`
	Description   string `json:"description"`
	Address       string `json:"address"`
	Mask          string `json:"mask"`
	SecurityLevel string `json:"security-level"`
	Priority      int    `json:"priority"`
	Summary       struct {
		Layer struct {
			Conf string `json:"conf"`
			Link string `json:"link"`
			IPv4 string `json:"ipv4"`
			IPv6 string `json:"ipv6"`
		} `json:"layer"`
	} `json:"summary"`
}

// RouteEntry represents an element of /show/ip/route.
type RouteEntry struct {
	Destination string `json:"destination"`
	Gateway     string `json:"gateway"`
	Interface   string `json:"interface"`
}

// HotspotResponse wraps /show/ip/hotspot response.
type HotspotResponse struct {
	Host []HotspotHost `json:"host"`
}

// HotspotHost is a single host entry.
type HotspotHost struct {
	IP       string `json:"ip"`
	MAC      string `json:"mac"`
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
	Active   any    `json:"active"`
}

// PingCheckListResponse represents /show/ping-check/ response.
type PingCheckListResponse struct {
	PingCheck []PingCheckProfile `json:"pingcheck"`
}

// PingCheckProfile is a single profile in the ping-check list.
type PingCheckProfile struct {
	Profile        string                          `json:"profile"`
	Host           []string                        `json:"host"`
	Mode           string                          `json:"mode"`
	UpdateInterval int                             `json:"update-interval"`
	MaxFails       int                             `json:"max-fails"`
	MinSuccess     int                             `json:"min-success"`
	Timeout        int                             `json:"timeout"`
	Port           int                             `json:"port"`
	Interface      map[string]PingCheckIfaceStatus `json:"interface"`
}

// PingCheckIfaceStatus represents a bound interface's check status.
type PingCheckIfaceStatus struct {
	SuccessCount int    `json:"successcount"`
	FailCount    int    `json:"failcount"`
	Status       string `json:"status"`
}

// VersionInfo holds /show/version response.
type VersionInfo struct {
	Release      string `json:"release"`
	Title        string `json:"title"`
	Arch         string `json:"arch"`
	HwID         string `json:"hw_id"`
	HwType       string `json:"hw_type"`
	Model        string `json:"model"`
	Device       string `json:"device"`
	Manufacturer string `json:"manufacturer"`
	Vendor       string `json:"vendor"`
	Series       string `json:"series"`
	NDW          struct {
		Components string `json:"components"`
	} `json:"ndw"`
}

// --- From nwg/rci.go ---

// WGInterface represents a WireGuard interface from /show/interface/{name}.
type WGInterface struct {
	ID          string          `json:"id"`
	Type        string          `json:"type"`
	Description string          `json:"description"`
	Link        string          `json:"link"`
	Connected   json.RawMessage `json:"connected"`
	Uptime      int64           `json:"uptime"`
	WireGuard   *WGSection      `json:"wireguard"`
	Summary     WGSummary       `json:"summary"`
}

// WGSection holds WireGuard-specific data.
type WGSection struct {
	Status string   `json:"status"`
	Peer   []WGPeer `json:"peer"`
}

// WGPeer is a single WireGuard peer.
type WGPeer struct {
	Online        bool   `json:"online"`
	LastHandshake int64  `json:"last-handshake"`
	RxBytes       int64  `json:"rxbytes"`
	TxBytes       int64  `json:"txbytes"`
	Via           string `json:"via"`
}

// WGSummary holds layer summary.
type WGSummary struct {
	Layer struct {
		Conf string `json:"conf"`
	} `json:"layer"`
}

// WGInterfaceInfo holds basic info about a Wireguard interface (name + description).
type WGInterfaceInfo struct {
	Name        string
	Description string
}

// NeverHandshake is the sentinel value RCI uses when no handshake has occurred.
const NeverHandshake int64 = 2147483647
