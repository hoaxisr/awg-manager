package connections

// Connection represents a single conntrack entry with resolved tunnel and client info.
type Connection struct {
	Protocol   string `json:"protocol"`
	Src        string `json:"src"`
	Dst        string `json:"dst"`
	SrcPort    int    `json:"srcPort"`
	DstPort    int    `json:"dstPort"`
	State      string `json:"state"`
	Packets    int64  `json:"packets"`
	Bytes      int64  `json:"bytes"`
	Interface  string `json:"interface"`
	TunnelID   string `json:"tunnelId"`
	TunnelName string `json:"tunnelName"`
	ClientMAC  string `json:"clientMac"`
	ClientName string `json:"clientName"`
}

// ConnectionStats holds aggregate counts over all connections (pre-filter).
type ConnectionStats struct {
	Total     int           `json:"total"`
	Direct    int           `json:"direct"`
	Tunneled  int           `json:"tunneled"`
	Protocols ProtocolStats `json:"protocols"`
}

// ProtocolStats breaks down connection count by protocol.
type ProtocolStats struct {
	TCP  int `json:"tcp"`
	UDP  int `json:"udp"`
	ICMP int `json:"icmp"`
}

// TunnelConnectionInfo describes a tunnel's connection count for the summary.
type TunnelConnectionInfo struct {
	Name      string `json:"name"`
	Interface string `json:"interface"`
	Count     int    `json:"count"`
}

// ListParams holds filtering and pagination parameters.
type ListParams struct {
	Tunnel   string // "all", "direct", or tunnel ID
	Protocol string // "all", "tcp", "udp", "icmp"
	Search   string // substring match on src, dst, clientName
	Offset   int
	Limit    int // default 50, max 500
}

// ListResponse is returned by Service.List().
type ListResponse struct {
	Stats       ConnectionStats                 `json:"stats"`
	Tunnels     map[string]TunnelConnectionInfo `json:"tunnels"`
	Connections []Connection                    `json:"connections"`
	Pagination  PaginationInfo                  `json:"pagination"`
}

// PaginationInfo describes the current page of results.
type PaginationInfo struct {
	Total    int `json:"total"`
	Offset   int `json:"offset"`
	Limit    int `json:"limit"`
	Returned int `json:"returned"`
}
